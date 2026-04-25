// eval_rank_localize is a one-shot evaluation harness for rank_by_query
// (PageRank), code_localize (LocAgent BFS), and code_localize_agent
// (LLM-driven LocAgent loop).
//
// Output modes:
//
//	default (text): human-readable summary of all three tools
//	-json:          structured JSON for harness scoring (Loc-Bench compare)
package main

import (
	"context"
	"encoding/json"
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

// jsonOutput is the structured shape emitted under -json. Loc-Bench
// scoring matches against these fields directly instead of substring-
// matching the human-readable form, which is too coarse to distinguish
// real model differences from output-formatting differences.
type jsonOutput struct {
	DB             string                  `json:"db"`
	Project        string                  `json:"project"`
	Query          string                  `json:"query"`
	SeedStrategy   string                  `json:"seed_strategy"`
	TopK           int                     `json:"top_k"`
	Depth          int                     `json:"depth"`
	AgentEnabled   bool                    `json:"agent_enabled"`
	NodeCount      int                     `json:"node_count"`
	EdgeCount      int                     `json:"edge_count"`
	EmbeddingCount int                     `json:"embedding_count"`
	RankByQuery    []ranking.RankedNode    `json:"rank_by_query"`
	RankByQueryErr string                  `json:"rank_by_query_error,omitempty"`
	CodeLocalize   []localize.LocalizedEntity  `json:"code_localize"`
	CodeLocalizeErr string                 `json:"code_localize_error,omitempty"`
	Agent          *locagent.Result        `json:"code_localize_agent,omitempty"`
	AgentErr       string                  `json:"code_localize_agent_error,omitempty"`
}

func main() {
	topK := flag.Int("top-k", 10, "max results")
	depth := flag.Int("depth", 3, "BFS depth for code_localize")
	seedStrategy := flag.String("seed-strategy", string(ranking.SeedStrategyHybrid), "substring | embedding | hybrid")
	runAgent := flag.Bool("agent", false, "also run code_localize_agent (requires ANTHROPIC_API_KEY)")
	jsonMode := flag.Bool("json", false, "emit structured JSON instead of human-readable text")
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

	nodeCount, _ := st.CountNodes(project)
	edgeCount, _ := st.CountEdges(project)
	embedCount, _ := st.EmbeddingCount(project)

	ctx := context.Background()
	strategy := ranking.SeedStrategy(*seedStrategy)

	out := jsonOutput{
		DB:             filepath.Base(dbPath),
		Project:        project,
		Query:          query,
		SeedStrategy:   *seedStrategy,
		TopK:           *topK,
		Depth:          *depth,
		AgentEnabled:   *runAgent,
		NodeCount:      nodeCount,
		EdgeCount:      edgeCount,
		EmbeddingCount: embedCount,
	}

	if !*jsonMode {
		fmt.Printf("=== eval ===\n")
		fmt.Printf("db: %s\n", out.DB)
		fmt.Printf("project: %s\n", project)
		fmt.Printf("query: %q\n", query)
		fmt.Printf("seed_strategy: %s, top_k: %d, depth: %d, agent: %v\n", *seedStrategy, *topK, *depth, *runAgent)
		fmt.Printf("graph: %d nodes, %d edges, %d embeddings\n\n", nodeCount, edgeCount, embedCount)
	}

	// rank_by_query
	if !*jsonMode {
		fmt.Println("=== rank_by_query ===")
	}
	ranked, err := ranking.RankByQueryWithStrategy(ctx, st, project, query, *topK, strategy)
	if err != nil {
		out.RankByQueryErr = err.Error()
		if !*jsonMode {
			fmt.Printf("ERROR: %v\n\n", err)
		}
	} else {
		out.RankByQuery = ranked
		if !*jsonMode {
			for i, r := range ranked {
				fmt.Printf("  %2d. %-30s  score=%.4f  %s:%s\n", i+1,
					truncate(r.Name, 30), r.Score, r.Label, truncate(r.QualifiedName, 60))
			}
			fmt.Println()
		}
	}

	// code_localize
	if !*jsonMode {
		fmt.Println("=== code_localize (primitives) ===")
	}
	localized, err := localize.CodeLocalizeWithStrategy(ctx, st, project, query, *depth, *topK, strategy)
	if err != nil {
		out.CodeLocalizeErr = err.Error()
		if !*jsonMode {
			fmt.Printf("ERROR: %v\n\n", err)
		}
	} else {
		out.CodeLocalize = localized
		if !*jsonMode {
			for i, r := range localized {
				fmt.Printf("  %2d. %-30s  score=%.4f  dist=%d  via=%v\n", i+1,
					truncate(r.Name, 30), r.Score, r.Distance, r.ReachedVia)
				fmt.Printf("      %s  %s:%d-%d\n", r.Label, truncate(r.FilePath, 70), r.StartLine, r.EndLine)
			}
			fmt.Println()
		}
	}

	// code_localize_agent
	if *runAgent {
		if !*jsonMode {
			fmt.Println("=== code_localize_agent (LLM loop) ===")
		}
		res, err := locagent.Run(ctx, st, project, query, *topK)
		if err != nil {
			out.AgentErr = err.Error()
			if !*jsonMode {
				fmt.Printf("ERROR: %v\n\n", err)
			}
		} else {
			out.Agent = res
			if !*jsonMode {
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

	if *jsonMode {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(&out); err != nil {
			fmt.Fprintf(os.Stderr, "json encode: %v\n", err)
			os.Exit(1)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
