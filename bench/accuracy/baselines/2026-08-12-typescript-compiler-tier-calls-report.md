# TypeScript compiler-tier CALLS accuracy — 2026-08-12

The candidate graph was evaluated on the public
[`sindresorhus/ky`](https://github.com/sindresorhus/ky) repository at
`3419113b48e034fdcf8fa6bd3be3da7b3d0d758f`. A pinned
`scip-typescript v0.4.0` index was compared with an independent TypeScript 5.9.3
compiler-API oracle. The oracle never reads SCIP, the graph database, or graph
output; a six-edge hand-enumerated fixture independently checks the oracle's
scope before use.

| Candidate | TP | FP | FN | Precision | Recall | F1 |
|---|---:|---:|---:|---:|---:|---:|
| Prior ingest | 85 | 0 | 53 | 1.000 | 0.616 | 0.762 |
| Candidate ingest | 138 | 0 | 0 | 1.000 | 1.000 | 1.000 |

The 53 missed edges had three general causes in our SCIP projection:

- `scip-typescript` represents top-level variable-assigned functions as value
  symbols, while the ingest accepted only symbols ending in `)`;
- generic calls place type arguments between a symbol reference and `(`, while
  call-shape recognition required an immediate `(`;
- a SCIP definition token may begin inside a declaration while the matching
  graph node begins at the declaration boundary.

The correction recognizes TypeScript value symbols only when their SCIP
enclosing range exactly matches an existing graph function span, accepts
balanced generic type arguments before `(`, and canonicalizes definition
coordinates to the containing graph node. This retains the no-fuzzy-endpoint
contract: ordinary local variables are explicitly covered by a negative test.

This is a strong compiler-tier result for the tested TypeScript scope and
fixture. It is not a language-general graph claim, a dynamic JavaScript call
graph, or evidence about repositories not measured here.

Reproduce the oracle and comparison with:

```bash
cd bench/accuracy/tools/oracle-typescript-compiler
npm ci
npm test
node main.cjs /path/to/ky/tsconfig.json > oracle.json

python3 bench/accuracy/compare_compiler_calls.py \
  --oracle oracle.json \
  --database /path/to/project.db \
  --project project-name \
  --scip-index /path/to/index.scip \
  --output report.json
```
