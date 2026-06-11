# LLM type-oracle pilot: Haiku vs heuristic resolver on ambiguous-dispatch buckets (2026-06-11)

**Verdict: VALIDATED FOR NEXT EXPERIMENT** — an LLM oracle given the call-site
source + in-repo candidates beats the heuristic resolver by **+30pp on the
suffix_match bucket** (95% vs 65%) at ~$0.002/call-site (Haiku 4.5). Not a
ship decision: n=40, one repo, one language. The next experiment (n=200,
NONE-heavy sample, recall side) is what a pipeline-integration decision
would hang on.

## Setup

- **Question**: on call sites the heuristic resolver handles with
  low-precision strategies, can an LLM pick the correct callee from the
  call-site source + candidate definitions?
- **Ground truth**: SCIP compiler-grade edges (scip-go) on this repo —
  the `CBM_SCIP_INDEX_PATH` ingest (PR #372/#374) run on 2026-06-11.
- **Arms**: heuristic-only index (scratch cache via `HOME` override, no
  SCIP) joined against the SCIP-ingested index on
  `(caller_qn, callee_short_name)`. 286 SCIP-covered files; 4,186
  ambiguous-bucket heuristic edges in covered files; 4,174 joinable sites
  (12 multi-target joins skipped).
- **Sample**: 20 per bucket, seed 20260611, stratified over
  {fuzzy, suffix_match, unique_name}. **fuzzy sampled 0** — all 116 fuzzy
  edges live in files scip-go does not cover (non-Go: bench scripts,
  vendored C), so the worst-precision bucket is unmeasured here.
- **Oracle**: `claude-haiku-4-5-20251001`, temp 0, max_tokens 1500, caller
  snippet (≤60 lines) + ≤12 in-repo candidates with first-line signatures,
  NONE allowed for external targets. Script:
  `bench/research/llm_type_oracle_pilot.py` (`--prep` then `--run`).

## Result (n=40)

| Bucket | n | Heuristic acc | LLM acc |
|---|---|---|---|
| suffix_match | 20 | 13 (65%) | **19 (95%)** |
| unique_name | 20 | 18 (90%) | 19 (95%) |
| **Total** | 40 | 31 (78%) | **38 (95%)** |

Cost: 31,494 in / 7,875 out tokens ≈ **$0.07 total** (~$0.002/site).
Wall: ~4 min sequential.

The lift concentrates exactly where the resolver is weakest: suffix_match
(measured 0.55–0.95 precision across fixtures, as low as 0.00–0.35 on
Python adversarial). unique_name was already strong (90%) and the LLM
matches it.

## Instrument bugs hit en route (both mine, both fixed before the number above)

1. **max_tokens=200 truncation artifact**: the first scored run reported
   LLM 5% on suffix_match — because harder prompts made Haiku reason
   step-by-step and hit the token ceiling before emitting the answer JSON.
   At max_tokens=1500 the same bucket scored 95%. A "capability" delta of
   90pp was entirely a harness parameter.
2. **Brace-naive JSON extraction**: `find("{")`/`rfind("}")` grabbed Go
   braces echoed in the model's reasoning. Fixed with a targeted
   `{"answer": "..."}` regex.

Per `verify-instrument-before-fix.md`: the 5% cell was an instrument
artifact, recognized because heuristic-vs-LLM divergence of that shape on
the SAME sites was implausible and the raw responses were inspected before
accepting the cell.

## Caveats (read before citing)

- **n=40, single repo (code-graph itself), Go only, prompt v1, one model.**
- **truth=NONE barely sampled** (4/40): the oracle's false-positive
  behavior on external callees — the failure mode that matters most for
  precision — is effectively unmeasured.
- **Corrective precision only**: sites sampled are ones where the
  heuristic EMITTED an edge. Calls the resolver dropped entirely (the
  recall side, where SCIP added 830 edges on this repo) are not in scope.
- **SCIP truth treated as oracle**: scip-go's own errors (if any) count
  against both arms equally, but absolute accuracies inherit them.
- Join key is `(caller_qn, callee_name)` — call-site line numbers are not
  matched, so a caller invoking two same-named functions resolves to one
  joined row (12 such joins skipped).

## Next experiment (what a pipeline decision needs)

1. n=200+ with a NONE-heavy stratum (external-call sites) to measure the
   oracle's FP rate, not just its pick accuracy.
2. Recall side: sample extracted-but-dropped calls (the resolver's
   unresolved bucket) and measure how often the oracle recovers a correct
   in-repo target SCIP confirms.
3. A second repo/language (Rust via `rust-analyzer scip`) — the fuzzy
   bucket's Janusian-chain cases (see `RESOLVER_DROP_FUZZY_JANUSIAN_CHAINS`
   in CLAUDE.md) are Rust-typical and absent from this Go-only sample.
4. Cost/latency envelope at pipeline scale: 5,414 ambiguous edges on this
   repo ≈ $11 + ~6h sequential at pilot settings — batching and
   restricting to suffix_match-class sites (~1K edges ≈ $2) is the
   plausible integration shape.
