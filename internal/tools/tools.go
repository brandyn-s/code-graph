package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DeusData/codebase-memory-mcp/internal/discover"
	"github.com/DeusData/codebase-memory-mcp/internal/pipeline"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
	"github.com/DeusData/codebase-memory-mcp/internal/watcher"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Version is the current release version, set from main.version via SetVersion().
// Defaults to "dev" for local builds.
var Version = "dev"

// SetVersion sets the package version from the build-injected main.version.
func SetVersion(v string) { Version = v }

// releaseURL is the GitHub API endpoint for latest release. Package-level var for test injection.
var releaseURL = "https://api.github.com/repos/DeusData/codebase-memory-mcp/releases/latest"

// Server wraps the MCP server with tool handlers.
type Server struct {
	mcp        *mcp.Server
	router     *store.StoreRouter
	config     *store.ConfigStore
	watcher    *watcher.Watcher
	queryCache *store.QueryCache
	ctx        context.Context // server lifetime context — cancelled on shutdown
	indexMu    sync.Mutex
	handlers   map[string]mcp.ToolHandler

	// Session-aware fields (set once via sync.Once, then immutable)
	sessionOnce    sync.Once
	sessionRoot    string // absolute path from client
	sessionProject string // derived from sessionRoot via ProjectNameFromPath
	indexStatus    atomic.Value
	indexStartedAt atomic.Value // time.Time — when current/last index started
	updateNotice   atomic.Value // string — set once by checkForUpdate, cleared after first injection
	updateOnce     sync.Once    // ensures checkForUpdate runs at most once per session
}

// ServerOption configures a Server.
type ServerOption func(*Server)

// WithConfig attaches a ConfigStore for reading runtime settings.
func WithConfig(c *store.ConfigStore) ServerOption {
	return func(s *Server) { s.config = c }
}

// NewServer creates a new MCP server with all tools registered.
func NewServer(r *store.StoreRouter, opts ...ServerOption) *Server {
	srv := &Server{
		router:     r,
		queryCache: store.NewQueryCache(200, 5*time.Minute),
		handlers:   make(map[string]mcp.ToolHandler),
	}
	for _, opt := range opts {
		opt(srv)
	}

	srv.mcp = mcp.NewServer(
		&mcp.Implementation{
			Name:    "codebase-memory-mcp",
			Version: Version,
		},
		&mcp.ServerOptions{
			InitializedHandler:      srv.onInitialized,
			RootsListChangedHandler: srv.onRootsChanged,
		},
	)

	srv.registerTools()
	srv.watcher = watcher.New(r, srv.syncProject)
	return srv
}

// StartWatcher launches the background file-change polling goroutine.
// It stores ctx for use by startAutoIndex and stops when ctx is cancelled.
func (s *Server) StartWatcher(ctx context.Context) {
	s.ctx = ctx
	go s.watcher.Run(ctx)
}

// syncProject is called by the watcher when file changes are detected.
// Uses TryLock to skip if an index operation is already in progress.
func (s *Server) syncProject(ctx context.Context, projectName, rootPath string) error {
	if !s.indexMu.TryLock() {
		slog.Debug("watcher.skip", "path", rootPath, "reason", "index_in_progress")
		return nil
	}
	defer s.indexMu.Unlock()
	st, err := s.router.ForProject(projectName)
	if err != nil {
		return fmt.Errorf("store for %s: %w", projectName, err)
	}
	p := pipeline.New(ctx, st, rootPath, discover.ModeFull)
	if err := p.Run(); err != nil {
		return err
	}
	s.queryCache.Invalidate()
	return nil
}

// MCPServer returns the underlying MCP server.
func (s *Server) MCPServer() *mcp.Server {
	return s.mcp
}

// Router returns the underlying StoreRouter for direct access (e.g. CLI mode).
func (s *Server) Router() *store.StoreRouter {
	return s.router
}

// SessionProject returns the auto-detected session project name (may be empty).
func (s *Server) SessionProject() string {
	return s.sessionProject
}

// SetSessionRoot sets the session root path directly (for CLI mode).
func (s *Server) SetSessionRoot(rootPath string) {
	go s.updateOnce.Do(s.checkForUpdate)
	s.sessionOnce.Do(func() {
		s.sessionRoot = rootPath
		if rootPath != "" {
			s.sessionProject = pipeline.ProjectNameFromPath(rootPath)
		}
	})
}

// --- Session detection ---

// onInitialized is called when the client sends notifications/initialized.
func (s *Server) onInitialized(ctx context.Context, req *mcp.InitializedRequest) {
	go s.updateOnce.Do(s.checkForUpdate)
	s.sessionOnce.Do(func() {
		s.sessionRoot = s.detectSessionRoot(ctx, req.Session)
		if s.sessionRoot != "" {
			s.sessionProject = pipeline.ProjectNameFromPath(s.sessionRoot)
			s.startAutoIndex()
		}
	})
}

// onRootsChanged re-detects session root if not yet set.
func (s *Server) onRootsChanged(ctx context.Context, req *mcp.RootsListChangedRequest) {
	go s.updateOnce.Do(s.checkForUpdate)
	s.sessionOnce.Do(func() {
		s.sessionRoot = s.detectSessionRoot(ctx, req.Session)
		if s.sessionRoot != "" {
			s.sessionProject = pipeline.ProjectNameFromPath(s.sessionRoot)
			s.startAutoIndex()
		}
	})
}

// detectSessionRoot tries multiple fallback strategies to find the project root.
func (s *Server) detectSessionRoot(ctx context.Context, session *mcp.ServerSession) string {
	// 1. Try MCP roots protocol
	if session != nil {
		result, err := session.ListRoots(ctx, nil)
		if err == nil && len(result.Roots) > 0 {
			uri := result.Roots[0].URI
			if path, ok := parseFileURI(uri); ok {
				slog.Info("session.root.from_roots", "path", path)
				return path
			}
		}
	}

	// 2. Fall back to process working directory
	if cwd, err := os.Getwd(); err == nil && cwd != "/" && cwd != os.Getenv("HOME") {
		slog.Info("session.root.from_cwd", "path", cwd)
		return cwd
	}

	// 3. Fall back to single indexed project from DB
	projects, err := s.router.ListProjects()
	if err == nil && len(projects) == 1 && projects[0].RootPath != "" {
		slog.Info("session.root.from_db", "path", projects[0].RootPath)
		return projects[0].RootPath
	}

	slog.Info("session.root.none", "reason", "no_roots_no_cwd_no_single_project")
	return ""
}

// parseFileURI extracts a filesystem path from a file:// URI.
func parseFileURI(uri string) (string, bool) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return "", false
	}
	path := u.Path
	// On Windows, file:///C:/path parses to /C:/path — strip leading / before drive letter
	if len(path) >= 3 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}
	return filepath.FromSlash(path), true
}

// defaultAutoIndexLimit is the maximum number of source files that auto-index
// will process for a never-before-indexed project. Override with config key
// "auto_index_limit". Projects above this limit require explicit index_repository.
const defaultAutoIndexLimit = 50000

// startAutoIndex triggers background indexing for the session project.
// Respects config: auto_index must be true (default: false).
// For never-indexed projects: only auto-indexes if file count <= auto_index_limit.
// For previously-indexed projects: always re-indexes (incremental, fast).
func (s *Server) startAutoIndex() {
	hasDB := s.router.HasProject(s.sessionProject)

	// Auto-index for new projects requires explicit opt-in via config.
	// Previously-indexed projects always get incremental re-index (cheap).
	if !hasDB {
		autoIndex := false
		fileLimit := defaultAutoIndexLimit
		if s.config != nil {
			autoIndex = s.config.GetBool(store.ConfigAutoIndex, false)
			fileLimit = s.config.GetInt(store.ConfigAutoIndexLimit, defaultAutoIndexLimit)
		}

		if !autoIndex {
			slog.Info("autoindex.skip",
				"reason", "auto_index_disabled",
				"hint", "run: codebase-memory-mcp config set auto_index true",
			)
			return
		}

		// Check file count before committing to index.
		// Prevents OOM when the server starts in a large monorepo.
		files, err := discover.Discover(s.ctx, s.sessionRoot, nil)
		if err != nil {
			slog.Warn("autoindex.discover.err", "err", err)
			return
		}
		if len(files) > fileLimit {
			slog.Warn("autoindex.skip",
				"reason", "too_many_files",
				"files", len(files),
				"limit", fileLimit,
				"hint", "call index_repository explicitly or increase auto_index_limit",
			)
			return
		}
		s.indexStatus.Store("indexing")
	} else {
		s.indexStatus.Store("ready")
	}

	go func() {
		if !s.indexMu.TryLock() {
			slog.Debug("autoindex.skip", "reason", "index_in_progress")
			return
		}
		defer s.indexMu.Unlock()

		s.indexStartedAt.Store(time.Now())
		if !hasDB {
			s.indexStatus.Store("indexing")
		}

		st, err := s.router.ForProject(s.sessionProject)
		if err != nil {
			slog.Warn("autoindex.store.err", "err", err)
			return
		}
		p := pipeline.New(s.ctx, st, s.sessionRoot, discover.ModeFull)
		if err := p.Run(); err != nil {
			slog.Warn("autoindex.err", "err", err)
			return
		}
		s.indexStatus.Store("ready")
		s.watcher.Watch(s.sessionProject, s.sessionRoot)
		slog.Info("autoindex.done", "project", s.sessionProject)
	}()
}

// --- Store routing ---

// resolveStore returns the Store for the given project, or the session project if empty.
func (s *Server) resolveStore(project string) (*store.Store, error) {
	if project == "*" || project == "all" {
		return nil, fmt.Errorf("cross-project queries are not supported; use list_projects to find a specific project name, or omit the project parameter to use the current session project")
	}
	if project == "" {
		project = s.sessionProject
	}
	if project == "" {
		return nil, fmt.Errorf("no project specified and no session project detected; pass project parameter")
	}
	if !s.router.HasProject(project) {
		return nil, fmt.Errorf("project %q not found; use list_projects to see available projects", project)
	}
	// Touch watcher so cross-project queries keep that project fresh.
	if project != s.sessionProject {
		s.watcher.TouchProject(project)
	}
	return s.router.ForProject(project)
}

// resolveProjectName returns the effective project name for routing.
//
// Resolution order:
//  1. Empty input → session project (existing behavior).
//  2. Exact match against registered canonical (path-mangled) name → return as-is.
//  3. Friendly-name match: input equals filepath.Base of a registered project's
//     RootPath (e.g. "claude-config" → "c-Users-user-.claude"). Returns
//     the canonical form when exactly one project matches.
//  4. Otherwise, return input unchanged so existing error paths surface.
func (s *Server) resolveProjectName(project string) string {
	if project == "" {
		return s.sessionProject
	}
	if s.router == nil || s.router.HasProject(project) {
		return project
	}
	infos, err := s.router.ListProjects()
	if err != nil {
		return project
	}
	matches := []string{}
	for _, info := range infos {
		if strings.HasPrefix(info.Name, "_") {
			continue
		}
		st, err := s.router.ForProject(info.Name)
		if err != nil {
			continue
		}
		proj, _ := st.GetProject(info.Name)
		if proj == nil {
			continue
		}
		if filepath.Base(proj.RootPath) == project {
			matches = append(matches, info.Name)
		}
	}
	if len(matches) == 1 {
		return matches[0]
	}
	return project
}

// addIndexStatus adds the index_status field to response data if indexing is in progress.
func (s *Server) addIndexStatus(data map[string]any) {
	status, _ := s.indexStatus.Load().(string)
	if status == "indexing" {
		data["index_status"] = "indexing"
	}
}

// addUpdateNotice prepends an update notice to the first tool response, then clears itself.
func (s *Server) addUpdateNotice(result *mcp.CallToolResult) {
	if notice, ok := s.updateNotice.Load().(string); ok && notice != "" {
		result.Content = append([]mcp.Content{&mcp.TextContent{Text: notice}}, result.Content...)
		s.updateNotice.Store("")
	}
}

// checkForUpdate fetches the latest GitHub release and stores a notice if newer.
func (s *Server) checkForUpdate() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", releaseURL, http.NoBody)
	if err != nil {
		slog.Warn("update check: request create failed", "err", err)
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("update check: http failed", "err", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		slog.Warn("update check: bad status", "status", resp.StatusCode)
		return
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		slog.Warn("update check: body read failed", "err", err)
		return
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &release); err != nil {
		slog.Warn("update check: json parse failed", "err", err)
		return
	}

	latest := strings.TrimPrefix(release.TagName, "v")
	if latest == "" || latest == Version {
		slog.Debug("update check: current", "version", Version, "latest", latest)
		return
	}
	if compareVersions(latest, Version) > 0 {
		notice := fmt.Sprintf(
			"⚡ Update available: v%s → v%s — run: codebase-memory-mcp update",
			Version, latest)
		s.updateNotice.Store(notice)
		slog.Info("update available", "current", Version, "latest", latest)
	}
}

// compareVersions compares two semver strings (e.g. "0.2.1" vs "0.2.0").
// Returns >0 if a > b, <0 if a < b, 0 if equal.
func compareVersions(a, b string) int {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")
	for i := 0; i < len(aParts) && i < len(bParts); i++ {
		ai, _ := strconv.Atoi(aParts[i])
		bi, _ := strconv.Atoi(bParts[i])
		if ai != bi {
			return ai - bi
		}
	}
	return len(aParts) - len(bParts)
}

// --- Tool registration ---

func (s *Server) addTool(tool *mcp.Tool, handler mcp.ToolHandler) {
	s.mcp.AddTool(tool, handler)
	s.handlers[tool.Name] = handler
}

// CallTool invokes a tool handler directly by name, bypassing MCP transport.
func (s *Server) CallTool(ctx context.Context, name string, argsJSON json.RawMessage) (*mcp.CallToolResult, error) {
	handler, ok := s.handlers[name]
	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
	if len(argsJSON) == 0 {
		argsJSON = json.RawMessage(`{}`)
	}
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      name,
			Arguments: argsJSON,
		},
	}
	return handler(ctx, req)
}

// ToolNames returns all registered tool names in sorted order.
func (s *Server) ToolNames() []string {
	names := make([]string, 0, len(s.handlers))
	for name := range s.handlers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (s *Server) registerTools() {
	s.registerGraphTools()
	s.registerProjectTools()
	s.registerTraceTools()
	s.registerDetectChanges()
	s.registerArchitectureTools()
	s.registerSecurityTools()
	s.registerDataFlowTool()
	s.registerSTIGEvidenceTool()
	s.registerHealthTool()
	s.registerReviewContextTool()
	s.registerVisualizeTool()
	s.registerAffectedTestsTool()
	s.registerCyclesTool()
	s.registerExplainTool()
	s.registerChangeCouplingTool()
	s.registerExplainServiceTool()
	s.registerServiceMapTool()
	s.registerDiffServicesTool()
	s.registerRelevantContextTool()
	s.registerGenerateReportTool()
	s.registerFindSimilarFunctionsTool()
	s.registerFindRationaleTool()
	s.registerRankByQueryTool()
	s.registerCodeLocalizeTool()
	s.registerCodeLocalizeAgentTool()
	s.registerDiffGraphTool()
}

// registerFindRationaleTool surfaces the Rationale nodes produced by
// passRationale. Primary use: compliance audits ("list every SAFETY:
// justification across the Rust code") and code-review context.
func (s *Server) registerFindRationaleTool() {
	s.addTool(&mcp.Tool{
		Name: "find_rationale",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},
		Description: "Return WHY/NOTE/HACK/SAFETY/TODO/FIXME/IMPORTANT/XXX comment annotations extracted from source, with their enclosing Function/Method/Class subject and file:line location. Filter by kind to audit a specific marker category. Useful for compliance passes (every unsafe justification, every documented HACK) and for surfacing design rationale in code review.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"project": {
					"type": "string",
					"description": "Project name (optional — uses session project if omitted)"
				},
				"kind": {
					"type": "string",
					"description": "Filter by marker kind. One of WHY, NOTE, HACK, SAFETY, TODO, FIXME, IMPORTANT, XXX. Omit to return all kinds."
				},
				"limit": {
					"type": "integer",
					"description": "Max rationale entries to return (1-500, default 50)"
				}
			}
		}`),
	}, s.handleFindRationale)
}

// registerDiffGraphTool surfaces diff_graph — symbol-level delta
// between two arbitrary git revisions, complementing detect_changes
// (which is scoped to uncommitted / staged / branch-vs-branch flows).
func (s *Server) registerDiffGraphTool() {
	s.addTool(&mcp.Tool{
		Name: "diff_graph",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},
		Description: "Given two git revisions, list which indexed symbols (Function/Method/Class/Struct/Interface/Trait/Enum) live in the files touched between them. Complements detect_changes (uncommitted / staged / branch) by accepting arbitrary SHAs — useful for 'what did we ship between v1.2.0 and v1.3.0?' review. Current index only: symbols deleted after to_sha cannot be surfaced.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"project": {
					"type": "string",
					"description": "Project name (optional — uses session project if omitted)"
				},
				"from_sha": {
					"type": "string",
					"description": "Starting revision: commit SHA (short or full), branch name, or HEAD~N"
				},
				"to_sha": {
					"type": "string",
					"description": "Ending revision: commit SHA (short or full), branch name, or HEAD"
				}
			},
			"required": ["from_sha", "to_sha"]
		}`),
	}, s.handleDiffGraph)
}

// registerFindSimilarFunctionsTool adds find_similar_functions — cosine
// top-K over Voyage embeddings, primary use case: "is this function's
// logic duplicated elsewhere?"
func (s *Server) registerFindSimilarFunctionsTool() {
	s.addTool(&mcp.Tool{
		Name: "find_similar_functions",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},
		Description: "Return the top-K functions/methods most cosine-similar to a given function's Voyage embedding. Useful for finding refactor candidates (two functions solving the same problem without a shared call path) and duplicated patterns. Requires embeddings to be populated — run index_repository with VOYAGE_API_KEY set first.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {
					"type": "string",
					"description": "Function name (exact Name match) or fully-qualified name. Ambiguous names produce a picker error listing candidates."
				},
				"project": {
					"type": "string",
					"description": "Project name (optional — uses session project if omitted)"
				},
				"limit": {
					"type": "integer",
					"description": "Max number of matches to return (1-50, default 10)"
				},
				"threshold": {
					"type": "number",
					"description": "Minimum cosine similarity score (0.0-1.0) — common values: 0.85 for \"worth investigating\", 0.92 for \"probable copy-paste\". Default 0.0 (return top-K regardless of score)."
				}
			},
			"required": ["name"]
		}`),
	}, s.handleFindSimilarFunctions)
}

// registerRankByQueryTool adds rank_by_query — bidirectional weighted
// PageRank with personalization on query-matched seed nodes. Primary use
// case: agent context selection — "give me the top-20 most relevant
// entities for this issue/question" — typically reducing context tokens
// by 3-5x vs dumping the full graph. Reference: Aider repo-map pattern
// (https://aider.chat/2023/10/22/repomap.html).
func (s *Server) registerRankByQueryTool() {
	s.addTool(&mcp.Tool{
		Name: "rank_by_query",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},
		Description: "Rank graph nodes by relevance to a query using bidirectional weighted PageRank. Best with specific symbol queries (function/class names): substring seeds match exactly, embedding seeds catch semantic neighbors. WORKS POORLY on long natural-language descriptions where surviving tokens are noise words (e.g., 'the install command runs lazily' tokenizes to 'install/command/runs/lazily', each matching dozens of unrelated symbols). For verbose issue descriptions, use code_localize_agent instead — the LLM-driven variant reasons about call paths rather than substring-matching. Algorithm: tokenize, seed via seed_strategy, run forward+reverse PageRank, return top-K by combined score. Bidirectional avoids pure-source collapse. Typical 3-5x token reduction vs dumping the full graph for context selection.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"description": "Natural-language query or symbol list. Tokens shorter than 2 chars and English stopwords (the/of/how/etc.) are dropped. At least one token must match a node Name or QualifiedName substring."
				},
				"project": {
					"type": "string",
					"description": "Project name (optional — uses session project if omitted)"
				},
				"top_k": {
					"type": "integer",
					"description": "Maximum number of ranked nodes to return (1-200, default 20). Higher values give wider context at cost of more tokens."
				},
				"seed_strategy": {
					"type": "string",
					"enum": ["substring", "embedding", "hybrid"],
					"description": "How to match query → seed nodes. 'substring' (legacy): tokens substring-match Name/QualifiedName. 'embedding': Voyage-embed the query, cosine-search node embeddings (requires VOYAGE_API_KEY + index with embeddings populated). 'hybrid' (default): both, deduplicated; falls back to substring if embeddings unavailable."
				}
			},
			"required": ["query"]
		}`),
	}, s.handleRankByQuery)
}

// registerCodeLocalizeTool adds code_localize — the LocAgent-style
// graph-guided code localization primitive. Given a natural-language
// issue or question, returns the top-K code entities (Functions,
// Methods, Classes) most relevant to investigate, computed via
// bidirectional BFS from query-matched seeds over CALLS / DEFINES /
// IMPORTS / CONTAINS / MEMBER_OF / IMPLEMENTS edges. Reference:
// LocAgent, ACL 2025, arXiv 2503.09089 — published 92.7% file-level
// localization accuracy on Loc-Bench with the LLM-in-the-loop variant;
// our primitives-only variant trades some accuracy for determinism.
func (s *Server) registerCodeLocalizeTool() {
	s.addTool(&mcp.Tool{
		Name: "code_localize",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},
		Description: "Graph-guided code localization (primitives-only LocAgent variant). Best with focused queries that name specific symbols or short error messages. WORKS POORLY on verbose multi-paragraph issue descriptions — tokens after stopword filter become noise words that substring-match thousands of unrelated symbols, and BFS amplifies the noise. For verbose Loc-Bench-style issues, use code_localize_agent — the LLM-driven variant bridges the 'issue talks about A, fix happens in B' gap that pure retrieval misses. Algorithm: match seeds via seed_strategy, BFS-expand up to `depth` hops over CALLS/DEFINES/IMPORTS/CONTAINS/MEMBER_OF/IMPLEMENTS/OVERRIDE/USES_TYPE bidirectionally, score each visited node by seed-score / 2^distance, return top-K with file:line. Reference: LocAgent (ACL 2025, arXiv 2503.09089) — published 92.7% file-level accuracy uses the LLM-in-the-loop variant; this primitives-only path trades accuracy for determinism and zero LLM cost.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"issue_description": {
					"type": "string",
					"description": "The issue/question/symbol-list to localize. Tokenized (drops 1-char tokens + English stopwords); at least one token must match a node Name or QualifiedName substring."
				},
				"project": {
					"type": "string",
					"description": "Project name (optional — uses session project if omitted)"
				},
				"depth": {
					"type": "integer",
					"description": "BFS expansion radius from each seed (0-5, default 3). Higher reaches more code but adds noise."
				},
				"top_k": {
					"type": "integer",
					"description": "Maximum number of localized entities to return (1-50, default 10)."
				},
				"seed_strategy": {
					"type": "string",
					"enum": ["substring", "embedding", "hybrid"],
					"description": "How to match issue → seed nodes. 'substring' (legacy): tokens substring-match Name/QualifiedName. 'embedding': Voyage-embed the issue, cosine-search node embeddings (requires VOYAGE_API_KEY + index with embeddings populated). 'hybrid' (default): both, deduplicated; falls back to substring if embeddings unavailable."
				}
			},
			"required": ["issue_description"]
		}`),
	}, s.handleCodeLocalize)
}

// registerCodeLocalizeAgentTool adds code_localize_agent — the LLM-driven
// LocAgent variant. Wraps our graph primitives in an iterative agent loop
// (rank_by_query → code_localize → narrow → finalize). Slower and more
// expensive per query than code_localize, but designed to match
// LocAgent's published 92.7% file-level localization on Loc-Bench by
// adding the intelligent narrowing layer that primitives-only cannot do.
//
// Requires ANTHROPIC_API_KEY. Falls back to errResult if missing.
func (s *Server) registerCodeLocalizeAgentTool() {
	s.addTool(&mcp.Tool{
		Name: "code_localize_agent",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  false, // LLM is non-deterministic
			OpenWorldHint:   boolPtr(true),
			DestructiveHint: boolPtr(false),
		},
		Description: "LLM-driven code localization (LocAgent ACL 2025 architecture). Use this for VERBOSE natural-language issue descriptions — Loc-Bench security writeups, multi-paragraph bug reports, anything where the issue text talks about A but the fix is in B. The LLM iteratively calls rank_by_query / code_localize / finalize, reasons about call paths and entry points, and returns a ranked list of entities to investigate. Demonstrably bridges the gap that primitives miss (n=1: pip Loc-Bench instance pypa__pip-13085 — primitives missed top-20, agent landed ground truth at #3). Cost: ~30-60s wall, ~$0.04-0.05 per query at Haiku 4.5 (~50K input tokens, 6 turns typical). Requires ANTHROPIC_API_KEY. For specific symbol queries (function names, exact identifiers), use code_localize instead — primitives are ~1000x faster with zero LLM cost.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"issue_description": {
					"type": "string",
					"description": "Natural-language issue/question to localize."
				},
				"project": {
					"type": "string",
					"description": "Project name (optional — uses session project if omitted)"
				},
				"top_k": {
					"type": "integer",
					"description": "Maximum number of entities to return (1-50, default 10)."
				},
				"include_transcript": {
					"type": "boolean",
					"description": "If true, include the agent's per-turn transcript (tool calls + summaries) for debugging. Default false."
				}
			},
			"required": ["issue_description"]
		}`),
	}, s.handleCodeLocalizeAgent)
}

// registerGenerateReportTool adds the generate_report MCP tool — writes
// ARCHITECTURE_REPORT.md to the repo root for always-on orientation via
// the PreToolUse hook installed by `codebase-memory-mcp install`.
func (s *Server) registerGenerateReportTool() {
	s.addTool(&mcp.Tool{
		Name: "generate_report",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false, // writes a file to the repo root
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},
		Description: "Write ARCHITECTURE_REPORT.md to the repo root — a one-page orientation doc (god nodes, communities + cohesion, cross-package boundaries, 5 suggested questions) derived from the indexed graph. Auto-runs at the end of index_repository; call manually to regenerate without reindexing. Intended to be read by coding assistants before Glob/Grep on an unfamiliar codebase.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"project": {
					"type": "string",
					"description": "Project name (optional — uses session project if omitted)"
				}
			}
		}`),
	}, s.handleGenerateReport)
}

func (s *Server) registerArchitectureTools() {
	s.addTool(&mcp.Tool{
		Name: "get_architecture",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},

		Description: "Get codebase architecture overview computed from the code graph. Call with aspects=['all'] for full orientation or select specific aspects. Available aspects: languages, packages (fan-in/out), entry_points, routes (HTTP endpoints), hotspots (most-called), boundaries (cross-package calls), services (cross-service links), layers (heuristic), clusters (Louvain community detection), file_tree, adr (stored Architecture Decision Record). Recommended first call when exploring an unfamiliar codebase.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"aspects": {
					"type": "array",
					"items": {"type": "string", "enum": ["all", "languages", "packages", "entry_points", "routes", "hotspots", "boundaries", "services", "layers", "clusters", "file_tree", "adr"]},
					"description": "Which architecture aspects to return. Default: ['all']. Use specific aspects to reduce output: ['languages', 'packages'] for quick orientation, ['hotspots', 'boundaries'] for dependency analysis, ['clusters'] for community detection across CALLS/HTTP/ASYNC edges."
				},
				"project": {
					"type": "string",
					"description": "Project to analyze. Defaults to session project."
				}
			}
		}`),
	}, s.handleGetArchitecture)

	s.addTool(&mcp.Tool{
		Name: "manage_adr",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			IdempotentHint:  false,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(true),
		},

		Description: "Manage the Architecture Decision Record (ADR) for a project. CRUD operations for a persistent, section-based architectural summary. Modes: get (retrieve, optional include filter), store (create/replace - all 6 sections required), update (patch sections, unmentioned preserved), delete (remove ADR - this is irreversible), auto (compute from indexed graph and store - no content arg needed). Fixed sections: PURPOSE, STACK, ARCHITECTURE, PATTERNS, TRADEOFFS, PHILOSOPHY. Max 8000 chars. Validation: store rejects missing sections; update rejects non-canonical keys. Use include=['STACK','PATTERNS'] with get to reduce token usage.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"mode": {
					"type": "string",
					"enum": ["get", "store", "update", "delete", "auto"],
					"description": "Operation: 'get' retrieves ADR, 'store' creates/replaces (all 6 sections required), 'update' patches sections (canonical keys only), 'delete' removes, 'auto' computes from indexed graph and stores (no content needed)."
				},
				"project": {
					"type": "string",
					"description": "Project name. Defaults to session project."
				},
				"content": {
					"type": "string",
					"description": "Full ADR markdown (required for mode='store'). Must contain all 6 ## SECTION headers: PURPOSE, STACK, ARCHITECTURE, PATTERNS, TRADEOFFS, PHILOSOPHY. Missing sections will be rejected."
				},
				"sections": {
					"type": "object",
					"additionalProperties": {"type": "string"},
					"description": "Section updates (required for mode='update'). Keys must be canonical section names (PURPOSE, STACK, ARCHITECTURE, PATTERNS, TRADEOFFS, PHILOSOPHY). Non-canonical keys are rejected. Values are new content. Unmentioned sections preserved."
				},
				"include": {
					"type": "array",
					"items": {"type": "string", "enum": ["PURPOSE", "STACK", "ARCHITECTURE", "PATTERNS", "TRADEOFFS", "PHILOSOPHY"]},
					"description": "Section filter for mode='get'. Returns only the listed sections instead of the full ADR. Example: ['STACK', 'PATTERNS'] returns ~800 chars instead of ~8000. Omit to get all sections."
				}
			},
			"required": ["mode"]
		}`),
	}, s.handleManageADR)
}

// registerGraphTools registers tools for graph querying, searching, and tracing.
func (s *Server) registerGraphTools() {
	s.registerIndexAndTraceTool()
	s.registerSchemaAndSnippetTools()
	s.registerSearchTools()
	s.registerQueryTool()
}

func (s *Server) registerIndexAndTraceTool() {
	s.addTool(&mcp.Tool{
		Name: "index_repository",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},

		Description: "Index a repository into the code graph. Parses source files with tree-sitter, extracts functions/classes/modules, resolves call relationships (CALLS), HTTP/async cross-service links, and git change coupling (FILE_CHANGES_WITH). Supports incremental reindex via content hashing. Auto-sync keeps the graph fresh after initial indexing. If repo_path is omitted, uses the session project root. Use mode='fast' for large repos (>50K files) - skips generated code, test fixtures, and large files (>512KB) for faster indexing at the cost of coverage. Returns error if repo_path does not exist or contains no parseable source files.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"repo_path": {
					"type": "string",
					"description": "Absolute path to the repository to index. If omitted, uses the auto-detected session project root."
				},
				"mode": {
					"type": "string",
					"enum": ["full", "fast"],
					"description": "Indexing mode. 'full' (default): parse all supported files. 'fast': aggressive filtering — skips generated code, test fixtures, docs, large files (>512KB), and non-source assets for faster indexing of large repos."
				},
				"force": {
					"type": "boolean",
					"description": "Force full re-index, ignoring cached file hashes. Use after deploying new enrichment features to ensure all post-flush passes run. Default: false."
				},
				"skip_report": {
					"type": "boolean",
					"description": "Skip writing ARCHITECTURE_REPORT.md to the repo root after indexing. Required when indexing read-only repos (bench fixtures, vendored code, protected paths) where any write violates policy. Default: false (report is written)."
				}
			}
		}`),
	}, s.handleIndexRepository)

	s.addTool(&mcp.Tool{
		Name: "trace_call_path",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},

		Description: "Trace the call path of a function (who calls it, what it calls). Requires exact function name. Returns hop-by-hop callees/callers with edge types (CALLS, HTTP_CALLS, ASYNC_CALLS, USAGE, OVERRIDE). If not found, returns similar name suggestions - use the qualified_name from suggestions to retry. Use depth=1 first, increase only if needed. Use direction='both' for full cross-service context - HTTP_CALLS from other services appear as inbound edges, so direction='outbound' alone misses them.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"function_name": {
					"type": "string",
					"description": "Name of the function to trace (e.g. 'ProcessOrder')"
				},
				"depth": {
					"type": "integer",
					"description": "Maximum BFS depth (1-5, default 3)"
				},
				"direction": {
					"type": "string",
					"description": "Traversal direction: 'outbound' (what it calls), 'inbound' (what calls it), or 'both'",
					"enum": ["outbound", "inbound", "both"]
				},
				"risk_labels": {
					"type": "boolean",
					"description": "Add risk classification (CRITICAL/HIGH/MEDIUM/LOW) based on hop depth. Hop 1=CRITICAL, 2=HIGH, 3=MEDIUM, 4+=LOW. Includes impact_summary with counts. Default false."
				},
				"min_confidence": {
					"type": "number",
					"description": "Minimum confidence threshold (0.0-1.0) for CALLS edges. Filters out low-confidence fuzzy matches. Bands: high (>=0.7), medium (>=0.45), speculative (<0.45). Default 0 (no filter)."
				},
				"project": {
					"type": "string",
					"description": "Project to trace in. Defaults to session project."
				},
				"include_source": {
					"type": "boolean",
					"description": "Inline source code for the root node and hop nodes under 50 lines. Makes trace results self-contained without follow-up get_code_snippet calls. Default: false."
				}
			},
			"required": ["function_name"]
		}`),
	}, s.handleTraceCallPath)
}

func (s *Server) registerSchemaAndSnippetTools() {
	s.addTool(&mcp.Tool{
		Name: "get_graph_schema",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},

		Description: "Return the schema of the indexed code graph: node label counts, edge type counts, relationship patterns (e.g. Function-CALLS->Function), and sample function/class names. Use to understand what's in the graph before querying.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"project": {
					"type": "string",
					"description": "Project to get schema for. Defaults to session project."
				}
			}
		}`),
	}, s.handleGetGraphSchema)

	s.addTool(&mcp.Tool{
		Name: "get_code_snippet",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},

		Description: "Retrieve source code for a function/class by name. Single mode: pass qualified_name (string). Batch mode: pass qualified_names (array of up to 10 strings) to fetch multiple snippets in one call - eliminates round trips after search_graph or trace_call_path. Returns source code, signature, return type, complexity, decorators, docstring, and caller/callee counts. Returns status='ambiguous' with suggestions when multiple matches found.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"qualified_name": {
					"type": "string",
					"description": "Name or qualified name of the function/class (single mode). Exact QN for precision, short name for discovery."
				},
				"qualified_names": {
					"type": "array",
					"items": {"type": "string"},
					"description": "Array of names to resolve in batch (max 10). Each entry is resolved independently. Use after search_graph or trace_call_path to read multiple functions in one call."
				},
				"project": {
					"type": "string",
					"description": "Project to search in. Defaults to session project."
				},
				"auto_resolve": {
					"type": "boolean",
					"description": "When true and <=2 ambiguous candidates exist, auto-pick the best match (highest degree, prefer non-test). Default: false."
				},
				"include_neighbors": {
					"type": "boolean",
					"description": "When true, include caller_names and callee_names arrays (up to 10 each) alongside the counts. Default: false."
				}
			}
		}`),
	}, s.handleGetCodeSnippet)
}

func (s *Server) registerSearchTools() {
	s.addTool(&mcp.Tool{
		Name: "search_code_semantic",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},
		Description: "Semantic code search using Voyage AI embeddings. Find code by natural language description — 'authentication middleware', 'GPS parsing logic', 'battery monitoring'. Unlike search_code (grep) and search_graph (structural), this understands meaning. Requires VOYAGE_API_KEY and a prior index_repository run. Returns functions, classes, structs ranked by semantic similarity. Use file_pattern and label filters to narrow scope.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"description": "Natural language search query. Describe what the code does, not exact keywords."
				},
				"project": {
					"type": "string",
					"description": "Project to search in. Defaults to session project."
				},
				"limit": {
					"type": "integer",
					"description": "Maximum results (default 10, max 50)"
				},
				"file_pattern": {
					"type": "string",
					"description": "Glob pattern to filter files (e.g. '*.rs', 'src/**')"
				},
				"label": {
					"type": "string",
					"description": "Node label filter: Function, Method, Class, Struct, Interface, Trait, Enum, Module, Type"
				}
			},
			"required": ["query"]
		}`),
	}, s.handleSearchCodeSemantic)

	s.addTool(&mcp.Tool{
		Name: "search_graph",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},

		Description: "Search the code knowledge graph for functions, classes, modules, routes, and other code elements. Case-insensitive by default. Use regex alternatives for broad matching: 'handler|hdlr|ctrl'. Returns nodes with connectivity info (in/out degree), sorted by relevance. Supports filters: label, name_pattern (regex), qn_pattern, file_pattern (glob), relationship/direction/degree. Use max_degree=0 with exclude_entry_points=true for dead code detection. Returns 10 results per page (offset to paginate, has_more flag). Note: relationship filter counts edges (degree filtering) but does not return edges - use query_graph with Cypher for edge listings.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"project": {
					"type": "string",
					"description": "Project to search in. Defaults to session project."
				},
				"label": {
					"type": "string",
					"description": "Node label filter: Function, Class, Module, Method, Interface, Enum, Type, File, Package, Folder, Route"
				},
				"name_pattern": {
					"type": "string",
					"description": "Regex pattern matched against the short node name. Case-insensitive by default. Supports full Go regex: '.*Handler$' (suffix), 'get|set|delete' (alternatives — no backslash before pipe), '^on[A-Z]' (prefix+char class). Best practice: include word variations in alternatives — 'auth|authenticate|authorization' (word forms), 'handler|hdlr|ctrl' (abbreviations), 'create|new|init' (synonyms). One regex with | replaces multiple separate searches."
				},
				"qn_pattern": {
					"type": "string",
					"description": "Regex pattern matched against the qualified name (full module path). Case-insensitive by default. Use to scope searches to directories/modules: '.*services\\.order\\..*' (order service), '.*tests\\..*' (test files only), '.*controller.*\\.handle.*' (handler methods in controllers). Combine with name_pattern for precise cross-cutting queries."
				},
				"file_pattern": {
					"type": "string",
					"description": "Glob pattern for file path within the project. Use to filter by directory ('**/services/**'), file extension ('*.py', '*.yaml'), or filename ('**/Makefile'). Essential for shared-repo projects where multiple languages coexist — e.g., use '*.html' to find only HTML files in a JavaScript project."
				},
				"relationship": {
					"type": "string",
					"description": "Filter by relationship type: CALLS, HTTP_CALLS, ASYNC_CALLS, IMPORTS, DEFINES, DEFINES_METHOD, HANDLES, CONTAINS_FILE, CONTAINS_FOLDER, CONTAINS_PACKAGE, IMPLEMENTS"
				},
				"direction": {
					"type": "string",
					"description": "Edge direction for degree filters: 'inbound', 'outbound', or 'any'",
					"enum": ["inbound", "outbound", "any"]
				},
				"min_degree": {
					"type": "integer",
					"description": "Minimum edge count (e.g. 10 for high fan-out functions)"
				},
				"max_degree": {
					"type": "integer",
					"description": "Maximum edge count (e.g. 0 for dead code detection)"
				},
				"min_complexity": {
					"type": "integer",
					"description": "Minimum cyclomatic complexity (e.g. 10 to surface gnarly functions for documentation/refactor focus). Nodes without a complexity property are excluded when this filter is set."
				},
				"max_complexity": {
					"type": "integer",
					"description": "Maximum cyclomatic complexity (e.g. 5 to surface simple candidates for early documentation). Nodes without a complexity property are excluded when this filter is set."
				},
				"exclude_entry_points": {
					"type": "boolean",
					"description": "Exclude entry points (route handlers, main(), framework-registered functions) from results. Use with max_degree=0 for accurate dead code detection."
				},
				"limit": {
					"type": "integer",
					"description": "Max results per page (default: 10). Use small limits and paginate with offset — response includes has_more flag."
				},
				"offset": {
					"type": "integer",
					"description": "Skip N results for pagination (default: 0). Check has_more in response to know if more pages exist."
				},
				"include_connected": {
					"type": "boolean",
					"description": "Include connected node names in results (default: false). Expensive — only enable when you need to see neighbor names."
				},
				"exclude_labels": {
					"type": "array",
					"items": {"type": "string"},
					"description": "Labels to exclude from results. Community nodes are excluded by default — pass [] to include them."
				},
				"sort_by": {
					"type": "string",
					"enum": ["relevance", "name", "degree"],
					"description": "Sort order. Default: relevance (exact match first, prefix match second, then by connectivity)"
				},
				"case_sensitive": {
					"type": "boolean",
					"description": "Match patterns case-sensitively. Default: false (case-insensitive). Set true for exact case matching."
				},
				"include_source": {
					"type": "boolean",
					"description": "Inline source code for functions/classes under 50 lines. Eliminates follow-up get_code_snippet calls. Default: false. Also includes node properties (signature, return_type, decorators) in results."
				}
			}
		}`),
	}, s.handleSearchGraph)

	s.addTool(&mcp.Tool{
		Name: "search_code",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},

		Description: "Search for text in source code files (like grep, scoped to indexed project). Case-insensitive by default. With regex=true, use alternatives for broad matching: 'TODO|FIXME|HACK'. Returns matching lines with file path, line number, and surrounding context. Returns 10 matches per page (offset to paginate, has_more flag). Use for string literals, error messages, TODO comments, config values, import statements. Prefer search_graph for finding functions/classes by structural name.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"pattern": {
					"type": "string",
					"description": "Text to search for. Case-insensitive by default. Literal string match unless regex=true. With regex=true: Go regex syntax (no backslash before pipe). Best practice: use alternatives for word form variance — 'deprecat|obsolete|legacy' catches 'deprecated', 'deprecating', 'obsolete', etc. A partial stem with alternatives is more effective than an exact word."
				},
				"file_pattern": {
					"type": "string",
					"description": "Glob pattern to filter files (e.g. '*.go', '*.py', '*.toml'). Use to focus search on specific file types or directories."
				},
				"regex": {
					"type": "boolean",
					"description": "Treat pattern as a regular expression (default: false)"
				},
				"max_results": {
					"type": "integer",
					"description": "Max matches per page (default: 10). Response includes has_more flag for pagination."
				},
				"offset": {
					"type": "integer",
					"description": "Skip N matches for pagination (default: 0). Check has_more in response."
				},
				"case_sensitive": {
					"type": "boolean",
					"description": "Match case-sensitively. Default: false (case-insensitive). Set true for exact case matching."
				},
				"project": {
					"type": "string",
					"description": "Project to search in. Defaults to session project."
				}
			},
			"required": ["pattern"]
		}`),
	}, s.handleSearchCode)
}

func (s *Server) registerQueryTool() {
	s.addTool(&mcp.Tool{
		Name: "query_graph",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},

		Description: "Execute a Cypher-like graph query (read-only subset). String matching is case-sensitive; use =~ '(?i)pattern' for case-insensitive regex. Supports MATCH, WHERE, RETURN, ORDER BY, LIMIT, DISTINCT, variable-length paths (*1..3). WHERE comparison operators: =, <>, <, >, <=, >=, =~ (regex), STARTS WITH, ENDS WITH, CONTAINS, IS NULL, IS NOT NULL, AND, OR. Aggregations: COUNT(*) (count all rows) and COUNT(var) (count non-null bindings of a variable). Write keywords (CREATE, DELETE, SET, MERGE, REMOVE) are rejected at parse with a clear error — read-only is enforced at parse time. DEFAULT ROW CAP IS 200 — pass max_rows (up to 10000) to raise it. Response includes 'effective_cap' always and 'capped: true' when results were truncated (check this before trusting totals on large result sets). Best for relationship patterns, filtered joins, path queries, and edge property filtering. Filterable edge properties: r.confidence, r.url_path, r.method, r.confidence_band, r.validated_by_trace, r.coupling_score. Edge types: CALLS, HTTP_CALLS, ASYNC_CALLS, IMPORTS, DEFINES, IMPLEMENTS, OVERRIDE, USAGE, FILE_CHANGES_WITH. Always use LIMIT. KNOWN ACCURACY BANDS (measured via bench/accuracy/, 2026-04-24, PyCG/Jedi/syn/go-ast oracles; ±35% oracle-class uncertainty per Jedi-vs-PyCG comparison): Python CALLS scope-aligned F1 ~0.54-0.99 (highly fixture-dependent — top-level packages score high, nested src/ layouts lower). Rust CALLS scope-aligned F1 ~0.82-0.91 across 3 fixtures (services, trait-heavy lib, utility lib). Go CALLS scope-aligned F1 ~0.54-0.68 across 3 fixtures (self-host, cobra, gin — non-self-hosted fixtures run ~10pp lower). IMPORTS: Python 0.94-0.96 after nested-package resolver fix; Rust sparse (resolver gap on `use crate::...` paths). Indirect calls (closures, fn pointers, trait objects) are NOT in the graph — code-graph's extractor doesn't emit CALLS edges for higher-order dispatch.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"description": "Cypher query, e.g. MATCH (f:Function)-[:CALLS]->(g:Function) WHERE f.name = 'main' RETURN g.name, g.qualified_name LIMIT 20"
				},
				"project": {
					"type": "string",
					"description": "Project to query. Defaults to session project."
				},
				"max_rows": {
					"type": "integer",
					"description": "Maximum result rows (default 200, max 10000). Overrides the internal row cap. Use higher values for COUNT/aggregation queries on large codebases."
				}
			},
			"required": ["query"]
		}`),
	}, s.handleQueryGraph)
}

// registerProjectTools registers tools for project management.
func (s *Server) registerProjectTools() {
	s.addTool(&mcp.Tool{
		Name: "list_projects",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},

		Description: "List all indexed projects with their node/edge counts, indexed_at timestamps, root paths, and database file locations. Returns all projects in a single response (no pagination). Returns an empty array if no projects are indexed.",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {}}`),
	}, s.handleListProjects)

	s.addTool(&mcp.Tool{
		Name: "delete_project",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(true),
		},

		Description: "Delete an indexed project and all its graph data (nodes, edges, file hashes). Removes the project's .db file. This action is irreversible. Returns error if the project does not exist.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"project_name": {
					"type": "string",
					"description": "Name of the project to delete"
				}
			},
			"required": ["project_name"]
		}`),
	}, s.handleDeleteProject)

	s.addTool(&mcp.Tool{
		Name: "index_status",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},

		Description: "Check the indexing status of a project. Returns whether the project is indexed, currently indexing, or not found. Shows last indexed timestamp, node/edge counts, and whether the index is initial or incremental. Use this to check if the graph is ready for queries.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"project": {
					"type": "string",
					"description": "Project name to check. Defaults to the auto-detected session project."
				}
			}
		}`),
	}, s.handleIndexStatus)
}

// --- Helpers ---

// jsonResult marshals data to JSON and returns as tool result.
func jsonResult(data any) *mcp.CallToolResult {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return errResult("json marshal err=" + err.Error())
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(b)},
		},
	}
}

// errResult returns a tool result indicating an error.
func errResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: msg},
		},
		IsError: true,
	}
}

// parseArgs unmarshals the raw JSON arguments into a map.
func parseArgs(req *mcp.CallToolRequest) (map[string]any, error) {
	if len(req.Params.Arguments) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(req.Params.Arguments, &m); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	return m, nil
}

// getStringArg extracts a string argument from parsed args.
func getStringArg(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok {
		return ""
	}
	str, ok := v.(string)
	if !ok {
		return ""
	}
	return str
}

// getIntArg extracts an integer argument with a default value.
func getIntArg(args map[string]any, key string, defaultVal int) int {
	v, ok := args[key]
	if !ok {
		return defaultVal
	}
	f, ok := v.(float64) // JSON numbers decode as float64
	if !ok {
		return defaultVal
	}
	return int(f)
}

// getMapStringArg extracts a map[string]string argument from parsed args.
func getMapStringArg(args map[string]any, key string) map[string]string {
	v, ok := args[key]
	if !ok {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	result := make(map[string]string, len(m))
	for k, val := range m {
		if s, ok := val.(string); ok {
			result[k] = s
		}
	}
	return result
}

// getBoolArg extracts a boolean argument from parsed args.
// getFloatArg extracts a float64 argument with a default value.
func getFloatArg(args map[string]any, key string, defaultVal float64) float64 {
	v, ok := args[key]
	if !ok {
		return defaultVal
	}
	f, ok := v.(float64)
	if !ok {
		return defaultVal
	}
	return f
}

func getBoolArg(args map[string]any, key string) bool {
	v, ok := args[key]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	if !ok {
		return false
	}
	return b
}

// boolPtr returns a pointer to a bool value. Used for optional ToolAnnotations fields.
func boolPtr(b bool) *bool { return &b }

// findNodeAcrossProjects searches for a node by simple name in the specified project.
// Falls back to the session project if no filter is given.
func (s *Server) findNodeAcrossProjects(name string, projectFilter ...string) (*store.Node, string, error) {
	filter := s.sessionProject
	if len(projectFilter) > 0 && projectFilter[0] != "" {
		if projectFilter[0] == "*" || projectFilter[0] == "all" {
			return nil, "", fmt.Errorf("cross-project queries are not supported; use list_projects to find a specific project name, or omit the project parameter to use the current session project")
		}
		filter = projectFilter[0]
	}
	if filter == "" {
		return nil, "", fmt.Errorf("no project specified and no session project detected")
	}
	if !s.router.HasProject(filter) {
		return nil, "", fmt.Errorf("project %q not found; use list_projects to see available projects", filter)
	}
	// Touch watcher so cross-project queries keep that project fresh.
	if filter != s.sessionProject {
		s.watcher.TouchProject(filter)
	}

	st, err := s.router.ForProject(filter)
	if err != nil {
		return nil, "", err
	}
	projects, _ := st.ListProjects()
	for _, p := range projects {
		nodes, findErr := st.FindNodesByName(p.Name, name)
		if findErr != nil {
			continue
		}
		if len(nodes) > 0 {
			return nodes[0], p.Name, nil
		}
	}
	return nil, "", fmt.Errorf("node not found: %s", name)
}
