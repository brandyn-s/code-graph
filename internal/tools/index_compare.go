package tools

import (
	"context"
	"fmt"
	"sort"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type projectIndexSymbol struct {
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name"`
	Label         string `json:"label"`
	FilePath      string `json:"file_path"`
	StartLine     int    `json:"start_line"`
	EndLine       int    `json:"end_line"`
}

type changedProjectIndexSymbol struct {
	QualifiedName string             `json:"qualified_name"`
	Before        projectIndexSymbol `json:"before"`
	After         projectIndexSymbol `json:"after"`
}

func indexDeclarationSymbols(st *store.Store, project string) (map[string]projectIndexSymbol, error) {
	nodes, err := st.AllNodes(project)
	if err != nil {
		return nil, err
	}
	out := make(map[string]projectIndexSymbol)
	for _, node := range nodes {
		switch node.Label {
		case "Function", "Method", "Class", "Struct", "Interface", "Trait", "Enum":
		default:
			continue
		}
		identity := node.QualifiedName
		if identity == "" {
			identity = node.Name
		}
		key := node.Label + "\x00" + node.FilePath + "\x00" + identity
		out[key] = projectIndexSymbol{
			Name: node.Name, QualifiedName: node.QualifiedName, Label: node.Label,
			FilePath: node.FilePath, StartLine: node.StartLine, EndLine: node.EndLine,
		}
	}
	return out, nil
}

func sortedLimitedStrings(values []string, limit int) ([]string, bool) {
	sort.Strings(values)
	if len(values) <= limit {
		return values, false
	}
	return values[:limit], true
}

func symbolLess(left, right *projectIndexSymbol) bool {
	if left.FilePath != right.FilePath {
		return left.FilePath < right.FilePath
	}
	if left.QualifiedName != right.QualifiedName {
		return left.QualifiedName < right.QualifiedName
	}
	if left.Label != right.Label {
		return left.Label < right.Label
	}
	return left.StartLine < right.StartLine
}

func (s *Server) handleCompareProjectIndexes(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}
	baseProject := getStringArg(args, "base_project")
	targetProject := getStringArg(args, "target_project")
	if baseProject == "" || targetProject == "" {
		return errResult("base_project and target_project are required canonical project names"), nil
	}
	if baseProject == targetProject {
		return errResult("base_project and target_project must be different indexes"), nil
	}
	limit := getIntArg(args, "limit", 100)
	if limit < 1 {
		limit = 1
	}
	if limit > 500 {
		limit = 500
	}

	baseStore, baseRelease, err := s.router.AcquireStore(baseProject)
	if err != nil {
		return errResult(fmt.Sprintf("open base project %q: %v", baseProject, err)), nil
	}
	defer baseRelease()
	targetStore, targetRelease, err := s.router.AcquireStore(targetProject)
	if err != nil {
		return errResult(fmt.Sprintf("open target project %q: %v", targetProject, err)), nil
	}
	defer targetRelease()

	baseHashes, err := baseStore.GetFileHashes(baseProject)
	if err != nil {
		return errResult(fmt.Sprintf("read base file snapshot: %v", err)), nil
	}
	targetHashes, err := targetStore.GetFileHashes(targetProject)
	if err != nil {
		return errResult(fmt.Sprintf("read target file snapshot: %v", err)), nil
	}
	addedFiles := make([]string, 0)
	removedFiles := make([]string, 0)
	modifiedFiles := make([]string, 0)
	unchangedFiles := 0
	for path, targetHash := range targetHashes {
		baseHash, exists := baseHashes[path]
		switch {
		case !exists:
			addedFiles = append(addedFiles, path)
		case baseHash.SHA256 != targetHash.SHA256:
			modifiedFiles = append(modifiedFiles, path)
		default:
			unchangedFiles++
		}
	}
	for path := range baseHashes {
		if _, exists := targetHashes[path]; !exists {
			removedFiles = append(removedFiles, path)
		}
	}
	addedCount, removedCount, modifiedCount := len(addedFiles), len(removedFiles), len(modifiedFiles)
	addedFiles, addedTruncated := sortedLimitedStrings(addedFiles, limit)
	removedFiles, removedTruncated := sortedLimitedStrings(removedFiles, limit)
	modifiedFiles, modifiedTruncated := sortedLimitedStrings(modifiedFiles, limit)

	baseSymbols, err := indexDeclarationSymbols(baseStore, baseProject)
	if err != nil {
		return errResult(fmt.Sprintf("read base declarations: %v", err)), nil
	}
	targetSymbols, err := indexDeclarationSymbols(targetStore, targetProject)
	if err != nil {
		return errResult(fmt.Sprintf("read target declarations: %v", err)), nil
	}
	addedSymbols := make([]projectIndexSymbol, 0)
	removedSymbols := make([]projectIndexSymbol, 0)
	changedSymbols := make([]changedProjectIndexSymbol, 0)
	unchangedSymbols := 0
	for key, targetSymbol := range targetSymbols {
		baseSymbol, exists := baseSymbols[key]
		switch {
		case !exists:
			addedSymbols = append(addedSymbols, targetSymbol)
		case baseSymbol != targetSymbol:
			changedSymbols = append(changedSymbols, changedProjectIndexSymbol{
				QualifiedName: targetSymbol.QualifiedName, Before: baseSymbol, After: targetSymbol,
			})
		default:
			unchangedSymbols++
		}
	}
	for key, baseSymbol := range baseSymbols {
		if _, exists := targetSymbols[key]; !exists {
			removedSymbols = append(removedSymbols, baseSymbol)
		}
	}
	addedSymbolCount, removedSymbolCount, changedSymbolCount := len(addedSymbols), len(removedSymbols), len(changedSymbols)
	sort.Slice(addedSymbols, func(i, j int) bool { return symbolLess(&addedSymbols[i], &addedSymbols[j]) })
	sort.Slice(removedSymbols, func(i, j int) bool { return symbolLess(&removedSymbols[i], &removedSymbols[j]) })
	sort.Slice(changedSymbols, func(i, j int) bool { return symbolLess(&changedSymbols[i].After, &changedSymbols[j].After) })
	symbolsTruncated := len(addedSymbols) > limit || len(removedSymbols) > limit || len(changedSymbols) > limit
	if len(addedSymbols) > limit {
		addedSymbols = addedSymbols[:limit]
	}
	if len(removedSymbols) > limit {
		removedSymbols = removedSymbols[:limit]
	}
	if len(changedSymbols) > limit {
		changedSymbols = changedSymbols[:limit]
	}

	baseContext := map[string]any{}
	targetContext := map[string]any{}
	s.addLiveIndexIdentity(baseContext, baseStore, baseProject, "")
	s.addLiveIndexIdentity(targetContext, targetStore, targetProject, "")
	return jsonResult(map[string]any{
		"base_project": baseProject, "target_project": targetProject,
		"comparison_contract": "immutable_index_snapshot_delta",
		"file_delta": map[string]any{
			"added_count": addedCount, "removed_count": removedCount,
			"modified_count": modifiedCount, "unchanged_count": unchangedFiles,
			"added": addedFiles, "removed": removedFiles, "modified": modifiedFiles,
			"truncated": addedTruncated || removedTruncated || modifiedTruncated,
		},
		"symbol_delta": map[string]any{
			"added_count": addedSymbolCount, "removed_count": removedSymbolCount,
			"changed_count": changedSymbolCount, "unchanged_count": unchangedSymbols,
			"added": addedSymbols, "removed": removedSymbols, "changed": changedSymbols,
			"truncated": symbolsTruncated,
		},
		"project_contexts":   map[string]any{"base": baseContext, "target": targetContext},
		"limit_per_category": limit,
		"result_scope":       "compares two immutable local index snapshots; it does not query repository history or enforce organization ACLs",
		"_metadata":          s.stdStatusToolMetadata(),
	}), nil
}
