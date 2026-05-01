# FN cluster shift analysis — 2026-04-30 vs 2026-05-01

**Date**: 2026-05-02
**Scope**: code-graph Go fixture, scope-aligned FN set
**Method**: set-diff on (caller_qn, callee_qn, edge_type) tuples
**Script**: `~/Documents/knowledge-base/research/_fn_diff.py`

## Headline

The Step 6 verification doc (`2026-05-02-step6-verification.md`) claimed:

> "Server.handleIndexRepository's 5 missed FNs from the prior baseline are no
> longer in the FN set (FN count dropped from 6 to 6 — same count, but 5 of
> them are now in `internal/pipeline` not `internal/tools`)."

**This claim is wrong.** The FN sets are identical: 6 persisted, 0 resolved, 0 introduced.

## Diff result

| Bucket | Count |
|---|---:|
| Resolved (FN → TP) | 0 |
| Introduced (new FN) | 0 |
| Persisted (still FN) | 6 |

## Persisted FNs (all 6, unchanged)

| caller | callee |
|---|---|
| `internal-tools.index.Server.handleIndexRepository` | `internal-tools.tools.errResult` |
| `internal-tools.index.Server.handleIndexRepository` | `internal-tools.tools.getBoolArg` |
| `internal-tools.index.Server.handleIndexRepository` | `internal-tools.tools.getStringArg` |
| `internal-tools.index.Server.handleIndexRepository` | `internal-tools.tools.jsonResult` |
| `internal-tools.index.Server.handleIndexRepository` | `internal-tools.tools.parseArgs` |
| `internal-store.store.Store.Close` | `internal-store.config.ConfigStore.Close` |

## Why the verification doc was wrong

The 2026-04-30 baseline had `sample_fn_scoped` only (6 edges, capped per project). The 2026-05-01 baseline added `fn_scoped_full` via PR #129. For the Go fixture's tiny FN set (6 total), the sample slice happened to capture all 6 — so the "full" set isn't bigger than the "partial" set was.

The verification author appears to have read the per-project FN counts and miscounted, or compared to a stale earlier baseline. Either way, no FN movement occurred.

## Implication

- The 5 `handleIndexRepository → tools.*` misses are **stable**, not migrating. They are likely a single class of bug: argument-parsing helper calls that the resolver doesn't track because they live in a sibling file within the same package. This is a fixable resolver gap (probably a same-package-shadow miss when the helpers are in a separate `.go` file from the caller).
- The `Store.Close → ConfigStore.Close` miss is a method-on-embedded-type / interface-dispatch resolver gap.
- Phase Y A2 (prefer same-package-shadow for method-body callers) may close 5 of these 6 by tracking the same-package helper functions correctly.

## Stop-and-ask gate triggered

The Step 6 verification doc has a factual error. Per the plan's gate:
> "Phase X.3 finds the FN shift is a re-index artifact (not a real shift) → STOP, document and ask whether to dig further"

This is the case. Recommendation: the FN shift was an illusion; no further investigation required. The 6 persisted FNs are genuine resolver gaps, addressable by Phase Y A2.
