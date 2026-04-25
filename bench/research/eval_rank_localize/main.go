// eval_rank_localize is a one-shot evaluation harness for rank_by_query
// (PageRank), code_localize (LocAgent BFS), and code_localize_agent
// (LLM-driven LocAgent loop).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/locagent"
	"github.com/DeusData/codebase-memory-mcp/internal/localize"
	"github.com/DeusData/codebase-memory-mcp/internal/ranking"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

func main() {
	topK := flag.Int("top-k", 10, "max results")
	depth := flag.Int("depth", 3, "BFS depth for code_localize")
	seedStrategy := flag.String("seed-strategy", string(ranking.SeedStrategyHybrid), "substring | embedding | hybrid")
	runAgent := flag.Bool("agent", false, "also run code_localize_agent (requires ANTHROPIC_API_KEY)")
	flag.Parse()

	args := flag.Args()
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: eval_rank_localize [flags] <db-path> <query>")
		flag.PrintDefaults()
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

	projects, err := st.ListProjects()
	if err != nil || len(projects) == 0 {
		fmt.Fprintf(os.Stderr, "list projects: %v\n", err)
		os.Exit(1)
	}
	project := projects[0].Name
	fmt.Printf("=== eval ===\n")
	fmt.Printf("db: %s\n", filepath.Base(dbPath))
	fmt.Printf("project: %s\n", project)
	fmt.Printf("query: %q\n", query)
	fmt.Printf("seed_strategy: %s, top_k: %d, depth: %d, agent: %v\n", *seedStrategy, *topK, *depth, *runAgent)

	nodeCount, _ := st.CountNodes(project)
	edgeCount, _ := st.CountEdges(project)
	embedCount, _ := st.EmbeddingCount(project)
	fmt.Printf("graph: %d nodes, %d edges, %d embeddings\n\n", nodeCount, edgeCount, embedCount)

	ctx := context.Background()
	strategy := ranking.SeedStrategy(*seedStrategy)

	fmt.Println("=== rank_by_query ===")
	ranked, err := ranking.RankByQueryWithStrategy(ctx, st, project, query, *topK, strategy)
	if err != nil {
		fmt.Printf("ERROR: %v\n\n", err)
	} else {
		for i, r := range ranked {
			fmt.Printf("  %2d. %-30s  score=%.4f  %s:%s\n", i+1,
				truncate(r.Name, 30), r.Score, r.Label, truncate(r.QualifiedName, 60))
		}
		fmt.Println()
	}

	fmt.Println("=== code_localize (primitives) ===")
	localized, err := localize.CodeLocalizeWithStrategy(ctx, st, project, query, *depth, *topK, strategy)
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

	if *runAgent {
		fmt.Println("=== code_localize_agent (LLM loop) ===")
		res, err := locagent.Run(ctx, st, project, query, *topK)
		if err != nil {
			fmt.Printf("ERROR: %v\n\n", err)
		} else {
			fmt.Printf("turns=%d, stop_reason=%s, input_tokens=%d, output_tokens=%d\n",
				res.Turns, res.StopReason, res.InputTokens, res.OutputTokens)
			for i, e := range res.Entities {
				fmt.Printf("  %2d. %s\n", i+1, e.QualifiedName)
				fmt.Printf("      %s — %s\n", e.FilePath, truncate(e.Reason, 100))
			}
			fmt.Println()
			fmt.Println("Transcript:")
			for _, t := range res.Transcript {
				fmt.Printf("  T%d %s %s: %s\n", t.Turn, t.Kind, t.ToolName, truncate(t.Summary, 150))
			}
			fmt.Println()
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
