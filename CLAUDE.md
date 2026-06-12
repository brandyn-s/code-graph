# CLAUDE.md

redacted fork of codebase-memory-mcp. Persistent code knowledge graph MCP server with security extensions.

## Key Commands

```bash
# Build (Makefile static-links the MinGW runtime on Windows — required)
make build

# Without make on Windows: KEEP -extldflags '-static'. Omitting it makes the .exe
# depend on libwinpthread-1.dll and die STATUS_ENTRYPOINT_NOT_FOUND (0xC0000139)
# when spawned with a stripped PATH (e.g. Claude Code MCP stdio). See PR #98.
CGO_ENABLED=1 go build -ldflags "-extldflags '-static'" -o bin/codebase-memory-mcp.exe ./cmd/codebase-memory-mcp/

# Linux/macOS (no special linker flags needed):
CGO_ENABLED=1 go build -o bin/codebase-memory-mcp ./cmd/codebase-memory-mcp/

# Test
go test ./... -count=1

# Lint (golangci-lint v2.10)
golangci-lint run ./...

# Format
gofmt -w .
```

## Architecture

- **Graph storage**: SQLite WAL mode at `~/.cache/codebase-memory-mcp/`. Louvain community detection for clustering.
- **Parsing**: tree-sitter AST for 27 languages via vendored C grammars (CGO). Go gets enhanced LSP-style type resolution. 38 unused grammars were cut on 2026-06-10 (usage audit: none of the 38 appeared in any redacted repo; the 10 largest grammars were all unused — ~390MB of 770MB vendored source). PowerShell was added in the same change (airbus-cert/tree-sitter-powershell @ d3984418, MIT) — used by 2 redacted repos that previously had no coverage. Restoring a cut language = restore its four files from git history at that commit: `internal/cbm/vendored/grammars/<dir>/`, `internal/cbm/grammar_<name>.c`, `internal/lang/<name>.go`, plus the enum/spec/switch entries in `internal/cbm/cbm.h`, `internal/cbm/lang_specs.c`, `internal/cbm/cbm.go`, and `internal/lang/lang.go`.
- **Pipeline**: Multi-pass indexing (structure -> definitions -> calls -> HTTP links -> OPA policy -> communities -> tests)
- **Cypher engine**: Custom lexer/parser/planner/executor. Read-only subset with variable-length paths.
- **Auto-sync**: Background watcher polls mtime+size, triggers incremental reindex. Adaptive polling intervals.
- **Security tools**: `query_security_surfaces` (auth/crypto/input patterns), `query_stig_evidence` (control -> code mapping), `trace_data_flow` (sensitive data paths)
- **Skills**: 4 embedded skills (exploring, tracing, quality, reference) installed via `codebase-memory-mcp install`

## Testing

```bash
# All tests
go test ./... -count=1

# Language parity (125+ cases)
go test ./internal/pipeline/ -run TestLangParity -v

# AST structure (90+ cases)
go test ./internal/pipeline/ -run TestASTDump -v

# Integration
go test ./internal/pipeline/ -run TestPipeline -v
```

## redacted Additions (beyond upstream)

### Security & Compliance Tools
| Tool | Source File | Purpose |
|------|-----------|---------|
| `query_security_surfaces` | `internal/tools/security.go` | Auth, crypto, input validation patterns with taint analysis (source subtypes, sanitizer nodes) |
| `trace_data_flow` | `internal/tools/dataflow.go` | Sensitive data path analysis with env var propagation tracking |
| `query_stig_evidence` | `internal/tools/stig_evidence.go` | STIG control → code evidence mapping |
| `index_health` | `internal/tools/health.go` | Graph coverage and quality report |

### Service Understanding Tools
| Tool | Source File | Purpose |
|------|-----------|---------|
| `explain_service` | `internal/tools/explain_service.go` | Service-level architecture summary (dependencies, endpoints, config) |
| `service_map` | `internal/tools/service_map.go` | Cross-service dependency map with noise filtering |
| `diff_services` | `internal/tools/diff_services.go` | Compare two services structurally |

> **Scope note**: These tools were built for Corsair's microservice architecture. They work best on repos with clear service boundaries (separate crates/packages, HTTP endpoints, config modules). Single-service or monolith repos get limited value.

### Developer Productivity Tools
| Tool | Source File | Purpose |
|------|-----------|---------|
| `get_affected_tests` | `internal/tools/affected_tests.go` | Find tests impacted by a code change |
| `detect_cycles` | `internal/tools/cycles.go` | Circular dependency detection with noise filtering |
| `explain_symbol` | `internal/tools/explain.go` | Explain what a symbol does with callers/callees/context |
| `get_change_coupling` | `internal/tools/change_coupling.go` | Files that historically co-change (from git history) |

### Review & Context Tools
| Tool | Source File | Purpose |
|------|-----------|---------|
| `get_review_context` | `internal/tools/review_context.go` | PR review context: what a change touches and what depends on it |
| `get_relevant_context` | `internal/tools/relevant_context.go` | Graph-based file context selection for LLM agents (callers, callees, tests, coupled files) |
| `visualize` | `internal/tools/visualize.go` | HTML graph visualization of node neighborhoods |
| `diff_graph` | `internal/tools/graph_diff.go` | Symbol-level delta between two arbitrary git revisions |
| `find_rationale` | `internal/tools/tools.go` | Extract WHY/NOTE/HACK/SAFETY/TODO annotations as graph nodes |
| `generate_report` | `internal/tools/orientation_report.go` | Write ARCHITECTURE_REPORT.md orientation doc from indexed graph |

### Code Localization & Ranking Tools
| Tool | Source File | Purpose |
|------|-----------|---------|
| `rank_by_query` | `internal/tools/rank.go` | Bidirectional weighted PageRank seeded on query-matched nodes; returns top-K most relevant entities. Best for SPECIFIC SYMBOL queries. (Aider repo-map pattern) |
| `code_localize` | `internal/tools/localize.go` | LocAgent BFS-style graph-guided localization: seed-match + bidirectional BFS over CALLS/DEFINES/IMPORTS/CONTAINS edges. Best for SPECIFIC SYMBOL queries. Primitives-only, deterministic, ~50ms. (LocAgent ACL 2025) |
| `code_localize_agent` | `internal/tools/localize_agent.go` | LLM-driven LocAgent variant: wraps the primitives in a multi-turn agent loop. Best for VERBOSE natural-language issues where the issue talks about A but the fix is in B. ~30-60s wall, ~$0.04-0.05/query at Haiku 4.5. Requires `ANTHROPIC_API_KEY`. |

> **Pick by query shape**: short symbol names → `rank_by_query` / `code_localize`. Multi-paragraph natural-language issue → `code_localize_agent`. Both primitive tools accept `seed_strategy`: `substring` (legacy), `embedding` (Voyage cosine), or `hybrid` (default; substring + embedding deduped, falls back to substring if no `VOYAGE_API_KEY`).

#### Measured Loc-Bench accuracy

n=16 sample (structured scorer, 2026-04-25, illustrative only — sampling noise dominates):

| Mode | File | Class | Func |
|------|------|-------|------|
| substring-primitives | 38% | 6% | 12% |
| hybrid-primitives | 44% | 6% | 19% |
| `code_localize_agent` (default config) | 94% | 50% | 88% |

n=200 measurement (2026-05-04, 4 workers, hybrid-agent, **iter=2 / MRR aggregation**, structured scorer):

| Mode | File Acc@10 | Class Acc@10 | Func Acc@10 |
|------|------|-------|------|
| `code_localize_agent` (Haiku 4.5, iter=2) | **86.0%** | **84.5%** | **73.5%** |

Avg cost $0.048/query, total $9.60. Indexed 200/200. See `bench/accuracy/baselines/2026-05-04-loc-bench-n200-iter2.md`.

Single-shot (iter=1) baseline (n=200, 2026-05-03): file=82.5%, class=46.5%, func=61.0%. The non-monotone class-gap (`func > class`) at iter=1 was an iteration-count artifact — single-shot picked semantically-near-but-not-canonical classes; MRR aggregation across iter=2 stabilizes on the right one (+38pp class lift). Phase A class-gap diagnosis closed by data, not by sample inspection.

**The defended number is the iter=2 measurement above.** iter=2 is the production default (`LOCAGENT_ITERATIONS=2`).

> **Reproduction status (2026-05-16)**: the 2026-05-12 re-run is **REFUSED** for publication — 142/200 instances indexed because 58 PR `base_commit` SHAs have been GC'd on GitHub since 2026-05-04 (see `bench/accuracy/baselines/2026-05-12-loc-bench-unreachable-tail-finding.md`). On the indexed subset of 142, the numbers shifted to file=80.3% / class=80.3% / func=73.9% — but the missing 67 instances are category-skewed (42% Security Vulnerability), so the May-4 → May-12 delta is not apples-to-apples. The 86.0/84.5/73.5 numbers remain the defended baseline as a *historical measurement*; re-baselining on the current reachable corpus is required before citing externally.

#### Methodology caveats — read before citing the numbers above externally

The 86.0% / 84.5% / 73.5% numbers are not directly comparable to the LocAgent paper's Loc-Bench numbers without three caveats:

1. **Sample size**: our n=200 is a sub-sample of Loc-Bench's published n=560. Bootstrap CI on n=200 is wide enough that ±2pp shifts are within sampling noise. Do NOT cite these as "matches paper Claude-3.5" without re-running on the full n=560 corpus.

2. **Scorer**: we use a structured scorer (`bench/research/eval_locbench_*.py`) that judges file/class/function match by expected_paths YAML. The paper's scorer is not directly reproduced in our harness; differences in canonicalization (e.g., `Class.method` vs `module.Class.method`) can shift class accuracy by several percentage points without any underlying capability change.

3. **iter=2 / MRR aggregation produced a +38pp class lift over iter=1** (2026-05-04 baseline). This is too large to be ordinary sampling noise — it's a protocol-level effect from how multi-shot results aggregate. The single-shot iter=1 number (class=46.5%) is a better lower bound for raw localization capability; iter=2 (class=84.5%) reflects the production protocol's choice to run multiple agents and rank by MRR. Both are honest measurements of different things.

When the next plan ships the joint-experiment failure audit (Plan 1 Phase C1 — `bench/research/unresolved_call_shapes.py` + 50-100 case classification), the iter=1 vs iter=2 ablation will surface the scorer/protocol component vs the capability component separately. Until then, treat the iter=2 numbers as production-protocol-effective and the iter=1 numbers as capability-lower-bound.

#### Comparison vs LocAgent paper (ACL 2025, arXiv 2503.09089)

LocAgent reports two main results — pay attention to which dataset:

| Benchmark | Model | File Acc@5 | File Acc@10 | Notes |
|---|---|---|---|---|
| **SWE-Bench-Lite** (n=274) | Qwen2.5-32B(ft) | **92.70%** | — | Their headline number. Different benchmark — SWE-Bench is bug-report-heavy and the 32B model is **fine-tuned** on SWE-Bench training data |
| **SWE-Bench-Lite** (n=274) | Claude-3.5 | 94.16% | — | Different benchmark |
| **Loc-Bench** (n=560) | Claude-3.5 | 83.39% | **86.07%** | Apples-to-apples vs us, but uses much more expensive model |
| **Loc-Bench** (n=560) | Qwen2.5-7B(ft) | 78.57% | **79.64%** | **Our peer comparison** — fine-tuned 7B open model |

We're running on **Loc-Bench with Haiku 4.5** at $0.05/query — at par with their **Qwen2.5-7B(ft) at $0.05/query**. To match their Claude-3.5 result (~86%), we'd need to use Sonnet/Claude 3.5 at ~$0.66/query (13x cost).

The earlier "we exceed 92.7%" claim in this doc was apples-to-oranges (n=16 Loc-Bench vs n=274 SWE-Bench-Lite). Removed.

#### Agent loop env vars (all opt-out — defaults are the measured-best config)

| Env var | Default | Purpose |
|---------|---------|---------|
| `LOCAGENT_ITERATIONS` | `2` | Number of agent iterations to run, then aggregate by mean reciprocal rank (MRR). Matches LocAgent paper's self-consistency (Section 3.2). Set to `1` for single-shot (legacy behavior, ~50% cost) |
| `LOCAGENT_PARALLEL` | unset | Set to `1` to dispatch the N independent iterations concurrently instead of serially. iter=2 is independent-sampling-with-MRR (no conditioning between iterations), so parallel is semantically safe. Tradeoff: ~50% wall-time reduction at iter=2 vs serial; rate-limit risk on tier-0/1 Anthropic accounts (the anthropic package's retry logic handles 429s). Plan 4 D2 falsifier (2026-05-06): synthetic test shows 3.1x speedup at N=3 with stub runOnce; real Loc-Bench wall-time delta TBD pending eval. |
| `LOCAGENT_PROMPT_VARIANT` | `open` | `open` uses LocAgent's 4-step CoT (categorize → link → trace → locate); `aggressive` reverts to a tighter 5-turn budget |
| `LOCAGENT_BFS_DEPTH` | `4` | BFS depth for `code_localize` inside the agent loop |
| `LOCAGENT_MAX_TURNS` | `20` | Hard cap; `open` prompt soft-targets 8 turns |
| `LOCAGENT_REWRITE` | unset | Set to `1` to enable a Haiku pre-step that extracts focused search terms. Measured to **regress** results on n=16; available for further experimentation |
| `EMBEDDING_SEED_MIN_COSINE` | `0.0` | Minimum cosine similarity for embedding seeds. PR #84 set this to 0.65 based on n=1; PR #91 reverted after n=16 showed the threshold filtered useful seeds |
| `ANTHROPIC_MODEL` | `claude-haiku-4-5-20251001` | Override to opt into Sonnet/Opus. Per LocAgent Loc-Bench results, Claude-3.5 hits ~86% file Acc@10 vs Qwen-7B(ft) ~80% — but at 13x cost ($0.66 vs $0.05/query) |

### Pipeline Additions
- OPA policy linker (`POLICY_GATES` edges connecting policy to enforced code)
- Terraform env var cross-referencing (`EnvVar` graph nodes)
- Lockfile parsing (dependency graph from package managers)
- Security tagging pass (labels nodes as auth/crypto/input/hardware_io)
- LRU query cache for `search_graph` and `query_graph`

### Resolver env vars

| Env var | Default | Purpose |
|---------|---------|---------|
| `CBM_MAX_FILE_BYTES` | unset (1MB default) | Per-file size cutoff for full-mode discovery (2026-06-10). Source files above the cutoff are skipped — above 1MB they are essentially always generated (parser tables, bundled assets, generated bindings): on this repo 44 such files carried 96% of indexable bytes and the definitions pass dropped 50.1s -> 2.8s (total index 82s -> 11s) with only generated-code nodes lost. Positive integer = cutoff in bytes; `0` = unlimited (pre-2026-06-10 behavior); unset/invalid = 1MB. Fast mode keeps its existing 512KB cutoff. |
| `CBM_SCIP_INDEX_PATH` | unset (off) | Opt-in precision tier (2026-06-10). Path to a SCIP index (`scip-go`, `rust-analyzer scip`, `scip-typescript`, `scip-python`, ...) for the repo being indexed. When set, the post-flush `scip_ingest` pass REPLACES heuristic CALLS edges for every file the index covers with compiler-grade edges derived from the index's occurrences (call-shaped references of function symbols, attributed to enclosing Function/Method nodes by span containment); files outside the index keep heuristic edges (precise-over-heuristic layering, same as Sourcegraph/Glean). Replacement requires BOTH endpoints index-covered — callees in files the indexer can't see (CGO sources, platform-gated files) keep their heuristic edges — and documents whose definition sites no longer match current node spans (stale index) are excluded from deletion AND derivation with a `drifted_files` warning. Derived edges carry `resolver_rule: "scip-ingest"`. Ground-truth eval vs the go/ast oracle (2026-06-10, this repo): covered-universe F1 0.931 → 0.962, recall 0.953 → 0.996. Measured on code-graph itself with scip-go (2026-06-10): 4,593 edges agree with the heuristic resolver; 969 heuristic-only edges (~58% generic-method-name fuzzy FPs like `.Error()`) removed; 830 SCIP-only edges (81% typed-receiver method calls the resolver missed) added. Ingest failures degrade to a warning — indexing never fails because of a bad index. Tests: `internal/pipeline/scip_ingest_test.go`. **Validated on production Rust (tunnl @ 274e53b, 2026-06-11)**: 19/19 documents, drifted=0, 93 heuristic edges replaced / 118 SCIP edges inserted, fuzzy bucket 5→0 (all spot-checked as external-crate FPs), drift guard verified both directions — recommended config for Rust repos (`rust-analyzer scip . --output index.scip`, regenerated at the indexed commit; `force=true` required to re-ingest an unchanged tree). Details: `bench/accuracy/baselines/2026-06-11-scip-tunnl-validation.md`. |
| `RESOLVER_DROP_LOOSE_CROSS_PACKAGE` | unset (production: emit) | When set to any non-empty string, drops emissions in the `cross-package-unique-name` and `cross-package-suffix` resolver-rule sub-buckets at index time. Per the 2026-05-06 sub-bucket-split measurement (PR #234), these two buckets have catastrophic precision (0.00-0.35) on Python adversarial fixtures while being high-precision (0.88-0.95) on Go. The eval harness sets this for Python fixtures to suppress the noise without affecting Go (where dropping would crater recall). The precise `cross-package-import-map` sub-bucket is NOT dropped — it resolves through explicit imported-alias bindings and is high-confidence by construction. Measured impact (2026-05-06): flask-adversarial F1 0.49 → 0.61 (+12pp); requests-adversarial F1 flat. See `bench/accuracy/baselines/2026-05-06-rec1-python-drop-finding.md`. |
| `RESOLVER_REQUIRE_IMPORTS_FOR_LOOSE_CROSS_PACKAGE` | unset (legacy emit-at-half-confidence) | Phase F (Plan 8-Phase Arc, 2026-05-09). When set to any non-empty string, the resolver's `unique_name` and `suffix_match` strategies require an explicit IMPORTS edge from the call site's module to the candidate's package. Candidates that aren't import-reachable are DROPPED instead of emitted at halved confidence. Default unset = current behavior (emit at halved confidence). The existing `RESOLVER_DROP_LOOSE_CROSS_PACKAGE` is more aggressive (Python drops the entire suffix bucket regardless of imports); this gate is more surgical (per-candidate import-reachability). The two are orthogonal — Phase F gate fires for any language with an import map; Python language-drop fires for the suffix bucket regardless. Useful for tightening precision on Go/Rust adversarial-style fixtures where the suffix bucket carries 0.85-0.95 precision but emit-at-half-confidence still leaks. Validate with adversarial Python + Go fixtures before promoting. |
| `RESOLVER_DROP_FUZZY_JANUSIAN_CHAINS` | **Python only by default since 2026-05-14 (Phase E)** | Drops fuzzy resolutions when (a) the call shape is multi-segment (root.field.method), (b) ReceiverType is empty, (c) candidate_set_size >= 2, and (d) all candidates are methods with distinct parent classes — the empirical signature of Janusian co-hallucinations. Phase A (2026-05-14) flipped this gate default-on globally; Phase E re-scoped to Python only after the PSM Rust re-baseline showed assetman regressed -2.2pp F1 because Rust trait dispatch genuinely produces distinct-parent candidate sets where the fuzzy resolver guesses correctly ~70% of the time (the gate killed 29 TPs vs 18 FPs). On Python adversarial fixtures the same bucket has precision 0.00 (0/11 on flask), so the gate is pure-win there. Env: `=1`/`true`/`yes` forces ON for every language (preserves opt-in Rust experimentation); `=0`/`false`/`no` forces OFF globally; unset = Python-only default. **Note**: the "29 TPs (trait dispatches to `get_result`, `run_effect`, etc.)" attribution in resolver.go:1043 is contested by `bench/research/2026-05-23-fuzzy-janusian-tp-loss-analysis.md` — direct source reading showed the dominant `get_result` bucket (25 of 61 gate-eligible edges) is diesel external-crate dispatch fuzzy-matched to in-graph candidates, not real trait dispatch. The actual recoverable TP count is closer to 10-15; the rest of the "29" is oracle artifact. Recommended lever: upstream Tier-2 receiver-type resolution, not gate-predicate tuning. |
| `RESOLVER_EMIT_ENUM_VARIANT_AS_PARENT` | unset (off) | Phase A''''-2 opt-in (2026-05-14). When set to any non-empty string, `Enum::Variant` call sites (e.g. `PostReloadResult::Rejected`) emit a CALLS edge to the parent Enum's QN when the variant-child node doesn't exist in the registry. Motivation: the C extractor doesn't emit individual EnumVariant nodes, so the variant-child QN is never registered and `resolveViaTypeStaticDispatch` drops these calls. Phase A''''-2 found these account for ~10+ extracted-but-dropped calls on assetman (PostReloadResult, CMSClientError, Revision enum variants). Emission target is the parent Enum, NOT a synthetic variant — whether this improves F1 against the syn oracle depends on whether `compare.py`'s impl-normalize / generic-normalize passes accept parent-QN as a match. Default off; production-default flip requires PSM Rust bootstrap CI per `eval-shipping-discipline.md`. Test coverage: `internal/pipeline/type_static_test.go::TestTypeStaticDispatch_EnumVariantOptInEmitsToEnum`. |

## Test Conventions

### Zero-value filter activation

When adding a filter field to `SearchParams` (or any struct where callers use `{}` literal construction), the filter MUST activate only on explicitly-set values, not the zero value. Use `> 0` / `!= ""` / `len(x) > 0` as the gate — never `>= 0` on an int field whose zero means "off".

Every new filter field needs a test with `SearchParams{}` default-constructed that asserts the filter is inactive. One line, catches the class of bug that PR #61 hit (MinComplexity=0 from zero-value activated the filter and broke snippet tests).

```go
func TestSearchParamsZeroValue_<field>_Inactive(t *testing.T) {
    // With SearchParams{}, <field> is zero; filter must be OFF.
    results, err := store.Search(ctx, SearchParams{Query: "x"})
    // ... assert results match baseline (filter not applied)
}
```

## Recovery procedures

When `~/.claude/scripts/verify-indexes.py` reports a code-graph DB has issues
(or `PRAGMA integrity_check` returns non-`ok`), use this table to map the
failure shape to the right action. Each row references the test that pins
the procedure's correctness — run the test if you're modifying recovery code
and want to confirm the procedure still works.

Full taxonomy: `internal/store/RECOVERY_TAXONOMY.md` (7 modes + 1 config-DB mode + Out-of-scope).

| Failure shape | Detection signal | Operator action | Test |
|---|---|---|---|
| WAL truncated (power loss) | None — silent and correct | None | `TestRecoverFromTruncatedWAL` |
| Indexer killed mid-pass (WAL mode) | Missing data; `integrity_check` passes | Re-run `index_repository` (incremental) | `TestRecoverFromUnflushedTransaction` |
| `.db-shm` deleted | None — SQLite re-creates | None | `TestRecoverFromMissingShm` |
| Corrupt header (`file is not a database`) | `OpenPath` returns wrapped error | `delete_project` + `index_repository(force=true)` | `TestCorruptHeaderReturnsActionableError` |
| Main `.db` deleted with orphan `-wal`/`-shm` | `OpenPath` returns "main DB missing but sidecar files present" | If unintentional: restore from backup before next open. Otherwise: `delete_project` + `index_repository` | `TestMissingDBWithOrphanSidecarReturnsError` |
| Concurrent writers contention | None at N≤3; `database is locked` may escape at N≥10 | Retry the operation; serialization is automatic | `TestConcurrentWritersSerialize` (N=2/3/10) |
| BulkWrite crash (MEMORY journal) | **`OpenPath` returns "Mode 7 corruption" via crash-marker + `PRAGMA quick_check`** | `delete_project` + `index_repository(force=true)` | `TestBulkWriteCrashMarkerSurfacesOnReopen`, `TestBulkWriteMarkerCorruptDBSurfacesError` |

End-to-end procedure (Mode 4 corruption → fresh re-index) is pinned by `TestEndToEndCorruptionRecovery`.

```bash
# Run the full recovery test suite
go test ./internal/store/ -run "TestRecover|TestBulkWrite|TestCorruptHeader|TestMissingDB|TestConcurrent|TestEndToEnd" -v -count=1
```

## Protected Repo

PR required to merge to main. Use `--repo redacted-org/code-graph` with `gh` CLI (the repo was transferred from `redacted-org` to `redacted-org` during the 2026-04-26 personal/director-managed-repos split).
