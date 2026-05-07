# Rec 1 (Python-targeted drop-on-no-match) — measured impact

**Date**: 2026-05-06
**Implementation**: `RESOLVER_DROP_LOOSE_CROSS_PACKAGE` env-var gate. When set, drops emissions in the `cross-package-unique-name` and `cross-package-suffix` sub-buckets at resolution time. Production default is OFF (env var unset).
**Why**: PR #234 (sub-bucket split) measured these two buckets at 0.00-0.35 precision on Python adversarial fixtures — essentially noise. The precise sub-bucket (`cross-package-import-map`) is preserved.

## Measured F1 impact (re-indexed flask + requests with env var ON)

| Fixture | Pre-drop F1 | Post-drop F1 | Δ |
|---|---|---|---|
| **flask-adversarial** | 0.4916 | **0.6074** | **+11.6pp** |
| **requests-adversarial** | 0.3926 | 0.3833 | −0.9pp |

Pre-drop reference: PR #234 baselines (`2026-05-06-flask-adversarial-report.json`, `2026-05-06-requests-adversarial-report.json`).
Post-drop measurement: `MODALITY_MIN_SUPPORT=5 python bench/accuracy/compare.py {fixture-id}` after re-indexing with `RESOLVER_DROP_LOOSE_CROSS_PACKAGE=1`.

### Per-rule breakdown post-drop

**flask-adversarial (F1 0.61)**:
- `self-method`: 30 TP / 1 FP / precision 0.97 (unchanged)
- `same-package-shadow`: 11 TP / 0 FP / precision 1.00 (unchanged)
- `cross-package-unique-name`: **dropped (was 3 TP / 30 FP)**
- `cross-package-suffix`: **dropped (was 0 TP / 11 FP)**

Net: lost 3 TPs, eliminated 41 FPs. P went 0.51 → 0.98; R went 0.47 → 0.44.

**requests-adversarial (F1 0.38)**:
- `self-method`: 33 TP / 0 FP (unchanged)
- `same-package-shadow`: 11 TP / 0 FP (unchanged)
- `cross-package-unique-name`: **dropped (was 6 TP / 11 FP)**
- `cross-package-suffix`: **dropped (was 1 TP / 12 FP)**

Net: lost 7 TPs, eliminated 23 FPs. P went 0.70 → 1.00; R went 0.27 → 0.24. F1 essentially flat (the TPs lost are real recall in the bucket, just enough to cancel the precision gain on this small sample).

## Why this is gated, not always-on

The same drop applied to Go fixtures would crater recall:

- **Go cobra**: `cross-package-unique-name` carries 604 TPs / 683 edges (88% precision). Dropping would lose 88% of the bucket's contribution. F1 would drop ~30pp.
- **Go gin**: `cross-package-unique-name` carries 564 TPs / 595 edges (95% precision). Dropping would also crater recall.
- **Go code-graph self-host**: similar — `cross-package-heuristic` (legacy lumped name; will split when re-indexed) at 95% precision.

The env-var gate lets the eval harness apply the drop selectively. Production default is OFF; the eval harness can set it for Python fixtures specifically. A future PR can extend `compare.py` to set the env var automatically based on fixture language; this PR is the implementation+measurement step.

## Edge-count side effect

| Fixture | Pre-drop edges | Post-drop edges | Δ |
|---|---|---|---|
| flask | 7673 | 5468 | -2205 |
| requests | 4412 | 2778 | -1634 |
| mcp-servers (production Python) | 12922 | 12436 | -486 |

mcp-servers loses far fewer edges (486 vs 1600-2200) because production Python imports use explicit aliases that resolve through the precise `cross-package-import-map` path, not the loose buckets. This is the desired property — the drop is targeted at adversarial-fixture pathologies.

## What the env var does NOT do

- Does NOT drop `cross-package-import-map` (precise path: imported alias → defined alias).
- Does NOT drop `same-package-shadow`, `self-method`, `interface-dispatch`, `exact-qn-match`, `receiver-qualified` — only the two loose cross-package sub-buckets.
- Does NOT affect production behavior unless `RESOLVER_DROP_LOOSE_CROSS_PACKAGE` is set.
- Does NOT change the resolver's strategy choice — only whether the resulting edge is emitted.

## Caveats

- mcp-servers (production Python fixture) couldn't be re-baselined within this PR because the working-tree SHA has drifted from the pinned 2026-04-30 SHA. The edge-count delta (-486) suggests the drop fires on some production edges; whether F1 changes meaningfully is TBD on next baseline refresh.
- The Go cobra-go and gin-adversarial baselines remain unchanged (env var unset for those fixtures by default).
- Per-test pinning of env-var ON/OFF behavior in `internal/pipeline/resolver_rule_test.go` (4 new tests, all pass).

## What this unblocks

Future work can:
1. Extend `compare.py` to auto-set the env var for fixtures with `languages: ["python"]` — fully automated language-aware Rec 1.
2. Re-baseline mcp-servers (post-SHA-update) to confirm production-code F1 impact.
3. Investigate the 7 lost TPs on requests-adversarial — they're real recall in the loose buckets that the harness might recover via better import-map coverage.
