# Tier-2 v0.1 — External-chain awareness

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to
> implement this plan task-by-task.

**Goal:** Close the dominant `get_result`-style assetman FP bucket
identified by PR #341 by teaching `resolveCallWithTypes` to recognize
chain roots that resolve to **external crates** and dropping those
edges instead of letting them fall through to fuzzy bare-name
resolution. Cargo-metadata ingestion populates the external-crate
set; a new `_external.<crate>` sentinel propagates through the chain
walker into `CallContext.ReceiverType`; the resolver gates on it and
drops before fuzzy fires.

**Architecture:** Three additive changes, no edge-type changes, no
schema changes. (1) New `cargo_metadata.go` pass at index time that
shells out to `cargo metadata --no-deps` and stores
`externalCrates set[string]` + `workspaceMembers set[string]` on the
Pipeline struct. (2) Chain walker (`resolveCallWithTypes` in
pipeline.go) extended: when chain root is a `crate::path::fn(...)`
expression AND the root crate is in `externalCrates` minus
`workspaceMembers`, set `chainReceiverType = "_external.<crate>"`
even when `returnTypes` lookup fails. (3) Registry resolver: when
`CallContext.ReceiverType` starts with `"_external."`, return empty
(drop) before the fuzzy fallback path is reached. Same property-on-
CALLS convention as v0.1/v0.2/v0.3 of the dispatch_kind work — no
new edge types.

**Tech Stack:** Go 1.26, tree-sitter (Rust grammar already
vendored), `cargo metadata` CLI shell-out (toolchain dependency is
acceptable — Rust projects building from source already have cargo).

**Repo:** `redacted-org/code-graph`. Implementation lands on a
separate `feat/tier2-v0.1-external-chain` branch after this design+plan PR
merges.

**Dependencies:** Builds on the existing PR #149 PerFuncTypeMap
infrastructure (`typeinfer.go`), the existing chain walker
(`resolveCallWithTypes`, pipeline.go:2145-2308), and the existing
`CallContext.ReceiverType` field (`resolver.go:133`). No prerequisite
PRs are blocking; v0.1 is the first slice that lands.

**Regression gate:** `bench/accuracy/compare.py psm-rust`
must show assetman scope-aligned F1 ≥ 0.88 (up from 0.855 with the
Phase E gate Python-only). No regression on the other Rust subsets
(canstatd, calibd, adsbd, apid). Loc-Bench iter=2 within ±2pp of the
defended 86.0/84.5/73.5 baseline.

---

## Task 1: Cargo-metadata ingestion at index time

**Finding:** The pipeline today has no signal for "is crate X
external to this workspace." The chain walker in
`resolveCallWithTypes` (pipeline.go:2161-2168) maintains
`p.rustCrateMap` which lists own-crate names (workspace members),
but doesn't have the inverse list of external dependencies. Without
that list, every chain root that's not a workspace member looks
identical to "internal but not yet resolved" — both produce the same
fuzzy fallback.

**Files:**
- New: `internal/pipeline/cargo_metadata.go` — shell-out + parse
- New: `internal/pipeline/cargo_metadata_test.go` — JSON fixture
  parse tests
- Modify: `internal/pipeline/pipeline.go` — invoke the new pass at
  index start (where `rustCrateMap` is built); store
  `externalCrates set[string]` + `workspaceMembers set[string]` on
  the Pipeline struct
- New: `internal/pipeline/testdata/cargo-metadata-simple.json` —
  single-crate fixture
- New: `internal/pipeline/testdata/cargo-metadata-workspace.json` —
  multi-member workspace fixture

**Step 1: Write the failing tests**

```go
// internal/pipeline/cargo_metadata_test.go
package pipeline

import "testing"

func TestParseCargoMetadataSimple(t *testing.T) {
    raw := mustReadTestData(t, "cargo-metadata-simple.json")
    res, err := parseCargoMetadata(raw)
    if err != nil { t.Fatal(err) }
    // Single-crate project with deps on serde + tokio + anyhow.
    if !res.ExternalCrates["serde"]   { t.Error("serde missing") }
    if !res.ExternalCrates["tokio"]   { t.Error("tokio missing") }
    if !res.ExternalCrates["anyhow"]  { t.Error("anyhow missing") }
    if res.WorkspaceMembers["serde"]  { t.Error("serde should NOT be a member") }
    if !res.WorkspaceMembers["my_app"] { t.Error("own crate my_app missing") }
}

func TestParseCargoMetadataWorkspace(t *testing.T) {
    raw := mustReadTestData(t, "cargo-metadata-workspace.json")
    res, err := parseCargoMetadata(raw)
    if err != nil { t.Fatal(err) }
    // Workspace with members [a, b, c]; a depends on b + tokio.
    // External set = tokio. Workspace members = {a, b, c}.
    if res.ExternalCrates["b"] {
        t.Error("workspace member b incorrectly classified as external")
    }
    if !res.ExternalCrates["tokio"] {
        t.Error("tokio external missing")
    }
    for _, m := range []string{"a", "b", "c"} {
        if !res.WorkspaceMembers[m] {
            t.Errorf("workspace member %s missing", m)
        }
    }
}

func TestParseCargoMetadataMalformed(t *testing.T) {
    _, err := parseCargoMetadata([]byte("not json"))
    if err == nil {
        t.Error("expected error on malformed JSON, got nil")
    }
}
```

**Step 2: Implement parseCargoMetadata**

The implementation accepts raw JSON bytes (not the cargo invocation)
so tests are hermetic. The pipeline integration handles the
shell-out separately, with the malformed-result path falling back to
an empty set (logged at slog.Warn) — never blocks indexing.

```go
// internal/pipeline/cargo_metadata.go
package pipeline

import (
    "encoding/json"
    "fmt"
)

type CargoMetadataResult struct {
    ExternalCrates   map[string]bool
    WorkspaceMembers map[string]bool
}

func parseCargoMetadata(raw []byte) (*CargoMetadataResult, error) {
    var doc struct {
        Packages         []struct {
            Name         string `json:"name"`
            Dependencies []struct {
                Name   string `json:"name"`
                Source string `json:"source"`  // empty for path/workspace deps
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
    for _, p := range doc.Packages {
        res.WorkspaceMembers[normalizeCargoCrateName(p.Name)] = true
    }
    for _, p := range doc.Packages {
        for _, d := range p.Dependencies {
            // Source is empty for workspace / path deps; non-empty
            // (e.g. "registry+https://...") for crates.io / git deps.
            if d.Source == "" { continue }
            name := normalizeCargoCrateName(d.Name)
            if res.WorkspaceMembers[name] { continue }
            res.ExternalCrates[name] = true
        }
    }
    return res, nil
}

// normalizeCargoCrateName replaces `-` with `_` in cargo crate names
// to match Rust identifier conventions (the way callers refer to the
// crate in `use` / `::` paths).
func normalizeCargoCrateName(name string) string {
    out := make([]byte, len(name))
    for i := 0; i < len(name); i++ {
        if name[i] == '-' { out[i] = '_' } else { out[i] = name[i] }
    }
    return string(out)
}
```

**Step 3: Wire into pipeline index-start**

In the same place where `p.rustCrateMap` is populated (search for
`rustCrateMap` in pipeline.go to find the index-start code path),
add:

```go
// After rustCrateMap is built:
if cargoTomlExists {
    raw, err := exec.Command("cargo", "metadata",
        "--format-version", "1", "--no-deps").Output()
    if err != nil {
        slog.Warn("cargo.metadata.failed", "err", err)
    } else {
        res, perr := parseCargoMetadata(raw)
        if perr != nil {
            slog.Warn("cargo.metadata.parse_failed", "err", perr)
        } else {
            p.externalCrates  = res.ExternalCrates
            p.workspaceMembers = res.WorkspaceMembers
        }
    }
}
```

The shell-out is wrapped in a 30-second timeout (`exec.CommandContext`
with `context.WithTimeout`). On timeout / failure / parse-failure,
`p.externalCrates` stays nil and the Task 2 chain walker change
simply doesn't fire (preserves current behavior).

**Step 4: Cache to avoid re-shelling**

The cargo metadata output is cached per-Pipeline (already
short-lived; one instance per indexing job). For repeated indexing
of the same repo across sessions, the OS file cache on `Cargo.toml` +
`Cargo.lock` already short-circuits cargo's own work; an explicit
cache layer is not required for v0.1.

---

## Task 2: Chain-walker `_external` sentinel

**Finding:** When `resolveCallWithTypes`'s chain walker (pipeline.go:
2192-2265) processes a callee like
`diesel::insert_into(asset_update_stages::table).values(...).get_result(conn)`,
the walker today:

1. Splits on `.` — `rootName = "diesel::insert_into(asset_update_stages::table)"`,
   then field/method segments.
2. Looks up `typeMap[rootName]` — fails (the bracketed expression
   isn't a variable name).
3. Sets `resolved = false`, returns to the caller without setting
   `chainReceiverType`.

The fall-through path lets the registry's bare-name fuzzy resolver
pick the only in-graph candidate for `get_result`
(`AssetIntrospectImpl.get_result`) — the FP.

**Files:**
- Modify: `internal/pipeline/pipeline.go`
  (`resolveCallWithTypes` ~lines 2192-2265)

**Step 1: Failing test**

Add to `internal/pipeline/pipeline_cbm_test.go` (or a sibling test
file — pick where chain-walker tests already live):

```go
func TestExternalChainRootProducesExternalReceiver(t *testing.T) {
    // Synthetic fixture: a Rust function that calls
    // diesel::insert_into(...).get_result(conn). The chain root
    // identifier is `diesel`; the pipeline has been told
    // p.externalCrates = {"diesel": true} via cargo metadata.
    //
    // Expected: chain walker returns chainReceiverType="_external.diesel"
    // and the resolver drops the edge entirely (no CALLS edge emitted
    // to AssetIntrospectImpl.get_result).
    p := setupPipelineWithExternalCrates(map[string]bool{"diesel": true})
    src := `fn run(&self, conn: &mut PgConnection) -> Result<X, E> {
        diesel::insert_into(t::table)
            .values(&self.x)
            .get_result(conn)
    }`
    // Index, then assert no edge from "run" to AssetIntrospectImpl.get_result.
    edges := indexAndCollectCallsEdges(p, src)
    for _, e := range edges {
        if strings.HasSuffix(e.TargetQN, ".get_result") &&
           !strings.HasPrefix(e.TargetQN, "_external.") {
            t.Errorf("expected external-drop on diesel chain; got internal edge to %s", e.TargetQN)
        }
    }
}
```

**Step 2: Chain-walker extension**

In `resolveCallWithTypes`, after the chain-walker's loop returns
`resolved=false`, add a check: if the rootName looks like a static
call path (`crate::...(...)`) AND the root crate is external, set
`chainReceiverType="_external.<crate>"`:

```go
// At end of the multi-segment chain block in resolveCallWithTypes,
// after the existing chainReceiverType assignment branches:

if chainReceiverType == "" && resolved == false {
    // The chain walker couldn't follow the chain. Check if the root
    // is an external-crate static call: `<crate>::path::fn(...)`.
    // The rootName here is the literal text before the first `.`
    // segment. For `diesel::insert_into(t::table).method`, rootName
    // is `diesel::insert_into(t::table)` — strip the args + the
    // path tail to get the root crate name.
    if idx := strings.Index(rootName, "::"); idx >= 0 {
        rootCrate := rootName[:idx]
        if p.externalCrates[rootCrate] && !p.workspaceMembers[rootCrate] {
            chainReceiverType = "_external." + rootCrate
        }
    }
}
```

**Step 3: Resolver-side gate**

In the registry's `ResolveCtx` (resolver.go — find the Tier-2
discriminator block), add an early return when `ReceiverType`
starts with `"_external."`:

```go
// At the top of ResolveCtx, before fuzzy fallback:
if strings.HasPrefix(ctx.ReceiverType, "_external.") {
    // The chain walker landed on an external-crate type; the actual
    // dispatch target isn't in our graph. Drop the call entirely
    // rather than fuzzy-matching the bare name into an unrelated
    // in-graph candidate (the PR #341 dominant FP shape).
    return ResolutionResult{}, false
}
```

**Step 4: Surface the gate in resolution-strategy logs**

When the External gate fires, log at slog.Debug with the rootCrate
+ callee bare name. This gives downstream baseline-comparison
queries visibility into "how many edges did the External gate
drop" without changing the on-disk edge schema.

```go
slog.Debug("tier2.external_drop",
    "root_crate", rootCrate,
    "callee", calleeName,
    "module", moduleQN,
)
```

---

## Task 3: Validation against assetman + Rust subsets

**Finding:** The v0.1 hypothesis is that the dominant `get_result`-
like FP bucket disappears. Verification is via the existing
`compare.py psm-rust` harness.

**Files:** None new. This is a measurement task.

**Step 1: Pre-change baseline (captured before any code change)**

Already exists from PR #341:
- Substrate query: 71 multi-candidate fuzzy edges in assetman
- 25 of 71 are the `get_result → AssetIntrospectImpl.get_result`
  bucket (the diesel external chain)
- assetman scope-aligned F1: 0.855 (per resolver.go:1043 Phase E
  comment, current main with gate Python-only)

**Step 2: Post-change measurement**

After Tasks 1+2 land, re-run:

```bash
# 1. Build v0.1 binary
CGO_ENABLED=1 go build -o /tmp/cmm-tier2-v01.exe ./cmd/codebase-memory-mcp/

# 2. Force-reindex PSM (binary version delta isn't detected by
#    incremental hash check — same gotcha as the v0.3 flow)
/tmp/cmm-tier2-v01.exe cli --raw delete_project \
    '{"project_name":"c-Users-user-Documents-GitHub-psm"}'
bench/accuracy/refresh_psm.sh --skip-embeddings

# 3. Run comparison
python3 bench/accuracy/compare.py psm-rust

# 4. Re-run the Janusian diagnostic to confirm bucket disappearance
python3 bench/accuracy/_query_assetman_janusian.py
```

**Step 3: Pass criteria**

| Metric | Pre-v0.1 | v0.1 target | Hard gate |
|---|---:|---:|---|
| assetman F1 (scope-aligned) | 0.855 | ≥0.88 | required |
| `get_result` bucket size | 25 | ≤5 | required |
| `_external.*`-drop count (slog Debug) | 0 | ≥20 | informational |
| canstatd / calibd / adsbd / apid F1 | (per 2026-05-14 baseline) | ±0.5pp tolerance | required |
| Loc-Bench iter=2 (file / class / func) | 86.0 / 84.5 / 73.5 | within ±2pp | required |
| `go test ./internal/pipeline/ -count=1` | pass | pass | required |

---

## Considered alternatives (not v0.1)

### Drop on chain-root unresolved instead of fuzzy-fall-through

Considered: when chain walker returns `resolved=false`, ALWAYS drop
the edge (skip fuzzy entirely). This is simpler than the
external-sentinel approach but craters recall — many chains
legitimately fail to resolve because the type system is hard
(closures, fn pointers, dyn dispatch) and the fuzzy fallback DOES
catch real TPs on those.

Rejected: hurts recall on `_unknown`-state chains, which include
real TPs the gate shouldn't touch.

### Skip the chain walker; gate purely on `use external_crate::*` imports

Considered: parse `use` statements at module level, build a set of
"external-bound names" per module, drop the call when the bare
callee bare-name matches no internal candidate AND the receiver
identifier is external-bound. Doesn't require chain walker changes.

Rejected: doesn't handle the `diesel::insert_into(...).method` case
because the chain root here is a STATIC CALL PATH, not a bound name.
The `use diesel::insert_into` import is sometimes there, sometimes
not; relying on `use` alone misses about half the FPs.

### Combine v0.1 + v0.2 in one PR

Considered: ship external-chain awareness AND generic-bound
receivers together. Faster overall path to the F1 ceiling.

Rejected: scope-discipline.md says one lever per PR. v0.1 is the
high-confidence pure-precision win; v0.2 is a precision+confidence
quality improvement with smaller raw F1 impact. Separating them
makes the per-slice measurement clean.

---

## Risks (per `verify-instrument-before-fix.md`)

1. **Cargo-metadata cost on large workspaces.** PSM has 275 packages;
   cargo metadata --no-deps takes ~1.5 seconds locally. Mitigation:
   already wrapped in a 30s timeout; failures fall back to empty
   external set (no behavior change). Budget: 5 seconds per index.
2. **External set false negatives.** A vendored dep that's not in
   `Cargo.toml` would be missed (e.g., a `[patch.crates-io]` entry
   pointing at a local path). Mitigation: only matters for FP
   suppression; missed externals produce status-quo behavior, not
   regressions. No need to fix in v0.1.
3. **External set false positives.** A workspace member named the
   same as an external crate would be misclassified. Mitigation:
   already handled — the `workspaceMembers` check overrides the
   external classification (Task 1 implementation).
4. **Chain-root parsing edge cases.** Multi-line chains and turbofish
   calls (`Vec::<T>::new()`) may not produce a clean `crate::path`
   rootName. Mitigation: ACC-008 already normalizes multi-line
   callees; turbofish is handled by ACC-002. The rootName text is
   trustworthy at the chain walker's input.
5. **Sentinel collision.** A real crate named `_external` would
   collide with our sentinel prefix. Mitigation: the leading
   underscore makes `_external` an invalid Rust crate name (Rust
   crate names can't start with `_`); no real collision possible.
6. **Implementation cost overrun**. If Task 2 turns out to be more
   invasive than estimated (e.g., the chain walker needs deeper
   refactoring to thread the new state), STOP and re-scope rather
   than blowing the v0.1 budget. Per `scope-discipline.md`:
   v0.1 should ship in ~1 week; if it's looking like 2+, split into
   smaller PRs.

## Non-goals

1. **Not v0.2 / v0.3.** Generic-bound receivers and std-unwrap
   chains are explicitly deferred. See `TIER2_RECEIVER_TYPE_DESIGN.md`.
2. **Not removing the existing fuzzy fallback.** The fallback stays
   as the `_unknown`-state path. v0.1 adds the `_external`-state
   path; it doesn't remove anything.
3. **Not addressing non-Rust languages.** Cargo metadata is
   Rust-specific. Python / TS / Go projects skip the new pass and
   behave identically.
4. **Not introducing new edge types.** External-classified chains
   produce NO edge (drop, don't emit). The `CALLS` schema is
   unchanged.
5. **Not exposing a new env var by default.** v0.1 enables the
   external-drop unconditionally for Rust. If a future incident
   warrants an opt-out, add `RESOLVER_TIER2_EXTERNAL_DROP` then —
   but per the `feedback_no-opt-ins` memory, smart defaults beat
   opt-ins; ship enabled.

## Cross-references

- Design doc: `TIER2_RECEIVER_TYPE_DESIGN.md` (this directory)
- Failure-mode source: `bench/research/2026-05-23-fuzzy-janusian-tp-loss-analysis.md`
- Recoverable-ceiling estimate: `bench/research/2026-05-10-assetman-janusian-finding.md`
- Existing chain walker: `internal/pipeline/pipeline.go:2145-2308`
- Existing PerFuncTypeMap: `internal/pipeline/typeinfer.go`
- Existing ResolveCtx / receiver type: `internal/pipeline/resolver.go:125-145`
- Cargo-metadata STOP finding (now superseded — at the crate-name
  abstraction this primitive IS the right tool): see PR thread
  prior to v0.3 implementation arc.
