# Edge types

Every relationship kind the graph can hold, in the order the store declares them.
The source of truth is `internal/store/edge_types.go`; a test fails when production
code emits an edge type that is not in this table, and when this page omits one.
Use `get_graph_schema` for the types actually present in a given index, and
`get_relationship_evidence` for the resolver, confidence, and revision behind any
single edge.

## Structure

| Type | Source | Target | Meaning |
|---|---|---|---|
| `CONTAINS` | Directory or Package | Directory, Package, or File | Filesystem and package containment produced by the structure pass. |
| `CONTAINS_FILE` | Directory | File | Direct file containment used for fast per-directory listings. |
| `DEFINES` | File or Module | Function, Class, or Variable | A file or module defines a top-level symbol. |
| `DEFINES_METHOD` | Class, Struct, or Trait | Method | A type defines a method. |
| `DEFINES_FIELD` | Class or Struct | Field | A type defines a field. |
| `MEMBER_OF` | Symbol | Community | Louvain community membership computed after indexing. |
| `PARAMETER_OF` | Parameter | Function | A parameter belongs to a function; used by data-flow reachability. |

## Calls

| Type | Source | Target | Meaning |
|---|---|---|---|
| `CALLS` | Function | Function | A resolved call. Properties carry resolver rule, strategy, confidence, and, for SCIP-derived edges, the artifact digest. |
| `CALLS_EXTERNAL` | Function | External stub | A call whose target lives outside the indexed repository. |
| `CALLS_PSEUDO` | Function | Pseudo target | A call to a language construct modelled as a pseudo node (modal dispatch, builtins). |
| `INDIRECT_CALLS` | Function | Function | A call reached through a function value, callback, or dispatch table. |
| `HTTP_CALLS` | Function | Route handler | A cross-service HTTP call matched to the handler that serves the route. |
| `ASYNC_CALLS` | Function | Function | A call across an async boundary (task spawn, message dispatch). |
| `HANDLES` | Route | Function | A route node is served by a handler function. |
| `CALL_REFERENCE` | Function or Module | Function or Method | A callable referenced at a value site (assignment, collection literal, argument) that resolves to exactly one target; not an invocation. The proven-single-target counterpart of `USAGE`. |

## Types and inheritance

| Type | Source | Target | Meaning |
|---|---|---|---|
| `IMPORTS` | Module or File | Module | An import statement, normalized for relative imports. |
| `IMPLEMENTS` | Type | Interface or Trait | A type implements an interface or trait. |
| `INHERITS` | Class | Class | Class inheritance. |
| `OVERRIDE` | Method | Method | A method overrides a parent or interface method. |
| `USES_TYPE` | Function or Field | Type | A signature or field references a type. |
| `USAGE` | Function or Module | Variable, Constant, Type, or Function | An identifier used at a value site where no unique callable target is proven (a non-callable symbol, or an ambiguous or fuzzy resolution). The unproven counterpart of `CALL_REFERENCE`. |
| `DECORATES` | Decorator | Function or Class | A decorator or attribute applied to a definition. |

## Data flow and errors

| Type | Source | Target | Meaning |
|---|---|---|---|
| `READS` | Function | Variable or Field | A read of a variable or field. |
| `WRITES` | Function | Variable or Field | A write to a variable or field. |
| `THROWS` | Function | Type | A function throws an exception type (statically typed languages). |
| `RAISES` | Function | Type | A function raises an exception type (Python). |

## Tests

| Type | Source | Target | Meaning |
|---|---|---|---|
| `TESTS` | Test function | Function | A test exercises a production function. |
| `TESTS_FILE` | Test file | File | A test file covers a production file by convention. |

## Configuration, infrastructure, and policy

| Type | Source | Target | Meaning |
|---|---|---|---|
| `CONFIGURES` | Config file or Service | Service, Function, or Variable | Configuration links a config artifact to the code it configures. |
| `DEPENDS_ON` | Package or Service | Package or Service | A declared dependency from a lockfile, manifest, or infrastructure module. |
| `READS_ENV` | Function | EnvVar | Code reads an environment variable. |
| `POLICY_GATES` | Policy | Function or Route | An OPA policy gates the target. |
| `RUNS_BINARY` | Service | Binary | A declared service runs a binary (Nix modules). |

## Messaging

| Type | Source | Target | Meaning |
|---|---|---|---|
| `PUBLISHES_TO` | Service or Function | Topic | Publishes to a topic (Zenoh, Nix service declarations). |
| `SUBSCRIBES_TO` | Service or Function | Topic | Subscribes to a topic. |
| `QUERIES` | Function | Topic | Issues a query on a topic (Zenoh). |
| `ANSWERS` | Function | Topic | Answers queries on a topic (Zenoh queryable). |

## Derived

| Type | Source | Target | Meaning |
|---|---|---|---|
| `FILE_CHANGES_WITH` | File | File | Co-change coupling mined from git history. |
| `SEMANTICALLY_SIMILAR_TO` | Function | Function | Embedding cosine similarity above threshold (opt-in). |
| `RATIONALE_FOR` | Rationale | Function or Class | A WHY/SAFETY/NOTE annotation explains the target. |

## Adding an edge type

1. Add the constant and an `EdgeTypeInfo` row in `internal/store/edge_types.go`.
2. Add the row above (the store test checks this file mentions every type).
3. Emit edges with `store.Edge<Name>`; literal strings in `Type:` fail the test.
4. If tools should traverse it, add it to the relevant family filter in `internal/store` and to `get_graph_schema`'s description.
