package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// tier2ExternalDropStrategy is the sentinel Strategy value resolveCallWithTypes
// sets on its returned ResolutionResult when the chain walker classified the
// chain root as an external crate (see populateCargoMetadata + the
// chain-walker check in resolveCallWithTypes). The caller in pipeline_cbm.go
// checks for this strategy to decide whether to fall back to fuzzy resolution.
//
// QualifiedName is always empty when this strategy is set — the edge is
// intentionally dropped, not resolved.
//
// Sentinel kept here next to populateCargoMetadata so all v0.1
// external-drop machinery lives in one file.
const tier2ExternalDropStrategy = "tier2_external_drop"

// CargoMetadataResult captures the external-vs-workspace classification
// derived from `cargo metadata --no-deps`. Consumed by the chain walker
// (Tier-2 v0.1) to mark chain roots whose crate is external — those
// chains drop instead of fuzzy-resolving the bare callee into an
// in-graph candidate.
//
// Crate names are normalized via normalizeCargoCrateName: `-` → `_`
// matches Rust identifier conventions (callers refer to crates as
// `foo_bar` even when the cargo package name is `foo-bar`).
type CargoMetadataResult struct {
	// ExternalCrates: deps with a non-empty `source` field (crates.io,
	// git, registry-other). Workspace members are excluded — a sibling
	// crate referenced via path dep would have empty source AND appear
	// in WorkspaceMembers.
	ExternalCrates map[string]bool
	// WorkspaceMembers: the workspace's own packages. Used to override
	// the "external" classification when a workspace member happens to
	// share a name with an external crate.
	WorkspaceMembers map[string]bool
}

// parseCargoMetadata parses the JSON output of `cargo metadata
// --format-version 1 --no-deps` into a CargoMetadataResult.
// Hermetic: takes the raw bytes; the pipeline integration handles
// the shell-out separately. Test fixtures live under testdata/.
func parseCargoMetadata(raw []byte) (*CargoMetadataResult, error) {
	var doc struct {
		Packages []struct {
			Name         string `json:"name"`
			Dependencies []struct {
				Name   string `json:"name"`
				Source string `json:"source"` // empty for path/workspace deps; non-empty for crates.io / git
			} `json:"dependencies"`
		} `json:"packages"`
		WorkspaceMembers []string `json:"workspace_members"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse cargo metadata: %w", err)
	}

	res := &CargoMetadataResult{
		ExternalCrates:   map[string]bool{},
		WorkspaceMembers: map[string]bool{},
	}

	// First pass: register all workspace members so the external
	// classification can override on collision.
	for _, p := range doc.Packages {
		res.WorkspaceMembers[normalizeCargoCrateName(p.Name)] = true
	}

	// Second pass: classify dependencies. A dep with a non-empty source
	// is external; if it ALSO appears as a workspace member (path-dep
	// to a sibling within the workspace), the member wins.
	for _, p := range doc.Packages {
		for _, d := range p.Dependencies {
			if d.Source == "" {
				continue // path/workspace dep — not external
			}
			name := normalizeCargoCrateName(d.Name)
			if res.WorkspaceMembers[name] {
				continue // workspace member overrides
			}
			res.ExternalCrates[name] = true
		}
	}

	return res, nil
}

// normalizeCargoCrateName replaces `-` with `_` in cargo crate names
// so the result matches how callers refer to the crate in `use` /
// `::` paths inside Rust source. cargo package names allow `-`, but
// Rust identifiers do not.
func normalizeCargoCrateName(name string) string {
	if !strings.ContainsRune(name, '-') {
		return name
	}
	return strings.ReplaceAll(name, "-", "_")
}

// runCargoMetadata shells out to `cargo metadata --format-version 1
// --no-deps` at the given root directory. Returns the parsed result,
// or nil + error on any failure (cargo missing, no Cargo.toml,
// timeout, malformed JSON). Failures are non-fatal — callers should
// log a warning and proceed with empty external/workspace sets so
// indexing isn't blocked by a missing toolchain.
//
// The 30-second timeout is generous for typical workspaces (PSM with
// 275 packages measured at ~1.5s wall) and bounded for pathological
// cases where cargo hangs on a corrupted Cargo.lock or network resolver.
func runCargoMetadata(rootDir string) (*CargoMetadataResult, error) {
	cargoToml := filepath.Join(rootDir, "Cargo.toml")
	if _, err := os.Stat(cargoToml); err != nil {
		return nil, fmt.Errorf("no Cargo.toml at %s: %w", rootDir, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "cargo", "metadata", "--format-version", "1", "--no-deps")
	cmd.Dir = rootDir
	// Suppress stderr (cargo emits "warning:" lines on stderr that we
	// don't want noisy in our logs); keep stdout for the JSON parse.
	cmd.Stderr = nil

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("cargo metadata: %w", err)
	}
	return parseCargoMetadata(out)
}

// populateCargoMetadata invokes runCargoMetadata at the pipeline's
// RepoPath and stores the result on the Pipeline struct's
// externalCrates + workspaceMembers fields. On any failure (no cargo,
// no Cargo.toml, timeout, parse error) the fields stay nil and a
// slog.Warn is emitted — the indexing pipeline proceeds with no
// external-classification information, which means Tier-2 v0.1's
// external-drop gate simply doesn't fire (graceful degradation to
// pre-v0.1 behavior).
func (p *Pipeline) populateCargoMetadata() {
	start := time.Now()
	res, err := runCargoMetadata(p.RepoPath)
	elapsed := time.Since(start)
	if err != nil {
		slog.Warn("pipeline.cargo_metadata.failed",
			"err", err,
			"elapsed_ms", elapsed.Milliseconds(),
		)
		return
	}
	p.externalCrates = res.ExternalCrates
	p.workspaceMembers = res.WorkspaceMembers
	slog.Info("pipeline.cargo_metadata",
		"external_crates", len(res.ExternalCrates),
		"workspace_members", len(res.WorkspaceMembers),
		"elapsed_ms", elapsed.Milliseconds(),
	)
}
