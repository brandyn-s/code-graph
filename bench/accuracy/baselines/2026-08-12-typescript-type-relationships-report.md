# TypeScript declared relationship accuracy — 2026-08-12

The normal graph was compared with an independent TypeScript 5.9.3 compiler-
API oracle on three pinned public projects. The oracle reads source and compiler
symbols; it never reads graph output or uses the graph as truth. A separate
hand-enumerated fixture proves five oracle edges before public measurement.

| Candidate | TP | FP | FN | Precision | Recall | F1 |
|---|---:|---:|---:|---:|---:|---:|
| Prior normal graph | 0 | 0 | 13 | 0.000 | 0.000 | 0.000 |
| Candidate normal graph | 13 | 0 | 0 | 1.000 | 1.000 | 1.000 |

The candidate result contains ten exact `INHERITS` edges from declared
`extends` clauses and three exact `IMPLEMENTS` edges. The measured repositories
and immutable revisions are:

- `sindresorhus/ky` at `3419113b48e034fdcf8fa6bd3be3da7b3d0d758f`;
- `Chainlit/chainlit` frontend at
  `8b2d4bacfd4fa2c8af72e2d140d527d20125b07b`;
- `blakeembrey/free-style` at
  `eb921ab5457f327ac24f0ea2822715d73af4cfd3`.

The dominant prior failure was extraction: TypeScript heritage clauses were
flattened or absent, so no measured relationship reached resolution. The fix
preserves clause kind and each declared target, including generic targets and
interfaces extending local type aliases. It then maps `extends` to `INHERITS`
and `implements` to `IMPLEMENTS` before normal registry resolution.

This is exact evidence for project-local declared TypeScript relationships in
the measured scope. It is not a claim about structural interface satisfaction,
runtime prototype changes, external dependency relationships, or other
languages.

Reproduce a repository measurement with:

```bash
node bench/accuracy/tools/oracle-typescript-compiler/main.cjs \
  /path/to/repo/tsconfig.json > oracle.json
python3 bench/accuracy/compare_typescript_relationships.py \
  --oracle oracle.json \
  --database /path/to/project.db \
  --project project-name \
  --repository-root /path/to/repo \
  --source-revision <git-object-id> \
  --output relationship-report.json
```
