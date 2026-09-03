package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/brandyn-s/code-graph/internal/pipeline"
	"github.com/brandyn-s/code-graph/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type diffSymbol struct {
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name"`
	Label         string `json:"label"`
	FilePath      string `json:"file_path"`
}

type diffFile struct {
	Path    string       `json:"path"`
	Status  string       `json:"status"`
	OldPath string       `json:"old_path,omitempty"`
	Symbols []diffSymbol `json:"symbols,omitempty"`
}

type diffGraphResult struct {
	Project      string         `json:"project"`
	FromSHA      string         `json:"from_sha"`
	ToSHA        string         `json:"to_sha"`
	FilesChanged int            `json:"files_changed"`
	Files        []diffFile     `json:"files"`
	SymbolCount  int            `json:"symbol_count"`
	Note         string         `json:"note,omitempty"`
	Metadata     map[string]any `json:"_metadata,omitempty"`
}

// handleDiffGraph serves the diff_graph MCP tool. Given two git
// revisions (commit SHAs, branch names, or HEAD~N references), runs
// `git diff --name-status from..to`, then looks up the current
// indexed graph's nodes in each changed file. Answers "what symbols
// did this commit range touch?" without reindexing either SHA.
//
// Distinct from detect_changes (which handles uncommitted work,
// staged changes, or branch-vs-branch comparisons) by accepting two
// arbitrary SHAs. Complements get_review_context for post-merge
// review: "what did we ship between releases v1.2.0 and v1.3.0?"
//
// Scope note: because code-graph indexes the current working tree,
// symbols are reported from the CURRENT state of the repo. Functions
// that existed in `from_sha` but were deleted by `to_sha` cannot be
// surfaced (they are no longer in the index). That is a limitation
// of the "no reindex" design choice; a full pre/post symbol diff
// would require indexing both SHAs, which is out of scope for this
// tool.
func (s *Server) handleDiffGraph(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	projectArg := getStringArg(args, "project")
	project := s.resolveProjectName(projectArg)
	if project == "" {
		return errResult("project is required (pass `project` explicitly or call from a session with a resolved project)"), nil
	}

	fromSHA := getStringArg(args, "from_sha")
	toSHA := getStringArg(args, "to_sha")
	if fromSHA == "" || toSHA == "" {
		return errResult("both from_sha and to_sha are required — commit SHA (short or full), branch name, or HEAD~N"), nil
	}

	st, err := s.router.ForProject(project)
	if err != nil {
		return errResult(fmt.Sprintf("resolve store: %v", err)), nil
	}

	// Resolve repo_path from the projects table so we know which
	// working tree to run git diff inside.
	projs, err := st.ListProjects()
	if err != nil {
		return errResult(fmt.Sprintf("list projects: %v", err)), nil
	}
	var repoPath string
	for _, p := range projs {
		if p.Name == project {
			repoPath = p.RootPath
			break
		}
	}
	if repoPath == "" {
		return errResult(fmt.Sprintf("no root_path for project %q — reindex with index_repository first", project)), nil
	}

	changed, err := pipeline.ParseGitDiffFilesBetween(repoPath, fromSHA, toSHA)
	if err != nil {
		return errResult(fmt.Sprintf("git diff %s..%s: %v", fromSHA, toSHA, err)), nil
	}

	out := diffGraphResult{
		Project:      project,
		FromSHA:      fromSHA,
		ToSHA:        toSHA,
		FilesChanged: len(changed),
		Files:        make([]diffFile, 0, len(changed)),
	}

	if len(changed) == 0 {
		out.Note = "no files changed between these revisions"
		out.Metadata = s.stdReadGraphMetadata(project)
		body, _ := json.MarshalIndent(out, "", "  ")
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(body)}}}, nil
	}

	for _, cf := range changed {
		entry := diffFile{
			Path:    cf.Path,
			Status:  cf.Status,
			OldPath: cf.OldPath,
		}
		// Symbol lookup: current index only. `D` (deleted) and renamed
		// sources are expected to miss — that is documented in the tool
		// description.
		if cf.Status != "D" {
			symbols := symbolsForFile(st, project, cf.Path)
			entry.Symbols = symbols
			out.SymbolCount += len(symbols)
		}
		out.Files = append(out.Files, entry)
	}

	// Sort files by the number of symbols they touch, desc. High-impact
	// files bubble to the top so a reviewer sees the interesting part of
	// the commit range first. Path as the deterministic tiebreaker — files
	// with equal symbol counts otherwise land in random order across runs,
	// breaking reproducibility for change-impact reporting.
	sort.Slice(out.Files, func(i, j int) bool {
		if len(out.Files[i].Symbols) != len(out.Files[j].Symbols) {
			return len(out.Files[i].Symbols) > len(out.Files[j].Symbols)
		}
		return out.Files[i].Path < out.Files[j].Path
	})

	if out.SymbolCount == 0 {
		out.Note = "git identified changed files but none map to currently-indexed symbols. Files may be outside the index scope (e.g. docs, lockfiles) or the symbols were deleted after to_sha."
	}
	out.Metadata = s.stdReadGraphMetadata(project)

	body, _ := json.MarshalIndent(out, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
	}, nil
}

// symbolsForFile returns the Function / Method / Class nodes the
// current index knows about in the given file. Used by diff_graph to
// answer "what symbols live in this changed file right now?"
func symbolsForFile(st *store.Store, project, filePath string) []diffSymbol {
	nodes, err := st.FindNodesByFile(project, filePath)
	if err != nil {
		return nil
	}
	out := make([]diffSymbol, 0, len(nodes))
	for _, n := range nodes {
		switch n.Label {
		case "Function", "Method", "Class", "Struct", "Interface", "Trait", "Enum":
			out = append(out, diffSymbol{
				Name:          n.Name,
				QualifiedName: n.QualifiedName,
				Label:         n.Label,
				FilePath:      n.FilePath,
			})
		}
	}
	return out
}
