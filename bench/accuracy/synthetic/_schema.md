# Synthetic Fixture `ground_truth.json` Schema

Hand-authored fixtures in `bench/accuracy/synthetic/<name>/` carry a
`ground_truth.json` file that enumerates the edges the fixture is the
oracle for.

Two fixture flavors share the file:

1. **Positive fixtures** (e.g., `rust-minimal`, `go-minimal`): every
   edge code-graph emits is enumerated as expected. Used by the
   prove-the-instrument gate (per `rules/verify-effectiveness.md`) —
   if the indexed fixture diverges from the enumeration, the oracle
   has a bug.

2. **Negative fixtures** (e.g., `rust-diesel-negative`): the fixture
   exercises a known co-hallucination class (external-crate / framework
   chain calls). Hand-author asserts which edges code-graph **must
   not** emit. Some control internal edges are still enumerated, but
   the regression-grade signal is the forbidden-emit list.

   Negative fixtures are independent of `oracle_rust_syn.py`. The
   fixture file itself IS the oracle; no external static-analysis
   tool participates. This is the auxiliary release gate the
   2026-05-02 roundtable Recommendation #1 prescribes.

## Schema

```json
{
  "description": "<one-line purpose>",
  "kind": "positive | negative",

  "expected_internal_calls": [
    {"from_qn": "...", "to_qn": "..."}
  ],

  "expected_internal_imports": [
    {"from_qn": "...", "to_qn": "..."}
  ],

  "forbidden_emitted_calls": [
    {
      "from_qn": "rust-diesel-negative.src.main.entry",
      "to_qn_pattern": "*\\.get_result",
      "reason": "Diesel trait method; external; must not bind to internal target"
    }
  ],

  "expected_resolver_rule_invariants": {
    "forbidden_rules": ["fuzzy-resolve", "unresolved-emitted"],
    "expected_rules_subset": ["exact-qn-match"]
  },

  "notes": "<gotchas, QN format quirks, empirically verified facts>"
}
```

### Field semantics

| Field | Required | Applies to | Meaning |
|---|---|---|---|
| `description` | yes | both | Human-readable purpose |
| `kind` | yes (new fixtures) | both | `positive` for the prove-the-instrument gate; `negative` for co-hallucination CI gate. Older fixtures without this field default to `positive` for backward compat. |
| `expected_internal_calls` | optional | both | Edges that MUST be emitted. Positive fixture: full enumeration. Negative fixture: control edges (verifies fixture indexes at all). |
| `expected_internal_imports` | optional | both | Same semantics as calls but for IMPORTS. |
| `forbidden_emitted_calls` | optional | negative | Edges that MUST NOT be emitted. Each entry: `from_qn` (caller), `to_qn_pattern` (glob or regex on emitted target QN), `reason` (why this would be a phantom). |
| `expected_resolver_rule_invariants` | optional | both | Used by `check_*_resolver_rule.py`-style gates. See go-minimal for example. |
| `notes` | recommended | both | QN-format quirks, empirically verified facts, design decisions. |

### `forbidden_emitted_calls` matching

`to_qn_pattern` matches against the canonicalized `to_qn` string of
each emitted edge from `from_qn`:

- Glob form: `*\.get_result` matches any QN ending in `.get_result`.
- Anchored form: `^Foo\.bar$` for exact regex match.

Implementation chooses regex (Python `re.fullmatch` with the pattern as
provided). Globs use `fnmatch.fnmatch` semantics; the harness picks one
syntax — see `check_negative_fixtures.py` for the live choice.

A forbidden hit is ANY emitted edge from `from_qn` whose `to_qn`
matches the pattern. The harness reports each phantom edge and exits
nonzero (or, for the regression mode, fails when phantom count exceeds
the documented baseline).

### QN format reminder

QNs use FLATTENED mod form per code-graph's `fqn.Compute`:

- `pub mod helpers { fn greet() }` in `<file>.rs` → `<project>.<file>.greet` (NOT `<project>.<file>.helpers.greet`)
- Impl scope IS included: `impl Foo { fn bar() }` → `<project>.<file>.Foo.bar`
- Generic `<W>` suffixes are stripped from impl-block type names (PR #149)
- Inline mod scope (incl. `#[cfg(test)] mod tests`) is NOT included

Verify against the indexed graph before authoring `ground_truth.json` — see
`bench/accuracy/synthetic/rust-minimal/ground_truth.json` for the
empirical reference.

## History

- **2026-05-02**: `kind` and `forbidden_emitted_calls` added per
  roundtable Recommendation #1
  (`~/Documents/knowledge-base/research/dispatch-runs/2026-05-02-codegraph-roundtable/results/META_SYNTHESIS.md`).
  Negative fixtures bypass `oracle_rust_syn.py` entirely — they catch
  the co-hallucination class that single-side oracle fixes regress on.
