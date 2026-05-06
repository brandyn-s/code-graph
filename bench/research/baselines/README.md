# MCP Tool Latency Baselines

Per-tool latency measurements for the code-graph MCP surface. Used for
regression detection.

## Files

- `YYYY-MM-DD-mcp-latency.json` — one baseline per measurement run. Each file
  is a point-in-time snapshot; updates are intentional after performance
  changes.

## Schema

```jsonc
{
  "schema_version": 1,
  "generated_at": "<ISO 8601>",
  "project": "<indexed project name>",
  "iterations": <int>,        // steady-state iterations per non-LLM probe
  "include_llm": <bool>,       // whether LLM-using probes ran
  "approach": "<string>",      // describes how calls were measured
  "tools": {
    "<tool_name>": {
      "cold_start_ns": <int>,  // first-call latency (separate from P-stats)
      "p50_ns": <int>,
      "p95_ns": <int>,
      "p99_ns": <int>,
      "mean_ns": <int>,
      "stddev_ns": <int>,
      "min_ns": <int>,
      "max_ns": <int>,
      "n": <int>,              // sample count after errors filtered
      "args": "<JSON string>", // example args used (for varied probes,
                                //   the first iteration's args)
      "rationale": "<string>", // why this tool was selected
      "errors": <int>,         // omitted if 0
      "skipped": <bool>,       // omitted if false
      "skip_reason": "<string>"
    }
  }
}
```

## Generating a new baseline

```bash
cd <repo-root>
CGO_ENABLED=1 go build -o mcp_latency_baseline.exe \
    ./bench/research/mcp_latency_baseline/
./mcp_latency_baseline.exe \
    -project c-Users-user-Documents-GitHub-code-graph \
    -iterations 50
# Add -include-llm to also measure code_localize_agent (cost ~$0.05/call,
# requires ANTHROPIC_API_KEY).
```

The probe writes `bench/research/baselines/<today>-mcp-latency.json` by
default. Pass `-output <path>` to override.

## Regression detection workflow

```bash
# 1. Capture a fresh measurement
./mcp_latency_baseline.exe -project <p> -output /tmp/new-latency.json

# 2. Diff against the most recent baseline. A simple criterion:
#    flag any tool whose P95 grew >2x.
python -c "
import json, sys, glob
old = json.load(open(sorted(glob.glob('bench/research/baselines/*-mcp-latency.json'))[-1]))
new = json.load(open('/tmp/new-latency.json'))
for tool, ns in new['tools'].items():
    if ns.get('skipped'):
        continue
    o = old['tools'].get(tool)
    if not o or o.get('skipped'):
        continue
    if ns['p95_ns'] > 2 * o['p95_ns'] and ns['p95_ns'] > 5_000_000:  # 5ms floor
        print(f\"{tool}: P95 {o['p95_ns']/1e6:.1f}ms -> {ns['p95_ns']/1e6:.1f}ms\")
"
```

Adjust the threshold for your sensitivity — 2x catches large regressions
without flagging noise. The 5ms floor avoids flagging sub-millisecond
numerical noise.

## Tool selection rationale

10 representative tools are probed today; the script header documents
each choice. Adding more tools is mechanical — extend `buildProbes`
in `bench/research/mcp_latency_baseline/main.go`.

## Cache-defeating argument variation

Some tools (`search_graph`, `query_graph`, `trace_call_path`,
`query_security_surfaces`, `query_stig_evidence`, `get_code_snippet`)
hit an LRU query cache that returns identical-args calls in microseconds.
The probe varies arguments per iteration (different query strings, limits,
function names, control IDs, qualified names) so each measurement reflects
real work, not cache hits.

`get_architecture` and `index_status` are deterministic aggregates with no
useful arg-variation surface; they're measured with constant args. Their
latency reflects the bounded aggregation work.

`search_code` may show P50=0ms for some project layouts where the project
root resolution returns no filesystem matches — it's not a cache effect,
it's an early-return path. If you care about search_code latency
specifically, point the probe at a project where the indexed name maps
cleanly to a filesystem path with content matching the probe's query
words.

## Approach: in-process invocation

The probe uses `Server.CallTool` (internal/tools/tools.go:452) to invoke
handlers directly. This bypasses MCP stdio framing and JSON-RPC envelope.

Rationale (also in main.go header): MCP framing is small (~1-3ms) and
stable; the dominant work is graph queries / SQLite / tree-sitter / Cypher,
which is exercised identically. In-process measurement is reproducible
(no subprocess, no env setup) and the resulting numbers are stable enough
for regression detection.

If you need to measure stdio-framing latency specifically, that's a
separate microbenchmark — keep it separate from the work-baseline so
regressions in the work don't get masked by framing variance.

## Cross-references

- Plan: `~/Documents/knowledge-base/plans/2026-05-06-codegraph-followup.md` Phase B
- Source: `bench/research/mcp_latency_baseline/main.go`
- Probe target rationale: see each `buildProbes` entry's `rationale` string
