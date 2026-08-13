# TypeScript method relationship accuracy — 2026-08-13

The normal graph now resolves direct, declared TypeScript and TSX method
overrides and interface implementations. An independent TypeScript 5.9.3
compiler-API oracle supplied ground truth; it reads compiler symbols and source,
never graph output. The public repositories and oracle bytes were frozen before
their corresponding graph queries.

| Candidate | TP | FP | FN | Precision | Recall | F1 |
|---|---:|---:|---:|---:|---:|---:|
| Prior normal graph | 0 | 0 | 70 | 0.000 | 0.000 | 0.000 |
| Candidate normal graph | 43 | 4 | 27 | 0.915 | 0.614 | 0.735 |

The hand-enumerated prove-the-instrument fixture improved from 0/2 to 2/2
method relationships and retained 5/5 declared type relationships: 7 TP, 0 FP,
and 0 FN overall.

## Public results

| Repository | Revision | Before TP/FP/FN | After TP/FP/FN |
|---|---|---:|---:|
| `microsoft/tsyringe` | `e033769d97cfb6cc4a8569e2b50eb32015453302` | 0/0/11 | 11/2/0 |
| `inversify/InversifyJS` | `fdd9186891e777884012984c64c271e576155f08` | 0/0/27 | 0/0/27 |
| `blakeembrey/free-style` | `eb921ab5457f327ac24f0ea2822715d73af4cfd3` | 0/0/9 | 9/0/0 |
| `nestjs/nest` `packages/core` | `674ac31d4f02d399fca9c491f352f5066d57b62d` | 0/0/23 | 23/2/0 |

The Nest override cell is the clearest public proof: all 15 compiler-declared
top-level overrides were recovered, with two conservative strict-comparator
false positives from overloaded `get` method families. Its override-only
precision is 0.882, recall is 1.000, and F1 is 0.938.

Across the three repositories whose in-scope types are represented as graph
class/interface nodes (`tsyringe`, `free-style`, and Nest core), the result is
43 TP, 4 FP, 0 FN: 0.915 precision, 1.000 recall, and 0.956 F1. The aggregate
four-repository row above remains the primary result so the current local-class
gap is not hidden.

## What changed

- TypeScript and TSX extraction now retains interface method signatures and
  abstract method signatures as graph method nodes.
- When a class directly extends or implements a project-local type, the normal
  pipeline links same-named declared methods with `OVERRIDE` edges. The owning
  type edge distinguishes compiler-oracle `overrides` from `implements`.
- Constructors are excluded from override generation.
- The existing type-relationship cells were byte-for-byte unchanged by this
  method work; no public type-relationship regression was observed.

## Remaining failure cells

All 27 false negatives are in InversifyJS test helpers that declare classes
inside functions. The graph currently emits no class or method endpoints for
those local declarations, so resolution has nothing to connect. Supporting
function-local classes is a separate graph-vocabulary change and was
deliberately not folded into this bounded patch.

The four strict false positives are name-level overloaded families:
`register`, `registerSingleton`, and two `get` families. The graph represents
the semantic same-name relationship, while the oracle's intentionally narrow
scope excludes overload sets. Signature-aware method identity is the bounded
follow-up if this cell matters in product usage.

This evidence does not claim language-wide compiler-grade accuracy. It excludes
structural interface satisfaction, inherited interface members, overload-set
identity, function-local classes, external declarations, dynamic mixins, and
prototype mutation.

The broader public search comparison and very-large-repository lifecycle and
resource measurements were already completed in their existing evidence
artifacts; they were not rerun for this relationship-only change.

Reproduce a repository comparison with:

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
