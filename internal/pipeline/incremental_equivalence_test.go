package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DeusData/codebase-memory-mcp/internal/discover"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

type incrementalEquivalenceScenario struct {
	name    string
	initial map[string]string
	mutate  func(*testing.T, string)
}

type canonicalStoreGraph struct {
	nodes []string
	edges []string
}

// TestIncrementalMatchesCleanAcrossChangeClasses treats a clean rebuild as the
// oracle for incremental indexing. It covers the dependency-invalidating edit
// classes most likely to leave stale nodes or edges behind. Latency and physical
// SQLite footprint are observations, not brittle test thresholds; exact graph
// equivalence is the correctness gate.
func TestIncrementalMatchesCleanAcrossChangeClasses(t *testing.T) {
	t.Setenv("CODE_GRAPH_FULL_REINDEX_EVERY", "0")
	t.Setenv("CODE_GRAPH_INCREMENTAL_CAP", "10000")

	scenarios := []incrementalEquivalenceScenario{
		{
			name: "file_rename",
			initial: map[string]string{
				"callee.go": `package sample

func transform(value int) int { return value + 1 }
`,
				"caller.go": `package sample

func invoke(value int) int { return transform(value) }
`,
				"anchor.go": `package sample

func anchor() int { return 1 }
`,
			},
			mutate: func(t *testing.T, dir string) {
				t.Helper()
				if err := os.Rename(filepath.Join(dir, "callee.go"), filepath.Join(dir, "renamed.go")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "file_deletion",
			initial: map[string]string{
				"obsolete.go": `package sample

func obsolete() int { return 9 }
`,
				"anchor.go": `package sample

func anchor() int { return 1 }
`,
			},
			mutate: func(t *testing.T, dir string) {
				t.Helper()
				if err := os.Remove(filepath.Join(dir, "obsolete.go")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "typescript_re_export",
			initial: map[string]string{
				"src/a.ts": `export function normalize(value: number): number { return value; }
`,
				"src/b.ts": `export function normalize(value: number): number { return value + 1; }
`,
				"src/index.ts": `export { normalize } from "./a";
`,
				"src/consumer.ts": `import { normalize } from "./index";

export function consume(value: number): number { return normalize(value); }
`,
			},
			mutate: func(t *testing.T, dir string) {
				t.Helper()
				writeFile(t, filepath.Join(dir, "src", "index.ts"), `export { normalize } from "./b"; // changed re-export target
`)
			},
		},
		{
			name: "go_receiver_type",
			initial: map[string]string{
				"types.go": `package sample

type Alpha struct{}
func (Alpha) Run() int { return 1 }

type Beta struct{}
func (Beta) Run() int { return 2 }
`,
				"caller.go": `package sample

func invokeReceiver(value Alpha) int { return value.Run() }
`,
				"anchor.go": `package sample

func anchor() int { return 1 }
`,
			},
			mutate: func(t *testing.T, dir string) {
				t.Helper()
				writeFile(t, filepath.Join(dir, "caller.go"), `package sample

// Receiver changed from Alpha to Beta.
func invokeReceiver(value Beta) int { return value.Run() }
`)
			},
		},
		{
			name: "typescript_import_target",
			initial: map[string]string{
				"src/left.ts": `export function execute(): number { return 1; }
`,
				"src/right_target.ts": `export function execute(): number { return 2; }
`,
				"src/consumer.ts": `import { execute } from "./left";

export function consume(): number { return execute(); }
`,
				"src/anchor.ts": `export const anchor = 1;
`,
			},
			mutate: func(t *testing.T, dir string) {
				t.Helper()
				writeFile(t, filepath.Join(dir, "src", "consumer.ts"), `import { execute } from "./right_target";

export function consume(): number { return execute(); }
`)
			},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			runIncrementalEquivalenceScenario(t, scenario)
		})
	}
}

func runIncrementalEquivalenceScenario(t *testing.T, scenario incrementalEquivalenceScenario) {
	t.Helper()
	repoDir := t.TempDir()
	for relPath, contents := range scenario.initial {
		writeFile(t, filepath.Join(repoDir, relPath), contents)
	}

	incrementalDB := filepath.Join(t.TempDir(), "incremental.db")
	incrementalStore := openPersistentStore(t, incrementalDB)
	defer incrementalStore.Close()
	initial := New(context.Background(), incrementalStore, repoDir, discover.ModeFull)
	if err := initial.Run(); err != nil {
		t.Fatalf("initial index: %v", err)
	}

	scenario.mutate(t, repoDir)
	incrementalStart := time.Now()
	incremental := New(context.Background(), incrementalStore, repoDir, discover.ModeFull)
	if err := incremental.Run(); err != nil {
		t.Fatalf("incremental index: %v", err)
	}
	incrementalElapsed := time.Since(incrementalStart)
	incrementalGraph := canonicalWholeGraph(t, incrementalStore, incremental.ProjectName)
	incrementalBytes := sqlitePhysicalBytes(t, incrementalStore, incrementalDB)

	cleanDB := filepath.Join(t.TempDir(), "clean.db")
	cleanStore := openPersistentStore(t, cleanDB)
	defer cleanStore.Close()
	cleanStart := time.Now()
	clean := New(context.Background(), cleanStore, repoDir, discover.ModeFull)
	if err := clean.Run(); err != nil {
		t.Fatalf("clean index: %v", err)
	}
	cleanElapsed := time.Since(cleanStart)
	cleanGraph := canonicalWholeGraph(t, cleanStore, clean.ProjectName)
	cleanBytes := sqlitePhysicalBytes(t, cleanStore, cleanDB)

	staleNodes, missingNodes := setDeltaCounts(incrementalGraph.nodes, cleanGraph.nodes)
	staleEdges, missingEdges := setDeltaCounts(incrementalGraph.edges, cleanGraph.edges)
	t.Logf(
		"mode=%s changed=%d deleted=%d stale_nodes=%d missing_nodes=%d stale_edges=%d missing_edges=%d incremental_ms=%.3f clean_ms=%.3f incremental_bytes=%d clean_bytes=%d",
		incremental.LastIndexDelta.Mode,
		incremental.LastIndexDelta.FilesChanged,
		incremental.LastIndexDelta.FilesDeleted,
		staleNodes,
		missingNodes,
		staleEdges,
		missingEdges,
		float64(incrementalElapsed.Microseconds())/1000,
		float64(cleanElapsed.Microseconds())/1000,
		incrementalBytes,
		cleanBytes,
	)

	if incremental.LastIndexDelta.Mode != "incremental" {
		t.Errorf("index mode = %q, want incremental", incremental.LastIndexDelta.Mode)
	}
	if staleNodes+missingNodes != 0 {
		t.Errorf("incremental nodes differ from clean rebuild:\n  %s", strings.Join(diff(incrementalGraph.nodes, cleanGraph.nodes), "\n  "))
	}
	if staleEdges+missingEdges != 0 {
		t.Logf("incremental import maps: %#v", incremental.importMaps)
		t.Errorf("incremental edges differ from clean rebuild:\n  %s", strings.Join(diff(incrementalGraph.edges, cleanGraph.edges), "\n  "))
	}
}

func openPersistentStore(t *testing.T, path string) *store.Store {
	t.Helper()
	s, err := store.OpenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func canonicalWholeGraph(t *testing.T, s *store.Store, project string) canonicalStoreGraph {
	t.Helper()
	nodes, err := s.AllNodes(project)
	if err != nil {
		t.Fatalf("AllNodes: %v", err)
	}
	strip := func(qn string) string {
		if qn == project {
			return "<ROOT>"
		}
		return strings.TrimPrefix(qn, project+".")
	}
	idToNode := make(map[int64]string, len(nodes))
	graph := canonicalStoreGraph{nodes: make([]string, 0, len(nodes))}
	for _, node := range nodes {
		identity := fmt.Sprintf("%s|%s", node.Label, strip(node.QualifiedName))
		idToNode[node.ID] = identity
		graph.nodes = append(graph.nodes, fmt.Sprintf(
			"%s|%s|%d:%d",
			identity,
			node.FilePath,
			node.StartLine,
			node.EndLine,
		))
	}
	edges, err := s.AllEdges(project)
	if err != nil {
		t.Fatalf("AllEdges: %v", err)
	}
	graph.edges = make([]string, 0, len(edges))
	for _, edge := range edges {
		graph.edges = append(graph.edges, fmt.Sprintf(
			"%s|%s|%s",
			idToNode[edge.SourceID],
			edge.Type,
			idToNode[edge.TargetID],
		))
	}
	sort.Strings(graph.nodes)
	sort.Strings(graph.edges)
	return graph
}

func setDeltaCounts(got, want []string) (gotOnly, wantOnly int) {
	gotSet := make(map[string]struct{}, len(got))
	for _, value := range got {
		gotSet[value] = struct{}{}
	}
	wantSet := make(map[string]struct{}, len(want))
	for _, value := range want {
		wantSet[value] = struct{}{}
	}
	for value := range gotSet {
		if _, ok := wantSet[value]; !ok {
			gotOnly++
		}
	}
	for value := range wantSet {
		if _, ok := gotSet[value]; !ok {
			wantOnly++
		}
	}
	return gotOnly, wantOnly
}

func sqlitePhysicalBytes(t *testing.T, s *store.Store, dbPath string) int64 {
	t.Helper()
	s.Checkpoint(context.Background())
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(dbPath + suffix)
		if err == nil {
			total += info.Size()
			continue
		}
		if !os.IsNotExist(err) {
			t.Fatalf("stat SQLite artifact %s: %v", dbPath+suffix, err)
		}
	}
	return total
}
