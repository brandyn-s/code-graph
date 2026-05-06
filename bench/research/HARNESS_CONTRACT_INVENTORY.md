# Harness contract inventory (descriptive, not certified)

**Date**: 2026-05-06
**Author**: original harness author (NOT independent — see roundtable
caveat below)
**Roundtable rec #1**: contract-certification sprint should NOT be
done by the original author. This document is the descriptive
*precursor* — it lists what contracts exist so an independent
reviewer can certify them without first having to discover them.

## Reading this document

For each contract, three columns:

- **Contract**: writer location → reader location.
- **Schema status**: `typed` (passes through `bench/research/schema.py`
  or a Go struct with json tags), `dict` (raw `map[string]any` /
  `dict`), `text` (markdown / plain text — no JSON contract).
- **Suspect-pattern flags**: any of the four bug shapes from the
  original session — `key-rename` risk (writer/reader use string
  keys), `nesting` risk (envelope/wrapper structure), `durability`
  risk (write-once vs incremental), `category-vocabulary` risk
  (string-literal sets that must agree across files).

Author bias caveat: I'm describing what I see. An independent reviewer
should re-grep, re-read, and challenge any "typed" claim.

## Inventory

### 1. Per-case JSON (Loc-Bench eval)

| Aspect | Value |
|---|---|
| Writer | `bench/research/eval_locbench_batch.py::_build_per_case_dict` |
| Readers | `locbench_failure_audit.py`, `d2_accuracy_compare.py` |
| Schema status | **typed** (via `bench/research/schema.py::PerCaseRecord`, PR #229) |
| On-disk shape | `{"schema_version", "n_total", ..., "cases": [PerCaseRecord, ...]}` |
| Suspect flags | none after PR #229; previously hit all 3 (key-rename, nesting, durability). |
| Tests | `test_schema_roundtrip.py` (10), `test_audit_legacy_fallback_quarantine.py` (5), `test_eval_persistence.py` (3) |

This is the contract Phases 1-3 hardened. Default-mode fail-loud after
this PR. Confidence: high.

### 2. Loc-Bench parquet ↔ select_instances category vocabulary

| Aspect | Value |
|---|---|
| Writer | upstream Loc-Bench dataset (parquet authored externally) |
| Reader | `bench/research/eval_locbench_batch.py::select_instances` |
| Schema status | **dict + literal-string set** (the bug class) |
| Suspect flags | **category-vocabulary risk** — `select_instances` references string literals "Bug Report" etc.; parquet uses same string literals in its `category` column. Drift between the two is silent (Bug #1 was 30 days). |
| Tests | `test_select_instances.py` source-grep regression (PR #230) |

The roundtable's S4 finding: source-grep test catches the specific
literal but not "writer changes vocabulary, reader still uses old
strings" — which would still drift silently. **Suggested independent
verification**: behavioral test that loads the parquet and asserts
every distinct category value is recognized by select_instances.

### 3. Per-case YAML scaffold (audit classification)

| Aspect | Value |
|---|---|
| Writer | `locbench_failure_audit.py::emit_todo_yaml` |
| Reader | `locbench_failure_audit.py::analyze_classified` (parses its own output, plus human edits in between) |
| Schema status | **dict** (custom YAML-like format, hand-parsed line-by-line) |
| Suspect flags | **key-rename risk** — `analyze_classified` parses by `s.startswith("bucket:")` etc. Renaming the writer's emit format would silently mis-parse. |
| Tests | `test_audit_propose_bucket.py` (12) — covers heuristic outputs, NOT the YAML round-trip per se. |

**Suggested independent verification**: YAML round-trip test that
writes a fixture via emit_todo_yaml and reads back via
analyze_classified, asserting bucket counts match.

### 4. Throughput JSON

| Aspect | Value |
|---|---|
| Writer | `bench/research/indexing_throughput/main.go` (Go, json.MarshalIndent on `result` struct) |
| Readers | `test_throughput_baseline_shape.py` (8 tests), various ad-hoc inspection scripts (e.g., the `_phase_inspector.py` scripts shipped during Plan 5 Phase D) |
| Schema status | **typed (Go side)** + **dict (Python side)** |
| Suspect flags | **cross-language drift** — Go writer uses `json:"phase"` tags; Python readers grep for `"phase"` literal. If the Go tag changes, the Python tests catch it but ad-hoc readers won't. |
| Tests | `test_throughput_baseline_shape.py` (PR #231) |

The test pins the JSON shape on every existing baseline. Suggested
independent verification: confirm Go struct tags match the test's
REQUIRED_TOP_LEVEL_KEYS exactly (currently 20 fields).

### 5. Pipeline.Progress callback ↔ throughput phase log

| Aspect | Value |
|---|---|
| Writer | `internal/pipeline/*.go` (Pipeline.Progress callback fires during indexing) |
| Reader | `bench/research/indexing_throughput/main.go::p.Progress` callback collects timings |
| Schema status | **callback signature** (`phase string, pct int, detail string`) |
| Suspect flags | **durability risk** — callback fires during the run; if Pipeline.Progress is conditional or skips a phase, the throughput log silently misses it and percentiles are wrong. |
| Tests | NONE direct. `test_throughput_baseline_shape.py` checks that 3 expected phases appear in EXISTING baselines, not that ALL phases fire correctly under arbitrary conditions. |

**Suggested independent verification**: integration test that runs
Pipeline.Run on a hand-controlled fixture and asserts every documented
phase emits a callback exactly once. Roundtable S2's "ad-hoc
inspection" gap.

### 6. Go MCP responses ↔ Python eval consumers

| Aspect | Value |
|---|---|
| Writer | `internal/tools/*.go::handleX` returns `*mcp.CallToolResult` containing JSON text |
| Readers | `bench/research/eval_rank_localize/main.go` consumes the response shape; Python eval consumes that wrapper |
| Schema status | **dict** on the Python side (parsed via the same liberal patterns the audit harness used pre-PR-229) |
| Suspect flags | **nesting + key-rename risk** — Go handler returns `agent_envelope` shape that wraps `code_localize_agent`. The Python parser was the bug-2 site. |
| Tests | `metadata_runtime_test.go` (6 of 33 instrumented handlers) for the Go side; nothing on the Python wrapper consumer side. |

**Largest known coverage gap.** 27 instrumented handlers untested at
runtime; no Python-side schema for the wrapper Go ↔ Python boundary.

### 7. ARCHITECTURE_REPORT.md

| Aspect | Value |
|---|---|
| Writer | `internal/tools/orientation_report.go::generateOrientationReport` |
| Readers | humans / coding agents (markdown, not JSON) |
| Schema status | **text** (no JSON contract) |
| Suspect flags | none of the 4 bug shapes apply directly; markdown structure drift is human-detectable. |
| Tests | nothing direct. Indirect: writer is part of `index_repository` integration. |

### 8. Loc-Bench fixture → ground-truth oracles

| Aspect | Value |
|---|---|
| Writer | parquet ground_truth + various `oracle_*.py` / `oracle-*-ast/main.go` files |
| Readers | `bench/accuracy/compare.py` (the Cypher accuracy harness) |
| Schema status | **multiple dict shapes** (each oracle has its own JSON output) |
| Suspect flags | **multiple writer/reader pairs** — the `compare.py` script consumes 5+ oracle outputs. Each is a separate contract. |
| Tests | per-oracle smoke tests scattered in `bench/accuracy/check_*.py`; no unified shape pinning. |

**Suggested independent verification**: enumerate every oracle output
schema, write a single round-trip test per oracle.

### 9. cmd/codebase-memory-mcp/install.go config writes

| Aspect | Value |
|---|---|
| Writer | `cmd/codebase-memory-mcp/install.go` (manipulates user settings JSON) |
| Reader | Claude Desktop, Claude Code |
| Schema status | **dict** (free-form JSON edits to user config files) |
| Suspect flags | **key-rename risk** — write path uses string-keyed maps; read path is in another process entirely (Claude clients). |
| Tests | nothing covers this end-to-end. |

Out of scope for the eval/trust harness BUT in scope for the
"harness fails by producing apparently-clean output" pattern.

### 10. anthropic API client request/response (LLM)

| Aspect | Value |
|---|---|
| Writer | `internal/anthropic/client.go` |
| Reader | Anthropic API (external) + parses response back into Go structs |
| Schema status | **typed** (Go structs with json tags, third-party API spec) |
| Suspect flags | **versioning risk** — Anthropic schema changes are the third-party's; we'd see them as request failures. |
| Tests | none in this repo (third-party contract). |

Out of scope.

## Summary

10 contracts identified. Status:

| Status | Count | Names |
|---|---|---|
| Schema-typed + tested + fail-loud after this PR | 1 | (1) per-case JSON |
| Schema-typed + partial tests | 2 | (4) throughput JSON, (10) Anthropic API client |
| Dict + targeted tests | 3 | (2) parquet category vocab, (3) audit YAML, (6) Go MCP responses |
| Dict + few/no tests | 4 | (5) Pipeline.Progress, (7) ARCHITECTURE_REPORT, (8) oracle outputs, (9) install.go config |

**Of the 10, only contract #1 has the discipline level the roundtable
recommended (typed + fail-loud + roundtrip tests + real-data
regression).** The other 9 vary from "moderately covered" to "no
coverage."

## What this document is NOT

- **NOT a certification.** I authored both the harness and this
  inventory; the roundtable's recommendation is for an independent
  reviewer to certify each contract.
- **NOT exhaustive.** I greppped for `json.Marshal` / `json.dump` /
  `json.load` patterns; contracts that move data via other formats
  (YAML, protobuf, env vars, files-as-text) may exist that I didn't
  find. Specifically: plan, knowledge-base, and skill files traverse
  authored boundaries that I didn't grep for.
- **NOT a fix proposal.** Each entry says where coverage is thin;
  the actual hardening (typed schemas, round-trip tests, fail-loud
  paths) is the certification work, which the roundtable said
  shouldn't be authored by me.

## Recommended next steps for an independent reviewer

1. Spot-check 3 random claims from this inventory against the actual
   source files. If any claim is wrong (typed where it's actually
   dict, tested where it's actually untested), trust drops.
2. Pick the 2 highest-stakes uncovered contracts (#5 Pipeline.Progress
   and #6 Go MCP responses by my read; the reviewer should reach their
   own ranking) and certify those first.
3. The mutation-testing recommendation (roundtable rec #2) lands
   downstream — operators authored by the reviewer, not the original
   author.

## Cross-references

- Roundtable: `~/Documents/roundtables/2026-05-06-getwell-verification/META_SYNTHESIS.md`
- Roundtable rec #1 (this doc is its descriptive precursor)
- Plan: `~/Documents/knowledge-base/plans/2026-05-06-codegraph-harness-getwell.md`
