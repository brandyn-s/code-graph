package tools

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/DeusData/codebase-memory-mcp/internal/discover"
	"github.com/DeusData/codebase-memory-mcp/internal/pipeline"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) handleIndexRepository(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	repoPath := getStringArg(args, "repo_path")
	if repoPath == "" {
		repoPath = getStringArg(args, "path") // accept both "repo_path" and "path"
	}
	if repoPath == "" {
		repoPath = s.sessionRoot // auto-detected from session
		if repoPath != "" {
			slog.Info("index.fallback_to_session_root", "path", repoPath,
				"hint", "no repo_path or path argument provided, using session root")
		}
	}
	if repoPath == "" {
		return errResult("repo_path is required (no session root detected)"), nil
	}

	// Parse and validate mode parameter
	modeStr := getStringArg(args, "mode")
	mode := discover.ModeFull
	if modeStr != "" {
		switch discover.IndexMode(modeStr) {
		case discover.ModeFull, discover.ModeFast:
			mode = discover.IndexMode(modeStr)
		default:
			return errResult(fmt.Sprintf("invalid mode %q: must be \"full\" or \"fast\"", modeStr)), nil
		}
	}

	// Resolve to absolute path
	absPath, err := filepath.Abs(repoPath)
	if err != nil {
		return errResult(fmt.Sprintf("invalid path: %v", err)), nil
	}

	// Reject paths that point to sensitive or system directories.
	if isForbiddenIndexPath(absPath) {
		return errResult(fmt.Sprintf("refused to index %s: path is a sensitive or system directory", absPath)), nil
	}

	// Validate path exists and is a directory before committing to a long index
	if info, statErr := os.Stat(absPath); statErr != nil {
		return errResult(fmt.Sprintf("path does not exist: %s", absPath)), nil
	} else if !info.IsDir() {
		return errResult(fmt.Sprintf("path is not a directory: %s", absPath)), nil
	}
	slog.Info("index.resolved_path", "input", repoPath, "absolute", absPath)

	projectName := pipeline.ProjectNameFromPath(absPath)

	// Lock to prevent concurrent indexing with auto-sync watcher
	s.indexMu.Lock()
	defer s.indexMu.Unlock()

	// Get per-project store
	st, err := s.router.ForProject(projectName)
	if err != nil {
		return errResult(fmt.Sprintf("store: %v", err)), nil
	}

	// If force=true, delete cached file hashes so classifyFiles treats all files as changed.
	// This ensures post-flush enrichment passes (security tags, communities) run on all files.
	forceReindex := getBoolArg(args, "force")
	if forceReindex {
		if err := st.DeleteFileHashes(projectName); err != nil {
			return errResult(fmt.Sprintf("clearing file hashes: %v", err)), nil
		}
	}

	// Run the indexing pipeline with progress reporting
	p := pipeline.New(ctx, st, absPath, mode)
	p.Progress = func(phase string, pct int, detail string) {
		slog.Info("index.progress", "project", projectName, "phase", phase, "pct", pct, "detail", detail)
	}
	if err := p.Run(); err != nil {
		return errResult(fmt.Sprintf("indexing failed: %v", err)), nil
	}

	// Invalidate query cache — indexed data has changed.
	s.queryCache.Invalidate()

	// Add to watcher so auto-sync keeps this project fresh.
	s.watcher.Watch(projectName, absPath)

	// Refresh ARCHITECTURE_REPORT.md at the repo root so the PreToolUse hook
	// (installed via `codebase-memory-mcp install`) has fresh content to
	// surface next time Glob/Grep fires. Report generation failure must NOT
	// fail the overall index — a stale or missing report is less bad than a
	// failed index, and the user can regenerate manually via generate_report.
	//
	// skip_report=true opts out of the write entirely. Required when indexing
	// read-only repos (bench fixtures, vendored code, protected paths) where
	// any write — even a generated doc — violates policy. Default is false so
	// normal usage is unchanged.
	if getBoolArg(args, "skip_report") {
		slog.Info("index.report.skipped", "project", projectName, "reason", "skip_report=true")
	} else if reportResult, reportErr := s.generateOrientationReport(projectName); reportErr != nil {
		slog.Warn("index.report.err", "project", projectName, "err", reportErr)
	} else {
		slog.Info("index.report.ok",
			"project", projectName,
			"path", reportResult.Path,
			"bytes", reportResult.Bytes)
	}

	// Update session state if this is the session project
	if projectName == s.sessionProject {
		s.indexStatus.Store("ready")
	}

	// Gather stats
	nodeCount, _ := st.CountNodes(projectName)
	edgeCount, _ := st.CountEdges(projectName)

	proj, _ := st.GetProject(projectName)
	indexedAt := store.Now()
	if proj != nil {
		indexedAt = proj.IndexedAt
	}

	// Distinguish first-time index (Created) from re-index (Updated).
	// proj is non-nil only when the project record already existed
	// before this Run() call. forceReindex=true is treated as Updated
	// because the project record persists through file-hash deletion.
	outcome := ActionOutcomeCreated
	if proj != nil {
		outcome = ActionOutcomeUpdated
	}

	result := map[string]any{
		"project":       projectName,
		"mode":          string(mode),
		"force_reindex": forceReindex,
		"nodes":         nodeCount,
		"edges":         edgeCount,
		"indexed_at":    indexedAt,
		"_metadata":     s.stdWriteToolMetadata(outcome),
	}

	// Check for ADR presence and suggest creation if missing
	adr, _ := st.GetADR(projectName)
	result["adr_present"] = adr != nil
	if adr == nil {
		result["adr_hint"] = "Project indexed. Consider creating an Architecture Decision Record: explore the codebase with get_architecture(aspects=['all']), then use manage_adr(mode='store') to persist architectural insights across sessions."
	}

	return jsonResult(result), nil
}
