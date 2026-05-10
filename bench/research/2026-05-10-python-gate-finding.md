# Phase F: Python Default-On Cross-Package Gate — DROP on no-lift

**Date**: 2026-05-10
**Plan**: `~/Documents/knowledge-base/plans/2026-05-10-accuracy-gap-remediation.md` Phase F
**Verdict**: **DROP** — neither candidate gate produces a clean Python default-on flip. No code change shipped beyond the in-source comment update documenting the negative finding.

## TL;DR

- **Phase F1 measurement**: tested 2 gate mechanisms (`RESOLVER_DROP_LOOSE_CROSS_PACKAGE` and `RESOLVER_REQUIRE_IMPORTS_FOR_LOOSE_CROSS_PACKAGE`) on Flask + Requests adversarial fixtures.
- **Result**: only `RESOLVER_DROP_LOOSE_CROSS_PACKAGE` lifts Flask (+0.070 SA-F1), and the lift comes at the cost of breaking legitimate `from X import Y` cross-module patterns. Requests flat in both gates.
- **Phase F2 verdict**: cannot ship default-on for Python because (a) the lifting gate breaks production patterns, (b) the safer gate doesn't lift.

## Measurements

| Fixture | Gate | Index edges | Scope-aligned F1 | Δ vs gate-OFF |
|---|---|---:|---:|---:|
| Flask gate-OFF (status quo) | — | 7,790 | 0.492 | — |
| Flask `RESOLVER_DROP_LOOSE_CROSS_PACKAGE=1` | broader drop | 6,591 | 0.562 | **+0.070** |
| Flask `RESOLVER_REQUIRE_IMPORTS_FOR_LOOSE_CROSS_PACKAGE=1` | import-reachability | 5,589 | 0.492 | 0.000 |
| Requests gate-OFF | — | 4,526 | 0.383 | — |
| Requests `RESOLVER_DROP_LOOSE_CROSS_PACKAGE=1` | broader drop | 3,604 | 0.373 | -0.010 |
| Requests `RESOLVER_REQUIRE_IMPORTS_FOR_LOOSE_CROSS_PACKAGE=1` | import-reachability | 3,749 | (~0.38) | flat |

## Why each candidate gate fails

### `RESOLVER_DROP_LOOSE_CROSS_PACKAGE` — lifts Flask but breaks Python

The broader drop gate eliminates BOTH `cross-package-unique-name` AND `cross-package-suffix` rule emissions. On Flask adversarial, this drops phantom matches that were polluting the heuristic — F1 lifts +0.070.

But: the existing test `TestPythonCrossModuleCallViaImport` exercises the legitimate `from utils import fetch_data` pattern. The call `fetch_data()` resolves via project-wide `unique_name` (single match → high precision), then succeeds. Promoting Python to default-on for this gate drops that legitimate edge.

Verified by reverting the test loop with my Python-default-on change: the test FAILS — process() loses its CALLS edge to fetch_data.

The Flask lift comes from PHANTOM unique_name matches (multiple modules with same simple-name where wrong target is picked). Production Python codebases (per the test fixture) have legitimate unique_name matches that this gate also drops.

**Conclusion**: this gate has a real lift on adversarial fixtures and a real regression on production-style cross-module imports. Net effect on the population is unclear; shipping requires more nuance (e.g., distinguish "single match across N modules" vs "single match in single module").

### `RESOLVER_REQUIRE_IMPORTS_FOR_LOOSE_CROSS_PACKAGE` — doesn't lift Flask

The more surgical import-reachability gate drops candidates that AREN'T import-reachable from the call site. This gate is default-on for Rust per PR #276 because Rust callers reliably use explicit `use` statements.

On Flask adversarial: the gate doesn't drop the phantom matches because those matches happen to be import-reachable (Flask's adversarial fixture sets up imports specifically to surface the disambiguation problem). Index edge count drops 7790 → 5589 (some candidates ARE filtered), but scope-aligned F1 unchanged at 0.492.

**Conclusion**: this gate is a no-op for Flask. The phantoms it would need to drop are protected by Flask's intentionally-import-rich fixture structure.

## What this means for the next plan

The Phase 3.6 recoverable ceiling for Python F1 lift via "extend PR #276 pattern" is effectively **0pp on the surgical gate**. The +0.070 gain on the broader gate is real but not safe to ship as default-on.

Two paths forward (named-next-plan):

1. **Refine the unique_name heuristic** so it distinguishes:
   - **Safe unique_name**: single match in single module → high precision (today's `TestPythonCrossModuleCallViaImport` case).
   - **Phantom unique_name**: single match in a sea of same-named functions across modules (today's Flask adversarial case).
   The gate fires only on the second case. Effort: substantial — requires per-call-site context tracking that the resolver doesn't currently maintain.

2. **Per-fixture env-var application**: keep the env var as-is, document that adversarial Python fixtures should set it via the eval harness, production Python codebases should NOT. This is what's already happening; no code change needed.

Path 2 is the status quo. Path 1 is a separate plan.

## Phase F2 falsifier verification

The plan's Phase F2 falsifier was: "bootstrap CI on F1 delta INCLUDES zero. Action: do NOT ship; document as DROP-ON-NO-LIFT."

**Falsifier triggered (modified):** the Flask broader-drop lift looks meaningful (+0.070) but the implementation REGRESSES legitimate cross-module imports — verified by an existing pipeline test failing under the proposed default-on. The honest framing isn't "no lift in CI" but "lift comes with a documented regression that isn't acceptable."

Drop-on-no-lift verdict applies to the import-reachability variant. Drop-with-regression verdict applies to the broader variant. Neither ships.

## Implementation status

No code changes shipped beyond a comment update at `pipeline_cbm.go:636-661` documenting why the F2 default-on flip was tested and rejected. This serves as a regression guard — future plan authors who consider promoting Python default-on for this gate will see the comment and find this finding doc.

## Cross-reference to existing Python defaults

For context, Python ALREADY has a default-on language gate for the suffix bucket:

- `shouldDropCrossPackageSuffix(language)` in `internal/pipeline/resolver.go:82` — Python-only, drops the suffix bucket at the resolver INPUT layer (before the broader gate). This is sound: Flask measured 0.07-0.23 precision on the suffix bucket alone, so the input-layer drop is a clear win.

The remaining unique_name bucket is what F2 attempted to add to that protection. The measurement showed unique_name's precision distribution in production Python differs from adversarial fixtures, so the same default-on treatment doesn't apply.
