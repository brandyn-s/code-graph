# Graph-only conceptual localization seed-quality replay — 2026-08-13

This paired replay asks one bounded question: does preserving lexical seed
quality through `code_localize` improve deterministic file localization?

The baseline flattened every matched seed to score `1.0`. A node matching two
independent anchors therefore started equal to a generic node matching only
one, and graph expansion amplified the generic seed set. The measured change
carries the already-computed exact-name and qualified-name match quality into
BFS personalization.

## Frozen method

- Cohort: the existing balanced public LocBench `n=80` cases and labels.
- Queries and stores: byte-unchanged preserved inputs from the 2026-08-12 run;
  no reindexing or label changes.
- Invocation: direct, zero-LLM `code_localize`, substring seeds, depth 3,
  `top_k=50`; the scorer keeps the first ten distinct files.
- Failures: retained as misses (7 in each arm).
- Baseline source: `eaaa894ef5d499811643c2d01d6c440e3fa3832e`;
  binary SHA-256
  `ebca219a36477fc085bf165f3e289675ce75cb723ee9c99f56c987bce9f10705`.
- Candidate source: `315f7a36e10f21aae464af7cd35d2eb244083673`;
  binary SHA-256
  `e4b59c55aa846224cde2dfea3409e82541e3ac12b6ce5af472c2adec84403d90`.
- Frozen result provenance: cases SHA-256
  `de09dcbbe31a48782391aeff92badbdf71853b7b0f8bdbdf6a483ed5d728e63b`
  and oracle SHA-256
  `1f0cdf82d808d1a5a488af67d621002dd909b8cb3900483090b194d1f5625d70`.

## Result

| Metric | Baseline | Candidate | Change |
|---|---:|---:|---:|
| File Acc@1 | 0.175 (14/80) | 0.200 (16/80) | +0.025 |
| File Acc@3 | 0.250 (20/80) | 0.300 (24/80) | +0.050 |
| File Acc@10 | 0.350 (28/80) | 0.400 (32/80) | +0.050 |
| MRR@10 | 0.21932 | 0.26012 | +0.04080 |

Paired rank movement was 12 improved cases, 2 regressed cases, and 66 ties.
The change improves every reported ranking metric, but the absolute
operating point remains below code-search on conceptual discovery.

A broader compound-token expansion was also tested and rejected before
shipping. It raised Acc@10 to 0.4625 but reduced Acc@1 back to 0.175 and caused
7 regressions. Those changes are not present in the released `.11`
implementation.

## Interpretation boundary

This is same-cohort paired iteration evidence, not a fresh independent public
benchmark or a general superiority claim. Graph-only localization remains
bounded by what the graph represents: concepts present only in source text and
absent from symbol/path identities still belong on the code-search route.
