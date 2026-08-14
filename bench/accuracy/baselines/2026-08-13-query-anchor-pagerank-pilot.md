# Query-anchor PageRank personalization pilot — 2026-08-13

Status: **rejected**. Deterministic tie ordering was retained; query-anchor
weighted personalization was removed before release.

## Question and gate

The experiment asked whether a seed matching more query anchors should receive
more PageRank personalization mass than a generic single-anchor seed. The
predeclared adoption gate was no regression in any reported ranking metric on
the bounded frozen replay.

## Method

- Baseline source: `61751ec6a6e3b6f7a729281459abf9def55b3c99`.
- Candidate source: `a80bcbd` (query-anchor weighting plus deterministic ties).
- Baseline binary SHA-256:
  `cdaf681620334d1bc4dd020201e4764f44d6d4573b61356e7ccae7a0a716eb06`.
- Candidate binary SHA-256:
  `75107cdabf38c624d82ad71d89fa5ecd1908fcce87699d99e055cb7b80eea8cb`.
- Inputs: the 20 still-retained frozen public stores from the prior public
  localization run; result input SHA-256
  `b90107ab9c2b390f5b16e1ad4f9294e8ea9ad7fee13527431a9b95da55fe4bc1`.
- Invocation: direct zero-LLM `rank_by_query`, substring seeds, `top_k=50`;
  scoring retained the first ten distinct files against unchanged labels.
- The other 60 stores from the broader `n=80` seed-quality replay were no
  longer retained. Recreating them after the bounded cohort had already
  falsified the no-regression hypothesis was not justified.

## Result

| Metric | Uniform baseline | Anchor-weighted candidate | Change |
|---|---:|---:|---:|
| File Acc@1 | 0.10 | 0.15 | +0.05 |
| File Acc@3 | 0.10 | 0.20 | +0.10 |
| File Acc@10 | 0.45 | 0.40 | **-0.05** |
| MRR@10 | 0.178333 | 0.211389 | +0.033056 |

Paired movement was 2 improved cases, 2 regressed cases, and 16 ties. The
Acc@10 regression failed the gate even though early-rank metrics improved.

## Decision

Commit `d42df35` removed query-anchor weighting and restored uniform
personalization. Canonical deterministic tie ordering remains: equal scores
sort by file path, qualified name, label, name, then stable node ID. No ranking
superiority claim follows from this `n=20` pilot, and the released `n=80`
`code_localize` seed-quality result remains the applicable graph-localization
baseline.
