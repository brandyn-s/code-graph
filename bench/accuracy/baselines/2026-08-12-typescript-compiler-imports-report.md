# TypeScript compiler-derived IMPORTS accuracy — 2026-08-12

The normal graph's module-level `IMPORTS` edges were compared with an
independent TypeScript 5.9.3 compiler-API oracle. The oracle resolves each
static import or re-export to a project-local source file and never reads the
graph database or graph output. A hand-enumerated fixture checks the oracle
before public-repository use.

| Repository | Candidate | TP | FP | FN | Precision | Recall | F1 |
|---|---|---:|---:|---:|---:|---:|---:|
| `sindresorhus/ky` | Prior | 0 | 0 | 83 | 0.000 | 0.000 | 0.000 |
| `sindresorhus/ky` | Candidate | 83 | 0 | 0 | 1.000 | 1.000 | 1.000 |
| `Chainlit/chainlit` frontend | Candidate | 373 | 0 | 0 | 1.000 | 1.000 | 1.000 |
| **Aggregate candidate** |  | **456** | **0** | **0** | **1.000** | **1.000** | **1.000** |

The correction addresses three measured mechanisms:

- Python relative-import normalization was incorrectly applied to every
  language, mutating TypeScript `./module.js` specifiers before JS resolution.
- TypeScript source imports commonly name emitted `.js` files while the
  compiler resolves them to `.ts` or `.tsx` source modules.
- Barrel files use `export ... from`, which is a module dependency even though
  it introduces no local call-resolution binding.

The candidate also resolves unique root-relative TypeScript specifiers such as
`components/Button`, matching projects that use `baseUrl`. Ambiguous suffixes
remain unresolved rather than guessed, scoped-package names remain external,
and non-code relative assets are excluded so CSS imports cannot collapse into
self edges.

This is a strong result for the measured TypeScript compiler program scopes. It
does not establish package-exports, arbitrary `paths` glob, dynamic `import()`,
JavaScript-without-tsconfig, or language-general IMPORTS accuracy.

Reproduce with:

```bash
cd bench/accuracy/tools/oracle-typescript-compiler
npm ci
npm test
node main.cjs /path/to/tsconfig.json > oracle.json

python3 bench/accuracy/compare_typescript_imports.py \
  --oracle oracle.json \
  --database /path/to/project.db \
  --project project-name \
  --output report.json
```
