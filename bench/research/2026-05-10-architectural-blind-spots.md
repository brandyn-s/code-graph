# Architectural Blind Spots — Named Next-Plan Targets

**Date**: 2026-05-10
**Plan**: `~/Documents/knowledge-base/plans/2026-05-10-accuracy-gap-remediation.md` Phase K1
**Goal**: Three known-gap areas documented as named-next-plan targets so future plans can attack them with substrate-count commands and Phase 3.5 / 3.6 starter fields already populated.

These three gaps are flagged in `known_accuracy.known_gaps` (per `mcp__code-graph__index_status`) as recognized failure modes that current code-graph tuning cannot close — they require deeper architectural work. This doc preserves the substrate measurements so the next plan author can write Phase 3.5 baselines without re-discovering the substrate.

## Target 1: Indirect dispatch (closures / fn-pointers / trait-objects → 0 CALLS edges)

### Description

Indirect call expressions produce no syntactic call site that the AST extractor can pair with a target function. When code does:

```rust
let cb: Box<dyn Fn(&Event) -> Result<()>> = Box::new(handle_event);
listeners.push(cb);
// Later:
for cb in listeners { cb(&event); }  // ← no CALLS edge from this site to handle_event
```

…the dispatch site `cb(&event)` produces no edge because the AST sees only `cb` as a local binding, not a function reference. The same pattern applies to `Vec<Box<dyn Trait>>`, `fn(args) -> ret` typed parameters, and async-callback closures.

### Substrate (PSM Rust)

Substrate command: `grep -rE "Box<dyn |fn\(.*\)\s*->|FnOnce|FnMut|impl Fn" PSM/ --include="*.rs"`

Measured 2026-05-10:
- `Box<dyn Trait>` sites: **171**
- `fn(...) -> ...` type sites: **34**
- `FnOnce` / `FnMut` / `impl Fn` closure params: **46**

Total indirect-dispatch sites: ~250 (with overlap; conservative count is ~150 unique sites).

### Layer

Extractor — specifically the `extract_calls.c` path (tree-sitter AST traversal). The call expression node has no resolvable identifier — the closure is a captured local.

### Recoverable ceiling

Capturing all 150-250 indirect dispatch sites as edges requires either:

1. **Static analysis with type inference**: trace the closure's source (e.g., `Box::new(handle_event)`) to identify the underlying function. Out of scope for current AST-only extractor.
2. **Runtime tracing**: instrument the binary, collect actual dispatch targets at runtime, augment the static graph with observed edges. Out of scope (requires runtime infrastructure separate from code-graph).
3. **rust-analyzer integration**: rust-analyzer maintains type-inference state; querying it for closure sources could provide the missing edges. Substantial integration effort.

Recoverable ceiling estimate: **+150-250 CALLS edges on PSM** if option 1 lands, more with option 2. Effort: 2-4 weeks for option 1 (rust-analyzer integration); option 2 requires a separate runtime-observability project.

### Why named-next-plan, not in this plan

Requires fundamental extractor architecture work (type inference or rust-analyzer integration). Comparable in scope to the original Go LSP integration — multi-week effort.

### Phase 3.5 / 3.6 starter fields

```
Substrate: ~150-250 indirect-dispatch sites in PSM (verified 2026-05-10).
Layer: extractor (extract_calls.c). Type inference required.
Max recoverable lift: +150-250 CALLS edges, +5-10pp recall on PSM Rust if all sites captured.
Local→terminal ladder: extractor change → reindex PSM → CALLS edge count delta.
Prior-plan attribution: known_accuracy.known_gaps flagged "closures/fn-pointers/trait-objects produce 0 CALLS edges" as architectural. No prior plan has attacked this.
```

## Target 2: Rust IMPORTS resolver (cross-crate `use crate::...` paths)

### Description

The Rust IMPORTS resolver currently emits very few edges — it was explicitly dropped from the May 3 PSM Rust baseline because the oracle/resolver disagreement was too noisy to compare. The dominant failure mode is `use crate::module::Foo` paths in workspace member crates: the resolver doesn't fully resolve these to their actual definition sites.

### Substrate (PSM Rust)

Substrate command: `grep -rE "^use crate::" PSM/ --include="*.rs"` and `grep -rE "^use [a-z_]+::" PSM/ --include="*.rs"`

Measured 2026-05-10:
- `use crate::...` paths: **1548**
- `use {libname}::...` cross-crate paths: **10,875**

Total `use` paths in PSM: ~12,400.

### Layer

IMPORTS resolver in `internal/pipeline/imports.go` (Rust path). The resolver currently handles simple module-path lookup but doesn't fully resolve through `pub use` re-exports, `lib.rs`-style module roots, or the workspace's `Cargo.toml` member structure.

### Recoverable ceiling

Closing this gap depends on the resolver's depth:

1. **Direct path resolution**: `use libnav::types::Foo` → resolve to `libnav/types/src/lib.rs`'s `Foo` definition. Achievable with Cargo.lock + workspace metadata. ~70-80% of imports.
2. **Through-`pub use` resolution**: `use libio::Session` → through `libio/src/lib.rs`'s `pub use` → actual definition in `libio/src/sync/session.rs`. Requires resolver to chase re-exports. ~15-20% of imports.
3. **Macro-generated imports**: paths defined by `derive` macros or `proc_macro_derive`. ~5%.

Recoverable ceiling: **~10,000 IMPORTS edges** vs current single-digit-percentage emit rate.

### Why named-next-plan, not in this plan

The IMPORTS resolver is a semi-greenfield rewrite — current implementation handles ~10% of cases at best. Achieving ~90% recall requires:
- Cargo.lock parsing (workspace metadata)
- Through-`pub use` resolver state (a node-relabeling pass)
- Test fixtures across workspace members

Estimated effort: 3-4 weeks dedicated.

### Phase 3.5 / 3.6 starter fields

```
Substrate: 12,400 use paths in PSM (verified 2026-05-10).
Layer: IMPORTS resolver in internal/pipeline/imports.go.
Max recoverable lift: ~10,000 IMPORTS edges (currently single-digit-percentage emit rate).
Local→terminal ladder: resolver rewrite → reindex PSM → IMPORTS edge count delta + downstream effect on CALLS resolver-rule cross-package-import-map (resolves more candidates correctly).
Prior-plan attribution: known_accuracy "imports_caveats" notes "Rust resolver sparse on use crate:: paths". Previous baselines explicitly drop IMPORTS from F1 measurement. No prior plan has rewritten this resolver.
```

## Target 3: Macro-expanded code (invisible to AST + oracle)

### Description

Tree-sitter parses Rust source as raw tokens. Macros (`#[derive(...)]`, `tokio::main`, `format!`, `println!`, `tracing::info!`, custom procedural macros) generate code at compile time that the AST never sees. The same blind spot affects the oracle (syn-based) — both extractor and oracle see only the macro INVOCATION, not its EXPANSION.

When `#[derive(From)]` generates an `impl From for Foo` block, code-graph correctly captures the IMPLEMENTS edge via the `extractImplementsRust` derive-aware path. But when `tokio::main` generates an entire async runtime entry, or when `tracing::info!("hello {}", value)` generates a series of nested function calls, the generated code is invisible.

### Substrate (PSM Rust)

Substrate command:
```bash
grep -rE "^#\[derive\(" PSM/ --include="*.rs" | wc -l       # 3049
grep -rE "#\[tokio::" PSM/ --include="*.rs" | wc -l          # 231
grep -rE "(format!|println!|tracing::(info|warn|error|debug)!)" PSM/ --include="*.rs" | wc -l  # 5363
```

Measured 2026-05-10:
- `#[derive(...)]` macro invocations: **3,049**
- `#[tokio::*]` attribute macros: **231**
- `format!` / `println!` / `tracing::*` macro calls: **5,363**

### Layer

Pre-extraction. The fix requires `cargo expand`-style preprocessing — running rustc's macro expansion before AST extraction. This is a fundamental change to the extraction pipeline.

### Recoverable ceiling

Of the substrate above:

- **`#[derive(From)]` and similar trait derives**: ALREADY captured by `extractImplementsRust`'s derive-aware path. No additional work needed.
- **`#[tokio::main]` and async-runtime macros**: generate an async runtime entry. Capturing this would surface "main calls runtime.block_on" — useful for blast-radius queries on async services. Recoverable if `cargo expand` integration lands.
- **`format!` / `tracing::*`**: generate internal `std::fmt::Arguments` calls. Recoverable but the generated calls are largely structural noise (every formatter call producing edges to `Arguments::new_v1`); marginal value.

Recoverable ceiling: ~200-400 additional CALLS/USAGE edges if `cargo expand` integration lands, mostly for tokio runtime wiring. Marginal recall improvement.

### Why named-next-plan, not in this plan

Requires running `cargo expand` (or a tree-sitter-rust-with-macros variant) BEFORE extraction. This is:

1. A pre-extraction pipeline change (current pipeline parses raw source).
2. Slower indexing (cargo expand is multi-second per crate).
3. Limited recall improvement (most macro-generated code is structural).

Effort: 2-3 weeks. ROI may be lower than the IMPORTS resolver work.

### Phase 3.5 / 3.6 starter fields

```
Substrate: ~8,500 macro-call sites in PSM (3049 derives + 231 tokio + 5363 format/tracing/etc).
Layer: pre-extraction (cargo expand integration).
Max recoverable lift: ~200-400 additional edges; most macro expansions are structural rather than load-bearing for blast-radius queries.
Local→terminal ladder: integrate cargo expand → reindex PSM → measure CALLS / USAGE edge delta. May regress index time substantially.
Prior-plan attribution: known_accuracy "macro-expanded code invisible on both oracle and extractor" — known architectural gap. No prior plan attempted.
```

## Summary

| Target | Substrate | Recoverable ceiling | Effort | Priority for next-plan |
|---|---|---|---|---|
| 1. Indirect dispatch | 150-250 sites | +5-10pp Rust CALLS recall | 2-4 weeks | Medium-high (target the closures + trait-objects, not full type-inference) |
| 2. IMPORTS resolver | 12,400 use paths | ~10,000 IMPORTS edges; downstream CALLS lift | 3-4 weeks | High (closes the largest measured-gap area; cascades into CALLS resolver-rule precision) |
| 3. Macro expansion | 8,500 macro sites | +200-400 edges; marginal | 2-3 weeks | Low (ROI vs effort) |

**Recommendation**: Target 2 (IMPORTS resolver) is the highest leverage because it cascades — fixing imports lets the CALLS resolver-rule's `cross-package-import-map` strategy fire on more candidates, lifting CALLS precision via the existing PR #276 import-reachability gate. The other two targets have isolated effects.

These targets are NOT in scope for the 2026-05-10 accuracy gap remediation plan — they require dedicated multi-week investments comparable to the original Go LSP integration. They are surfaced here so future plan authors can quote substrate counts without re-discovering them.
