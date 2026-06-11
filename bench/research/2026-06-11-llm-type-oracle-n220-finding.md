# LLM type-oracle experiment 2: n=220 across four strata vs SCIP truth (2026-06-11)

**Verdict: MECHANISM CONFIRMED AT POWER — integration shape is now clear
and narrower than "run the oracle on ambiguous edges."** The oracle is
near-compiler-grade where it picks among in-repo candidates (97-98%) but
mediocre at recognizing external callees (56% NONE-detection). It should
run on the suffix_match bucket and the unresolved-call (recall) bucket;
it should NOT run on unique_name (it would degrade a 100%-precision
bucket to 97%).

## Setup

Extends the n=40 pilot (`2026-06-11-llm-type-oracle-pilot-finding.md`,
PR #380) per its pre-registered design. Both arms regenerated fresh at
main `6d3f08e` immediately before sampling (heuristic-only scratch cache
+ SCIP-ingested scratch cache from a fresh `scip-go` index — same-commit
instrument freshness). Haiku 4.5, temp 0, max_tokens 1500, seed 20260612.
Script: `bench/research/llm_type_oracle_n220.py` (`--prep`/`--run`).

Strata (dedup'd on `(caller_qn, callee_name)`, multi-target joins skipped):

| Stratum | n | Pool | What it measures |
|---|---|---|---|
| corrective-suffix | 60 | 788 | wrong-target risk where suffix_match emitted, SCIP target exists in-repo |
| corrective-unique | 60 | 2,629 | same, for unique_name |
| none-heavy | 50 | 757 | FP behavior: heuristic emitted, SCIP says no in-repo callee |
| recall | 50 | 1,922 | recovery: SCIP edge exists, heuristic emitted nothing |

## Results (Wilson 95% intervals)

| Stratum | n | Heuristic | LLM |
|---|---|---|---|
| corrective-suffix | 60 | 32 (53%) | **58 (97%)** [89%, 99%] |
| corrective-unique | 60 | 60 (**100%**) | 58 (97%) [89%, 99%] |
| none-heavy | 50 | 0 (0%)* | 28 (56%) [42%, 69%] |
| recall | 50 | 0 (0%)* | **49 (98%)** [90%, 100%] |
| **Total** | 220 | 92 (42%) | 193 (88%) |

\* 0% by construction — these strata are defined by the heuristic being
wrong (emitted where SCIP says NONE) or absent (emitted nothing).

Cost: 162K in / 41K out tokens ≈ **$0.37**. Zero API errors, zero parse
failures (the pilot's two instrument bugs stayed fixed).

## What changed vs the pilot's picture

1. **unique_name needs no oracle.** With truth=NONE sites separated into
   their own stratum, unique_name is 100% on in-repo-truth sites — the
   pilot's 90% blended in NONE sites. The oracle's 97% here is strictly
   worse. Integration must scope to suffix_match-class sites only.
2. **The recall side is the biggest surprise: 98% recovery.** Given the
   call site + same-name in-repo candidates, the oracle recovers
   dropped calls almost perfectly. The pool is large (1,922 pairs on
   this repo — bigger than the 830-edge SCIP-only delta because pairs
   dedupe differently than edges), so this is the largest absolute
   opportunity: an oracle pass over extracted-but-unresolved calls.
3. **NONE-detection is the weak axis (56%)**, confirming the pilot's
   under-sampled suspicion. 22/50 external calls got bound to an in-repo
   candidate. Two readings, both actionable: (a) any oracle pass must
   expect ~44% FP leakage on external-callee sites unless the prompt or
   candidate scoring is improved; (b) some "truth NONE" labels are
   SCIP's own conservatism (e.g. dispatch shapes scip-go doesn't link),
   so 56% is a floor estimate — a hand-audit of a 15-site sample of the
   none-heavy misses is the cheap next probe before trusting the 44%
   as pure oracle error.

## Net pipeline arithmetic (this repo, suffix bucket)

suffix_match pool composition: 788 in-repo-truth + the suffix share of
the 757 NONE pool. At measured rates, an oracle pass over the suffix
bucket converts 53%-correct edges into ~97%-correct on in-repo sites
while mislabeling ~44% of external sites — still a large net precision
gain on a bucket whose measured precision history is 0.55-0.95 (and
0.00-0.35 on Python adversarial). The recall pass is nearly pure upside
at 98%. Cost envelope at pilot settings: suffix bucket ~$2, recall
bucket ~$4 per full pass on a repo this size.

## Caveats

- Still one repo, one language (Go), one model/prompt. **Rust axis is
  BLOCKED ON TOOLCHAIN on this host** (no cargo/rustc/rust-analyzer;
  PSM is the natural Rust corpus once a toolchain lands — the fuzzy
  bucket's Janusian cases are Rust-typical and remain unmeasured).
- The 21 `drifted_files` from SCIP ingest persist even with a
  same-commit fresh index — a stable extractor-vs-scip span
  disagreement on those files, not staleness; they are simply outside
  the truth set here.
- SCIP-as-oracle inherits scip-go's own misses, which matters most for
  the none-heavy stratum (see reading 3b above).

## Next decision point

Pipeline integration would be an opt-pass design question (where in the
resolver, batch vs per-edge, which buckets) — that is a /superplan-scale
design, not another measurement. The measurement side is now sufficient
for Go; the open measurement items are the none-heavy hand-audit (15
sites, ~30 min, $0) and the Rust axis (blocked on toolchain).
