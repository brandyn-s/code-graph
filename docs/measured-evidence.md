# Measured evidence

Every figure below is bounded by its exact revision, language, edge type,
fixture, and oracle. None establishes universal compiler precision or general
product superiority.

- **Heuristic Go CALLS:** five production subsets against a deterministic
  `go/ast` oracle measured scope-aligned precision `0.953`, recall `1.000`, and
  F1 `0.976`. Raw unscoped precision was `0.540`; do not conflate the two.
- **Compiler-tier Go CALLS:** an independent SSA/RTA oracle over code-graph and
  Cobra measured precision `0.969`, recall `0.932`, and F1 `0.950`. Cobra
  recall was `0.867` and remains a visible gap.
- **Compiler-tier TypeScript CALLS:** a TypeScript compiler-API oracle over a
  pinned Ky revision measured 138 TP, 0 FP, and 0 FN for the scoped static call
  and constructor shapes.
- **Normal-tier TypeScript IMPORTS:** pinned Ky and Chainlit frontend scopes
  measured 456 TP, 0 FP, and 0 FN for the declared static resolution shapes.
- **TypeScript declared relationships:** Ky, Chainlit, and free-style scopes
  measured 13/13 `INHERITS`/`IMPLEMENTS` edges. The small count is explicit;
  it is not a language-wide guarantee.
- **Very large repository:** a pinned 39,222,246-line LLVM checkout indexed into
  729,625 nodes and 2,302,869 edges in 2,198 seconds with 9.43 GB peak RSS and
  a 2.89 GB database. Five warm queries measured 9.78 seconds p50 and 10.50
  seconds p95. A later focused localization path reduced one fixed query's
  repeat median to 3.02 seconds and fresh peak RSS to 0.627 GB without reducing
  index size.
- **Graph-only conceptual localization:** on frozen public LocBench `n=80`, a
  seed-quality change improved Acc@1 from `0.175` to `0.200` and MRR@10 from
  `0.219` to `0.260`. This remains a weak operating point; use `code-search`
  for conceptual discovery. The n=80 case list and preserved inputs are not
  in this repository, so treat it as an internal paired replay; see
  [`bench/public/locbench/README.md`](../bench/public/locbench/README.md).
- **Agent localization (reproducible):** the n=200 `code_localize_agent`
  baseline (file/class/function Acc@10 86.0 / 84.5 / 73.5) runs from a pinned
  instance list with [`bench/public/locbench/run.sh`](../bench/public/locbench/run.sh);
  budget about $50 and 4 hours.
- **Incremental equivalence:** the test matrix covers modification, deletion,
  TypeScript re-export, call relationships, and no-op lifecycle cases against
  clean rebuilds. See
  [`bench/accuracy/baselines/2026-08-13-incremental-clean-equivalence.md`](../bench/accuracy/baselines/2026-08-13-incremental-clean-equivalence.md).

The synthetic fixtures, oracles, and regression gates that back these numbers
live under [`bench/accuracy/`](../bench/accuracy/) and run in CI. The
historical v0.3.0 language-coverage exercise is preserved in
[benchmarks.md](benchmarks.md).
