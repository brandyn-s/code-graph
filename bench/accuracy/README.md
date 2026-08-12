# code-graph edge-level accuracy harness

Measures precision/recall/F1 of code-graph's structural edge extraction against
independent oracles, on SHA-pinned fixtures. Freezes baselines so future
improvements are measurable.

**Goal**: answer "is code-graph's CALLS extraction above 85% recall / 60%
precision?" with a number, not a guess. Sui et al. (ICSE 2020) reported
median 0.884 recall / 20-30% precision for Java static analyzers; we adjust
the precision target up because Python/Go code has less reflection
complexity than Java.

## Why this is LLM-only but not circular

code-graph already consumes LSP internally (`internal/cbm/cbm.go`), so LSP
can't serve as an independent oracle. Instead:

| Edge type | Language | Oracle | Trust |
|-----------|----------|--------|-------|
| CALLS     | Python   | PyCG (static analysis; peer-reviewed micro-benchmark) | High |
| IMPORTS   | Python   | Python `ast` stdlib (deterministic) | High |
| HTTP_CALLS | all     | Opus + Sonnet ensemble with 2/3 majority tiebreaker | Medium |
| CALLS     | Rust     | `syn` 2.x visitor (AST-level, matches code-graph's tree-sitter granularity) | High |
| IMPORTS   | Rust     | (not measured — code-graph's Rust IMPORTS resolver emits few edges in practice) | N/A |
| CALLS     | Go       | `go callgraph -algo=rta` (RTA matches code-graph's gopls-informed extraction) | High — but see caveats |
| IMPORTS   | Go       | `go list -json` (authoritative from the Go toolchain) | High |

None require human verification. Ground truth is frozen to JSON and
re-used for every future regression run.

## Directory layout

```
bench/accuracy/
  __init__.py
  README.md              this file
  fixtures.json          SHA-pinned fixtures (initial: mcp-servers @ 81fa7d5)
  common.py              Edge dataclass, SHA verification, subprocess wrapper
  oracle_pycg.py             CALLS ground truth via PyCG (Python)
  oracle_ast_imports.py      IMPORTS ground truth via Python ast
  oracle_llm_ensemble.py     HTTP_CALLS ground truth via Opus+Sonnet
  oracle_rust_syn.py         CALLS ground truth via syn 2.x (Rust)
  oracle_go_callgraph.py     CALLS + IMPORTS ground truth via go callgraph (Go)
  compare.py                 TP/FP/FN/P/R/F1 reporter
  run_baseline.py            orchestrator: verifies SHA, runs all oracles, runs code-graph, produces report
  tools/oracle-rust-syn/     Cargo crate for the Rust syn-based oracle binary
  synthetic/                 hand-authored fixtures with hand-enumerated ground truth
    rust-minimal/            used by prove-the-instrument gate
    go-minimal/
  cache/                     per-(fixture, sha, tool) oracle output cache
  baselines/                 frozen baseline reports — committed to git
```

## Reproduction

```bash
# One-time: ensure `uv` is installed (https://github.com/astral-sh/uv).
# PyCG needs Python 3.11 + 3 local patches; `_env.py` provisions that venv
# idempotently on first run. No manual setup required.

pip install anthropic   # only LLM-ensemble oracle needs this on the system python

# Verify binary is current:
CGO_ENABLED=1 go build -o bin/codebase-memory-mcp.exe ./cmd/codebase-memory-mcp/

# Run each oracle + compare:
python bench/accuracy/oracle_pycg.py mcp-servers       # CALLS  (auto-bootstraps bench venv + patches)
python bench/accuracy/oracle_ast_imports.py mcp-servers # IMPORTS (stdlib only)
python bench/accuracy/oracle_llm_ensemble.py mcp-servers # HTTP_CALLS (needs ANTHROPIC_API_KEY)
python bench/accuracy/compare.py mcp-servers

# Rust: first ensure the syn oracle binary is built (auto-bootstraps on first call)
python bench/accuracy/oracle_rust_syn.py psm-rust
# Index each subset first (use skip_report=true to respect read-only fixtures)
python bench/accuracy/compare.py psm-rust

# Go: requires `go install golang.org/x/tools/cmd/callgraph@latest`
python bench/accuracy/oracle_go_callgraph.py code-graph-go

# Independent compiler-tier CALLS oracle (Go SSA/RTA; no SCIP/graph truth)
cd bench/accuracy/tools/oracle-go-rta
go test ./...
go run . /path/to/go/module > oracle.json
cd ../../../..
python bench/accuracy/compare_compiler_calls.py \
  --oracle oracle.json \
  --database /path/to/project.db \
  --project project-name \
  --scip-index /path/to/index.scip \
  --output compiler-report.json

# Independent compiler-tier CALLS oracle (TypeScript compiler API)
cd bench/accuracy/tools/oracle-typescript-compiler
npm ci
npm test
node main.cjs /path/to/tsconfig.json > oracle.json
cd ../../../..
python bench/accuracy/compare_compiler_calls.py \
  --oracle oracle.json \
  --database /path/to/project.db \
  --project project-name \
  --scip-index /path/to/index.scip \
  --output compiler-report.json
# Output:
#   bench/accuracy/baselines/YYYY-MM-DD-mcp-servers-report.md   (human)
#   bench/accuracy/baselines/YYYY-MM-DD-mcp-servers-report.json (machine)
```

## Prove-the-instrument gate

Before measuring code-graph on a real fixture, every oracle must first pass a
synthetic-fixture gate: hand-authored source with hand-enumerable ground
truth, verified FP=0 FN=0. This catches oracle bugs before they pollute a
published baseline (see `rules/verify-effectiveness.md` for the full rule).

```bash
# Rust syn oracle: 8 expected edges (6 CALLS + 2 IMPORTS)
./bench/accuracy/tools/oracle-rust-syn/target/release/oracle-rust-syn.exe \
  bench/accuracy/synthetic/rust-minimal rust-minimal

# Go callgraph oracle: 5 expected CALLS after init-chain filter
cd bench/accuracy/synthetic/go-minimal && \
  ~/go/bin/callgraph.exe -algo=rta -format=graphviz ./...
```

Ground truth lives in each synthetic fixture's `ground_truth.json`.

### Pre-provisioning the bench venv (CI, first-timers)

```bash
python bench/accuracy/_env.py
```

Creates `~/.cache/code-graph-bench/py311/` (uv-managed), installs
`setuptools<81` + `pycg==0.0.7`, and applies three patches to
`pycg/machinery/imports.py` that make PyCG work on Python 3.11+.
Idempotent — subsequent calls return the cached interpreter path.

## Environment

- `AWS_BEARER_TOKEN_BEDROCK` — required for LLM ensemble oracle; refresh via `aws sso login` if 401.
- `ANTHROPIC_MODEL_OPUS` — defaults to `claude-opus-4-7` (override for testing).
- `ANTHROPIC_MODEL_SONNET` — defaults to `claude-sonnet-4-6`.
- Code-graph binary at `bin/codebase-memory-mcp.exe` in this repo.
- Fixture repos at paths in `fixtures.json`; must be at pinned SHA (harness exits 2 on drift).

## Regression workflow

1. Run baseline, commit `baselines/YYYY-MM-DD-<fixture>-report.{md,json}` to git.
2. Make an improvement (edit Go, rebuild binary).
3. Re-run baseline. The compare tool reports `was X, now Y, delta Z` against the most recent baseline JSON.
4. If numbers improved: commit the new baseline as the new reference. If they regressed: revert or fix.

## Current-state snapshot (no re-baselining)

`snapshot.py` re-formats the latest `baselines/*.json` per fixture into
`CURRENT.md` with per-fixture freshness bands (FRESH / STALE / OLD /
UNKNOWN) computed from baseline mtime vs binary mtime. It does **not**
re-index, re-run oracles, or re-run compare — it's a pure re-formatter
so a grader can see at-a-glance which on-disk metrics still reflect the
current binary.

```bash
python bench/accuracy/snapshot.py             # writes bench/accuracy/CURRENT.md
python bench/accuracy/snapshot.py --print     # stdout instead of file
```

When grading code-graph (or answering "what's the current accuracy
state?"), read `CURRENT.md` FIRST; consult the leverage-doc
(`baselines/YYYY-MM-DD-accuracy-gap-inventory.md`) only for "what's
open after the latest fixes" — those two are different artifacts and
go stale on different timelines.

## PR checklist — when does a PR require re-baselining?

PRs that touch accuracy-sensitive code MUST re-baseline at least one
fixture per affected language before merging, and commit the updated
`baselines/YYYY-MM-DD-<fixture>-report.{md,json}` files in the same PR.

Trigger paths (any change under these directories REQUIRES a fresh baseline):

- `internal/resolver/` — call-resolution rules, strategy selection,
  precision gates
- `internal/extractor/` — language-specific edge extractors
- `internal/cbm/` — tree-sitter LSP-style type resolution
- `internal/pipeline/` — multi-pass indexing, sequencing

Minimum re-baseline scope per affected directory:

| Touched | Re-baseline at least |
|---|---|
| Python parser/resolver | `mcp-servers` (production Python) + one adversarial Python fixture |
| Rust parser/resolver | `psm-rust` (production Rust, all subsets) |
| Go parser/resolver | `code-graph-go` (production Go) + `cobra-go` |
| Pipeline / cross-cutting | All four production fixtures |

Why this is mechanical, not nightly cron: the harness takes 10-30 min
per fixture (re-index + oracle + compare). Authors who just modified
the affected code already have the build hot; one re-baseline in their
PR is cheap. A nightly cron over all fixtures would be hours of CI
budget for marginal value (most days nothing accuracy-sensitive
changes).

Reviewer check: in PRs that touch the trigger paths, confirm
`bench/accuracy/baselines/` has at least one new file dated within the
last 24 hours. If absent, request re-baselining before approval.

## Metrics reported

Per edge type, three metrics:

- **Exact**: strict `(from_qn, to_qn, type)` equality. Baseline number — may be depressed by scope mismatches.
- **Suffix-3**: permissive match on the last 3 QN segments. Useful for
  spotting QN-drift artifacts between oracle and code-graph.
- **Scope-aligned**: restricted to edges whose caller is in the oracle's
  analyzed-caller set. This is the apples-to-apples accuracy reading —
  it excludes code-graph edges from callers the oracle never reached
  (e.g., test files PyCG's entry-point analysis doesn't touch).

The gap between "Exact" and "Scope-aligned" is the *scope-mismatch
artifact* — not an accuracy bug. For mcp-servers CALLS, raw exact
precision is 14.5% but scope-aligned is 93.5% because code-graph indexes
the full repo while PyCG only walks 5 service entry points.

## Known limitations

- **PyCG is flow-insensitive**; it will have its own false negatives on dynamic dispatch. We measure comparative drift, not absolute truth.
- **LLM ensemble non-determinism**: runs are cached per `(file_sha, model)` so results are reproducible across re-runs, but cache invalidation on file change means the first run of a new fixture SHA is stochastic.
- **Rust oracle is syntactic**: `syn` parses unexpanded source (same as code-graph's tree-sitter). Method-call receiver types are unresolved on both sides, so bare-method-call edges drop from both. This is apples-to-apples.
- **Rust IMPORTS dropped from measurement**: code-graph's `use crate_name::Type` resolver is incomplete (empirically 0 edges on a canstatd re-index, 8 across the full 260-crate psm). The oracle drops IMPORTS rather than report a misleading F1.
- **Go QN alignment**: `go callgraph` emits Go-native symbols (`github.com/org/repo/pkg.Func`) while code-graph stores sanitized-path QNs (`c-Users-...pkg.file.Func`). Fully aligning these requires per-file `go/ast` walking to know which `.go` file each function lives in. The current oracle is plumbed end-to-end but the QN normalization for multi-file packages is deferred.
- **Oracle scope ≠ code-graph scope**: oracles only walk source files with explicit `fn`/`def`; code-graph also indexes Cargo.toml (as config nodes), infrascan artifacts, and `diesel` query DSL macros. These show up as legitimate code-graph edges the oracle doesn't see. Scope-aligned metric filters this artifact.
