// HTTP link pass: route matching, infra URL sites, JSON config discovery, file hashing.
//
// Split from pipeline.go without behaviour changes.
package pipeline

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/brandyn-s/code-graph/internal/discover"
	"github.com/brandyn-s/code-graph/internal/fqn"
	"github.com/brandyn-s/code-graph/internal/httplink"
	"github.com/brandyn-s/code-graph/internal/store"
	"github.com/zeebo/xxh3"
)

// passHTTPLinks runs the HTTP linker to discover cross-service HTTP calls.
func (p *Pipeline) passHTTPLinks() error {
	// Clean up stale Route/InfraFile nodes and HTTP_CALLS/HANDLES/ASYNC_CALLS edges before re-running
	_ = p.Store.DeleteNodesByLabel(p.ProjectName, "Route")
	_ = p.Store.DeleteNodesByLabel(p.ProjectName, "InfraFile")
	_ = p.Store.DeleteEdgesByType(p.ProjectName, "HTTP_CALLS")
	_ = p.Store.DeleteEdgesByType(p.ProjectName, "HANDLES")
	_ = p.Store.DeleteEdgesByType(p.ProjectName, "ASYNC_CALLS")

	// Index infrastructure files (Dockerfiles, compose, cloudbuild, .env)
	p.passInfraFiles()

	// Scan config files for env var URLs and create synthetic Module nodes
	envBindings := ScanProjectEnvURLs(p.RepoPath)
	if len(envBindings) > 0 {
		p.injectEnvBindings(envBindings)
	}

	linker := httplink.New(p.Store, p.ProjectName)

	// Feed InfraFile environment URLs into the HTTP linker
	infraSites := p.extractInfraCallSites()
	if len(infraSites) > 0 {
		linker.AddCallSites(infraSites)
		slog.Info("pass4.infra_callsites", "count", len(infraSites))
	}

	links, err := linker.Run()
	if err != nil {
		return err
	}
	slog.Info("pass4.httplinks", "links", len(links))
	return nil
}

// extractInfraCallSites extracts URL values from InfraFile environment properties
// and converts them to HTTPCallSite entries for the HTTP linker.
func (p *Pipeline) extractInfraCallSites() []httplink.HTTPCallSite {
	infraNodes, err := p.Store.FindNodesByLabel(p.ProjectName, "InfraFile")
	if err != nil {
		return nil
	}

	var sites []httplink.HTTPCallSite
	for _, node := range infraNodes {
		// InfraFile nodes use different property keys depending on source:
		// compose files: "environment", Dockerfiles/shell/.env: "env_vars",
		// cloudbuild: "deploy_env_vars"
		for _, envKey := range []string{"environment", "env_vars", "deploy_env_vars"} {
			sites = append(sites, extractEnvURLSites(node, envKey)...)
		}
	}
	return sites
}

// extractEnvURLSites extracts HTTP call sites from a single env property of an InfraFile node.
func extractEnvURLSites(node *store.Node, propKey string) []httplink.HTTPCallSite {
	env, ok := node.Properties[propKey]
	if !ok {
		return nil
	}

	// env_vars are stored as map[string]string (from Go), but after JSON round-trip
	// through SQLite they come back as map[string]any.
	var sites []httplink.HTTPCallSite
	switch envMap := env.(type) {
	case map[string]any:
		for _, val := range envMap {
			valStr, ok := val.(string)
			if !ok {
				continue
			}
			sites = append(sites, urlSitesFromValue(node, valStr)...)
		}
	case map[string]string:
		for _, valStr := range envMap {
			sites = append(sites, urlSitesFromValue(node, valStr)...)
		}
	}
	return sites
}

// urlSitesFromValue extracts URL paths from a string value and creates HTTPCallSite entries.
func urlSitesFromValue(node *store.Node, val string) []httplink.HTTPCallSite {
	if !strings.Contains(val, "http://") && !strings.Contains(val, "https://") && !strings.HasPrefix(val, "/") {
		return nil
	}

	paths := httplink.ExtractURLPaths(val)
	sites := make([]httplink.HTTPCallSite, 0, len(paths))
	for _, path := range paths {
		sites = append(sites, httplink.HTTPCallSite{
			Path:                path,
			SourceName:          node.Name,
			SourceQualifiedName: node.QualifiedName,
			SourceLabel:         "InfraFile",
		})
	}
	return sites
}

// injectEnvBindings creates or updates Module nodes for config files that contain
// environment variable URL bindings. These synthetic constants feed into the
// HTTP linker's call site discovery.
func (p *Pipeline) injectEnvBindings(bindings []EnvBinding) {
	byFile := make(map[string][]EnvBinding)
	for _, b := range bindings {
		byFile[b.FilePath] = append(byFile[b.FilePath], b)
	}

	count := 0
	for filePath, fileBindings := range byFile {
		moduleQN := fqn.ModuleQN(p.ProjectName, filePath)
		constants := buildConstantsList(fileBindings)

		if p.mergeWithExistingModule(moduleQN, constants) {
			count += len(fileBindings)
			continue
		}

		_, _ = p.Store.UpsertNode(&store.Node{
			Project:       p.ProjectName,
			Label:         "Module",
			Name:          filepath.Base(filePath),
			QualifiedName: moduleQN,
			FilePath:      filePath,
			Properties:    map[string]any{"constants": constants},
		})
		count += len(fileBindings)
	}

	if count > 0 {
		slog.Info("envscan.injected", "bindings", count, "files", len(byFile))
	}
}

// buildConstantsList converts env bindings to "KEY = VALUE" constant strings, capped at 50.
func buildConstantsList(bindings []EnvBinding) []string {
	constants := make([]string, 0, len(bindings))
	for _, b := range bindings {
		constants = append(constants, b.Key+" = "+b.Value)
	}
	if len(constants) > 50 {
		constants = constants[:50]
	}
	return constants
}

// mergeWithExistingModule merges new constants into an existing Module node's constant list.
// Returns true if the module existed and was updated.
func (p *Pipeline) mergeWithExistingModule(moduleQN string, constants []string) bool {
	existing, _ := p.Store.FindNodeByQN(p.ProjectName, moduleQN)
	if existing == nil {
		return false
	}
	existConsts, ok := existing.Properties["constants"].([]any)
	if !ok {
		return false
	}
	seen := make(map[string]bool, len(existConsts))
	for _, c := range existConsts {
		if s, ok := c.(string); ok {
			seen[s] = true
		}
	}
	for _, c := range constants {
		if !seen[c] {
			existConsts = append(existConsts, c)
		}
	}
	if existing.Properties == nil {
		existing.Properties = map[string]any{}
	}
	existing.Properties["constants"] = existConsts
	_, _ = p.Store.UpsertNode(existing)
	return true
}

// jsonURLKeyPattern matches JSON keys that likely contain URL/endpoint values.
var jsonURLKeyPattern = regexp.MustCompile(`(?i)(url|endpoint|base_url|host|api_url|service_url|target_url|callback_url|webhook|href|uri|address|server|origin|proxy|redirect|forward|destination)`)

// processJSONFile extracts URL-related string values from JSON config files.
// Uses a key-pattern allowlist to avoid flooding constants with noise.
func (p *Pipeline) processJSONFile(f discover.FileInfo) error {
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return err
	}

	var parsed any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("json parse: %w", err)
	}

	var constants []string
	extractJSONURLValues(parsed, "", &constants, 0)

	if len(constants) == 0 {
		return nil
	}

	// Cap at 20 constants per JSON file
	if len(constants) > 20 {
		constants = constants[:20]
	}

	moduleQN := fqn.ModuleQN(p.ProjectName, f.RelPath)
	err = p.upsertNode(&store.Node{
		Project:       p.ProjectName,
		Label:         "Module",
		Name:          filepath.Base(f.RelPath),
		QualifiedName: moduleQN,
		FilePath:      f.RelPath,
		Properties:    map[string]any{"constants": constants},
	})
	return err
}

// extractJSONURLValues recursively extracts key=value pairs from JSON where
// the key matches the URL key pattern or the value looks like a URL/path.
func extractJSONURLValues(v any, key string, out *[]string, depth int) {
	if depth > 20 {
		return
	}

	switch val := v.(type) {
	case map[string]any:
		for k, child := range val {
			extractJSONURLValues(child, k, out, depth+1)
		}
	case []any:
		for _, child := range val {
			extractJSONURLValues(child, key, out, depth+1)
		}
	case string:
		if key == "" || val == "" {
			return
		}
		// Include if key matches URL pattern
		if jsonURLKeyPattern.MatchString(key) {
			*out = append(*out, key+" = "+val)
			return
		}
		// Include if value looks like a URL or API path
		if looksLikeURL(val) {
			*out = append(*out, key+" = "+val)
		}
	}
}

// looksLikeURL returns true if s appears to be a URL or API path.
func looksLikeURL(s string) bool {
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return true
	}
	// Path starting with /api/ or containing at least 2 segments
	if strings.HasPrefix(s, "/") && strings.Count(s, "/") >= 2 {
		// Skip version-like paths: /1.0.0, /v2, /en
		seg := strings.TrimPrefix(s, "/")
		return len(seg) > 3
	}
	return false
}

// safeRowToLine converts a tree-sitter row (uint) to a 1-based line number (int).
// Returns math.MaxInt if the value would overflow.
// stripBOM removes a UTF-8 BOM (0xEF 0xBB 0xBF) from the start of source.
// Common in C# and Windows-generated files; tree-sitter may choke on BOM bytes.
func stripBOM(source []byte) []byte {
	if len(source) >= 3 && source[0] == 0xEF && source[1] == 0xBB && source[2] == 0xBF {
		return source[3:]
	}
	return source
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := xxh3.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fullModeMaxFileSize returns the per-file size cutoff for full-mode
// discovery. Source files above ~1MB are essentially always GENERATED
// (tree-sitter parser tables, bundled assets, generated bindings) — on this
// very repo, 44 such files carried 96% of all indexable bytes and dominated
// the definitions pass (~50s of an 82s index), while contributing only
// graph noise. Fast mode has long had a 512KB cutoff; full mode previously
// had none. 1MB matches the cutoff Sourcegraph-class indexers use.
//
// Override with CBM_MAX_FILE_BYTES: a positive integer sets the cutoff in
// bytes; "0" disables the limit entirely (pre-2026-06-10 behavior); unset
// or unparsable uses the 1MB default. Skipped files are visible as the
// difference in pipeline.discovered counts.
func fullModeMaxFileSize() int64 {
	// Single source of truth lives in discover so index_health and the
	// watcher apply the identical cutoff (they discover through the same
	// package but don't import pipeline). See discover.FullModeMaxFileSize.
	return discover.FullModeMaxFileSize()
}
