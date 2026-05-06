# Sub-bucket split of `cross-package-heuristic` — diagnostic finding

**Date**: 2026-05-06
**Scope**: Split the lumped `cross-package-heuristic` resolver_rule bucket into three sub-buckets reflecting the underlying registry strategies. Re-baseline the 4 fixtures where the lumped bucket was load-bearing.

## Why the split

The 2026-05-06 adversarial-fixture re-run (PR #233) showed cross-package-heuristic precision varied by an order of magnitude across fixtures (0.07 on flask vs 0.95 on code-graph-go) within the same lumped bucket. The lumped bucket couldn't surface which sub-strategy was emitting the phantoms, so Rec 1 (drop-on-no-match) couldn't be designed safely:

- Blanket drop on the bucket: kills 70-95% of TPs on Go fixtures.
- No drop: leaves catastrophic 0.07 precision on Python adversarial fixtures.

The split makes the underlying strategies addressable individually.

## What changed

`internal/pipeline/resolver_rule.go::resolverRuleFromRegistryStrategy` now dispatches:

| Registry strategy | Pre-split bucket | Post-split bucket |
|---|---|---|
| `import_map` | `cross-package-heuristic` | **`cross-package-import-map`** (precise) |
| `unique_name` | `cross-package-heuristic` | **`cross-package-unique-name`** (project-wide name) |
| `import_map_suffix` | `cross-package-heuristic` | **`cross-package-suffix`** (dangerous fall-through) |
| `suffix_match` | `cross-package-heuristic` | **`cross-package-suffix`** |

Plus `isCrossPackageRule()` helper for callers that need to fire on the FAMILY (e.g., the Janusian-ambiguity penalty in `pipeline_cbm.go`). The legacy `ResolverRuleCrossPackageHeuristic` constant is retained as a back-compat anchor for legacy baseline JSONs that carry the old string.

## Post-split per-rule precision

scope-aligned, MODALITY_MIN_SUPPORT=5

| Fixture | F1 | cross-package-import-map | cross-package-unique-name | cross-package-suffix |
|---|---|---|---|---|
| **Python WEAK flask-adv** | 0.49 | (no edges) | **0.09** (3 TP / 30 FP) | **0.00** (0 TP / 11 FP) |
| **Python WEAK requests-adv** | 0.39 | (no edges) | 0.35 (6 TP / 11 FP) | **0.08** (1 TP / 12 FP) |
| **Go WEAK gin-adv** | 0.72 | (no edges) | 0.95 (564 TP / 31 FP) | **0.48** (45 TP / 48 FP) |
| Go STRONG cobra (post-split) | 0.94 | (no edges) | 0.88 (604 TP / 79 FP) | (no edges) |

Note: `cross-package-import-map` doesn't appear in any post-split baseline. The registry's `import_map` strategy fires only when the imported alias has a tracked binding; on these fixtures, most cross-package resolutions go through `unique_name` or `suffix_match` instead. That's a Python/Go-extractor characteristic (limited Python import-map coverage), not a split-design issue.

## Findings

### 1. cross-package-suffix is the consistent danger across all languages

| Fixture | cross-package-suffix precision |
|---|---|
| Python flask-adv | **0.00** |
| Python requests-adv | **0.08** |
| Go gin-adv | **0.48** |

Drop-on-no-match for the suffix sub-bucket is **universally beneficial** — it never has high precision on any tested fixture. This is the cleanest target for Rec 1.

### 2. cross-package-unique-name varies by language

| Fixture | cross-package-unique-name precision |
|---|---|
| Python flask-adv | 0.09 |
| Python requests-adv | 0.35 |
| Go gin-adv | **0.95** |
| Go cobra | **0.88** |

On Go, `unique_name` resolution is high-precision (project-wide unique-name lookup is reliable when the project has clean naming conventions). On Python adversarial fixtures it's catastrophically noisy. Different per-language fix:

- **Python**: drop-on-no-match for `cross-package-unique-name` is a clear net win.
- **Go**: leave `cross-package-unique-name` alone (88-95% precision; the bucket carries 57-68% of TPs).

### 3. The lumped bucket was hiding heterogeneity

Pre-split flask data showed `cross-package-heuristic: precision 0.07, support 44`. Post-split:

- `cross-package-unique-name`: 0.09 precision, 33 edges
- `cross-package-suffix`: 0.00 precision, 11 edges

The 0.07 lumped number was ALMOST entirely from the suffix sub-bucket dragging down a slightly less-bad unique-name bucket. This is the kind of distinction the split was designed to expose.

### 4. PR #233's "F1 improvement of +11pp on flask if Rec 1 is applied" estimate was directionally right but coarse

PR #233 estimated dropping the entire `cross-package-heuristic` bucket on flask: TP=44→41, FP=42→1. That assumed the whole bucket (44 edges) goes. Post-split, the bucket components are:

- `cross-package-unique-name`: 33 edges (3 TP / 30 FP)
- `cross-package-suffix`: 11 edges (0 TP / 11 FP)

If we **only** drop `cross-package-suffix`: TP=44→44 (no TPs lost), FP=42→31. P=0.59 → R=0.47 → F1=**0.52** (+3pp).
If we drop **both** sub-buckets: TP=44→41, FP=42→1. P=0.98, R=0.44, F1=**0.61** (+12pp). 

The +11pp PR #233 estimate was the all-or-nothing case. The split now lets us pick the targeted variant.

## Implications for Rec 1 design

Per-language drop-on-no-match policy:

| Sub-bucket | Python | Go | Rust |
|---|---|---|---|
| `cross-package-import-map` | leave alone | leave alone | leave alone |
| `cross-package-unique-name` | **drop-on-no-match** | leave alone | TBD (PSM not re-baselined post-split) |
| `cross-package-suffix` | **drop-on-no-match** | **drop-on-no-match** | leave alone* |

*Rust pre-split data showed the cross-package-heuristic bucket at 0.89 precision overall and only 47% of FPs. Without the post-split breakdown on Rust, conservative default is leave alone. PSM uses 5 per-crate sub-projects; post-split re-baseline requires per-crate re-indexing (deferred).

## Caveats

- Post-split data covers 4 fixtures (flask, requests, gin, cobra). PSM (Rust) and code-graph-go (Go self-host) still have pre-split data; they're informative but use the legacy lumped bucket.
- The Janusian-ambiguity penalty (`pipeline_cbm.go:656`) was updated to fire on `isCrossPackageRule(rule)` so it covers all 3 sub-buckets; existing tests still pass. The penalty's behavior is unchanged from a downstream-consumer perspective.
- Legacy baselines in `bench/accuracy/baselines/2026-05-03-*-report.json` retain the old `cross-package-heuristic` string in their JSON. They remain valid; the legacy constant is intentionally retained for back-compat.

## What's now ready

Rec 1 (drop-on-no-match) can be designed selectively per (language × sub-bucket). The most targeted version — drop-on-no-match for `cross-package-suffix` only — should be tested first as it has the most universal evidence of low precision (0.00-0.48 across all post-split fixtures).
