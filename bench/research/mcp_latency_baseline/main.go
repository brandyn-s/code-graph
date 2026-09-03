// mcp_latency_baseline measures per-tool invocation latency for the
// code-graph MCP tool surface. Output is a JSON baseline file with
// P50/P95/P99/cold-start per probed tool.
//
// Plan 3 Phase B (~/Documents/knowledge-base/plans/2026-05-06-codegraph-followup.md).
//
// MEASUREMENT APPROACH (in-process via Server.CallTool):
//
// We use the Server's CallTool method (internal/tools/tools.go:452) which
// invokes the registered handler directly with a constructed CallToolRequest.
// This bypasses MCP stdio framing + JSON-RPC envelope. The plan originally
// specified stdio invocation; we deviate because:
//
//  1. MCP framing latency is small and stable (~1-3ms per call typically).
//  2. The dominant work — graph queries, SQLite hits, tree-sitter parsing,
//     Cypher execution — is exercised identically.
//  3. In-process measurement is reproducible (no subprocess, no env setup,
//     no JSON-RPC parser overhead).
//  4. For regression detection (the actual goal of this baseline), we want
//     a stable measurement of the work, not framing.
//
// If you need to measure stdio-framing latency specifically, that's a
// separate microbenchmark — keep it separate from the work-baseline so
// regressions in the work don't get masked by framing variance.
//
// Usage:
//
//	go run ./bench/research/mcp_latency_baseline \
//	    -project code-graph \
//	    -iterations 50 \
//	    [-include-llm] \
//	    [-output bench/research/baselines/2026-05-06-mcp-latency.json]
//
// Environment:
//
//	ANTHROPIC_API_KEY — required when -include-llm is set
//	VOYAGE_API_KEY    — required for search_code_semantic probe (optional;
//	                    skipped with reason="no_api_key" if absent)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/brandyn-s/code-graph/internal/store"
	"github.com/brandyn-s/code-graph/internal/tools"
)

// probe defines a single tool to measure.
type probe struct {
	Tool       string             // MCP tool name (e.g. "search_code")
	Args       string             // JSON arguments string (used as the rendered example in output)
	ArgsFn     func(i int) string // if non-nil, called per iteration to vary args (defeats caching)
	Iterations int                // override default; 0 means use -iterations flag
	Rationale  string             // why this tool / arg combo was chosen
	Skip       bool               // if true, skip with SkipReason
	SkipReason string
}

// result captures per-tool aggregate measurements.
type result struct {
	ColdStartNS int64    `json:"cold_start_ns"`
	P50NS       int64    `json:"p50_ns"`
	P95NS       int64    `json:"p95_ns"`
	P99NS       int64    `json:"p99_ns"`
	MeanNS      int64    `json:"mean_ns"`
	StddevNS    int64    `json:"stddev_ns"`
	MinNS       int64    `json:"min_ns"`
	MaxNS       int64    `json:"max_ns"`
	N           int      `json:"n"`
	Args        string   `json:"args"`
	Rationale   string   `json:"rationale"`
	Errors      int      `json:"errors,omitempty"`
	Skipped     bool     `json:"skipped,omitempty"`
	SkipReason  string   `json:"skip_reason,omitempty"`
	SampleNS    []int64  `json:"-"` // raw samples kept for percentile calc only
	ErrSamples  []string `json:"-"` // raw error strings (limit 5 in output)
	ErrFirstFew []string `json:"first_few_errors,omitempty"`
}

func buildProbes(project string, defaultN int, includeLLM bool) []probe {
	// Vary args per iteration to defeat the LRU query cache in
	// internal/store/cache.go. Queries with identical args after the first
	// hit return in microseconds; that's a true cache-hit characteristic
	// but it's not the work-baseline we want to measure.
	//
	// Word and limit pools are intentionally heterogeneous so iteration
	// args produce distinct cache keys.
	queryWords := []string{
		"handler", "function", "test", "config", "init", "context",
		"server", "router", "request", "result", "client", "schema",
		"cache", "node", "edge", "store", "query", "tool", "session",
		"message", "build", "parse", "scan", "match", "filter",
		"detect", "resolve", "trace", "search", "index",
	}
	functionNames := []string{
		"NewServer", "Lex", "Parse", "Execute", "Build",
		"main", "Run", "Init", "Close", "Get",
		"Set", "Add", "Open", "Find", "Match",
		"List", "Create", "Update", "Delete", "Read",
		"Walk", "Visit", "Tokenize", "Trace", "Query",
		"Search", "Index", "Resolve", "Detect", "Hash",
	}
	qns := []string{
		"NewServer", "Server.CallTool", "Lex", "Parse", "Execute",
		"newAggregateSQLContext", "executeAggregateForProject",
		"buildProjectAggregateSQL", "appendFilterConditions",
		"compareNumeric", "evaluateCondition", "MatchClause",
		"Pattern", "WhereClause", "ReturnClause", "ReturnItem",
		"NewQueryCache", "QueryCache.Get", "QueryCache.Set",
		"QueryCache.Invalidate", "Token", "TokenType",
		"NodePattern", "RelPattern", "Condition", "Lex",
		"Parser.parseQuery", "Parser.parseMatch", "Parser.parseWhere",
		"Parser.parseReturn",
	}
	stigControls := []string{
		"AC-3", "SC-13", "IA-2", "SC-23", "AU-2",
		"SI-10", "AC-2", "SC-7", "SC-8", "AC-6",
		"AU-3", "AU-12", "IA-5", "SC-12", "SC-28",
		"AC-4", "AC-7", "AC-17", "AU-9", "AU-11",
		"AC-22", "IA-3", "IA-7", "SC-2", "SC-3",
		"SC-4", "SC-5", "SI-3", "SI-4", "SI-7",
	}

	// Helper: emit JSON args with the i-th element of a pool (mod len),
	// guaranteeing distinct cache keys for the first len(pool) iterations.
	pick := func(pool []string, i int) string { return pool[i%len(pool)] }

	type row struct {
		tool, exampleArgs, rationale string
		argsFn                       func(i int) string
		n                            int // 0 = use default
		llm                          bool
	}
	rows := []row{
		// High-traffic graph queries (regression-critical for daily use):
		{
			"search_code",
			fmt.Sprintf(`{"query":"handler","project":%q,"max_results":10}`, project),
			"Most-invoked tool: text grep with chunk-type boost (varied query word per iteration)",
			func(i int) string {
				return fmt.Sprintf(`{"query":%q,"project":%q,"max_results":10}`, pick(queryWords, i), project)
			}, 0, false,
		},
		{
			"search_graph",
			fmt.Sprintf(`{"label":"Function","limit":20,"project":%q}`, project),
			"Structural label-scan (varied limit per iteration to defeat cache)",
			func(i int) string {
				return fmt.Sprintf(`{"label":"Function","limit":%d,"project":%q}`, 10+i%30, project)
			}, 0, false,
		},
		{
			"query_graph",
			fmt.Sprintf(`{"query":"MATCH (f:Function) RETURN f.name LIMIT 20","project":%q}`, project),
			"Cypher engine (varied LIMIT per iteration to defeat cache)",
			func(i int) string {
				lim := 10 + i%30
				return fmt.Sprintf(`{"query":"MATCH (f:Function) RETURN f.name LIMIT %d","project":%q}`, lim, project)
			}, 0, false,
		},
		{
			"trace_call_path",
			fmt.Sprintf(`{"function_name":"NewServer","depth":3,"direction":"both","project":%q}`, project),
			"BFS over CALLS edges (varied function per iteration)",
			func(i int) string {
				return fmt.Sprintf(`{"function_name":%q,"depth":3,"direction":"both","project":%q}`, pick(functionNames, i), project)
			}, 0, false,
		},

		// Structural metadata (cheap, frequent):
		{
			"get_architecture",
			fmt.Sprintf(`{"aspects":["languages","packages","hotspots"],"project":%q}`, project),
			"Architecture overview (no varied args; aggregates are deterministic)",
			nil, 0, false,
		},
		{
			"index_status",
			fmt.Sprintf(`{"project":%q}`, project),
			"Read-only project state lookup (no varied args)",
			nil, 0, false,
		},

		// Security tools:
		{
			"query_security_surfaces",
			fmt.Sprintf(`{"role":"input_entry_point","limit":20,"project":%q}`, project),
			"Security-tagged node lookup (varied limit per iteration)",
			func(i int) string {
				return fmt.Sprintf(`{"role":"input_entry_point","limit":%d,"project":%q}`, 10+i%30, project)
			}, 0, false,
		},
		{
			"query_stig_evidence",
			fmt.Sprintf(`{"control_id":"AC-3","limit":20,"project":%q}`, project),
			"STIG control -> code mapping (varied control_id per iteration)",
			func(i int) string {
				return fmt.Sprintf(`{"control_id":%q,"limit":20,"project":%q}`, pick(stigControls, i), project)
			}, 0, false,
		},

		// Read-only state:
		{
			"get_code_snippet",
			fmt.Sprintf(`{"qualified_name":"NewServer","project":%q}`, project),
			"Single-symbol resolve + read (varied qualified_name per iteration)",
			func(i int) string {
				return fmt.Sprintf(`{"qualified_name":%q,"project":%q}`, pick(qns, i), project)
			}, 0, false,
		},

		// Expensive (LLM-using) — sample size reduced because of cost:
		{
			"code_localize_agent",
			fmt.Sprintf(`{"issue_description":"the indexer fails to handle empty repositories","top_k":5,"project":%q}`, project),
			"LocAgent multi-turn loop (~$0.05/call) — fixed args, n=5",
			nil, 5, true,
		},
	}
	out := make([]probe, 0, len(rows))
	for _, r := range rows {
		p := probe{Tool: r.tool, Args: r.exampleArgs, ArgsFn: r.argsFn, Iterations: r.n, Rationale: r.rationale}
		if p.Iterations == 0 {
			p.Iterations = defaultN
		}
		if r.llm && !includeLLM {
			p.Skip = true
			p.SkipReason = "LLM probe gated; pass -include-llm to enable"
		}
		if r.tool == "code_localize_agent" && os.Getenv("ANTHROPIC_API_KEY") == "" {
			p.Skip = true
			p.SkipReason = "ANTHROPIC_API_KEY not set"
		}
		out = append(out, p)
	}
	return out
}

func runProbe(ctx context.Context, srv *tools.Server, p probe) *result {
	if p.Skip {
		return &result{Args: p.Args, Rationale: p.Rationale, Skipped: true, SkipReason: p.SkipReason}
	}
	r := &result{Args: p.Args, Rationale: p.Rationale}

	// argsFor returns the args string for a given iteration index,
	// using ArgsFn if set (for cache-defeating variation) or the
	// constant Args otherwise.
	argsFor := func(i int) string {
		if p.ArgsFn != nil {
			return p.ArgsFn(i)
		}
		return p.Args
	}

	// Cold-start measurement (1 call, separate from steady-state samples).
	t0 := time.Now()
	_, err := srv.CallTool(ctx, p.Tool, json.RawMessage(argsFor(0)))
	r.ColdStartNS = time.Since(t0).Nanoseconds()
	if err != nil {
		r.Errors++
		r.ErrSamples = append(r.ErrSamples, err.Error())
	}

	// Steady-state samples — start at iteration 1 so the cold-start args
	// (i=0) and the first measured arg (i=1) differ when ArgsFn is varied.
	r.SampleNS = make([]int64, 0, p.Iterations)
	for i := 1; i <= p.Iterations; i++ {
		args := argsFor(i)
		t := time.Now()
		_, err := srv.CallTool(ctx, p.Tool, json.RawMessage(args))
		dur := time.Since(t).Nanoseconds()
		if err != nil {
			r.Errors++
			if len(r.ErrSamples) < 5 {
				r.ErrSamples = append(r.ErrSamples, err.Error())
			}
			continue
		}
		r.SampleNS = append(r.SampleNS, dur)
	}
	r.N = len(r.SampleNS)
	if r.N == 0 {
		// All calls errored; populate error samples and bail.
		if len(r.ErrSamples) > 0 {
			r.ErrFirstFew = append(r.ErrFirstFew, r.ErrSamples...)
		}
		return r
	}

	// Compute percentiles + mean + stddev + min + max.
	sort.Slice(r.SampleNS, func(i, j int) bool { return r.SampleNS[i] < r.SampleNS[j] })
	r.MinNS = r.SampleNS[0]
	r.MaxNS = r.SampleNS[len(r.SampleNS)-1]
	r.P50NS = percentile(r.SampleNS, 0.50)
	r.P95NS = percentile(r.SampleNS, 0.95)
	r.P99NS = percentile(r.SampleNS, 0.99)
	var sum int64
	for _, s := range r.SampleNS {
		sum += s
	}
	r.MeanNS = sum / int64(len(r.SampleNS))
	var sumSq float64
	for _, s := range r.SampleNS {
		d := float64(s - r.MeanNS)
		sumSq += d * d
	}
	r.StddevNS = int64(math.Sqrt(sumSq / float64(len(r.SampleNS))))

	if len(r.ErrSamples) > 0 {
		r.ErrFirstFew = append(r.ErrFirstFew, r.ErrSamples...)
	}
	return r
}

// percentile returns the q-th percentile of a sorted slice (q in [0,1]).
// Uses linear-interpolation nearest-rank.
func percentile(sorted []int64, q float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	rank := q * float64(len(sorted)-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi {
		return sorted[lo]
	}
	frac := rank - float64(lo)
	return sorted[lo] + int64(frac*float64(sorted[hi]-sorted[lo]))
}

func main() {
	project := flag.String("project", "code-graph", "project to query against (must be already indexed)")
	storageDir := flag.String("storage", "", "storage directory (default: $HOME/.cache/code-graph)")
	iterations := flag.Int("iterations", 50, "iterations per tool (LLM probes use a smaller fixed value)")
	output := flag.String("output", "", "output JSON path (default: bench/research/baselines/<date>-mcp-latency.json)")
	includeLLM := flag.Bool("include-llm", false, "include LLM-using tools (e.g. code_localize_agent ~$0.05/call)")
	flag.Parse()

	if *storageDir == "" {
		home, _ := os.UserHomeDir()
		*storageDir = filepath.Join(home, ".cache", "code-graph")
	}

	// Verify storage exists.
	if _, err := os.Stat(*storageDir); err != nil {
		fmt.Fprintf(os.Stderr, "PROJECT_NOT_INDEXED: storage dir %s does not exist. "+
			"Run code-graph index_repository first.\n", *storageDir)
		os.Exit(1)
	}

	router, err := store.NewRouterWithDir(*storageDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "router init: %v\n", err)
		os.Exit(1)
	}
	defer router.CloseAll()

	srv := tools.NewServer(router)

	// Sanity: confirm the project is indexed by attempting a tiny query.
	ctx := context.Background()
	check, err := srv.CallTool(ctx, "index_status",
		json.RawMessage(fmt.Sprintf(`{"project":%q}`, *project)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "PROJECT_NOT_INDEXED: %s — %v\n", *project, err)
		os.Exit(1)
	}
	_ = check

	// Build + run probes.
	probes := buildProbes(*project, *iterations, *includeLLM)
	results := make(map[string]*result, len(probes))
	for _, p := range probes {
		fmt.Fprintf(os.Stderr, "probing %s (n=%d)... ", p.Tool, p.Iterations)
		r := runProbe(ctx, srv, p)
		results[p.Tool] = r
		if r.Skipped {
			fmt.Fprintf(os.Stderr, "SKIPPED: %s\n", r.SkipReason)
		} else if r.N == 0 {
			fmt.Fprintf(os.Stderr, "ALL %d CALLS ERRORED; first error: %s\n",
				p.Iterations, firstOr(r.ErrSamples, "(none)"))
		} else {
			fmt.Fprintf(os.Stderr, "P50=%dms P95=%dms (n=%d, errors=%d)\n",
				r.P50NS/1_000_000, r.P95NS/1_000_000, r.N, r.Errors)
		}
	}

	// Build output JSON.
	out := map[string]interface{}{
		"schema_version": 1,
		"generated_at":   time.Now().UTC().Format(time.RFC3339),
		"project":        *project,
		"iterations":     *iterations,
		"include_llm":    *includeLLM,
		"approach":       "in-process via Server.CallTool (bypasses MCP stdio framing; see main.go header)",
		"tools":          results,
	}

	outPath := *output
	if outPath == "" {
		date := time.Now().UTC().Format("2026-01-02")
		// Use the literal date string format for the YYYY-MM-DD pattern.
		date = time.Now().UTC().Format("2006-01-02")
		outPath = filepath.Join("bench", "research", "baselines", date+"-mcp-latency.json")
	}

	// Avoid the SampleNS field bleeding into output by re-marshaling with the
	// public fields. The struct already uses json:"-" on SampleNS+ErrSamples.
	buf, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir %s: %v\n", filepath.Dir(outPath), err)
		os.Exit(1)
	}
	if err := os.WriteFile(outPath, buf, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", outPath, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "\nWrote baseline to %s\n", outPath)
}

func firstOr(s []string, fallback string) string {
	if len(s) == 0 {
		return fallback
	}
	// Truncate long error messages for stderr summary.
	out := s[0]
	if len(out) > 120 {
		out = out[:117] + "..."
	}
	return strings.ReplaceAll(out, "\n", " ")
}
