package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/pipeline"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerReviewContextTool() {
	s.addTool(&mcp.Tool{
		Name: "get_review_context",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},
		Description: "Generate a token-optimized review context from git changes. Runs detect_changes internally, then distills blast radius, test coverage gaps, dependency chains, and cross-service impacts into a compact markdown summary (~200-500 tokens). Designed for LLM consumption — returns structured markdown, not JSON. Use before reviewing commits or PRs to understand what changed and what's affected.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"scope": {
					"type": "string",
					"description": "Which changes to analyze: 'unstaged' (working tree), 'staged' (git add), 'all' (HEAD, default), 'branch' (compare with base_branch)",
					"enum": ["unstaged", "staged", "all", "branch"]
				},
				"base_branch": {
					"type": "string",
					"description": "Base branch for scope=branch comparison (default: main)"
				},
				"depth": {
					"type": "integer",
					"description": "Maximum BFS depth for impact tracing (1-5, default 3)"
				},
				"max_tokens": {
					"type": "integer",
					"description": "Approximate token budget for the summary (default 500, min 200, max 2000). Longer budgets include more impacted symbols and dependency details."
				},
				"project": {
					"type": "string",
					"description": "Project to analyze. Defaults to session project."
				}
			}
		}`),
	}, s.handleGetReviewContext)
}

func (s *Server) handleGetReviewContext(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	scopeStr := getStringArg(args, "scope")
	if scopeStr == "" {
		scopeStr = "all"
	}
	scope := pipeline.DiffScope(scopeStr)

	baseBranch := getStringArg(args, "base_branch")
	depth := getIntArg(args, "depth", 3)
	if depth < 1 {
		depth = 1
	}
	if depth > 5 {
		depth = 5
	}
	maxTokens := getIntArg(args, "max_tokens", 500)
	if maxTokens < 200 {
		maxTokens = 200
	}
	if maxTokens > 2000 {
		maxTokens = 2000
	}

	project := getStringArg(args, "project")
	effectiveProject := s.resolveProjectName(project)

	st, repoPath, projName, resolveErr := s.resolveDetectRepo(effectiveProject)
	if resolveErr != nil {
		return resolveErr, nil
	}

	changedFiles, err := pipeline.ParseGitDiffFiles(repoPath, scope, baseBranch)
	if err != nil {
		return errResult(fmt.Sprintf("git diff: %v", err)), nil
	}

	if len(changedFiles) == 0 {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "## Review Context\n\nNo changes detected."},
			},
		}, nil
	}

	hunks, err := pipeline.ParseGitDiffHunks(repoPath, scope, baseBranch)
	if err != nil {
		slog.Warn("review_context.hunks.err", "err", err)
	}

	changedSymbols := mapChangesToSymbols(st, projName, changedFiles, hunks)
	impactedSymbols, allEdges := traceImpact(st, changedSymbols, depth)

	testCoverage := findTestCoverage(st, changedSymbols)
	depChains := traceOutboundDeps(st, changedSymbols)

	md := buildReviewMarkdown(changedFiles, changedSymbols, impactedSymbols, allEdges, testCoverage, depChains, maxTokens)

	result := &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: md},
		},
	}
	s.addUpdateNotice(result)
	return result, nil
}

// testCoverageInfo holds test coverage data for a symbol.
type testCoverageInfo struct {
	SymbolName string
	TestCount  int
	TestNames  []string
}

// findTestCoverage checks TESTS edges for each changed symbol.
func findTestCoverage(st *store.Store, symbols []*store.Node) []testCoverageInfo {
	var result []testCoverageInfo
	for _, sym := range symbols {
		info := testCoverageInfo{SymbolName: sym.Name}
		edges, err := st.FindEdgesByTargetAndType(sym.ID, "TESTS")
		if err != nil {
			slog.Debug("review_context.tests.err", "sym", sym.Name, "err", err)
			result = append(result, info)
			continue
		}
		info.TestCount = len(edges)
		for _, e := range edges {
			if len(info.TestNames) < 3 {
				if testNode, findErr := st.FindNodeByID(e.SourceID); findErr == nil {
					info.TestNames = append(info.TestNames, testNode.Name)
				}
			}
		}
		result = append(result, info)
	}
	return result
}

// depChainEntry holds outbound dependency info for a symbol.
type depChainEntry struct {
	SymbolName string
	Deps       []string
}

// traceOutboundDeps traces 1-hop outbound CALLS from each changed symbol.
func traceOutboundDeps(st *store.Store, symbols []*store.Node) []depChainEntry {
	var result []depChainEntry
	for _, sym := range symbols {
		entry := depChainEntry{SymbolName: sym.Name}
		edges, err := st.FindEdgesBySourceAndType(sym.ID, "CALLS")
		if err != nil {
			result = append(result, entry)
			continue
		}
		for _, e := range edges {
			if len(entry.Deps) >= 5 {
				break
			}
			if target, findErr := st.FindNodeByID(e.TargetID); findErr == nil {
				entry.Deps = append(entry.Deps, target.Name)
			}
		}
		result = append(result, entry)
	}
	return result
}

//nolint:cyclop // aggregates change data into structured markdown — splitting would fragment the output logic
func buildReviewMarkdown(
	files []pipeline.ChangedFile,
	symbols []*store.Node,
	impacted []impactedSymbol,
	edges []store.EdgeInfo,
	testCov []testCoverageInfo,
	deps []depChainEntry,
	maxTokens int,
) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## Review Context: %d file(s), %d symbol(s) changed\n\n", len(files), len(symbols))

	// Changed files
	b.WriteString("### Changed Files\n")
	for _, f := range files {
		fmt.Fprintf(&b, "- %s `%s`\n", rcFileStatus(f.Status), f.Path)
	}
	b.WriteString("\n")

	// Changed symbols
	if len(symbols) > 0 {
		b.WriteString("### Changed Symbols\n")
		for _, sym := range symbols {
			fmt.Fprintf(&b, "- [%s] `%s` (%s:%d)\n", sym.Label, sym.Name, sym.FilePath, sym.StartLine)
		}
		b.WriteString("\n")
	}

	// Blast radius
	if len(impacted) > 0 {
		var critical, high, medium, low int
		for _, is := range impacted {
			switch store.HopToRisk(is.Hop) {
			case store.RiskCritical:
				critical++
			case store.RiskHigh:
				high++
			case store.RiskMedium:
				medium++
			case store.RiskLow:
				low++
			}
		}

		fmt.Fprintf(&b, "### Blast Radius (%d impacted", len(impacted))
		if critical > 0 {
			fmt.Fprintf(&b, ", %d CRITICAL", critical)
		}
		b.WriteString(")\n")

		maxImpacted := maxTokens / 50
		if maxImpacted < 5 {
			maxImpacted = 5
		}
		if maxImpacted > len(impacted) {
			maxImpacted = len(impacted)
		}

		for i := 0; i < maxImpacted; i++ {
			is := impacted[i]
			risk := string(store.HopToRisk(is.Hop))
			fileLoc := ""
			if is.Node.FilePath != "" {
				fileLoc = fmt.Sprintf(" (%s:%d)", is.Node.FilePath, is.Node.StartLine)
			}
			fmt.Fprintf(&b, "- [%s] `%s` <- %s%s\n", risk, is.Node.Name, is.ChangedBy, fileLoc)
		}
		if maxImpacted < len(impacted) {
			fmt.Fprintf(&b, "- ... and %d more (HIGH: %d, MEDIUM: %d, LOW: %d)\n", len(impacted)-maxImpacted, high, medium, low)
		}
		b.WriteString("\n")
	}

	// Cross-service impacts
	var crossServiceLines []string
	seen := make(map[string]bool)
	for _, e := range edges {
		if e.Type == "HTTP_CALLS" || e.Type == "ASYNC_CALLS" {
			line := fmt.Sprintf("- %s: %s -> %s", e.Type, e.FromName, e.ToName)
			if !seen[line] {
				seen[line] = true
				crossServiceLines = append(crossServiceLines, line)
			}
		}
	}
	if len(crossServiceLines) > 0 {
		b.WriteString("### Cross-Service Impact\n")
		for _, line := range crossServiceLines {
			b.WriteString(line + "\n")
		}
		b.WriteString("\n")
	}

	// Test coverage
	if len(testCov) > 0 {
		b.WriteString("### Test Coverage\n")
		sorted := make([]testCoverageInfo, len(testCov))
		copy(sorted, testCov)
		// TestCount asc, then SymbolName asc. Untested symbols bubble to
		// the top by intent; SymbolName is the deterministic tiebreaker so
		// the listing of untested-or-equal-coverage symbols is stable
		// across runs.
		sort.Slice(sorted, func(i, j int) bool {
			if sorted[i].TestCount != sorted[j].TestCount {
				return sorted[i].TestCount < sorted[j].TestCount
			}
			return sorted[i].SymbolName < sorted[j].SymbolName
		})
		hasGaps := false
		for _, tc := range sorted {
			if tc.TestCount == 0 {
				hasGaps = true
				fmt.Fprintf(&b, "- `%s`: **UNTESTED**\n", tc.SymbolName)
			} else {
				testStr := strings.Join(tc.TestNames, ", ")
				if len(tc.TestNames) < tc.TestCount {
					testStr += fmt.Sprintf(" (+%d more)", tc.TestCount-len(tc.TestNames))
				}
				fmt.Fprintf(&b, "- `%s`: %d test(s) [%s]\n", tc.SymbolName, tc.TestCount, testStr)
			}
		}
		b.WriteString("\n")
		if hasGaps {
			b.WriteString("**Note:** Untested symbols should be prioritized for review.\n\n")
		}
	}

	// Dependency chains (only with sufficient budget)
	if maxTokens >= 400 && len(deps) > 0 {
		hasDeps := false
		for _, d := range deps {
			if len(d.Deps) > 0 {
				hasDeps = true
				break
			}
		}
		if hasDeps {
			b.WriteString("### Dependencies\n")
			for _, d := range deps {
				if len(d.Deps) > 0 {
					fmt.Fprintf(&b, "- `%s` -> %s\n", d.SymbolName, strings.Join(d.Deps, ", "))
				}
			}
			b.WriteString("\n")
		}
	}

	return b.String()
}

// rcFileStatus returns a human-readable label for git diff status codes.
func rcFileStatus(status string) string {
	switch status {
	case "A":
		return "added"
	case "M":
		return "modified"
	case "D":
		return "deleted"
	case "R":
		return "renamed"
	case "C":
		return "copied"
	default:
		return status
	}
}
