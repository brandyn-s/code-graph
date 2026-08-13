# TypeScript compiler relationship oracle

## Problem

TypeScript graph relationships need accuracy measurements that do not read
SCIP artifacts, graph databases, or graph output. Without them, successful
index generation and tree-sitter extraction demonstrate operability but not
edge correctness. The measured unit is one project-local edge. Exact endpoint
agreement is success; false positives and false negatives are equally costly
because either can invalidate a relationship proof.

## Contract

The oracle loads one `tsconfig.json` through TypeScript's public compiler API,
uses the type checker to resolve each source call or constructor expression,
and emits unique caller/callee declaration coordinates for project-local graph
endpoints. The measured vocabulary includes declarations, methods,
constructors, accessors, and top-level variable-assigned functions. Nested
anonymous functions are attributed to the enclosing stored graph function;
callable type signatures and dynamic targets without a resolved signature are
outside the measured scope.

The hand-enumerated fixture is deliberately small and exercises cross-file
imports, functions, constructors, methods, a callable interface, and a nested
arrow. Its exact edge set must pass before the oracle is used on a real
repository.

The same compiler program also enumerates project-local `extends` and
`implements` heritage clauses. It preserves the clause kind and binds both
endpoints to declaration coordinates. The production graph projects them as
`INHERITS` and `IMPLEMENTS`, respectively.

For classes, the oracle additionally compares directly declared, uniquely
named methods with methods declared directly on each project-local heritage
target. Matching methods on a directly extended class are `overrides`; matching
methods on a directly implemented class or interface are `implements`. The
scope deliberately excludes inherited interface members, structural
satisfaction without an `implements` clause, fields/accessors, overload sets,
external declarations, mixins, and prototype mutation. Those exclusions keep
each compiler edge exactly representable by the graph's method-node vocabulary.

## Independence and limits

The implementation shares no code, parser, resolver, or data path with the
tree-sitter production relationship resolver and never reads code-graph
output. For CALLS it does use the TypeScript compiler front end, as does
`scip-typescript`, so the hand-enumerated fixture is the independent check on
that oracle. For normal-tier type and method relationships, the compiler and
production parser stacks are independent. The comparator recovers method
relationship kind from the owning type pair (`INHERITS` or `IMPLEMENTS`) rather
than treating every graph `OVERRIDE` edge as equivalent. This supports accuracy
claims only for the tested static, direct, project-local declarations—not
dynamic mixins, declaration-only packages, overloads, or language-wide
relationship completeness.
