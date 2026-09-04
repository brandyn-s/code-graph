package pipeline

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/brandyn-s/code-graph/internal/discover"
)

// TestIncrementalMatchesCleanUnderRandomEdits is the randomized companion to
// TestIncrementalMatchesCleanAcrossChangeClasses. Starting from the checked-in
// go-minimal fixture, it applies a seeded sequence of random add, modify,
// delete, and rename operations; after every operation the incrementally
// maintained graph must equal a clean rebuild of the same tree. A clean
// rebuild is the oracle; any divergence is a bug in change classification,
// dependent expansion, or stale-row cleanup.
//
// Seeds are fixed so a failure is reproducible with -run and the logged seed.
func TestIncrementalMatchesCleanUnderRandomEdits(t *testing.T) {
	if testing.Short() {
		t.Skip("randomized incremental property test skipped in -short mode")
	}
	t.Setenv("CODE_GRAPH_FULL_REINDEX_EVERY", "0")
	t.Setenv("CODE_GRAPH_INCREMENTAL_CAP", "10000")

	const opsPerSeed = 12
	for _, seed := range []int64{1, 2, 3, 5, 8, 13, 21, 34, 55, 89} {
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			runRandomIncrementalSequence(t, seed, opsPerSeed)
		})
	}
}

type editCorpus struct {
	dir   string
	rng   *rand.Rand
	files map[string][]string // relative path -> function names defined there
	next  int
}

func runRandomIncrementalSequence(t *testing.T, seed int64, ops int) {
	t.Helper()
	corpus := &editCorpus{
		dir:   t.TempDir(),
		rng:   rand.New(rand.NewSource(seed)), //nolint:gosec // deterministic test data, not security material
		files: map[string][]string{},
	}
	seedFromGoMinimal(t, corpus)

	incrementalStore := openPersistentStore(t, filepath.Join(t.TempDir(), "incremental.db"))
	defer incrementalStore.Close()
	initial := New(context.Background(), incrementalStore, corpus.dir, discover.ModeFull)
	if err := initial.Run(); err != nil {
		t.Fatalf("initial index: %v", err)
	}

	for step := 1; step <= ops; step++ {
		op := corpus.applyRandomOp(t)

		incremental := New(context.Background(), incrementalStore, corpus.dir, discover.ModeFull)
		if err := incremental.Run(); err != nil {
			t.Fatalf("seed=%d step=%d op=%s incremental index: %v", seed, step, op, err)
		}
		incrementalGraph := canonicalWholeGraph(t, incrementalStore, incremental.ProjectName)

		cleanStore := openPersistentStore(t, filepath.Join(t.TempDir(), fmt.Sprintf("clean-%d.db", step)))
		clean := New(context.Background(), cleanStore, corpus.dir, discover.ModeFull)
		if err := clean.Run(); err != nil {
			cleanStore.Close()
			t.Fatalf("seed=%d step=%d op=%s clean index: %v", seed, step, op, err)
		}
		cleanGraph := canonicalWholeGraph(t, cleanStore, clean.ProjectName)
		cleanStore.Close()

		staleNodes, missingNodes := setDeltaCounts(incrementalGraph.nodes, cleanGraph.nodes)
		staleEdges, missingEdges := setDeltaCounts(incrementalGraph.edges, cleanGraph.edges)
		t.Logf("seed=%d step=%d op=%s mode=%s files=%d nodes=%d edges=%d stale_nodes=%d missing_nodes=%d stale_edges=%d missing_edges=%d",
			seed, step, op, incremental.LastIndexDelta.Mode, len(corpus.files),
			len(cleanGraph.nodes), len(cleanGraph.edges), staleNodes, missingNodes, staleEdges, missingEdges)
		if staleNodes+missingNodes != 0 {
			t.Fatalf("seed=%d step=%d op=%s: incremental nodes differ from clean rebuild:\n  %s",
				seed, step, op, strings.Join(diff(incrementalGraph.nodes, cleanGraph.nodes), "\n  "))
		}
		if staleEdges+missingEdges != 0 {
			t.Fatalf("seed=%d step=%d op=%s: incremental edges differ from clean rebuild:\n  %s",
				seed, step, op, strings.Join(diff(incrementalGraph.edges, cleanGraph.edges), "\n  "))
		}
	}
}

// seedFromGoMinimal copies the go-minimal fixture into the corpus directory
// and records the functions main.go defines so later edits can call them.
func seedFromGoMinimal(t *testing.T, c *editCorpus) {
	t.Helper()
	src, err := filepath.Abs(filepath.Join("..", "..", "bench", "accuracy", "synthetic", "go-minimal"))
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.Walk(src, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() {
			return walkErr
		}
		if filepath.Ext(path) != ".go" && filepath.Base(path) != "go.mod" {
			return nil
		}
		rel, _ := filepath.Rel(src, path)
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		writeFile(t, filepath.Join(c.dir, rel), string(data))
		return nil
	})
	if err != nil {
		t.Fatalf("copy go-minimal: %v", err)
	}
	c.files["main.go"] = []string{"entry", "leaf", "helper"}
}

// callableNames returns every function defined in the root package, sorted so
// random selection is deterministic across runs.
func (c *editCorpus) callableNames() []string {
	total := 0
	for _, fns := range c.files {
		total += len(fns)
	}
	names := make([]string, 0, total)
	for _, fns := range c.files {
		names = append(names, fns...)
	}
	sort.Strings(names)
	return names
}

func (c *editCorpus) generatedFiles() []string {
	var out []string
	for rel := range c.files {
		if rel != "main.go" {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}

// applyRandomOp mutates the tree with one of: add a file whose functions call
// existing ones, rewrite an existing generated file with new call targets,
// delete a generated file, or rename one. It returns a short label.
func (c *editCorpus) applyRandomOp(t *testing.T) string {
	t.Helper()
	generated := c.generatedFiles()
	choice := c.rng.Intn(4)
	if len(generated) == 0 {
		choice = 0
	}
	switch choice {
	case 0:
		c.next++
		rel := fmt.Sprintf("gen%d.go", c.next)
		c.writeGenerated(t, rel, 1+c.rng.Intn(3))
		return "add:" + rel
	case 1:
		rel := generated[c.rng.Intn(len(generated))]
		c.writeGenerated(t, rel, 1+c.rng.Intn(3))
		return "modify:" + rel
	case 2:
		rel := generated[c.rng.Intn(len(generated))]
		if err := os.Remove(filepath.Join(c.dir, rel)); err != nil {
			t.Fatal(err)
		}
		delete(c.files, rel)
		return "delete:" + rel
	default:
		rel := generated[c.rng.Intn(len(generated))]
		c.next++
		renamed := fmt.Sprintf("moved%d.go", c.next)
		if err := os.Rename(filepath.Join(c.dir, rel), filepath.Join(c.dir, renamed)); err != nil {
			t.Fatal(err)
		}
		c.files[renamed] = c.files[rel]
		delete(c.files, rel)
		return "rename:" + rel + "->" + renamed
	}
}

// writeGenerated writes a root-package Go file defining n functions, each
// calling a random subset of the currently defined functions (possibly ones
// this file replaces, which then become unresolved, exactly like real edits).
func (c *editCorpus) writeGenerated(t *testing.T, rel string, n int) {
	t.Helper()
	targets := c.callableNames()
	base := strings.TrimSuffix(strings.TrimSuffix(rel, ".go"), "")
	var defined []string
	var b strings.Builder
	b.WriteString("package main\n\n")
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("%s_fn%d_%d", sanitizeIdent(base), i, c.rng.Intn(1000))
		defined = append(defined, name)
		fmt.Fprintf(&b, "func %s() {\n", name)
		calls := c.rng.Intn(3)
		for j := 0; j < calls && len(targets) > 0; j++ {
			fmt.Fprintf(&b, "\t%s()\n", targets[c.rng.Intn(len(targets))])
		}
		if c.rng.Intn(2) == 0 {
			b.WriteString("\tleaf()\n")
		}
		b.WriteString("}\n\n")
	}
	writeFile(t, filepath.Join(c.dir, rel), b.String())
	c.files[rel] = defined
}

func sanitizeIdent(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}
