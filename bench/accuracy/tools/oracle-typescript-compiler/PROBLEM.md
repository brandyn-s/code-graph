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
endpoints to declaration coordinates. The production graph currently projects
both clause kinds as `INHERITS`; the comparison may score that common edge
vocabulary while retaining the compiler's kind as a diagnostic dimension.

## Independence and limits

The implementation shares no code, parser, resolver, or data path with the
tree-sitter production relationship resolver and never reads code-graph
output. For CALLS it does use the TypeScript compiler front end, as does
`scip-typescript`, so the hand-enumerated fixture is the independent check on
that oracle. For normal-tier type relationships, the compiler and production
parser stacks are independent. This supports accuracy claims only for the
tested static, project-local declarations—not dynamic mixins, declaration-only
packages, or language-wide relationship completeness.
