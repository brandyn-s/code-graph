// eval_rank_localize is a one-shot evaluation harness for the rank_by_query
// (PageRank) and code_localize (LocAgent-style BFS) MCP tools.
//
// Purpose: Gate β validation. Unit tests prove the algorithms work on
// synthetic graphs. This program runs them against real indexed projects
// to verify behavior on production-scale data.
//
// Usage:
//
//	go run ./bench/research/eval_rank_localize <db-path> <"query">
//
// Example:
//
//	go run ./bench/research/eval_rank_localize \
//	  ~/.cache/codebase-memory-mcp/c-Users-user-Documents-GitHub-mcp-servers.db \
//	  "how does authentication work"
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/localize"
	"github.com/DeusData/codebase-memory-mcp/internal/ranking"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

func main() {
	topK := flag.Int("top-k", 10, "max results")
	depth := flag.Int("depth", 3, "BFS depth for code_localize")
	flag.Parse()

	args := flag.Args()
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: eval_rank_localize <db-path> <query>")
		os.Exit(2)
	}
	dbPath := args[0]
	query := strings.Join(args[1:], " ")

	if _, err := os.Stat(dbPath); err != nil {
		fmt.Fprintf(os.Stderr, "db not found: %v\n", err)
		os.Exit(2)
	}

	st, err := store.OpenPath(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open store: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()

	// Project name = filename without extension. The store doesn't enforce
	// that the path-derived name matches what's stored in the projects
	// table — pull the actual project from the DB.
	projects, err := st.ListProjects()
	if err != nil {
		fmt.Fprintf(os.Stderr, "list projects: %v\n", err)
		os.Exit(1)
	}
	if len(projects) == 0 {
		fmt.Fprintf(os.Stderr, "no projects in %s\n", dbPath)
		os.Exit(1)
	}
	project := projects[0].Name
	fmt.Printf("=== eval ===\n")
	fmt.Printf("db: %s\n", filepath.Base(dbPath))
	fmt.Printf("project: %s\n", project)
	fmt.Printf("query: %q\n", query)
	fmt.Printf("top_k: %d, depth: %d\n", *topK, *depth)

	nodeCount, _ := st.CountNodes(project)
	edgeCount, _ := st.CountEdges(project)
	fmt.Printf("graph: %d nodes, %d edges\n\n", nodeCount, edgeCount)

	// === A-2: rank_by_query ===
	fmt.Println("=== rank_by_query (PageRank) ===")
	ranked, err := ranking.RankByQuery(st, project, query, *topK)
	if err != nil {
		fmt.Printf("ERROR: %v\n\n", err)
	} else {
		for i, r := range ranked {
			fmt.Printf("  %2d. %-30s  score=%.4f  %s:%s\n", i+1,
				truncate(r.Name, 30), r.Score, r.Label, truncate(r.QualifiedName, 60))
		}
		fmt.Println()
	}

	// === A-1: code_localize ===
	fmt.Println("=== code_localize (LocAgent BFS) ===")
	localized, err := localize.CodeLocalize(st, project, query, *depth, *topK)
	if err != nil {
		fmt.Printf("ERROR: %v\n\n", err)
	} else {
		for i, r := range localized {
			fmt.Printf("  %2d. %-30s  score=%.4f  dist=%d  via=%v\n", i+1,
				truncate(r.Name, 30), r.Score, r.Distance, r.ReachedVia)
			fmt.Printf("      %s  %s:%d-%d\n", r.Label, truncate(r.FilePath, 70), r.StartLine, r.EndLine)
		}
		fmt.Println()
	}

	// === Token cost estimate ===
	fmt.Println("=== token cost estimate ===")
	// Rough heuristic: each result is ~30 tokens (name + qn + label + score).
	rankTokens := len(ranked) * 30
	locTokens := len(localized) * 50 // larger because includes file:line range
	fullDumpTokens := nodeCount * 30 // worst-case naive: dump every node
	fmt.Printf("  rank_by_query top_k=%d: ~%d tokens\n", *topK, rankTokens)
	fmt.Printf("  code_localize top_k=%d: ~%d tokens\n", *topK, locTokens)
	fmt.Printf("  naive full-graph dump: ~%d tokens\n", fullDumpTokens)
	if rankTokens > 0 {
		fmt.Printf("  rank_by_query reduction: %.1fx vs full dump\n", float64(fullDumpTokens)/float64(rankTokens))
	}
	if locTokens > 0 {
		fmt.Printf("  code_localize reduction: %.1fx vs full dump\n", float64(fullDumpTokens)/float64(locTokens))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
