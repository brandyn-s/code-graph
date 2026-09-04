# Resolver tiers

Call resolution turns each extracted call site into a `CALLS` edge, or
counts it as unresolved on the caller (`unresolved_call_count`). Two tiers
exist; the second is opt-in.

| Tier | Selected by | What it uses |
|---|---|---|
| `registry` (default) | always on | The project symbol registry (exact, same-module, import-map, unique-name, suffix and fuzzy strategies), constructor-assignment typing of locals (`inferTypesCBM`), field and return-type chain walking, Rust trait-impl mapping, and the Go LSP module in C (`internal/cbm/lsp/go_lsp.c`). |
| `lsp_local` | `CODE_GRAPH_RESOLVER_TIER=lsp_local` | Everything above plus, for Python and Rust: parameter type annotations, pytest fixture parameters, return annotations the extractor does not surface, and method lookup on base classes. Implemented in Go from the definitions and source text already in hand (`internal/pipeline/resolver_tier_lsp_local.go`). |

Upstream codebase-memory-mcp resolves Python and Rust with dedicated C
modules (`py_lsp.c`, `rust_lsp.c`, their own type registry and negative
memo; about 12k lines). This fork's `lsp_local` tier is not a port of those
modules. It is the measured, low-risk subset that closed the gaps visible in
this fork's own unresolved-call diagnostics.

## What `lsp_local` does

1. **Annotated parameters.** `def register(self, bp: "Blueprint")` and
   `fn run(m: &RegexMatcher)` bind the parameter to the class in the
   per-function type map, so `bp.register(...)` resolves through the same
   receiver-type path as constructor-typed locals. `Optional[X]`, `X | None`,
   quotes, `&`, `&mut`, lifetimes, `Box<X>`, `Arc<X>` and `Option<X>` are
   transparent; containers such as `list[X]` and `Vec<X>` are not bound.
2. **pytest fixtures.** A fixture's type comes from its return annotation or
   from its `return`/`yield` expression (`app = Flask(...); return app`,
   `return app.test_client()`, `with app.test_client() as c: yield c`),
   resolved iteratively so fixtures that depend on fixtures work. Untyped
   parameters of test functions, fixtures and functions in `test_*.py` /
   `conftest.py` are bound to the fixture type. A fixture defined in the
   same file wins over the nearest `conftest.py`, which wins over the
   project-wide name; a name whose fixtures disagree on type across files
   is not bound.
3. **Return annotations.** `-> "FlaskClient"` and `-> Result<Searcher, E>`
   feed the return-type map used by the chain walker for
   `obj.method().other()`.
4. **Inherited methods.** When the receiver's class is known but
   `Class.method` does not exist, the base classes (Python `class X(Base)`)
   are searched breadth-first; a hit resolves with strategy
   `lsp_local_inherited` and rule `receiver-qualified`.

Edges whose receiver root was typed by the tier, or that were resolved by the
inherited lookup, carry `resolver_tier: "lsp_local"`. Return-annotation
injections are counted in the `resolver.lsp_local.built` log line but are not
tagged per edge.

## Measuring

```bash
make build
python bench/accuracy/unresolved_calls.py /path/to/repo --debug            # default tier
CODE_GRAPH_RESOLVER_TIER=lsp_local python bench/accuracy/unresolved_calls.py /path/to/repo --debug
```

`--debug` indexes with `RESOLVER_TIER2_DEBUG=1` and classifies every dropped
call site as external (no project symbol with that short name; no local tier
can recover it) or in-registry (a candidate existed but could not be
discriminated).

Measured 2026-09-04 on the adversarial fixtures (flask `2ac8988`, requests
`f43f750`) and ripgrep (`HEAD` of the day), `.py` / `.rs` files only:

| Repository | Call sites | Unresolved, registry | Unresolved, lsp_local | CALLS edges | `resolver_tier=lsp_local` edges |
|---|---|---|---|---|---|
| flask | 2793 → 2755 | 1810 (0.648) | 1674 (0.608) | 983 → 1081 | 396 |
| requests | 2025 | 1140 (0.563) | 1139 (0.562) | 885 → 886 | 1 |
| ripgrep | 10627 → 10628 | 6260 (0.589) | 6225 (0.586) | 4367 → 4403 | 0 (return-annotation gains only) |

Of flask's in-registry unresolved sites, 777 became 601; the tier applied
106 parameter bindings, 492 fixture bindings and 397 inherited resolutions.
Rust parameters were already typed by the extractor's type-assign records,
so the tier's Rust contribution is limited to return annotations.

Adversarial CALLS F1 (pycg oracle, package scope): flask 0.588 → 0.585
(P 0.564 → 0.559, one additional false positive, recall unchanged);
requests 0.681 → 0.681. Most of the tier's new edges are in test code, which
the oracle scope does not cover. The tier therefore stays opt-in until a
fixture with test-scope ground truth shows the precision of the new edges.

## What remains from upstream's hybrid LSP

- Rust: `Type::new().builder().build()` chains rooted in a static call whose
  return type is `Self` or a generic, trait-method resolution through generic
  bounds, `impl Trait for Type` dispatch beyond the existing trait-impl map,
  and crate-path (`grep_searcher::SearcherBuilder`) to workspace-member
  mapping. ripgrep's largest in-registry unresolved buckets
  (`SearcherBuilder::new`, `RegexMatcher::new`, `td.path`) are of this kind.
- Python: flow-sensitive local typing (reassignment, `isinstance` narrowing),
  class attributes assigned outside `__init__`, `@property` return types,
  decorators that change signatures, and `**kwargs`-driven construction.
- Both: the negative memo and cross-file type registry that let upstream skip
  known-external receivers cheaply.
