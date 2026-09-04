package tools

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"github.com/brandyn-s/code-graph/internal/discover"
	"github.com/brandyn-s/code-graph/internal/indexidentity"
	"github.com/brandyn-s/code-graph/internal/pipeline"
	"github.com/brandyn-s/code-graph/internal/store"
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
	precisionSelection, err := s.resolveGraphPrecision(args, projectName, absPath)
	if err != nil {
		return errResult(err.Error()), nil
	}

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
	terminalIdentityGuardArmed := false
	terminalIdentityCode := "indexing_aborted"
	var terminalIdentityCause error
	if existedBefore {
		if err := st.SetIndexIdentityState(
			projectName,
			indexidentity.StatusPending,
			"indexing is in progress; checkout identity has not been recaptured",
		); err != nil {
			if projectName == s.sessionProject {
				s.indexStatus.Store("degraded")
			}
			return errResult(fmt.Sprintf("invalidate previous index identity: %v", err)), nil
		}
		terminalIdentityGuardArmed = true
	}
	defer func() {
		if !terminalIdentityGuardArmed {
			return
		}
		if terminalIdentityCause == nil {
			terminalIdentityCause = fmt.Errorf(
				"index_repository exited before persisting a terminal checkout identity state",
			)
		}
		if stateErr := persistTerminalIndexIdentityError(
			st,
			projectName,
			terminalIdentityCode,
			terminalIdentityCause,
		); stateErr != nil {
			slog.Error(
				"index.identity.terminal_state_err",
				"project", projectName,
				"cause", terminalIdentityCause,
				"state_err", stateErr,
			)
		}
		if projectName == s.sessionProject {
			s.indexStatus.Store("degraded")
		}
	}()
	captureIdentity := s.indexIdentityCapture()
	startIdentity, startIdentityErr := captureIdentity(absPath)
	if startIdentityErr != nil {
		slog.Error("index.identity.start_capture_err", "project", projectName, "err", startIdentityErr)
	}

	// If force=true, delete cached file hashes so classifyFiles treats all files as changed.
	// This ensures post-flush enrichment passes (security tags, communities) run on all files.
	forceReindex := getBoolArg(args, "force")
	if forceReindex {
		if err := st.DeleteFileHashes(projectName); err != nil {
			terminalIdentityCode = "clearing_file_hashes_failed"
			terminalIdentityCause = err
			return errResult(fmt.Sprintf("clearing file hashes: %v", err)), nil
		}
	}

	// Run the indexing pipeline with progress reporting
	p := pipeline.New(ctx, st, absPath, mode)
	configurePipelinePrecision(p, precisionSelection)
	p.Progress = func(phase string, pct int, detail string) {
		slog.Info("index.progress", "project", projectName, "phase", phase, "pct", pct, "detail", detail)
	}
	if err := p.Run(); err != nil {
		terminalIdentityGuardArmed = true
		terminalIdentityCode = "indexing_failed"
		terminalIdentityCause = err
		if stateErr := persistTerminalIndexIdentityError(
			st,
			projectName,
			"indexing_failed",
			err,
		); stateErr != nil {
			slog.Error(
				"index.identity.terminal_state_err",
				"project", projectName,
				"index_err", err,
				"state_err", stateErr,
			)
		} else {
			terminalIdentityGuardArmed = false
		}
		if projectName == s.sessionProject {
			s.indexStatus.Store("degraded")
		}
		return errResult(fmt.Sprintf("indexing failed: %v", err)), nil
	}
	s.persistGraphPrecision(projectName, precisionSelection, &p.SCIPStatus)
	terminalIdentityGuardArmed = true
	if err := st.SetIndexIdentityState(
		projectName,
		indexidentity.StatusPending,
		"graph indexing completed but checkout identity has not been captured",
	); err != nil {
		terminalIdentityCode = "mark_identity_pending_failed"
		terminalIdentityCause = err
		return errResult(fmt.Sprintf("mark index identity pending: %v", err)), nil
	}

	// Invalidate query cache — indexed data has changed.
	s.queryCache.Invalidate()

	// Add to watcher so auto-sync keeps this project fresh.
	s.watcher.Watch(projectName, absPath)

	// Orientation reports are opt-in and, by default, written under the cache
	// directory (<cache>/reports/<project>/) rather than into the checkout.
	// A default index_repository run therefore leaves `git status` exactly as
	// it found it, and the identity captured below describes the checkout
	// the graph was actually built from. Callers ask for a report with
	// write_report=true (preferred) or the legacy skip_report=false; the
	// choice is sticky per project (2026-06-11) so an omitted argument never
	// silently flips the behaviour recorded for a repository. report_path
	// may point inside the checkout; that write happens only after the end
	// identity capture and is the caller's explicit, attributable choice.
	wantReport, reportDecisionReason := s.resolveReportPreference(args, projectName)
	reportPath := getStringArg(args, "report_path")

	// Capture the end identity before any optional report write. Persist an
	// envelope only when start and end generations match, so the graph is
	// never labeled with a checkout state it did not coherently scan.
	endIdentity, endIdentityErr := captureIdentity(absPath)
	identityErr := persistCoherentIndexIdentity(
		st,
		projectName,
		startIdentity,
		startIdentityErr,
		endIdentity,
		endIdentityErr,
	)
	if identityErr != nil {
		slog.Error("index.identity.incoherent", "project", projectName, "err", identityErr)
	}

	// Optional orientation report. Report failure must NOT fail the index; a
	// missing report is less bad than a failed index and generate_report can
	// produce one later. Written after the identity capture so the graph's
	// identity is never attributed to a file this tool created.
	if !wantReport {
		slog.Info("index.report.skipped", "project", projectName, "reason", reportDecisionReason)
	} else if reportResult, reportErr := s.generateOrientationReport(projectName, reportPath); reportErr != nil {
		slog.Warn("index.report.err", "project", projectName, "err", reportErr)
	} else {
		slog.Info("index.report.ok",
			"project", projectName,
			"path", reportResult.Path,
			"bytes", reportResult.Bytes,
			"in_checkout", writesIntoCheckout(absPath, reportResult.Path))
	}

	identityRecord, identityRecordErr := st.GetIndexIdentity(projectName)
	switch {
	case identityRecordErr != nil:
		terminalIdentityCode = "identity_finalization_failed"
		terminalIdentityCause = identityRecordErr
	case identityRecord == nil:
		terminalIdentityCode = "identity_finalization_failed"
		terminalIdentityCause = fmt.Errorf("identity finalization returned a nil record")
	case identityRecord.Status == indexidentity.StatusPending:
		terminalIdentityCode = "identity_finalization_failed"
		terminalIdentityCause = fmt.Errorf("identity remained pending after finalization")
	default:
		terminalIdentityGuardArmed = false
	}

	// Update session state if this is the session project.
	if projectName == s.sessionProject {
		if identityRecordErr != nil ||
			identityRecord == nil ||
			identityRecord.Status != indexidentity.StatusCaptured {
			s.indexStatus.Store("degraded")
		} else {
			s.indexStatus.Store("ready")
		}
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
		"index_delta": map[string]any{
			"mode":             p.LastIndexDelta.Mode,
			"files_discovered": p.LastIndexDelta.FilesDiscovered,
			"files_changed":    p.LastIndexDelta.FilesChanged,
			"files_deleted":    p.LastIndexDelta.FilesDeleted,
			"files_unchanged":  p.LastIndexDelta.FilesUnchanged,
		},
	}
	result["graph_precision"] = graphPrecisionResult(precisionSelection, &p.SCIPStatus)
	if precisionSelection.Tier == graphPrecisionSCIP && p.SCIPStatus.State != "applied" {
		result["status"] = "degraded"
		result["precision_reason"] = "SCIP precision was requested but not applied; graph results are heuristic"
	}
	embeddingCount, embeddingCountErr := st.EmbeddingCount(projectName)
	embeddingModels, embeddingModelsErr := st.EmbeddingModelCounts(projectName)
	if embeddingCountErr != nil || embeddingModelsErr != nil {
		result["embedding_status"] = "error"
		result["embedding_count"] = 0
		result["embedding_models"] = map[string]int{}
		result["embedding_reason"] = fmt.Sprintf(
			"embedding inventory unavailable: count=%v models=%v",
			embeddingCountErr,
			embeddingModelsErr,
		)
	} else {
		result["embedding_status"] = "captured"
		result["embedding_count"] = embeddingCount
		result["embedding_models"] = embeddingModels
		result["embedding_reason"] = ""
	}
	if coherent := addIndexIdentity(result, st, projectName); !coherent {
		result["status"] = "degraded"
		if identityErr != nil && result["identity_reason"] == "" {
			result["identity_reason"] = fmt.Sprintf("checkout identity unavailable: %v; re-run index_repository", identityErr)
		}
	}

	// Check for ADR presence and suggest creation if missing
	adr, _ := st.GetADR(projectName)
	result["adr_present"] = adr != nil
	if adr == nil {
		result["adr_hint"] = "Project indexed. Consider creating an Architecture Decision Record: explore the codebase with get_architecture(aspects=['all']), then use manage_adr(mode='store') to persist architectural insights across sessions."
	}

	return jsonResult(result), nil
}

// resolveReportPreference decides whether index_repository writes an
// orientation report and returns the reason for the decision.
//
// Precedence: explicit write_report, then explicit (legacy) skip_report,
// then the per-project preference persisted from an earlier explicit call,
// then the default of no report. Explicit choices are persisted so a later
// call that omits both arguments inherits them instead of reverting.
func (s *Server) resolveReportPreference(args map[string]any, projectName string) (wantReport bool, reason string) {
	prefKey := store.ConfigReportSkipPrefix + projectName
	persist := func(want bool) {
		if s.config == nil {
			return
		}
		if err := s.config.Set(prefKey, strconv.FormatBool(!want)); err != nil {
			slog.Warn("index.report.pref_persist_err", "project", projectName, "err", err)
		}
	}
	if _, ok := args["write_report"]; ok {
		want := getBoolArg(args, "write_report")
		persist(want)
		return want, "write_report=" + strconv.FormatBool(want)
	}
	if _, ok := args["skip_report"]; ok {
		want := !getBoolArg(args, "skip_report")
		persist(want)
		return want, "skip_report=" + strconv.FormatBool(!want)
	}
	if s.config != nil {
		if raw := s.config.Get(prefKey, ""); raw != "" {
			want := !s.config.GetBool(prefKey, true)
			return want, "persisted_preference"
		}
	}
	return false, "default"
}
