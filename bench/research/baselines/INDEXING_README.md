# Indexing Throughput Baselines

Per-run measurements of `pipeline.Run()` wall time, peak memory, and
per-phase breakdown. Used for regression detection and capacity planning.

## Files

`YYYY-MM-DD-indexing-throughput[-suffix].json` — one baseline per run.
Optional suffix names the target (`-cypher`, `-self`, `-psm`, etc.) so
multiple targets can be tracked in parallel.

## Schema

```jsonc
{
  "schema_version": 1,
  "generated_at": "<ISO 8601>",
  "target": "<absolute path indexed>",
  "mode": "full" | "fast" | "incremental",
  "go_version": "go1.26.2",
  "num_cpu": 8,
  "goos": "windows",
  "goarch": "amd64",
  "cold_start_ns": <int>,        // process-start -> first phase callback
  "total_wall_ns": <int>,        // pipeline.Run() wall time
  "peak_heap_inuse_bytes": <int>,
  "peak_sys_bytes": <int>,
  "phases": [
    {
      "phase": "<name>",          // discover / structure / definitions / ...
      "pct": <int>,                 // pct progress callback's value
      "detail": "<callback detail string>",
      "wall_ns": <int>,             // time from previous phase callback
      "heap_inuse_bytes": <int>,
      "sys_bytes": <int>
    }
  ],
  "node_count": <int>,
  "edge_count": <int>,
  "nodes_per_sec": <float>,
  "edges_per_sec": <float>,
  // Phase-time percentiles (across all phases except the first;
  // the first captures cold-start and isn't representative)
  "p50_phase_ns": <int>,
  "p95_phase_ns": <int>,
  "p99_phase_ns": <int>
}
```

## Generating a new baseline

```bash
cd <repo-root>
CGO_ENABLED=1 go build -o indexing_throughput.exe \
    ./bench/research/indexing_throughput/
./indexing_throughput.exe \
    -target ./internal/cypher \
    -mode full \
    -output bench/research/baselines/<date>-indexing-throughput-cypher.json
```

Other useful runs:

```bash
# Self-index (large target — measures realistic full-repo cost)
./indexing_throughput.exe -target . -output bench/research/baselines/<date>-indexing-throughput-self.json

# Fast mode (aggressive filtering)
./indexing_throughput.exe -target . -mode fast -output bench/research/baselines/<date>-indexing-throughput-self-fast.json

# Incremental (re-index against an already-populated store; pass
# -keep-db on the previous run, then -mode incremental on this one)
./indexing_throughput.exe -target . -mode full -keep-db -output bench/research/baselines/<date>-indexing-throughput-cold.json
./indexing_throughput.exe -target . -mode incremental -output bench/research/baselines/<date>-indexing-throughput-warm.json
```

## What the cold-start number captures

`cold_start_ns` is the time from process start to the first
`Pipeline.Progress` callback firing. This includes:

- Go runtime initialization
- Tree-sitter grammar registration (CGO `_init` calls)
- Store opening + WAL setup
- `pipeline.New()` setup including Rust crate map building
- The discovery walk's first phase callback

It does NOT include MCP stdio framing — that's a separate concern
covered by `bench/research/mcp_latency_baseline/` (Plan 3 Phase B).

## What memory measurement covers

`peak_heap_inuse_bytes` is the maximum `runtime.MemStats.HeapInuse`
sampled at each phase boundary. Captures Go-side heap allocation only.

`peak_sys_bytes` is the maximum `runtime.MemStats.Sys`, which
includes the OS-allocated total (heap, stacks, runtime metadata) for
the Go process. Closer to OS RSS but still excludes CGO-side
tree-sitter memory.

True OS RSS would require platform-specific syscalls
(`GetProcessMemoryInfo` on Windows, `/proc/self/status` on Linux);
those weren't worth the cross-platform tax for this baseline.
Trade-off documented here so future improvements know what's measurable
beyond the current baseline.

## Regression detection workflow

```bash
# Capture fresh measurement
./indexing_throughput.exe -target ./internal/cypher \
    -output /tmp/new-throughput.json

# Diff against most-recent baseline. Flag any tool whose total wall
# time grew >2x or peak heap grew >1.5x.
python -c "
import json, sys, glob
old = json.load(open(sorted(glob.glob('bench/research/baselines/*-indexing-throughput-cypher.json'))[-1]))
new = json.load(open('/tmp/new-throughput.json'))
if new['total_wall_ns'] > 2 * old['total_wall_ns']:
    print(f'WALL REGRESSION: {old[\"total_wall_ns\"]/1e9:.2f}s -> {new[\"total_wall_ns\"]/1e9:.2f}s')
if new['peak_heap_inuse_bytes'] > 1.5 * old['peak_heap_inuse_bytes']:
    print(f'HEAP REGRESSION: {old[\"peak_heap_inuse_bytes\"]/1e6:.1f}MB -> {new[\"peak_heap_inuse_bytes\"]/1e6:.1f}MB')
"
```

## Cross-references

- Plan: `~/Documents/knowledge-base/plans/2026-05-06-codegraph-followup.md`
  (Plan 4 T3b post-roundtable)
- Roundtable: `~/Documents/roundtables/2026-05-06-code-graph-performance/results/META_SYNTHESIS.md` F5
- MCP tool latency baseline (sibling): `bench/research/baselines/2026-05-06-mcp-latency.json` (Plan 3 Phase B)
