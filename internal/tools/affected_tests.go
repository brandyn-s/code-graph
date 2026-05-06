package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/DeusData/codebase-memory-mcp/internal/pipeline"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerAffectedTestsTool() {
	s.addTool(&mcp.Tool{
		Name: "get_affected_tests",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},
		Description: "Find which tests need to run for the current git changes. Runs detect_changes internally, then traces TESTS edges for each changed symbol to identify exactly which test functions cover the modified code. Use before committing to run only the relevant tests instead of the full suite. Returns test functions grouped by the changed symbol they cover, plus a list of untested changed symbols.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"scope": {
					"type": "string",
					"description": "Which changes to analyze: 'unstaged', 'staged', 'all' (default), 'branch'",
					"enum": ["unstaged", "staged", "all", "branch"]
				},
				"base_branch": {
					"type": "string",
					"description": "Base branch for scope=branch comparison (default: main)"
				},
				"project": {
					"type": "string",
					"description": "Project to analyze. Defaults to session project."
				}
			}
		}`),
	}, s.handleAffectedTests)
}

func (s *Server) handleAffectedTests(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
		return jsonResult(map[string]any{
			"affected_tests": []any{},
			"untested":       []any{},
			"summary":        "No changes detected.",
		}), nil
	}

	hunks, err := pipeline.ParseGitDiffHunks(repoPath, scope, baseBranch)
	if err != nil {
		slog.Warn("affected_tests.hunks.err", "err", err)
	}

	changedSymbols := mapChangesToSymbols(st, projName, changedFiles, hunks)

	type affectedTest struct {
		TestName     string `json:"test_name"`
		TestQN       string `json:"test_qn"`
		TestFile     string `json:"test_file"`
		CoversSymbol string `json:"covers_symbol"`
	}

	var tests []affectedTest
	var untested []map[string]any
	testSeen := make(map[int64]bool)

	for _, sym := range changedSymbols {
		edges, findErr := st.FindEdgesByTargetAndType(sym.ID, "TESTS")
		if findErr != nil {
			continue
		}
		if len(edges) == 0 {
			untested = append(untested, map[string]any{
				"name":      sym.Name,
				"label":     sym.Label,
				"file_path": sym.FilePath,
			})
			continue
		}
		for _, e := range edges {
			if testSeen[e.SourceID] {
				continue
			}
			testSeen[e.SourceID] = true
			testNode, findErr := st.FindNodeByID(e.SourceID)
			if findErr != nil || testNode == nil {
				continue
			}
			tests = append(tests, affectedTest{
				TestName:     testNode.Name,
				TestQN:       testNode.QualifiedName,
				TestFile:     testNode.FilePath,
				CoversSymbol: sym.Name,
			})
		}
	}

	// Deduplicate test files for a run command hint
	testFiles := make(map[string]bool)
	for _, t := range tests {
		if t.TestFile != "" {
			testFiles[t.TestFile] = true
		}
	}
	fileList := make([]string, 0, len(testFiles))
	for f := range testFiles {
		fileList = append(fileList, f)
	}

	responseData := map[string]any{
		"affected_tests":  tests,
		"untested":        untested,
		"test_files":      fileList,
		"changed_symbols": len(changedSymbols),
		"tests_found":     len(tests),
		"untested_count":  len(untested),
		"_metadata":       s.stdReadGraphMetadata(effectiveProject),
	}

	return jsonResult(responseData), nil
}
