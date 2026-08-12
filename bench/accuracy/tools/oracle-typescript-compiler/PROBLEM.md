# TypeScript compiler CALLS oracle

## Problem

The optional TypeScript SCIP tier needs an accuracy measurement that does not
read SCIP artifacts, graph databases, or graph output. Without it, successful
index generation demonstrates operability but not edge correctness.

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

## Independence and limits

The implementation shares no code or data path with SCIP ingestion and never
reads code-graph output. It does use the TypeScript compiler front end, as does
`scip-typescript`, so the hand-enumerated fixture is the independent check on
the oracle itself. This supports a compiler-tier accuracy claim for the tested
scope, not a claim of complete dynamic JavaScript/TypeScript call resolution.
