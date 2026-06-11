package tools

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

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

	// Acquire the per-project store with a ref held for the whole index.
	// ForProject returns an UNPROTECTED handle: the router's evictor closes
	// stores idle > idleTimeout (30s) with refs == 0, and a long index never
	// goes back through the router to refresh lastUsed. 2026-06-11 incident:
	// every index_repository call crossing ~30s wall time had its *sql.DB
	// closed mid-run — the in-flight transaction still committed (data
	// intact on disk), but every post-commit read failed silently, so the
	// response reported nodes=0/edges=0, action_outcome="created", and a
	// fabricated indexed_at. AcquireStore blocks eviction until release.
	st, release, err := s.router.AcquireStore(projectName)
	if err != nil {
		return errResult(fmt.Sprintf("store: %v", err)), nil
	}
	defer release()

	// Snapshot project existence BEFORE the run. GetProject after Run()
	// always finds the record (runPasses upserts it at the start), so a
	// post-Run read can only answer "what is indexed_at", never "did this
	// call create the project" — reading it post-Run made every successful
	// first-time index report action_outcome="updated".
	preProj, _ := st.GetProject(projectName)
	existedBefore := preProj != nil

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
	// any write — even a generated doc — violates policy. Default for a
	// project with NO recorded preference is false so normal usage is
	// unchanged.
	//
	// The choice is STICKY per project (2026-06-11): an explicitly provided
	// skip_report is persisted to the config store, and calls that OMIT the
	// argument inherit the recorded choice instead of silently reverting to
	// report-writing. Before this, one explicit call without the flag — from
	// any session, tool, or future caller — re-created the report in a repo
	// whose owner had opted out on every prior index, and the write was
	// unattributable after the fact. generate_report remains an explicit
	// always-write override.
	_, skipProvided := args["skip_report"]
	skipReport := getBoolArg(args, "skip_report")
	prefKey := "report.skip." + projectName
	if skipProvided {
		if s.config != nil {
			if err := s.config.Set(prefKey, strconv.FormatBool(skipReport)); err != nil {
				slog.Warn("index.report.pref_persist_err", "project", projectName, "err", err)
			}
		}
	} else if s.config != nil {
		skipReport = s.config.GetBool(prefKey, false)
	}
	if skipReport {
		reason := "skip_report=true"
		if !skipProvided {
			reason = "persisted_preference"
		}
		slog.Info("index.report.skipped", "project", projectName, "reason", reason)
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

	// Gather stats from the pipeline's LastNodeCount/LastEdgeCount fields,
	// populated at the end of Run(). The 2026-05-26 zero-counts incident
	// (code-search fast-mode wrote 5,640 nodes, response reported 0/0) was
	// originally attributed to post-bulk-write read visibility and "fixed"
	// by moving the count read into the pipeline — but the pipeline read
	// the counts through the same store handle, so nothing changed. The
	// actual cause (confirmed 2026-06-11) was the router's evictor closing
	// the store's *sql.DB mid-index once the run crossed idleTimeout; the
	// AcquireStore ref above is the real fix. The pipeline fields remain
	// the source of truth here to keep the count adjacent to the commit.
	nodeCount := p.LastNodeCount
	edgeCount := p.LastEdgeCount

	proj, projErr := st.GetProject(projectName)
	indexedAt := store.Now()
	if projErr != nil {
		// With the ref held this should be unreachable; if it fires anyway,
		// surface it instead of silently fabricating indexed_at.
		slog.Warn("index.get_project.err", "project", projectName, "err", projErr)
	} else if proj != nil {
		indexedAt = proj.IndexedAt
	}

	// Distinguish first-time index (Created) from re-index (Updated) using
	// the pre-Run snapshot. forceReindex=true is treated as Updated because
	// the project record persists through file-hash deletion.
	outcome := ActionOutcomeUpdated
	if !existedBefore {
		outcome = ActionOutcomeCreated
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
