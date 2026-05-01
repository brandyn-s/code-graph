# internal/store per-project diagnosis — deferred

**Date**: 2026-05-02
**Verdict**: Skip per-project plateau-diagnosis on internal/store. Reason: top-site error pattern matches the same oracle gap identified in PR #137 (runIncrementalPasses investigation).

## What the recipe's mitigation gate said

The plateau-fixes plan (follow-up #3) prescribed:

> **Mitigation**: run blast_radius FULL on store first — if top-1% share is high (analog of pipeline's 21.8%), there's a single cell to find. If it's low (< 5%), the problem is uniform and not recipe-shaped.

Store's measured `top_1pct_share = 6.1%` (post-Y.3 + CBM-fix baseline). That's just above the 5% threshold — borderline uniform. Marginal recipe applicability on this signal alone.

## Direct inspection of top-3 sites confirms oracle gap

Examined top-3 sites' FP edges (FN=0 at all three):

**`StoreRouter.migrate`** (6 FPs):
- `Store.config.ConfigStore.Close` (typed-receiver method)
- `StoreRouter.migrateProject` (same-receiver method)
- `StoreRouter.renameLegacy` (same-receiver method)
- `nodes.scanner.Scan` (typed-receiver method)
- `Open` (package fn)
- `Querier.QueryContext` (interface method dispatch)

**`Store.archLayers`** (4 FPs):
- `Store.archBoundaries` (same-receiver method)
- `ConfigStore.Close`, `scanner.Scan`, `Querier.Query` (all receiver methods)

**`StoreRouter.migrateProject`** (4 FPs):
- `ConfigStore.Close`, `scanner.Scan`, `Querier.ExecContext`, `Querier.QueryRowContext` (all receiver methods)

**Every single one is a `recv.method` call on a typed receiver.** Same shape as the runIncrementalPasses → `Pipeline.passXxx` pattern (PR #137).

## Why deferring is correct

Per PR #137: the oracle's `extractCallee` emits `<recv_ident>.<method>` form (e.g. `r.migrateProject`, `s.archBoundaries`, `q.QueryContext`). The Python wrapper drops these because the receiver identifier doesn't match a filename segment. So they're scored as FPs even though code-graph emitted them correctly.

Running per-project plateau-diagnosis on store would surface the same cell as the aggregate did (cross-package-heuristic at high candidate_set_size, etc.) — but the underlying truth is that most of these "FPs" are instrument-dropped TPs. Designing a code-graph filter against measurement noise is exactly the trap PR #137 warned about.

## Action

- No code-graph changes here.
- After follow-up #5 (oracle receiver-method resolution) lands, re-run `compare.py code-graph-go` and check internal/store F1. If it jumps to ~0.85-0.95 (in line with cbm and tools), the per-project recipe is unnecessary.
- If store still has dominant residual error after the oracle fix, re-evaluate this deferral.

## Meta-lesson (already recorded in PR #137)

The plateau-diagnosis recipe correctly surfaces failure cells. It does NOT distinguish between "code-graph error in this cell" and "instrument error in this cell." When top FP shapes match a known instrument-gap pattern, the right move is to fix the instrument and re-measure — not to design a code-graph filter.

This is the third instance of the pattern in three sessions:
1. PR #134 (FN-shift): set-diff revealed Step 6 doc's claim was wrong (instrument-state interpretation error).
2. PR #136 (CBM QN): 5 "resolver-missing" FNs were caller-QN-format-mismatch (instrument bug at definition time).
3. PR #137 (this finding's parent): 34 FPs at one site were oracle-dropped TPs (instrument bug at measurement time).

The recipe should grow a Step 5b: **before designing a fix, sample 3-5 of the cell's edges and verify they are real failures, not measurement artifacts.**
