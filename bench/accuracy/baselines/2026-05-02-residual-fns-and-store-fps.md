# Post-Y.5 residuals — 2 FNs and 72 store FPs

**Date**: 2026-05-02
**Baseline**: post-oracle-Y.5 fix (PR #140). F1=0.9801.
**Scope**: investigates the 2 remaining FNs and the largest residual error mass (internal/store's 72 FPs / 229 method-body FPs).

## Part A — 2 residual FNs are a real code-graph extraction bug

The 2 FNs persisted post-Y.5:

```
ConfigStore.GetBool -> ConfigStore.Get
ConfigStore.GetInt  -> ConfigStore.Get
```

Both are direct same-receiver method calls in `internal/store/config.go`:

```go
func (c *ConfigStore) GetBool(key string, defaultVal bool) bool {
    raw := c.Get(key, "")    // line 68 — should emit ConfigStore.GetBool -> ConfigStore.Get
    ...
}

func (c *ConfigStore) GetInt(key string, defaultVal int) int {
    raw := c.Get(key, "")    // line 81 — same shape
    ...
}
```

These are exactly the case Y.5 was designed to track (self-receiver method dispatch). The oracle correctly emits both edges. **Code-graph emits zero CALLS edges from GetBool, GetInt, or Close.**

### Bug isolation

Three layers:

1. **CBM call extractor** — VERIFIED CORRECT. A standalone Go test against a synthetic `ConfigStore.GetBool` source produces both expected calls (`c.Get` and `strconv.ParseBool`) with correct `EnclosingFuncQN`. So CBM is not the bug.

2. **`collectLSPResolvedEdges` + `resolveCallEdge`** — likely bug. The LSP path's `lspCallerMethods` deduplication tracks `(CallerQN, shortName)` to suppress the registry path for already-LSP-resolved calls. If the LSP claims to resolve `(GetBool, "Get")` but produces a malformed `CalleeQN` that gets filtered downstream, the edge is silently lost — both paths skip emit.

3. **Store layer** — unlikely. `Set`, `Delete`, `All` and `Get` itself emit edges normally; the schema works.

### Why GetBool, GetInt, Close specifically

Inspected all methods in `config.go`:

| Method | Internal calls in source | Emitted CALLS edges |
|--------|---:|---:|
| `Get` | 0 (only external `QueryRowContext.Scan`) | 2 (Querier.QueryRowContext, scanner.Scan) ✓ |
| `Set` | 0 (only external `ExecContext`) | 1 (Querier.ExecContext) ✓ |
| `Delete` | 0 (only external `ExecContext`) | 1 ✓ |
| `All` | 0 (only external `QueryContext`, `Scan`) | 2 ✓ |
| `GetBool` | 1 (`c.Get`) + 1 external (`strconv.ParseBool`) | **0 ✗** |
| `GetInt` | 1 (`c.Get`) + 1 external (`strconv.Atoi`) | **0 ✗** |
| `Close` | 0 (only external `c.db.Close`) | **0 ✗** |

The methods that emit have either a chained-method call (`c.db.X(...).Y(...)`) or a single direct external call. The methods that DON'T emit have either:

- A direct same-receiver internal call (GetBool, GetInt: `c.Get(...)`)
- Or a single deep-selector return-statement call (Close: `return c.db.Close()`)

Without C-side debugging, the precise trigger isn't known. Likely the LSP-resolution path identifies a candidate but emits an invalid edge that gets filtered, while flagging the (caller, shortName) pair in `lspCallerMethods` so the registry fallback also skips.

### Recommended fix path

Open follow-up #6a: instrument `collectLSPResolvedEdges` and `resolveCallEdge` with per-call trace logging, run on a 3-method synthetic Go fixture (one self-receiver call, one chained-call, one return-statement call), and observe where each call is dropped.

**Headroom**: 2 FN → TP. Negligible F1 impact (~0.1pp recall). Worth fixing for correctness, not for the metric.

## Part B — 72 store FPs are oracle-side interface-dispatch tracking gaps

internal/store has 72 FPs post-Y.5 (P=0.51, the lowest of the 5 subsets). Top callee names:

| Callee simple name | FP count | Pattern |
|--------------------|---------:|---------|
| `Query` | 28 | `Querier.Query` interface dispatch |
| `Scan` | 16 | `nodes.scanner.Scan` interface dispatch |
| `Exec` | 9 | `Querier.Exec` interface dispatch |
| `ExecContext` | 5 | `Querier.ExecContext` |
| `Now` | 4 | likely `time.Now` (external) |
| `QueryRow`, `QueryContext`, `Prepare`, `Open`, `QueryRowContext` | 9 total | All Querier interface methods |
| `migrate`, `initSchema` | 2 | self-receiver methods |

**66 of 72 (92%) are calls to `internal-store.store.Querier.<method>`**, which is the project's own SQL Querier interface (defined in `store.go`, abstracts over `*sql.DB` and `*sql.Tx`).

### What's happening

Code-graph stores a `Querier` field on `Store`:

```go
func (s *Store) Q() Querier { return s.q }
```

Inside methods like `(s *Store) archBoundaries()`, code does:

```go
rows, err := s.Q().Query(ctx, "SELECT ...")
```

Code-graph's resolver tracks `s.Q()`'s return type (Querier) via type inference and resolves `Query` to `Querier.Query` (the interface method definition in store.go). This is a **real, correctly resolved** edge.

The oracle has no type info. Its `extractCallee` sees the call expression `s.Q().Query(...)` whose `.X` is a CallExpr (not an Ident), falls into the deep-selector branch, and emits bare `"Query"`. The wrapper's bare-name resolution then either drops (multiple Query candidates) or resolves to the wrong target.

After my Y.5 wrapper change (drop multi-candidate bare names), these edges are dropped — they don't reach the oracle's TP set. Code-graph emits them; oracle doesn't. They count as FPs.

### The edges are correct; the oracle can't see them

Spot-checked 5 of the 72: every one is a real, type-correct interface-method call in source. The receiver type chain `s.Q()` → Querier is statically derivable, but only with method-return-type tracking.

### Recommended fix path

Open follow-up #7a: extend the oracle binary to track method return types within a file. When parsing `func (X) Method() Y { ... }`, record `(X, Method) → Y`. When seeing a call `<recv>.method().method2()`, look up `<RecvTypeOfRecv, method>` in the return-type map to determine the type that `.method2` is being called on. Resolve to `<Y>.<method2>` form, which the wrapper's `recv_method_to_qns` index already handles.

**Headroom**: ~70 FP → TP for internal/store. Aggregate F1 lift estimate: ~+1pp (P 0.96 → 0.97). Per-project: internal/store F1 from 0.67 → 0.85+.

### Why this is "lower priority" than the Y.5 instrument fix

The Y.5 fix recovered 545 TPs and dropped 334 FPs (+8.7pp F1). This proposed return-type tracking would recover ~70 TPs and drop ~0 FPs (~+1pp F1). Diminishing returns. Worth doing if other measurement work is happening anyway, but no longer the highest-ROI follow-up.

## Summary table

| Residual | Cause | Layer | Fix effort | Impact |
|----------|-------|-------|------------|--------|
| 2 FNs (GetBool/GetInt → Get) | Code-graph extraction bug (LSP-vs-registry interaction) | code-graph internal/pipeline + cbm LSP | Medium (C-side debugging) | ~0.1pp recall |
| 72 store FPs (Querier interface dispatch) | Oracle method-return-type tracking gap | bench/accuracy oracle | Medium (oracle Go AST extension) | ~+1pp F1, +18pp internal/store F1 |
| 6 store FPs (other patterns) | Mix — likely external calls (time.Now) and self-receiver methods that should resolve | code-graph + oracle | Small | <0.5pp |

## Next priorities

After this report, the F1 0.9801 baseline is fairly close to ceiling for the Go fixture. The two residual classes (CBM/LSP extraction bug + oracle method-chain return-type tracking) each need their own focused investigation. Neither blocks production use of code-graph — they're long-tail accuracy work.

A fresh Go fixture (different repo) would be more valuable than further squeezing the code-graph self-hosting fixture. Self-hosting fixtures hit a ceiling where remaining residuals are oracle limitations specific to the harness's static analysis depth.
