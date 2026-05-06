// indexing_throughput measures the wall time, peak memory, and per-phase
// breakdown of code-graph's pipeline.Run() against a target directory.
//
// Plan 4 T3b (~/Documents/knowledge-base/plans/2026-05-06-codegraph-followup.md
// post-roundtable). Closes performance gaps #8 (memory footprint), #9 (cold-
// start), and #10 (indexing throughput) from the 2026-05-06 roundtable
// META_SYNTHESIS F5.
//
// MEASUREMENT APPROACH:
//
// The harness drives Pipeline.Run() in-process (not via the MCP binary)
// because:
//
//  1. The dominant work — tree-sitter parsing, multi-pass extraction,
//     SQLite writes — is exercised identically.
//  2. In-process measurement is reproducible (no subprocess setup).
//  3. We can wire Pipeline.Progress callbacks to record per-phase wall
//     times, which the binary doesn't surface.
//
// Memory measurement uses runtime.MemStats. This captures Go-side heap
// allocations but NOT CGO-side (tree-sitter) memory. The Sys field
// reflects total OS memory committed by the Go runtime including CGO
// arena overhead, so we report both HeapInuse and Sys peak.
//
// Cold-start time is measured separately: time from process start to
// the first phase callback ("discover").
//
// Output is a JSON baseline file with the same schema pattern as
// bench/research/baselines/2026-05-06-mcp-latency.json (Plan 3 Phase B).
//
// Usage:
//
//	go run ./bench/research/indexing_throughput \
//	    -target . \
//	    -output bench/research/baselines/2026-05-06-indexing-throughput.json
//
//	# Incremental run (re-index against an already-indexed project):
//	go run ./bench/research/indexing_throughput -target . -mode incremental
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/DeusData/codebase-memory-mcp/internal/discover"
	"github.com/DeusData/codebase-memory-mcp/internal/pipeline"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

type phaseTiming struct {
	Phase     string `json:"phase"`
	Pct       int    `json:"pct"`
	Detail    string `json:"detail"`
	WallNS    int64  `json:"wall_ns"`
	HeapInuse uint64 `json:"heap_inuse_bytes"`
	Sys       uint64 `json:"sys_bytes"`
}

type result struct {
	SchemaVersion int           `json:"schema_version"`
	GeneratedAt   string        `json:"generated_at"`
	Target        string        `json:"target"`
	Mode          string        `json:"mode"`
	GoVersion     string        `json:"go_version"`
	NumCPU        int           `json:"num_cpu"`
	GOOS          string        `json:"goos"`
	GOARCH        string        `json:"goarch"`
	ColdStartNS   int64         `json:"cold_start_ns"`
	TotalWallNS   int64         `json:"total_wall_ns"`
	PeakHeapBytes uint64        `json:"peak_heap_inuse_bytes"`
	PeakSysBytes  uint64        `json:"peak_sys_bytes"`
	Phases        []phaseTiming `json:"phases"`
	NodeCount     int           `json:"node_count"`
	EdgeCount     int           `json:"edge_count"`
	FilesScanned  int           `json:"files_scanned,omitempty"`
	NodesPerSec   float64       `json:"nodes_per_sec"`
	EdgesPerSec   float64       `json:"edges_per_sec"`
	// PhasePercentiles holds P50/P95/P99 across phase wall times,
	// useful for spotting which phases dominate runtime.
	P50PhaseNS int64 `json:"p50_phase_ns"`
	P95PhaseNS int64 `json:"p95_phase_ns"`
	P99PhaseNS int64 `json:"p99_phase_ns"`
}

func percentile(sorted []int64, q float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	idx := int(q * float64(len(sorted)-1))
	return sorted[idx]
}

func main() {
	target := flag.String("target", ".", "Directory to index (default: current dir)")
	storageDir := flag.String("storage", "", "Storage directory (default: $HOME/.cache/codebase-memory-mcp-throughput-bench)")
	mode := flag.String("mode", "full", "Index mode: full | fast | incremental")
	output := flag.String("output", "", "Output JSON path (default: bench/research/baselines/<today>-indexing-throughput.json)")
	keepDB := flag.Bool("keep-db", false, "Keep the bench DB after measurement (default: clean up)")
	flag.Parse()

	processStart := time.Now()

	absTarget, err := filepath.Abs(*target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "abs path: %v\n", err)
		os.Exit(1)
	}
	if info, statErr := os.Stat(absTarget); statErr != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "target is not a directory: %s\n", absTarget)
		os.Exit(1)
	}

	if *storageDir == "" {
		home, _ := os.UserHomeDir()
		*storageDir = filepath.Join(home, ".cache", "codebase-memory-mcp-throughput-bench")
	}
	if err := os.MkdirAll(*storageDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir storage: %v\n", err)
		os.Exit(1)
	}

	// Construct a per-run DB path so concurrent runs don't collide.
	projectName := pipeline.ProjectNameFromPath(absTarget)
	dbPath := filepath.Join(*storageDir, projectName+".db")
	// "incremental" isn't a discover.IndexMode — code-graph achieves
	// incremental indexing by running a full pass against an already-
	// populated store (file hashes determine what to re-extract).
	// In our harness: full or fast WIPE the DB; incremental does NOT
	// wipe, so a second run sees the populated store.
	if *mode == "full" || *mode == "fast" {
		_ = os.Remove(dbPath)
		_ = os.Remove(dbPath + "-wal")
		_ = os.Remove(dbPath + "-shm")
	}

	st, err := store.OpenPath(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "store open: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		_ = st.Close()
		if !*keepDB {
			_ = os.Remove(dbPath)
			_ = os.Remove(dbPath + "-wal")
			_ = os.Remove(dbPath + "-shm")
		}
	}()

	pipelineMode := discover.ModeFull
	switch *mode {
	case "fast":
		pipelineMode = discover.ModeFast
	case "incremental":
		// Use ModeFull; incremental-ness comes from running against a
		// populated store, not from a discover-side mode.
		pipelineMode = discover.ModeFull
	}

	ctx := context.Background()
	p := pipeline.New(ctx, st, absTarget, pipelineMode)

	// Wire progress callback. Per-phase callbacks fire from inside
	// Pipeline.Run() at well-defined boundaries (discover, structure,
	// definitions, registry, inherits, decorates, dataflow, calls,
	// http, async, opa, communities, tests, ...).
	var (
		mu          sync.Mutex
		phaseLog    []phaseTiming
		coldStartNS int64
		peakHeap    uint64
		peakSys     uint64
		lastPhase   = time.Now()
	)
	p.Progress = func(phase string, pct int, detail string) {
		mu.Lock()
		defer mu.Unlock()
		now := time.Now()
		if coldStartNS == 0 {
			coldStartNS = now.Sub(processStart).Nanoseconds()
		}
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		if ms.HeapInuse > peakHeap {
			peakHeap = ms.HeapInuse
		}
		if ms.Sys > peakSys {
			peakSys = ms.Sys
		}
		phaseLog = append(phaseLog, phaseTiming{
			Phase:     phase,
			Pct:       pct,
			Detail:    detail,
			WallNS:    now.Sub(lastPhase).Nanoseconds(),
			HeapInuse: ms.HeapInuse,
			Sys:       ms.Sys,
		})
		lastPhase = now
	}

	runStart := time.Now()
	if err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "pipeline.Run: %v\n", err)
		os.Exit(1)
	}
	totalWall := time.Since(runStart).Nanoseconds()

	// Final memory snapshot.
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	if ms.HeapInuse > peakHeap {
		peakHeap = ms.HeapInuse
	}
	if ms.Sys > peakSys {
		peakSys = ms.Sys
	}

	nodeCount, _ := st.CountNodes(projectName)
	edgeCount, _ := st.CountEdges(projectName)

	// Compute phase-time percentiles. Drop the first phase from the
	// percentile pool: it captures cold-start setup including
	// rust-crate map building, which is structurally different from
	// the per-pass work we want to characterize.
	durs := make([]int64, 0, len(phaseLog))
	for i, p := range phaseLog {
		if i == 0 {
			continue
		}
		durs = append(durs, p.WallNS)
	}
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })

	res := result{
		SchemaVersion: 1,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Target:        absTarget,
		Mode:          *mode,
		GoVersion:     runtime.Version(),
		NumCPU:        runtime.NumCPU(),
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		ColdStartNS:   coldStartNS,
		TotalWallNS:   totalWall,
		PeakHeapBytes: peakHeap,
		PeakSysBytes:  peakSys,
		Phases:        phaseLog,
		NodeCount:     nodeCount,
		EdgeCount:     edgeCount,
		P50PhaseNS:    percentile(durs, 0.50),
		P95PhaseNS:    percentile(durs, 0.95),
		P99PhaseNS:    percentile(durs, 0.99),
	}
	if totalWall > 0 {
		secs := float64(totalWall) / 1e9
		res.NodesPerSec = float64(nodeCount) / secs
		res.EdgesPerSec = float64(edgeCount) / secs
	}

	outPath := *output
	if outPath == "" {
		date := time.Now().UTC().Format("2006-01-02")
		outPath = filepath.Join("bench", "research", "baselines", date+"-indexing-throughput.json")
	}
	buf, err := json.MarshalIndent(&res, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir output: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(outPath, buf, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write output: %v\n", err)
		os.Exit(1)
	}

	// Human-readable summary on stderr.
	fmt.Fprintf(os.Stderr,
		"\n=== indexing_throughput %s mode ===\n"+
			"target:           %s\n"+
			"go:               %s %s/%s NumCPU=%d\n"+
			"cold start:       %.3f s (process start -> first phase)\n"+
			"total wall:       %.3f s\n"+
			"peak heap inuse:  %.1f MiB\n"+
			"peak sys:         %.1f MiB\n"+
			"phases:           %d\n"+
			"phase P50/P95/P99: %d / %d / %d ms\n"+
			"nodes:            %d (%.1f/s)\n"+
			"edges:            %d (%.1f/s)\n"+
			"output:           %s\n",
		*mode, absTarget,
		runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU(),
		float64(coldStartNS)/1e9,
		float64(totalWall)/1e9,
		float64(peakHeap)/(1024*1024),
		float64(peakSys)/(1024*1024),
		len(phaseLog),
		res.P50PhaseNS/1_000_000, res.P95PhaseNS/1_000_000, res.P99PhaseNS/1_000_000,
		nodeCount, res.NodesPerSec,
		edgeCount, res.EdgesPerSec,
		outPath,
	)
}
