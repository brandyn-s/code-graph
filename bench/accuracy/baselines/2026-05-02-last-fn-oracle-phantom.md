# Last persistent FN — oracle phantom, not code-graph miss

**Date**: 2026-05-02
**FN edge**: `Store.Close → ConfigStore.Close`
**Verdict**: **Oracle phantom.** The edge does not exist in the source. Code-graph correctly omits it. The oracle hallucinates it via flawed bare-name resolution.

## Source code

```go
// internal/store/store.go:261
func (s *Store) Close() error {
    return s.db.Close()
}

// internal/store/config.go:128
func (c *ConfigStore) Close() error {
    return c.db.Close()
}
```

`s.db` and `c.db` are both `*sql.DB` (stdlib). Both `Close()` calls go to `sql.DB.Close()` — **external, not internal**. Neither method calls `ConfigStore.Close` internally.

## What the oracle records

`bench/accuracy/cache/go-ast-code-graph-go-e2807ce.json` shows TWO `*.Close → ConfigStore.Close` edges:

```
from: c-Users-...-internal-store.config.ConfigStore.Close
to:   c-Users-...-internal-store.config.ConfigStore.Close       (self-loop!)
file: config.go, line: 129

from: c-Users-...-internal-store.store.Store.Close
to:   c-Users-...-internal-store.config.ConfigStore.Close       (the persistent FN)
file: store.go, line: 262
```

The self-loop is the giveaway: it's clearly impossible for `ConfigStore.Close` to call itself recursively. Both edges are wrong.

## Why the oracle hallucinates them

For deep-selector calls like `s.db.Close()`, the oracle's `extractCallee` (line 162-179) cannot determine the type of `s.db`. It falls through to the `// For deeper selectors or type assertions, emit just the method name.` path and emits **bare callee name** `"Close"`.

The Python wrapper (oracle_go_ast.py:168-181) then resolves bare names against `fn_def_map`:

```python
if len(segs) == 1:
    resolved = fn_def_map.get(to)
    if resolved:
        kept.append(Edge(from_qn=..., to_qn=resolved, ...))
```

`fn_def_map` is built as a **first-write-wins** dict: `simpleName → first_qn_seen`. With `ConfigStore.Close` being the only `Close` defined in the indexed subset, every bare-name `Close` call gets resolved to it.

So:
- `Store.Close` calls `s.db.Close()` → oracle emits `"Close"` → wrapper resolves to `ConfigStore.Close` → fake edge.
- `ConfigStore.Close` calls `c.db.Close()` → same path → self-loop.

Both edges are oracle artifacts, not source truth.

## Implication

This is the third distinct oracle bug surfaced in this session:

1. **PR #137** (this session, runIncrementalPasses): oracle DROPS real receiver method calls when receiver identifier doesn't match a filename. ~34 fake FPs at one site.
2. **PR #138** (this session, store deferral): same drop pattern at store's top sites. Likely accounts for tens of fake FPs across internal/store.
3. **This finding** (last persistent FN): oracle HALLUCINATES receiver method calls when the receiver type is unknown and bare-name resolution lands on the wrong method. 1 fake FN at this site; potentially more wherever method names collide.

All three are facets of the same root issue: the oracle has no type information. Without it, `recv.method` can't be resolved correctly, and the wrapper's heuristics fail in both directions (drop real edges, hallucinate fake ones).

## Action

- No code-graph changes (this PR is just the diagnostic record).
- After follow-up #5 (oracle receiver-method resolution) lands, this fake FN should disappear. Expected end state: 0 FNs on the Go fixture once the oracle uses Go's type system properly.
- The 6 original baseline FNs are now fully accounted for: 5 were caller-QN-format mismatch (PR #136 fixed) + 1 was an oracle phantom (this finding). None were real code-graph misses.

## What this means for the harness's confidence

The Step 6 baseline reported FN=6 and treated those as resolver gaps. None of them were. The harness's FN signal on this fixture was effectively noise. Both Phase Y.2's premise ("close the FN cluster") and the entire FN-cluster dimension of the recipe were measuring instrument noise, not code-graph behavior.

The recipe's Step 5b ("sample 3-5 of the cell's edges and verify they are real failures") is essential — and should fire for FNs as much as FPs.
