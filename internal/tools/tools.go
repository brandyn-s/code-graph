package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/brandyn-s/code-graph/internal/discover"
	"github.com/brandyn-s/code-graph/internal/indexidentity"
	"github.com/brandyn-s/code-graph/internal/pipeline"
	"github.com/brandyn-s/code-graph/internal/selfupdate"
	"github.com/brandyn-s/code-graph/internal/store"
	"github.com/brandyn-s/code-graph/internal/watcher"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Version is the current release version, set from main.version via SetVersion().
// Defaults to "dev" for local builds.
var Version = "dev"

// SetVersion sets the package version from the build-injected main.version.
func SetVersion(v string) { Version = v }

// releaseURL is the GitHub API endpoint for latest release. Package-level var for test injection.
//
// Points at this fork, NOT upstream DeusData: this build carries the fork's
// security/tooling additions, and an upstream-pointed check would prompt
// `code-graph update` to replace the binary with an upstream build that
// silently drops every fork addition.
var releaseURL = "https://api.github.com/repos/brandyn-s/code-graph/releases/latest"

var fetchRelease = selfupdate.FetchRelease

// Server wraps the MCP server with tool handlers.
type Server struct {
	mcp             *mcp.Server
	router          *store.StoreRouter
	config          *store.ConfigStore
	watcher         *watcher.Watcher
	queryCache      *store.QueryCache
	ctx             context.Context // server lifetime context — cancelled on shutdown
	indexMu         sync.Mutex
	handlers        map[string]mcp.ToolHandler
	toolDefinitions map[string]*mcp.Tool
	// toolset decides which registered tools are advertised over MCP.
	toolset string

	// Test seam for deterministic start/end checkout coherence checks.
	captureIndexIdentity func(string) (*indexidentity.Envelope, error)

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
// WithToolset overrides CODE_GRAPH_TOOLSET for this server instance.
func WithToolset(toolset string) ServerOption {
	return func(s *Server) {
		if toolset == ToolsetFull {
			s.toolset = ToolsetFull
		} else {
			s.toolset = ToolsetCore
		}
	}
}

// Toolset reports which toolset this server advertises.
func (s *Server) Toolset() string { return s.toolset }

// AdvertisedToolNames returns the sorted tool names this server advertises
// over MCP under its toolset.
func (s *Server) AdvertisedToolNames() []string {
	names := make([]string, 0, len(s.toolDefinitions))
	for name := range s.toolDefinitions {
		if toolsetIncludes(s.toolset, name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func WithConfig(c *store.ConfigStore) ServerOption {
	return func(s *Server) { s.config = c }
}

// NewServer creates a new MCP server with all tools registered.
func NewServer(r *store.StoreRouter, opts ...ServerOption) *Server {
	srv := &Server{
		router:          r,
		queryCache:      store.NewQueryCache(200, 5*time.Minute),
		handlers:        make(map[string]mcp.ToolHandler),
		toolDefinitions: make(map[string]*mcp.Tool),
		toolset:         ActiveToolset(),
	}
	for _, opt := range opts {
		opt(srv)
	}

	srv.mcp = mcp.NewServer(
		&mcp.Implementation{
			Name:    "code-graph",
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

// errSyncBusy signals the watcher that a sync was skipped because another
// index operation holds indexMu. It MUST be an error, not nil: the watcher
// commits its new snapshot only on a nil return, and a nil here marked the
// detected change as synced with no reindex having run — the change was
// then never retried (git strategy never forces full snapshots).
var errSyncBusy = errors.New("sync skipped: index in progress")

// syncProject is called by the watcher when file changes are detected.
// Uses TryLock to skip if an index operation is already in progress.
func (s *Server) syncProject(ctx context.Context, projectName, rootPath string) error {
	if !s.indexMu.TryLock() {
		slog.Debug("watcher.skip", "path", rootPath, "reason", "index_in_progress")
		return errSyncBusy
	}
	defer s.indexMu.Unlock()
	// Hold a ref for the whole pipeline run. ForProject is unprotected:
	// the router's evictor closes refs==0 stores idle >30s, and a watcher
	// reindex crossing that window had its *sql.DB closed mid-run — the
	// same 2026-06-11 incident class index_repository already guards
	// against with AcquireStore (see tools/index.go).
	st, release, err := s.router.AcquireStore(projectName)
	if err != nil {
		return fmt.Errorf("store for %s: %w", projectName, err)
	}
	defer release()
	if err := st.SetIndexIdentityState(
		projectName,
		indexidentity.StatusPending,
		"watcher reindex is in progress; checkout identity has not been recaptured",
	); err != nil {
		return fmt.Errorf("invalidate previous index identity: %w", err)
	}
	captureIdentity := s.indexIdentityCapture()
	startIdentity, startIdentityErr := captureIdentity(rootPath)
	p := pipeline.New(ctx, st, rootPath, discover.ModeFull)
	precisionSelection, err := s.configureStoredGraphPrecision(p, projectName, rootPath)
	if err != nil {
		return fmt.Errorf("configure graph precision: %w", err)
	}
	if err := p.Run(); err != nil {
		if stateErr := persistTerminalIndexIdentityError(
			st,
			projectName,
			"watcher_indexing_failed",
			err,
		); stateErr != nil {
			slog.Error(
				"watcher.identity.terminal_state_err",
				"project", projectName,
				"index_err", err,
				"state_err", stateErr,
			)
		}
		return err
	}
	s.persistGraphPrecision(projectName, precisionSelection, &p.SCIPStatus)
	endIdentity, endIdentityErr := captureIdentity(rootPath)
	identityErr := persistCoherentIndexIdentity(
		st,
		projectName,
		startIdentity,
		startIdentityErr,
		endIdentity,
		endIdentityErr,
	)
	s.queryCache.Invalidate()
	return identityErr
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
	if sessionSupportsRoots(session) {
		result, err := session.ListRoots(ctx, nil) //nolint:staticcheck // MCP roots are deprecated (SEP-2577) but remain functional through the deprecation window; clients still send them
		if err == nil && len(result.Roots) > 0 {
			uri := result.Roots[0].URI
			if path, ok := parseFileURI(uri); ok {
				slog.Info("session.root.from_roots", "path", path)
				return path
			}
		}
	}

	// 2. Fall back to process working directory.
	// Refuse to register the home directory, drive root, or common
	// system/cache dirs as a project root — this would trigger an
	// unbounded full-index walk (~/.cache, AppData, etc.) and wedge
	// the server. The previous guard used os.Getenv("HOME"), which
	// returns empty on Windows (Windows uses USERPROFILE), so the
	// check was non-functional on Windows. isForbiddenSessionRoot
	// uses os.UserHomeDir() for cross-platform correctness and adds
	// drive-root + system-dir checks.
	if cwd, err := os.Getwd(); err == nil && !isForbiddenSessionRoot(cwd) {
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

// rootsListingForbiddenFrom is the first MCP protocol version at which the
// specification forbids server-initiated requests such as roots/list
// (SEP-2322 / SEP-2575); the Go SDK rejects them with an error at or above it.
// Protocol versions are ISO dates, so string comparison orders them.
const rootsListingForbiddenFrom = "2026-07-28"

func sessionSupportsRoots(session *mcp.ServerSession) bool {
	if session == nil {
		return false
	}
	return rootsListingAllowed(session.InitializeParams())
}

// rootsListingAllowed reports whether the negotiated session lets the server
// ask the client for its roots: the client declared the (deprecated) roots
// capability and the protocol version still permits server-initiated
// requests. Newer sessions fall through to the working-directory and
// single-project fallbacks in detectSessionRoot.
func rootsListingAllowed(params *mcp.InitializeParams) bool {
	if params == nil || params.Capabilities == nil {
		return false
	}
	if params.ProtocolVersion >= rootsListingForbiddenFrom {
		return false
	}
	return params.Capabilities.RootsV2 != nil //nolint:staticcheck // MCP roots are deprecated (SEP-2577) but remain functional through the deprecation window; clients still send them
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

// isForbiddenSessionRoot returns true if `path` should NOT be registered as a
// project root via CWD fallback. Catches the failure mode where Claude Code
// spawns the MCP server with CWD = user's home directory (Windows shell
// default), which would otherwise trigger an unbounded full-index walk of
// the home tree (~/.cache, AppData, Documents, …) and wedge the server.
//
// Two-tier check:
//   - Exact-match forbidden: drive root, POSIX root, user home, parent of
//     home (e.g., C:\Users). Subdirectories of home are NOT rejected here —
//     ~/Documents/GitHub/foo is a legitimate project.
//   - Scope-anywhere forbidden: anything under ~/.cache, ~/AppData,
//     ~/.npm, ~/.cargo, /etc, /var, /usr, /opt, C:\Windows, C:\Program
//     Files (x86?), C:\ProgramData.
//
// Cross-platform note: pre-fix code used os.Getenv("HOME"), which returns
// empty on Windows (Windows uses USERPROFILE). os.UserHomeDir() resolves
// the home correctly on all platforms.
func isForbiddenSessionRoot(path string) bool {
	if path == "" {
		return true
	}
	clean := filepath.Clean(path)
	cleanLower := strings.ToLower(clean)

	// POSIX root
	if clean == "/" || clean == `\` {
		return true
	}
	// Windows drive root variants:
	//   "C:\"  -> clean stays "C:\"
	//   "C:/"  -> clean becomes "C:\"
	//   "C:"   -> clean becomes "C:." on Windows (drive-relative current dir)
	//   "C:."  -> clean stays "C:."
	if len(clean) >= 2 && clean[1] == ':' {
		rest := clean[2:]
		if rest == "" || rest == `\` || rest == "/" || rest == "." {
			return true
		}
	}

	// Home and parent-of-home exact-match
	home, _ := os.UserHomeDir()
	if home != "" {
		cleanHome := filepath.Clean(home)
		if strings.EqualFold(clean, cleanHome) {
			return true
		}
		if strings.EqualFold(clean, filepath.Dir(cleanHome)) {
			return true
		}
	}

	// Scope-anywhere forbidden roots
	var scopes []string
	if home != "" {
		cleanHome := filepath.Clean(home)
		for _, sub := range []string{".cache", "AppData", ".npm", ".cargo"} {
			scopes = append(scopes, filepath.Join(cleanHome, sub))
		}
	}
	scopes = append(scopes,
		`C:\Windows`,
		`C:\Program Files`,
		`C:\Program Files (x86)`,
		`C:\ProgramData`,
		"/etc", "/var", "/usr", "/opt", "/sys", "/proc",
	)
	sepStr := string(filepath.Separator)
	for _, bad := range scopes {
		cleanBad := strings.ToLower(filepath.Clean(bad))
		if cleanLower == cleanBad || strings.HasPrefix(cleanLower, cleanBad+sepStr) {
			return true
		}
	}
	return false
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
				"hint", "run: code-graph config set auto_index true",
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
	}
	go func() {
		if err := s.runAutoIndex(hasDB); errors.Is(err, errSyncBusy) {
			slog.Debug("autoindex.skip", "reason", "index_in_progress")
		} else if err != nil {
			slog.Warn("autoindex.err", "err", err)
		} else {
			slog.Info("autoindex.done", "project", s.sessionProject)
		}
	}()
}

func (s *Server) runAutoIndex(hasDB bool) error {
	if !s.indexMu.TryLock() {
		return errSyncBusy
	}
	defer s.indexMu.Unlock()

	s.indexStartedAt.Store(time.Now())
	s.indexStatus.Store("indexing")

	st, release, err := s.router.AcquireStore(s.sessionProject)
	if err != nil {
		s.indexStatus.Store("degraded")
		return fmt.Errorf("auto-index store: %w", err)
	}
	defer release()

	if hasDB {
		if err := st.SetIndexIdentityState(
			s.sessionProject,
			indexidentity.StatusPending,
			"auto-index is in progress; checkout identity has not been recaptured",
		); err != nil {
			s.indexStatus.Store("degraded")
			return fmt.Errorf("invalidate previous auto-index identity: %w", err)
		}
	}

	captureIdentity := s.indexIdentityCapture()
	startIdentity, startIdentityErr := captureIdentity(s.sessionRoot)
	pipelineContext := s.ctx
	if pipelineContext == nil {
		pipelineContext = context.Background()
	}
	p := pipeline.New(pipelineContext, st, s.sessionRoot, discover.ModeFull)
	precisionSelection, err := s.configureStoredGraphPrecision(p, s.sessionProject, s.sessionRoot)
	if err != nil {
		s.indexStatus.Store("degraded")
		return fmt.Errorf("configure graph precision: %w", err)
	}
	if err := p.Run(); err != nil {
		s.indexStatus.Store("degraded")
		reason := fmt.Sprintf("auto-index failed after invalidating checkout identity: %v", err)
		if stateErr := st.SetIndexIdentityState(
			s.sessionProject,
			indexidentity.StatusError,
			reason,
		); stateErr != nil {
			slog.Warn("autoindex.identity_failure_state.err", "err", stateErr)
		}
		return fmt.Errorf("auto-index pipeline: %w", err)
	}
	s.persistGraphPrecision(s.sessionProject, precisionSelection, &p.SCIPStatus)
	if err := st.SetIndexIdentityState(
		s.sessionProject,
		indexidentity.StatusPending,
		"auto-index completed but checkout identity has not been recaptured",
	); err != nil {
		s.indexStatus.Store("degraded")
		return fmt.Errorf("mark auto-index identity pending: %w", err)
	}

	s.queryCache.Invalidate()
	s.watcher.Watch(s.sessionProject, s.sessionRoot)

	endIdentity, endIdentityErr := captureIdentity(s.sessionRoot)
	if err := persistCoherentIndexIdentity(
		st,
		s.sessionProject,
		startIdentity,
		startIdentityErr,
		endIdentity,
		endIdentityErr,
	); err != nil {
		s.indexStatus.Store("degraded")
		return fmt.Errorf("auto-index identity coherence: %w", err)
	}

	s.indexStatus.Store("ready")
	return nil
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

	release, err := fetchRelease(ctx, releaseURL)
	if err != nil {
		// Missing gh or unavailable private-repository credentials should not
		// spam one warning per MCP startup. The explicit `update` command
		// returns the same authenticated-fetch error to the operator.
		slog.Debug("update check: authenticated release fetch failed", "err", err)
		return
	}

	latest := release.LatestVersion()
	if latest == "" || latest == Version {
		slog.Debug("update check: current", "version", Version, "latest", latest)
		return
	}
	if compareVersions(latest, Version) > 0 {
		notice := fmt.Sprintf(
			"⚡ Update available: v%s → v%s — run: code-graph update",
			Version, latest)
		s.updateNotice.Store(notice)
		slog.Info("update available", "current", Version, "latest", latest)
	}
}

// compareVersions compares two semver strings (e.g. "0.2.1" vs "0.2.0").
// Returns >0 if a > b, <0 if a < b, 0 if equal.
func compareVersions(a, b string) int {
	return selfupdate.CompareVersions(a, b)
}

// --- Tool registration ---

func (s *Server) addTool(tool *mcp.Tool, handler mcp.ToolHandler) {
	_, definitionExists := s.toolDefinitions[tool.Name]
	_, handlerExists := s.handlers[tool.Name]
	if definitionExists || handlerExists {
		panic(fmt.Sprintf("duplicate MCP tool registration %q", tool.Name))
	}
	if s.mcp != nil && toolsetIncludes(s.toolset, tool.Name) {
		s.mcp.AddTool(tool, handler)
	}
	if s.handlers != nil {
		s.handlers[tool.Name] = handler
	}
	if s.toolDefinitions == nil {
		s.toolDefinitions = make(map[string]*mcp.Tool)
	}
	s.toolDefinitions[tool.Name] = tool
}

func withTraceEdgeTypes(schemaJSON json.RawMessage) map[string]any {
	var schema map[string]any
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		panic(fmt.Sprintf("invalid trace_call_path input schema: %v", err))
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		panic("trace_call_path input schema has no properties object")
	}
	defaultEdgeTypes := append([]string(nil), traceDefaultEdgeTypes[:]...)
	supportedEdgeTypes := append([]string(nil), traceSupportedEdgeTypes[:]...)
	properties["edge_types"] = map[string]any{
		"type":        "array",
		"items":       map[string]any{"type": "string", "enum": supportedEdgeTypes},
		"minItems":    1,
		"maxItems":    len(supportedEdgeTypes),
		"uniqueItems": true,
		"default":     defaultEdgeTypes,
		"description": fmt.Sprintf(
			"Relationship types to traverse. Defaults to call-like relationships: %s. Non-call relationships are opt-in: USAGE, OVERRIDE.",
			strings.Join(defaultEdgeTypes, ", "),
		),
	}
	return schema
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

// RegisteredToolDefinitionsJSON returns the canonical JSON representation of
// the MCP tool definitions registered by the server. Registration only builds
// in-memory definitions: it does not start a transport, create a store, access
// the network, or invoke any tool handler.
func RegisteredToolDefinitionsJSON() ([]byte, error) {
	definitionServer := &Server{
		toolDefinitions: make(map[string]*mcp.Tool),
	}
	definitionServer.registerTools()

	names := make([]string, 0, len(definitionServer.toolDefinitions))
	for name := range definitionServer.toolDefinitions {
		names = append(names, name)
	}
	sort.Strings(names)

	// Round-trip each SDK definition through generic JSON values so schemas
	// backed by json.RawMessage and schemas backed by maps serialize identically.
	definitions := make([]any, 0, len(names))
	for _, name := range names {
		encoded, err := json.Marshal(definitionServer.toolDefinitions[name])
		if err != nil {
			return nil, fmt.Errorf("marshal registered tool %q: %w", name, err)
		}
		var canonical any
		if err := json.Unmarshal(encoded, &canonical); err != nil {
			return nil, fmt.Errorf("canonicalize registered tool %q: %w", name, err)
		}
		definitions = append(definitions, canonical)
	}

	encoded, err := json.MarshalIndent(definitions, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal canonical registered tool definitions: %w", err)
	}
	return append(encoded, '\n'), nil
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
	s.registerDegreeFilterTool()
}
