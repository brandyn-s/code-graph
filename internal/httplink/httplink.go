package httplink

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// RouteHandler represents a discovered HTTP route handler.
type RouteHandler struct {
	Path              string
	Method            string
	FunctionName      string
	QualifiedName     string
	HandlerRef        string // resolved handler function reference (e.g. "h.CreateOrder")
	ResolvedHandlerQN string // set by createRegistrationCallEdges — actual handler QN
	Protocol          string // "ws", "sse", or "" (standard HTTP)
	SourceExtractor   string // Phase B1 (2026-05-08): tags which extractor produced this entry — surfaced on Route node properties + routes.extractor.summary slog
}

// HTTPCallSite represents a discovered HTTP call site.
type HTTPCallSite struct {
	Path                string
	Method              string // best-effort: "GET", "POST", etc. or "" if unknown
	SourceName          string
	SourceQualifiedName string
	SourceLabel         string // "Function", "Method", or "Module"
	IsAsync             bool   // true when source uses async dispatch keywords
}

// HTTPLink represents a matched HTTP call from caller to handler.
type HTTPLink struct {
	CallerQN    string
	CallerLabel string
	HandlerQN   string
	URLPath     string
	EdgeType    string // "HTTP_CALLS" or "ASYNC_CALLS"
}

// Linker discovers cross-service HTTP calls and creates HTTP_CALLS edges.
type Linker struct {
	store          *store.Store
	project        string
	config         *LinkerConfig
	routesByFunc   map[string][]int // funcQN → indices into routes slice
	extraCallSites []HTTPCallSite   // injected from pipeline (e.g., InfraFile URLs)
}

// New creates a new HTTP Linker.
func New(s *store.Store, project string) *Linker {
	return &Linker{store: s, project: project, config: DefaultConfig()}
}

// SetConfig sets the linker configuration. If cfg is nil, defaults are used.
func (l *Linker) SetConfig(cfg *LinkerConfig) {
	if cfg != nil {
		l.config = cfg
	}
}

// AddCallSites allows the pipeline to inject additional call sites from infra files.
func (l *Linker) AddCallSites(sites []HTTPCallSite) {
	l.extraCallSites = append(l.extraCallSites, sites...)
}

// regex patterns for route and URL discovery
var (
	// Python decorators: @app.post("/path"), @router.get(""), @router.get("/path")
	pyRouteRe = regexp.MustCompile(`@\w+\.(get|post|put|delete|patch)\(\s*["']([^"']*)["']`)

	// Go gin/chi routes: .POST("/path"), .Get("/path"), .POST("", handler)
	goRouteRe = regexp.MustCompile(`\.(GET|POST|PUT|DELETE|PATCH|Get|Post|Put|Delete|Patch)\(\s*["']([^"']*)["']`)

	// Go gin group: .Group("/prefix")
	goGroupRe = regexp.MustCompile(`(\w+)\s*(?::=|=)\s*\w+\.Group\(\s*["']([^"']+)["']`)

	// Go gin/chi route handler reference: captures the last argument (handler, not middleware)
	// .POST("/path", h.CreateOrder) or .Get("/path", handler)
	goRouteHandlerRe = regexp.MustCompile(`\.(GET|POST|PUT|DELETE|PATCH|Get|Post|Put|Delete|Patch)\s*\(\s*"[^"]*"\s*(?:,\s*[\w.]+)*,\s*([\w.]+)\s*\)`)

	// Go chi: r.Route("/prefix", func(r chi.Router) { ... })
	goChiRouteRe = regexp.MustCompile(`\.Route\(\s*"([^"]+)"\s*,\s*func`)

	// Express.js routes: captures (receiver).(method)("path") — filtered by allowlist
	expressRouteRe = regexp.MustCompile(`(\w+)\.(get|post|put|delete|patch)\(\s*["'` + "`" + `]([^"'` + "`" + `]+)["'` + "`" + `]`)

	// Express.js handler reference: captures (receiver).(method)("path", ..., handler)
	expressHandlerRe = regexp.MustCompile(`(\w+)\.(get|post|put|delete|patch)\(\s*["'` + "`" + `][^"'` + "`" + `]+["'` + "`" + `]\s*(?:,\s*[\w.]+)*,\s*([\w.]+)\s*\)`)

	// Java Spring annotations: @GetMapping("/path"), @PostMapping, @RequestMapping
	springMappingRe = regexp.MustCompile(`@(Get|Post|Put|Delete|Patch|Request)Mapping\(\s*(?:value\s*=\s*)?["']([^"']+)["']`)

	// Rust Actix annotations: #[get("/path")], #[post("/path")]
	actixRouteRe = regexp.MustCompile(`#\[(get|post|put|delete|patch)\(\s*"([^"]+)"`)

	// Rust Axum routes: Router::new().route("/path", get(handler))
	// Captures path (group 1), HTTP method (group 2), handler symbol (group 3).
	// Method must be lowercase (axum convention: get/post/put/delete/patch);
	// handler is a bare identifier or `module::handler`.
	axumRouteRe = regexp.MustCompile(`\.route\s*\(\s*"([^"]+)"\s*,\s*(get|post|put|delete|patch|head|options|trace|connect)\s*\(\s*([\w:]+)\s*\)`)

	// Phase C (2026-05-08): actix-web BUILDER style.
	// Pattern: `.route(PATH, web::METHOD().to(HANDLER))` — distinct from
	// axum's `.route(PATH, METHOD(HANDLER))` by requiring the `web::` prefix
	// on the method, which actix-web uses but axum does not. PSM has 206
	// of these (60% of its HTTP route surface) currently uncovered.
	// The trailing `\s*\)` matches the OUTER `.route(...)` closing paren so
	// the depth-tracking loop in extractActixBuilderRoutes doesn't drift
	// when stepping past the match. Without it, `.route(`'s `(` is consumed
	// inside the match but its `)` is left for the loop, decrementing depth
	// without a paired increment and prematurely popping scopes.
	// Phase B (2026-05-08, plan 2026-05-08-d-implement-actix-extension):
	// extended to handle the 142 of 206 PSM actix-builder routes the v1
	// regex missed. Three changes:
	//   1. (?s) flag — `\s` and `[^()]*` cross newlines so multi-line
	//      `.route(\n  PATH,\n  web::get().to(HANDLER)\n)` matches.
	//   2. (?:[\w:]+::)? — optional namespace prefix before the method
	//      builder. Matches `web::get()`, `paperclip::actix::web::get()`,
	//      and bare `get()` (after `use actix_web::web::get;`).
	//   3. Trailing `)` of outer `.route(...)` consumed (unchanged from v1)
	//      so the depth-tracking loop doesn't drift.
	// PSM Phase 3.5 sample of uncovered shapes:
	//   apid/src/main.rs:499         multi-line axum-shape (skipped — different framework)
	//   assetman/src/auth/mod.rs:42  multi-line web::get
	//   assetman/src/routes/http.rs:68 paperclip::actix::web::get
	//   assetman/src/routes/mod.rs:43 bare get() after `use ... ::get`
	actixBuilderRouteRe = regexp.MustCompile(`(?s)\.route\s*\(\s*"([^"]*)"\s*,\s*(?:[\w:]+::)?(get|post|put|delete|patch|head|options|trace|connect)\s*\(\s*\)\s*\.to\s*\(\s*([\w:]+)\s*\)\s*,?\s*\)`)

	// Phase C: scope path declaration. Tracked by paren-depth bookkeeping
	// to support nested `web::scope("/api/v1").service(web::scope("/device")...)`.
	// Captures the scope's path literal.
	actixScopeRe = regexp.MustCompile(`web::scope\s*\(\s*"([^"]*)"\s*\)`)

	// PHP Laravel routes: Route::get("/path", Route::post("/path"
	laravelRouteRe = regexp.MustCompile(`Route::(get|post|put|delete|patch)\(\s*["']([^"']+)["']`)

	// C# ASP.NET route attributes: [HttpGet("/path")], [Route("/path")]
	aspnetRouteRe     = regexp.MustCompile(`\[(Http(?:Get|Post|Put|Delete|Patch))\(\s*"([^"]+)"`)
	aspnetRouteAttrRe = regexp.MustCompile(`\[Route\(\s*"([^"]+)"`)

	// Kotlin Ktor routes: get("/path") {, post("/path") {
	ktorRouteRe = regexp.MustCompile(`\b(get|post|put|delete|patch)\(\s*"([^"]+)"\s*\)`)

	// Laravel handler: Route::get("/path", [Controller::class, "method"]) or "Controller@method"
	laravelHandlerArrayRe = regexp.MustCompile(`Route::(get|post|put|delete|patch)\(\s*["'][^"']+["']\s*,\s*\[(\w+)::class\s*,\s*["'](\w+)["']\]`)
	laravelHandlerAtRe    = regexp.MustCompile(`Route::(get|post|put|delete|patch)\(\s*["'][^"']+["']\s*,\s*["'](\w+)@(\w+)["']`)

	// URL patterns in source: https://host/path or http://host/path — captures domain and path
	// Phase H4 (Plan 8-Phase Arc, 2026-05-09): host group extended to
	// accept :port. Pre-Phase-H, the host class `[a-zA-Z0-9.\-]+` rejected
	// `localhost:9090` so URLs in shell scripts like
	// `curl http://localhost:9090/api/health` failed extraction. Adding
	// `:` makes hostname:port match.
	urlRe = regexp.MustCompile(`https?://([a-zA-Z0-9.\-:]+)(/[a-zA-Z0-9_/:.\-]+)`)

	// Path-only patterns: "/api/something" (quoted paths starting with /)
	pathRe = regexp.MustCompile(`["'](/[a-zA-Z0-9_/:.\-]{2,})["']`)

	// C1 (Phase C, Rust reqwest URL extraction): two patterns the existing
	// urlRe / pathRe miss but appear constantly in real reqwest call sites.
	//
	// formatMacroRe captures Rust `format!("...", ...)` and `format!("...")` —
	// the format string itself is exposed for /path extraction. Without
	// this, `format!("{}/api/users", base)` slips past pathRe (the format
	// string's leading char is `{`, not `/`) and the call site emits no
	// HTTP_CALLS edge despite having a fully qualified URL substring.
	formatMacroRe = regexp.MustCompile(`format!\s*\(\s*"([^"]+)"`)

	// pathInFormatRe finds /path-shaped substrings inside a format!() string.
	// Anchored with the same path-charset as pathRe (no quote requirement
	// because we're scanning AFTER unquoting the format string).
	pathInFormatRe = regexp.MustCompile(`/[a-zA-Z][a-zA-Z0-9_/:.\-]*`)

	// rustConstUrlRe captures Rust top-level `const|static|let NAME [: &str]
	// = "URL"` definitions. Scoped to the simplest, most common shapes —
	// covers `const FOO: &str = "https://..."`, `static FOO: &'static str =
	// "/api/x"`, and bare `let FOO = "https://..."` at file-top scope. Used
	// to resolve identifier references inside function bodies that hand
	// the const to `client.get(NAME)`.
	rustConstUrlRe = regexp.MustCompile(`(?m)^\s*(?:pub(?:\s*\([^)]*\))?\s+)?(?:const|static|let)\s+([A-Za-z_][A-Za-z0-9_]*)\s*(?::\s*&?\s*'?[a-zA-Z_]*\s*(?:str|&str)?)?\s*=\s*"((?:https?://[^"\\]+)|/[^"\\]+)"`)

	// C2 (Phase C, TS/JSX fetch reclassification): JavaScript template
	// literals `\`...\`` are the fetch URL form pathRe never sees because
	// pathRe requires `'` or `"` quotes. `fetch(\`/api/users/${id}\`)`
	// carries `/api/users/` inside the backticks; without this, the call
	// emits no HTTP_CALLS edge. Same shape and trailing-slash policy as
	// formatMacroRe + pathInFormatRe.
	templateLiteralRe = regexp.MustCompile("`([^`]+)`")

	// Python WebSocket routes: @app.websocket("/path"), @app.websocket("")
	pyWSRouteRe = regexp.MustCompile(`@\w+\.websocket\(\s*["']([^"']*)["']`)

	// Spring WebSocket: @MessageMapping("/path")
	springWSRe = regexp.MustCompile(`@MessageMapping\(\s*["']([^"']+)["']`)

	// Kotlin Ktor WebSocket: webSocket("/path") {
	ktorWSRe = regexp.MustCompile(`\bwebSocket\(\s*"([^"]+)"\s*\)`)

	// FastAPI prefix: app.include_router(var, prefix="/prefix")
	fastAPIIncludeRe = regexp.MustCompile(`\.include_router\(\s*(\w+)\s*,\s*prefix\s*=\s*["']([^"']+)["']`)

	// Python import: from module.path import var_name
	pyImportRe = regexp.MustCompile(`from\s+([\w.]+)\s+import\s+(\w+)`)

	// Express prefix: app.use("/prefix", routerVar)
	expressUseRe = regexp.MustCompile(`\.use\(\s*["'` + "`" + `]([^"'` + "`" + `]+)["'` + "`" + `]\s*,\s*(\w+)`)

	// JS/TS import patterns for router variable resolution
	jsRequireRe = regexp.MustCompile(`(?:const|let|var)\s+(\w+)\s*=\s*require\(\s*["']([^"']+)["']`)
	jsImportRe  = regexp.MustCompile(`import\s+(\w+)\s+from\s+["']([^"']+)["']`)

	// Path param normalizers
	colonParamRe = regexp.MustCompile(`:[a-zA-Z_]+`)
	braceParamRe = regexp.MustCompile(`\{[a-zA-Z_]+\}`)
)

// expressReceiverAllowlist restricts Express route matching to known router variable names.
// Prevents false positives from req.get("Header"), res.get("key"), map.get("key"), etc.
var expressReceiverAllowlist = map[string]bool{
	"app": true, "router": true, "server": true, "api": true,
	"routes": true, "express": true, "route": true,
}

// clientSidePathSegments are file-path conventions that virtually always
// indicate client-side JavaScript (browser code), not Node.js Express/Koa
// servers. The express extractor's regex matches `<receiver>.METHOD("/path",
// ...)` patterns which can fire on client-side router calls (e.g.
// `app.get(`/api/${id}`)` in a fetch wrapper, or `route.get(...)` in a
// frontend SPA router). Files under these path segments are excluded from
// express route extraction.
//
// 2026-05-08 incident: PSM has 46 Route nodes with handlers under
// `sysmanager/public/scripts/` (e.g. saveInfo, processCalibration,
// refreshBatteryDiagnostics). These are CLIENT-side functions issuing
// fetch() calls; the express extractor mislabeled them as server routes,
// inflating PSM's unlinked-route denominator and creating spurious
// linked-routes (16 of 110 HTTP_CALLS edges pointed at client-side
// "handlers" that can never receive HTTP).
var clientSidePathSegments = []string{
	"/public/scripts/", "/public/js/", "/static/scripts/", "/static/js/",
	"/assets/js/", "/dist/", "/build/", "/.next/", "/.nuxt/",
}

// isClientSideJSPath returns true if the file path matches a known
// client-side JavaScript convention. Path comparison is case-insensitive
// and uses forward slashes; a leading slash is prepended so segments at
// the project root (e.g. "build/output.js") match `/build/`.
func isClientSideJSPath(filePath string) bool {
	if filePath == "" {
		return false
	}
	normalized := "/" + strings.TrimPrefix(
		strings.ReplaceAll(strings.ToLower(filePath), "\\", "/"),
		"/",
	)
	for _, seg := range clientSidePathSegments {
		if strings.Contains(normalized, seg) {
			return true
		}
	}
	return false
}

// hasExpressServerEvidence returns true when the source contains
// instantiation evidence of an Express/Koa/Fastify server. Server-side
// JS virtually always has `express()`, `Router()`, `require('express')`,
// or `from 'express'` somewhere in the file. Client-side JS that happens
// to have `app.get(...)` or `route.get(...)` patterns lacks these.
//
// This is a content-check FALLBACK applied to per-function extraction
// where path heuristics are insufficient.
func hasExpressServerEvidence(source string) bool {
	// Cheap substring checks before any regex
	if strings.Contains(source, "express(") {
		return true
	}
	if strings.Contains(source, "Router(") {
		return true
	}
	if strings.Contains(source, "require('express')") || strings.Contains(source, `require("express")`) {
		return true
	}
	if strings.Contains(source, "from 'express'") || strings.Contains(source, `from "express"`) {
		return true
	}
	// Fastify / Koa server signatures
	if strings.Contains(source, "fastify(") || strings.Contains(source, "Koa(") || strings.Contains(source, "new Koa(") {
		return true
	}
	return false
}

// File extension helpers to scope framework-specific route regexes.
// Use filepath.Ext + ToLower for case-insensitive matching.
func isGoFile(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == ".go"
}

// isJSFile returns true for client-or-server JavaScript/TypeScript files.
// Used by the broader graph layer (CALLS, USAGE, etc.) where React/Vue
// component files matter equally with Node.js server files.
func isJSFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".js", ".ts", ".jsx", ".tsx", ".mjs", ".cjs", ".mts", ".cts":
		return true
	}
	return false
}

// isServerJSFile returns true for JavaScript/TypeScript files that
// could host an Express/Fastify/Koa-style HTTP server. Deliberately
// EXCLUDES `.jsx` / `.tsx` — those are React component files; any
// `.get('/foo')` / `.post('/bar')` matches in JSX are virtually always
// client-side handlers (router wiring, fetch wrappers, mocked routes
// in Storybook), not server-mounted routes. PSM 2026-05-07 baseline:
// 144 Route nodes; the JSX/TSX subset that this filter removes was the
// dominant false-positive source per the test battery.
func isServerJSFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".js", ".ts", ".mjs", ".cjs", ".mts", ".cts":
		return true
	}
	return false
}

func isPHPFile(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == ".php"
}

func isKotlinFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".kt" || ext == ".kts"
}

// Run executes the HTTP linking pass.
func (l *Linker) Run() ([]HTTPLink, error) {
	proj, err := l.store.GetProject(l.project)
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	rootPath := proj.RootPath

	// Load config from project root if using defaults
	if l.config.HTTPLinker.MinConfidence == nil &&
		l.config.HTTPLinker.FuzzyMatching == nil &&
		len(l.config.HTTPLinker.ExcludePaths) == 0 {
		l.config = LoadConfig(rootPath)
	}

	l.routesByFunc = make(map[string][]int)
	routes := l.discoverRoutes(rootPath)
	slog.Info("httplink.routes", "count", len(routes))

	// Build routesByFunc index
	for i, rh := range routes {
		l.routesByFunc[rh.QualifiedName] = append(l.routesByFunc[rh.QualifiedName], i)
	}

	// Resolve cross-file group prefixes before inserting Route nodes
	l.resolveCrossFileGroupPrefixes(routes, rootPath) // Go gin
	l.resolveFastAPIPrefixes(routes, rootPath)        // Python FastAPI
	l.resolveExpressPrefixes(routes, rootPath)        // JS/TS Express

	// Resolve handler references first so insertRouteNodes can use the actual handler QN
	l.createRegistrationCallEdges(routes)

	// Insert Route nodes and HANDLES edges (uses ResolvedHandlerQN from above)
	l.insertRouteNodes(routes, rootPath)

	callSites := l.discoverCallSites(rootPath)
	callSites = append(callSites, l.extraCallSites...)
	slog.Info("httplink.callsites", "count", len(callSites))

	links := l.matchAndLink(routes, callSites)
	slog.Info("httplink.links", "count", len(links))

	return links, nil
}

// insertRouteNodes creates Route nodes for each discovered route handler and
// HANDLES edges from the handler function to the Route node.
func (l *Linker) insertRouteNodes(routes []RouteHandler, rootPath string) {
	for i := range routes {
		l.insertSingleRouteNode(&routes[i], rootPath)
	}
	slog.Info("httplink.route_nodes", "count", len(routes))
}

// insertSingleRouteNode creates a Route node and HANDLES edge for one route handler.
func (l *Linker) insertSingleRouteNode(rh *RouteHandler, rootPath string) {
	normalMethod := rh.Method
	if normalMethod == "" {
		normalMethod = "ANY"
	}
	normalPath := strings.ReplaceAll(rh.Path, "/", "_")
	normalPath = strings.Trim(normalPath, "_")
	routeQN := rh.QualifiedName + ".route." + normalMethod + "." + normalPath
	routeName := normalMethod + " " + rh.Path

	// Use resolved handler QN if available, otherwise fall back to registering function
	handlerQN := rh.QualifiedName
	if rh.ResolvedHandlerQN != "" {
		handlerQN = rh.ResolvedHandlerQN
	}

	// Look up handler node BEFORE creating Route — we need its file_path
	handlerNode, _ := l.store.FindNodeByQN(l.project, handlerQN)

	routeProps := l.buildRouteProps(rh, handlerNode, rootPath)

	// Inherit file_path and line range from handler function
	var filePath string
	var startLine, endLine int
	if handlerNode != nil {
		if handlerNode.FilePath != "" {
			filePath = handlerNode.FilePath
		}
		startLine = handlerNode.StartLine
		endLine = handlerNode.EndLine
	}

	routeID, err := l.store.UpsertNode(&store.Node{
		Project:       l.project,
		Label:         "Route",
		Name:          routeName,
		QualifiedName: routeQN,
		FilePath:      filePath,
		StartLine:     startLine,
		EndLine:       endLine,
		Properties:    routeProps,
	})
	if err != nil || routeID == 0 {
		return
	}

	l.linkHandlerToRoute(handlerNode, routeID, routeQN)
}

// buildRouteProps constructs the properties map for a Route node, including protocol detection.
func (l *Linker) buildRouteProps(rh *RouteHandler, handlerNode *store.Node, rootPath string) map[string]any {
	handlerQN := rh.QualifiedName
	if rh.ResolvedHandlerQN != "" {
		handlerQN = rh.ResolvedHandlerQN
	}

	routeProps := map[string]any{
		"method":  rh.Method,
		"path":    rh.Path,
		"handler": handlerQN,
	}

	// Phase B1 (2026-05-08): tag the Route node with the extractor that
	// produced it so downstream queries (psm_compare.py routes-by-extractor)
	// can attribute coverage gaps to the right extractor.
	if rh.SourceExtractor != "" {
		routeProps["extractor"] = rh.SourceExtractor
	}

	// Protocol from route extraction (Python websocket, Spring MessageMapping, Ktor webSocket)
	if rh.Protocol != "" {
		routeProps["protocol"] = rh.Protocol
		return routeProps
	}

	// Detect protocol from handler source if not already set
	if handlerNode != nil && handlerNode.FilePath != "" && handlerNode.StartLine > 0 {
		handlerSource := readSourceLines(rootPath, handlerNode.FilePath, handlerNode.StartLine, handlerNode.EndLine)
		if protocol := detectProtocol(handlerSource); protocol != "" {
			routeProps["protocol"] = protocol
		}
	}

	return routeProps
}

// linkHandlerToRoute creates HANDLES edge and marks handler as entry point.
func (l *Linker) linkHandlerToRoute(handlerNode *store.Node, routeID int64, routeQN string) {
	if handlerNode == nil {
		return
	}

	if _, edgeErr := l.store.InsertEdge(&store.Edge{
		Project:  l.project,
		SourceID: handlerNode.ID,
		TargetID: routeID,
		Type:     "HANDLES",
		Properties: map[string]any{
			"confidence_tier": store.ConfidenceInferred,
		},
	}); edgeErr != nil {
		// FK failures expected: LastInsertId() can return stale IDs for upserted Route nodes
		slog.Info("httplink.handles_edge.skip", "route", routeQN)
	}

	// Mark handler as entry point (for dead code detection)
	if handlerNode.Properties == nil {
		handlerNode.Properties = map[string]any{}
	}
	handlerNode.Properties["is_entry_point"] = true
	if _, upsertErr := l.store.UpsertNode(handlerNode); upsertErr != nil {
		slog.Warn("httplink.entry_point.err", "err", upsertErr)
	}
}

// isTestNode returns true if the node is from a test file.
// Checks the is_test property set during pipeline pass 1, with a file path heuristic fallback.
func isTestNode(n *store.Node) bool {
	if isTest, ok := n.Properties["is_test"].(bool); ok && isTest {
		return true
	}
	// Fallback: common test path patterns
	fp := filepath.ToSlash(n.FilePath)
	return containsTestSegment(fp, "test") ||
		containsTestSegment(fp, "tests") ||
		containsTestSegment(fp, "__tests__") ||
		strings.Contains(fp, "_test.") ||
		strings.Contains(fp, ".test.") ||
		strings.Contains(fp, ".spec.")
}

// containsTestSegment checks if a path contains a directory segment named seg.
// Matches both "seg/..." (at start) and ".../seg/..." (mid-path).
func containsTestSegment(fp, seg string) bool {
	return strings.HasPrefix(fp, seg+"/") || strings.Contains(fp, "/"+seg+"/")
}

// discoverRoutes finds route handler registrations from Function nodes.
//
//nolint:gocognit // WHY: inherent complexity from multi-framework route discovery
func (l *Linker) discoverRoutes(rootPath string) []RouteHandler {
	var routes []RouteHandler

	funcs, err := l.store.FindNodesByLabel(l.project, "Function")
	if err != nil {
		slog.Warn("httplink.routes.funcs.err", "err", err)
		return routes
	}

	methods, err := l.store.FindNodesByLabel(l.project, "Method")
	if err != nil {
		slog.Warn("httplink.routes.methods.err", "err", err)
	} else {
		funcs = append(funcs, methods...)
	}

	// Track which PHP files have Function/Method nodes (for dedup in module scan)
	phpFilesWithFuncs := map[string]bool{}

	// Phase B1 (2026-05-08): per-extractor counters. Surfaces which extractor
	// contributed which RouteHandler entries. The pre-instrumentation state
	// gave a single aggregate count; post-instrumentation reveals which
	// extractor is producing 0 routes (= covered shape missing for that
	// language) vs which is dominant. Drives the PSM-first methodology.
	extractorCounts := map[string]int{}
	tagAndCount := func(name string, newRoutes []RouteHandler) []RouteHandler {
		extractorCounts[name] += len(newRoutes)
		for i := range newRoutes {
			if newRoutes[i].SourceExtractor == "" {
				newRoutes[i].SourceExtractor = name
			}
		}
		return newRoutes
	}

	for _, f := range funcs {
		// Skip test files — test fixtures should not produce Route nodes
		if isTestNode(f) {
			continue
		}

		// Python: check decorators property
		routes = append(routes, tagAndCount("python", extractPythonRoutes(f))...)

		// Java: check annotation-based decorators (Spring)
		routes = append(routes, tagAndCount("java", extractJavaRoutes(f))...)

		// Rust: check attribute decorators (Actix)
		routes = append(routes, tagAndCount("rust-actix-attribute", extractRustRoutes(f))...)

		// C# ASP.NET: check attribute decorators
		routes = append(routes, tagAndCount("aspnet", extractASPNetRoutes(f))...)

		// Source-based route discovery — each framework's regex only applies to its own file types
		if f.FilePath != "" && f.StartLine > 0 && f.EndLine > 0 {
			source := readSourceLines(rootPath, f.FilePath, f.StartLine, f.EndLine)
			if source != "" {
				if isGoFile(f.FilePath) {
					routes = append(routes, tagAndCount("go", extractGoRoutes(f, source))...)
				}
				if isServerJSFile(f.FilePath) {
					routes = append(routes, tagAndCount("express", extractExpressRoutes(f, source))...)
				}
				if isPHPFile(f.FilePath) {
					routes = append(routes, tagAndCount("laravel", extractLaravelRoutes(f, source))...)
				}
				if isKotlinFile(f.FilePath) {
					routes = append(routes, tagAndCount("ktor", extractKtorRoutes(f, source))...)
				}
				if isRustFile(f.FilePath) {
					// Phase D2 (2026-05-07): axum builder-style routes.
					// Actix attribute routes are handled above by extractRustRoutes.
					routes = append(routes, tagAndCount("rust-axum-builder", extractAxumRoutes(f, source))...)
					// Phase C (2026-05-08): actix-web builder-style routes.
					// Pattern: .route(PATH, web::METHOD().to(HANDLER)) inside
					// optional web::scope("/prefix") chains. PSM's sysmanager
					// service uses this shape exclusively (206 PSM routes).
					routes = append(routes, tagAndCount("rust-actix-builder", extractActixBuilderRoutes(f, source))...)
				}
			}
		}

		if strings.HasSuffix(f.FilePath, ".php") {
			phpFilesWithFuncs[f.FilePath] = true
		}
	}

	// Defer the routes.extractor.summary emit — module-scan extractors
	// (Express on JS modules, Laravel on PHP modules) also contribute.
	// We emit after both function-loop and module-loop complete.
	defer func() {
		slog.Info(
			"routes.extractor.summary",
			"counts", extractorCounts,
			"total", len(routes),
		)
	}()

	// Module-level route scanning: some frameworks register routes at file top level
	// (not inside any function body). Scan modules for route patterns.
	modules, err := l.store.FindNodesByLabel(l.project, "Module")
	if err != nil {
		slog.Warn("httplink.routes.modules.err", "err", err)
		return routes
	}

	for _, m := range modules {
		// Skip test files
		if isTestNode(m) {
			continue
		}

		isPHP := strings.HasSuffix(m.FilePath, ".php")
		// Server-side JS/TS only — excludes .jsx/.tsx (React client).
		// See isServerJSFile comment for the rationale.
		isJSTS := isServerJSFile(m.FilePath)

		if !isPHP && !isJSTS {
			continue
		}
		// For PHP: skip files where routes were already extracted from function bodies
		if isPHP && phpFilesWithFuncs[m.FilePath] {
			continue
		}
		source := readSourceFull(rootPath, m.FilePath)
		if source == "" {
			continue
		}
		if isPHP {
			routes = append(routes, tagAndCount("laravel-module", extractLaravelRoutes(m, source))...)
		}
		if isJSTS {
			// Content-evidence skip: at the module level, the whole-file
			// source should contain `express()` / `Router()` / similar
			// server instantiation if this is a real Node.js server file.
			// Client-side modules that happen to have app.get(...) shapes
			// (frontend SPA routers, fetch wrappers) lack these signals.
			// Combined with the path-segment skip inside extractExpressRoutes,
			// this drops the 2026-05-08 PSM false-positive bucket
			// (46 routes from public/scripts/*.js modules).
			if !hasExpressServerEvidence(source) {
				continue
			}
			routes = append(routes, tagAndCount("express-module", extractExpressRoutes(m, source))...)
		}
	}

	return routes
}

// extractPythonRoutes extracts route handlers from Python decorator metadata.
func extractPythonRoutes(f *store.Node) []RouteHandler {
	var routes []RouteHandler

	decs, ok := f.Properties["decorators"]
	if !ok {
		return routes
	}

	// decorators is stored as []any (JSON deserialized)
	decList, ok := decs.([]any)
	if !ok {
		return routes
	}

	for _, d := range decList {
		decStr, ok := d.(string)
		if !ok {
			continue
		}
		// Standard HTTP routes
		matches := pyRouteRe.FindAllStringSubmatch(decStr, -1)
		for _, m := range matches {
			routes = append(routes, RouteHandler{
				Path:          m[2],
				Method:        strings.ToUpper(m[1]),
				FunctionName:  f.Name,
				QualifiedName: f.QualifiedName,
			})
		}
		// WebSocket routes: @app.websocket("/path")
		if wm := pyWSRouteRe.FindStringSubmatch(decStr); wm != nil {
			routes = append(routes, RouteHandler{
				Path:          wm[1],
				Method:        "WS",
				Protocol:      "ws",
				FunctionName:  f.Name,
				QualifiedName: f.QualifiedName,
			})
		}
	}

	return routes
}

// extractGoRoutes extracts route registrations from Go source code (gin/chi patterns).
// Resolves gin group prefixes and chi r.Route("/prefix", func...) blocks.
func extractGoRoutes(f *store.Node, source string) []RouteHandler {
	routes := make([]RouteHandler, 0, 4)

	// Build a map of variable name → group prefix from gin Group() calls
	groupPrefixes := map[string]string{}
	for _, gm := range goGroupRe.FindAllStringSubmatch(source, -1) {
		groupPrefixes[gm[1]] = gm[2]
	}

	// Chi Route() prefix stack for nested r.Route("/prefix", func...) blocks
	type chiBlock struct {
		prefix string
		depth  int
	}
	var chiStack []chiBlock
	braceDepth := 0

	lines := strings.Split(source, "\n")
	for _, line := range lines {
		// Detect chi .Route("/prefix", func...) blocks
		if cm := goChiRouteRe.FindStringSubmatch(line); cm != nil {
			chiStack = append(chiStack, chiBlock{prefix: cm[1], depth: braceDepth})
		}

		// Track brace depth
		braceDepth += strings.Count(line, "{") - strings.Count(line, "}")

		// Pop closed chi blocks
		for len(chiStack) > 0 && braceDepth <= chiStack[len(chiStack)-1].depth {
			chiStack = chiStack[:len(chiStack)-1]
		}

		rm := goRouteRe.FindStringSubmatch(line)
		if rm == nil {
			continue
		}
		method := strings.ToUpper(rm[1])
		path := rm[2]

		// Apply chi prefix stack if active, otherwise try gin group resolution
		if len(chiStack) > 0 {
			var fullPrefix string
			for _, block := range chiStack {
				fullPrefix = strings.TrimRight(fullPrefix, "/") + "/" + strings.TrimLeft(block.prefix, "/")
			}
			if path == "/" || path == "" {
				path = fullPrefix
			} else {
				path = strings.TrimRight(fullPrefix, "/") + "/" + strings.TrimLeft(path, "/")
			}
		} else {
			path = resolveGroupPrefix(line, rm[1], path, groupPrefixes)
		}

		// Capture handler reference (last argument) for CALLS edge creation
		var handlerRef string
		if hm := goRouteHandlerRe.FindStringSubmatch(line); hm != nil {
			handlerRef = hm[2]
		}

		routes = append(routes, RouteHandler{
			Path:          path,
			Method:        method,
			FunctionName:  f.Name,
			QualifiedName: f.QualifiedName,
			HandlerRef:    handlerRef,
		})
	}

	return routes
}

// resolveGroupPrefix resolves a router group prefix for a route line.
// It finds the receiver variable (e.g., "contracts" in "contracts.POST")
// and looks up its group prefix.
func resolveGroupPrefix(line, method, path string, groupPrefixes map[string]string) string {
	idx := strings.Index(line, "."+method+"(")
	if idx <= 0 {
		return path
	}
	prefix := strings.TrimSpace(line[:idx])
	parts := strings.Fields(prefix)
	if len(parts) == 0 {
		return path
	}
	receiver := parts[len(parts)-1]
	gp, ok := groupPrefixes[receiver]
	if !ok {
		return path
	}
	resolved := strings.TrimRight(gp, "/") + "/" + strings.TrimLeft(path, "/")
	if resolved == "/" {
		return gp
	}
	return resolved
}

// extractExpressRoutes extracts route registrations from JS/TS source (Express/Koa patterns).
// Uses receiver allowlist to avoid false positives from req.get(), res.get(), etc.
//
// 2026-05-08: also filters by path-segment when the file path matches a
// known client-side convention (/public/scripts/, /static/js/, /dist/, etc.).
// Without this filter, the regex fires on client-side JS doing e.g.
// `app.get(\`/api/${x}\`)` in a fetch wrapper. PSM 2026-05-08 baseline: 46
// false-positive routes from sysmanager/public/scripts/*.js were mislabeled
// as server routes. Source-content evidence (presence of `express()` /
// `Router()` instantiation) is enforced ONLY at the module-level call site
// because per-function source is just the function body and won't contain
// the module-level express() instantiation.
func extractExpressRoutes(f *store.Node, source string) []RouteHandler {
	routes := make([]RouteHandler, 0, 4)
	// Path-segment skip: known client-side conventions. Applies universally
	// (per-function and per-module). Empty FilePath (test fixtures) is a no-op
	// since no segments match.
	if isClientSideJSPath(f.FilePath) {
		return routes
	}
	for _, line := range strings.Split(source, "\n") {
		rm := expressRouteRe.FindStringSubmatch(line)
		if rm == nil {
			continue
		}
		// rm[1]=receiver, rm[2]=method, rm[3]=path
		receiver := strings.ToLower(rm[1])
		if !expressReceiverAllowlist[receiver] {
			continue
		}

		// Express overloads .get(): with 1 arg it's a config getter (app.get('trust proxy')),
		// with 2+ args it's a route (app.get('/path', handler)). Only .get() has this
		// overload — .post/.put/.delete/.patch are always routes.
		if strings.EqualFold(rm[2], "get") {
			// Check if there's a comma after the closing quote — indicates a callback/handler arg
			matchEnd := strings.Index(line, rm[0]) + len(rm[0])
			rest := strings.TrimSpace(line[matchEnd:])
			if !strings.HasPrefix(rest, ",") {
				continue // Single-arg app.get('setting') — config getter, skip
			}
		}

		var handlerRef string
		hm := expressHandlerRe.FindStringSubmatch(line)
		if hm != nil {
			handlerRef = hm[3] // group 3 after adding receiver capture
		}

		routes = append(routes, RouteHandler{
			Path:          rm[3],
			Method:        strings.ToUpper(rm[2]),
			FunctionName:  f.Name,
			QualifiedName: f.QualifiedName,
			HandlerRef:    handlerRef,
		})
	}
	return routes
}

// extractJavaRoutes extracts routes from Java Spring annotations in decorators.
func extractJavaRoutes(f *store.Node) []RouteHandler {
	var routes []RouteHandler
	decs, ok := f.Properties["decorators"]
	if !ok {
		return routes
	}
	decList, ok := decs.([]any)
	if !ok {
		return routes
	}
	for _, d := range decList {
		decStr, ok := d.(string)
		if !ok {
			continue
		}
		// Standard Spring HTTP mappings
		matches := springMappingRe.FindAllStringSubmatch(decStr, -1)
		for _, m := range matches {
			method := strings.ToUpper(m[1])
			if method == "REQUEST" {
				method = "" // RequestMapping doesn't specify method
			}
			routes = append(routes, RouteHandler{
				Path:          m[2],
				Method:        method,
				FunctionName:  f.Name,
				QualifiedName: f.QualifiedName,
			})
		}
		// Spring WebSocket: @MessageMapping("/path")
		if wm := springWSRe.FindStringSubmatch(decStr); wm != nil {
			routes = append(routes, RouteHandler{
				Path:          wm[1],
				Method:        "WS",
				Protocol:      "ws",
				FunctionName:  f.Name,
				QualifiedName: f.QualifiedName,
			})
		}
	}
	return routes
}

// extractRustRoutes extracts routes from Rust Actix attribute decorators.
func extractRustRoutes(f *store.Node) []RouteHandler {
	var routes []RouteHandler
	decs, ok := f.Properties["decorators"]
	if !ok {
		return routes
	}
	decList, ok := decs.([]any)
	if !ok {
		return routes
	}
	for _, d := range decList {
		decStr, ok := d.(string)
		if !ok {
			continue
		}
		matches := actixRouteRe.FindAllStringSubmatch(decStr, -1)
		for _, m := range matches {
			routes = append(routes, RouteHandler{
				Path:          m[2],
				Method:        strings.ToUpper(m[1]),
				FunctionName:  f.Name,
				QualifiedName: f.QualifiedName,
			})
		}
	}
	return routes
}

// extractAxumRoutes extracts Rust Axum route registrations from
// function source. Pattern: `Router::new().route("/path", get(handler))`.
//
// Phase D2 (2026-05-07): closes the recall gap where Rust services
// using axum (rather than actix) had no route extraction at all.
// The actix path uses attribute decorators (#[get("/path")]); axum
// declares routes inline via builder calls. The handler symbol is
// captured for downstream HandlerRef resolution but is not currently
// used to resolve to a Function node — caller registration logic is
// shared with other extractors and resolves by name match.
func extractAxumRoutes(f *store.Node, source string) []RouteHandler {
	routes := make([]RouteHandler, 0, 4)
	for _, line := range strings.Split(source, "\n") {
		matches := axumRouteRe.FindAllStringSubmatch(line, -1)
		for _, m := range matches {
			// m[1]=path, m[2]=method, m[3]=handler
			handler := m[3]
			// Strip module prefix from handler ref (e.g. `routes::list_users`
			// → `list_users`) so HandlerRef matches the Function node's Name.
			if idx := strings.LastIndex(handler, "::"); idx >= 0 {
				handler = handler[idx+2:]
			}
			routes = append(routes, RouteHandler{
				Path:          m[1],
				Method:        strings.ToUpper(m[2]),
				FunctionName:  f.Name,
				QualifiedName: f.QualifiedName,
				HandlerRef:    handler,
			})
		}
	}
	return routes
}

// isRustFile returns true for `.rs` source files. Rust route extractors
// (Actix attribute-based + Axum builder-based) are gated by this so
// non-Rust files don't get scanned with Rust-specific regexes.
func isRustFile(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == ".rs"
}

// extractActixBuilderRoutes extracts Rust actix-web BUILDER-style route
// registrations from function source. Pattern:
//   web::scope("/prefix")
//       .route(PATH, web::METHOD().to(HANDLER))
//
// Phase C (2026-05-08, plan 2026-05-08-psm-first-code-graph-search-fix):
// closes the dominant HTTP_CALLS recall gap on PSM. Pre-Phase C, the only
// actix path was attribute-style (#[get("/path")]) via extractRustRoutes.
// PSM's sysmanager service uses the builder shape — 206 routes uncovered
// (~60% of PSM's HTTP route surface).
//
// Scope nesting: paths concatenate. `web::scope("/api/v1")` containing
// `web::scope("/device")` containing `.route("/timeline", ...)` produces
// path `/api/v1/device/timeline`. Tracked via paren-depth bookkeeping
// because scopes are chained through `.service(...)` calls and the
// closing depth is what bounds the scope's lifetime.
//
// Empty-path routes (`.route("", web::get().to(...))`) emit at the parent
// scope path itself; the trailing-slash trim happens when the path
// concatenates to a non-empty parent.
func extractActixBuilderRoutes(f *store.Node, source string) []RouteHandler {
	routes := make([]RouteHandler, 0, 8)
	if !strings.Contains(source, "web::") {
		return routes
	}

	// Each scope entry: (path, depth_at_open).
	// When paren-depth drops back to depth_at_open or below, pop.
	type scopeEntry struct {
		path  string
		depth int
	}
	var scopes []scopeEntry
	depth := 0

	// Walk source character by character. At each position, check whether the
	// substring beginning here matches a scope-open or a route-registration.
	// Track paren depth between matches to know when scopes close.
	for i := 0; i < len(source); i++ {
		ch := source[i]
		switch ch {
		case '(':
			depth++
			continue
		case ')':
			depth--
			// Pop scopes whose declared depth is now greater than the
			// new current depth — those scopes' enclosing context just
			// closed.
			for len(scopes) > 0 && scopes[len(scopes)-1].depth > depth {
				scopes = scopes[:len(scopes)-1]
			}
			continue
		}
		// Try scope match starting at this position
		if i+len("web::scope") < len(source) && source[i] == 'w' {
			if loc := actixScopeRe.FindStringSubmatchIndex(source[i:]); loc != nil && loc[0] == 0 {
				// loc[0]..loc[1] is the full scope match; loc[2]..loc[3] is the captured path
				scopePath := source[i+loc[2] : i+loc[3]]
				// Push at the CURRENT depth — that's the depth of the
				// scope's enclosing context (the parent .service() or
				// cfg.service() paren). Scope is active until current
				// depth drops below this value.
				scopes = append(scopes, scopeEntry{path: scopePath, depth: depth})
				// Skip ahead past the matched scope literal to avoid re-matching the path
				// inside route extraction (the inner `"PATH"` won't trigger the route regex
				// without a preceding `.route(`).
				i += loc[1] - 1
				continue
			}
		}
		// Try route match starting at this position
		if ch == '.' {
			if loc := actixBuilderRouteRe.FindStringSubmatchIndex(source[i:]); loc != nil && loc[0] == 0 {
				routePath := source[i+loc[2] : i+loc[3]]
				method := strings.ToUpper(source[i+loc[4] : i+loc[5]])
				handler := source[i+loc[6] : i+loc[7]]
				if hidx := strings.LastIndex(handler, "::"); hidx >= 0 {
					handler = handler[hidx+2:]
				}
				// Concatenate scope chain + route path
				var sb strings.Builder
				for _, s := range scopes {
					sb.WriteString(s.path)
				}
				sb.WriteString(routePath)
				fullPath := sb.String()
				// Strip trailing slash unless path is exactly "/" (root)
				if len(fullPath) > 1 && strings.HasSuffix(fullPath, "/") {
					fullPath = fullPath[:len(fullPath)-1]
				}
				if fullPath == "" {
					// Skip degenerate empty paths — happens if both scope chain
					// and route path are empty strings.
					i += loc[1] - 1
					continue
				}
				routes = append(routes, RouteHandler{
					Path:          fullPath,
					Method:        method,
					FunctionName:  f.Name,
					QualifiedName: f.QualifiedName,
					HandlerRef:    handler,
				})
				i += loc[1] - 1
				continue
			}
		}
	}

	return routes
}

// extractLaravelRoutes extracts route registrations from PHP Laravel source.
func extractLaravelRoutes(f *store.Node, source string) []RouteHandler {
	routes := make([]RouteHandler, 0, 4)
	for _, line := range strings.Split(source, "\n") {
		rm := laravelRouteRe.FindStringSubmatch(line)
		if rm == nil {
			continue
		}

		// Try to extract handler reference from [Controller::class, "method"] or "Controller@method"
		var handlerRef string
		if am := laravelHandlerArrayRe.FindStringSubmatch(line); am != nil {
			handlerRef = am[3] // method name from [Controller::class, "method"]
		} else if atm := laravelHandlerAtRe.FindStringSubmatch(line); atm != nil {
			handlerRef = atm[3] // method name from "Controller@method"
		}

		routes = append(routes, RouteHandler{
			Path:          rm[2],
			Method:        strings.ToUpper(rm[1]),
			FunctionName:  f.Name,
			QualifiedName: f.QualifiedName,
			HandlerRef:    handlerRef,
		})
	}
	return routes
}

// extractASPNetRoutes extracts route handlers from C# ASP.NET attribute metadata.
func extractASPNetRoutes(f *store.Node) []RouteHandler {
	var routes []RouteHandler

	decs, ok := f.Properties["decorators"]
	if !ok {
		return routes
	}

	decList, ok := decs.([]any)
	if !ok {
		return routes
	}

	for _, d := range decList {
		decStr, ok := d.(string)
		if !ok {
			continue
		}
		// [HttpGet("/path")] pattern
		matches := aspnetRouteRe.FindAllStringSubmatch(decStr, -1)
		for _, m := range matches {
			method := strings.TrimPrefix(m[1], "Http")
			routes = append(routes, RouteHandler{
				Path:          m[2],
				Method:        strings.ToUpper(method),
				FunctionName:  f.Name,
				QualifiedName: f.QualifiedName,
			})
		}
		// [Route("/path")] pattern
		routeMatches := aspnetRouteAttrRe.FindAllStringSubmatch(decStr, -1)
		for _, m := range routeMatches {
			routes = append(routes, RouteHandler{
				Path:          m[1],
				Method:        "",
				FunctionName:  f.Name,
				QualifiedName: f.QualifiedName,
			})
		}
	}
	return routes
}

// extractKtorRoutes extracts route handlers from Kotlin Ktor source code.
func extractKtorRoutes(f *store.Node, source string) []RouteHandler {
	routes := make([]RouteHandler, 0, 4)
	for _, line := range strings.Split(source, "\n") {
		// Standard HTTP routes
		if rm := ktorRouteRe.FindStringSubmatch(line); rm != nil {
			routes = append(routes, RouteHandler{
				Path:          rm[2],
				Method:        strings.ToUpper(rm[1]),
				FunctionName:  f.Name,
				QualifiedName: f.QualifiedName,
			})
			continue
		}
		// WebSocket routes: webSocket("/path") {
		if wm := ktorWSRe.FindStringSubmatch(line); wm != nil {
			routes = append(routes, RouteHandler{
				Path:          wm[1],
				Method:        "WS",
				Protocol:      "ws",
				FunctionName:  f.Name,
				QualifiedName: f.QualifiedName,
			})
		}
	}
	return routes
}

// discoverCallSites finds HTTP URL references in Module constants and Function source.
func (l *Linker) discoverCallSites(rootPath string) []HTTPCallSite {
	var sites []HTTPCallSite

	// Module constants
	modules, err := l.store.FindNodesByLabel(l.project, "Module")
	if err != nil {
		slog.Warn("httplink.callsites.modules.err", "err", err)
	} else {
		for _, m := range modules {
			sites = append(sites, extractModuleCallSites(m)...)
		}
	}

	// Function/Method source
	funcs, err := l.store.FindNodesByLabel(l.project, "Function")
	if err != nil {
		slog.Warn("httplink.callsites.funcs.err", "err", err)
	} else {
		for _, f := range funcs {
			sites = append(sites, extractFunctionCallSites(f, rootPath)...)
		}
	}

	methods, err := l.store.FindNodesByLabel(l.project, "Method")
	if err != nil {
		slog.Warn("httplink.callsites.methods.err", "err", err)
	} else {
		for _, f := range methods {
			sites = append(sites, extractFunctionCallSites(f, rootPath)...)
		}
	}

	return sites
}

// extractModuleCallSites extracts HTTP paths from module constants.
func extractModuleCallSites(m *store.Node) []HTTPCallSite {
	var sites []HTTPCallSite

	constants, ok := m.Properties["constants"]
	if !ok {
		return sites
	}

	constList, ok := constants.([]any)
	if !ok {
		return sites
	}

	for _, c := range constList {
		cStr, ok := c.(string)
		if !ok {
			continue
		}
		paths := extractURLPaths(cStr)
		for _, p := range paths {
			sites = append(sites, HTTPCallSite{
				Path:                p,
				SourceName:          m.Name,
				SourceQualifiedName: m.QualifiedName,
				SourceLabel:         "Module",
			})
		}
	}

	return sites
}

// detectHTTPMethod tries to find the HTTP method used near a URL path in source code.
func detectHTTPMethod(source string) string {
	upper := strings.ToUpper(source)
	for _, verb := range []string{"POST", "PUT", "DELETE", "PATCH", "GET"} {
		// Python: requests.post(, httpx.post(
		if strings.Contains(upper, "REQUESTS."+verb+"(") || strings.Contains(upper, "HTTPX."+verb+"(") {
			return verb
		}
		// Go: "POST" near http.NewRequest
		if strings.Contains(upper, `"`+verb+`"`) && strings.Contains(upper, "HTTP.") {
			return verb
		}
		// JS: method: "POST", method: 'POST'
		if strings.Contains(upper, "METHOD") && strings.Contains(upper, verb) {
			return verb
		}
		// Java: HttpMethod.POST, .method(POST
		if strings.Contains(upper, "HTTPMETHOD."+verb) {
			return verb
		}
		// Rust: reqwest::Client::new().post(, .get(
		if strings.Contains(source, "."+strings.ToLower(verb)+"(") {
			return verb
		}
		// PHP: curl CURLOPT_CUSTOMREQUEST
		if strings.Contains(upper, "CURLOPT") && strings.Contains(upper, verb) {
			return verb
		}
	}
	return ""
}

// httpClientKeywords are patterns indicating actual HTTP client usage.
// A function must contain at least one of these to be considered an HTTP call site.
var httpClientKeywords = []string{
	// Python
	"requests.get", "requests.post", "requests.put", "requests.delete", "requests.patch",
	"httpx.", "aiohttp.", "urllib.request",
	// Go
	"http.Get", "http.Post", "http.NewRequest", "client.Do(",
	// JavaScript/TypeScript
	"fetch(", "axios.", ".ajax(",
	// Java
	"HttpClient", "RestTemplate", "WebClient", "OkHttpClient",
	"HttpURLConnection", "openConnection(",
	// Rust
	"reqwest::", "hyper::", "surf::", "ureq::",
	// PHP
	"curl_exec", "curl_init", "Guzzle", "Http::get", "Http::post",
	// Scala
	"sttp.", "http4s", "HttpClient", "wsClient",
	// C++
	"curl_easy", "cpr::Get", "cpr::Post", "httplib::",
	// Lua
	"socket.http", "http.request", "curl.",
	// C#
	"HttpClient", "WebClient", "RestClient", "HttpWebRequest",
	// Kotlin
	"OkHttpClient", "HttpClient", "ktor.client",
	// Generic
	"send_request", "http_client",
	// Phase H3 (Plan 8-Phase Arc, 2026-05-09): shell scripts. Combined
	// with Phase H4's `:port` host-group extension, this enables URL
	// extraction from `curl http://localhost:9090/...` patterns common
	// in dev scripts and health-check probes.
	"curl ",
}

// asyncDispatchKeywords indicate cross-service async dispatch via HTTP.
// Functions containing these keywords create ASYNC_CALLS edges instead of HTTP_CALLS.
var asyncDispatchKeywords = []string{
	// Cloud Tasks (GCP) — task body contains HTTP URL
	"CreateTask", "create_task",
	// Pub/Sub publish (GCP) — push subscriptions deliver via HTTP
	"topic.Publish", "publisher.publish", "topic.publish",
	// AWS SQS/SNS — SQS + Lambda/HTTP, SNS + HTTP subscription
	"sqs.send_message", "sns.publish",
	// RabbitMQ — exchange → HTTP consumer
	"basic_publish",
	// Kafka — consumer often fronted by HTTP
	"producer.send", "producer.Send",
}

// wsPatterns indicate WebSocket usage in handler source.
var wsPatterns = []string{
	// Go: gorilla/nhooyr websocket
	"websocket.Upgrade", "websocket.Accept", "upgrader.Upgrade",
	// JS/TS: ws library, socket.io
	`ws.on("connection`, `io.on("connection`, "new WebSocket(",
	// Generic
	"WebSocketSession", "wsHandler",
}

// ssePatterns indicate Server-Sent Events usage in handler source.
var ssePatterns = []string{
	"text/event-stream",
	"EventSourceResponse",
	"SseEmitter",
	"ServerSentEvent",
	"event-stream",
}

// detectProtocol checks handler source for WebSocket or SSE patterns.
// Returns "ws", "sse", or "" (standard HTTP).
func detectProtocol(source string) string {
	for _, p := range wsPatterns {
		if strings.Contains(source, p) {
			return "ws"
		}
	}
	for _, p := range ssePatterns {
		if strings.Contains(source, p) {
			return "sse"
		}
	}
	return ""
}

// extractFunctionCallSites extracts HTTP paths from function source code.
func extractFunctionCallSites(f *store.Node, rootPath string) []HTTPCallSite {
	sites := make([]HTTPCallSite, 0, 4)

	if f.FilePath == "" || f.StartLine <= 0 || f.EndLine <= 0 {
		return sites
	}

	// Skip Python dunder methods — they configure, not call
	if strings.HasPrefix(f.Name, "__") && strings.HasSuffix(f.Name, "__") {
		return sites
	}

	source := readSourceLines(rootPath, f.FilePath, f.StartLine, f.EndLine)
	if source == "" {
		return sites
	}

	// C1 (Phase C, Rust reqwest URL extraction): when the function body
	// references a top-level const URL by name (e.g.
	// `client.get(BASE_URL)`), the function-line slice misses the const
	// definition — the URL literal lives outside f.StartLine..f.EndLine.
	// Resolve those references by scanning the whole file for const URL
	// bindings, then appending the URL strings of any const referenced
	// inside the function body. Existing extractURLPaths picks them up
	// from the appended literals.
	//
	// Scoped to .rs files so this doesn't perturb non-Rust paths
	// (Python's f-strings, JS template literals, etc. already work via
	// the format!() and pathRe paths).
	if strings.HasSuffix(strings.ToLower(f.FilePath), ".rs") {
		if extra := resolveRustConstURLs(rootPath, f.FilePath, source); extra != "" {
			source = source + "\n" + extra
		}
	}

	// Require at least one HTTP client or async dispatch keyword to avoid
	// false positives from functions that merely store URL strings in variables
	hasHTTPClient := false
	for _, kw := range httpClientKeywords {
		if strings.Contains(source, kw) {
			hasHTTPClient = true
			break
		}
	}

	hasAsyncDispatch := false
	for _, kw := range asyncDispatchKeywords {
		if strings.Contains(source, kw) {
			hasAsyncDispatch = true
			break
		}
	}

	if !hasHTTPClient && !hasAsyncDispatch {
		return sites
	}

	// Sync (HTTP client) takes precedence over async dispatch
	isAsync := hasAsyncDispatch && !hasHTTPClient

	method := detectHTTPMethod(source)

	paths := extractURLPaths(source)
	for _, p := range paths {
		sites = append(sites, HTTPCallSite{
			Path:                p,
			Method:              method,
			SourceName:          f.Name,
			SourceQualifiedName: f.QualifiedName,
			SourceLabel:         f.Label,
			IsAsync:             isAsync,
		})
	}

	return sites
}

// externalDomains are well-known external API domains whose paths
// should not be matched against internal route handlers.
//
// 2026-05-08 addition: w3.org and www.w3.org. JSX SVG elements
// declare `xmlns="http://www.w3.org/2000/svg"`; the urlRe regex
// extracts `(www.w3.org, /2000/svg)` and produces phantom HTTP_CALLS
// edges to internal handlers named "main" / "router" / etc. that
// happen to match the path "/2000/svg". Post-Phase-D2 PSM index
// observed 3 of 20 HTTP_CALLS as `performLLMSearch → /2000/svg` —
// purely from React component source containing `<svg xmlns="...">`.
// These are documentation namespace identifiers, never HTTP fetches.
var externalDomains = []string{
	"googleapis.com",
	"google.com",
	"github.com",
	"gitlab.com",
	"docker.com",
	"docker.io",
	"npmjs.org",
	"pypi.org",
	"cloudflare.com",
	"sentry.io",
	"aws.amazon.com",
	// XML namespace declarations — not HTTP fetches.
	"w3.org",
	"www.w3.org",
	"xmlns.com",
	// Schema / spec URIs that appear in source as identifiers, not fetches.
	"json-schema.org",
	"schemas.microsoft.com",
}

// defaultExcludePaths are common utility endpoints that produce noise in HTTP_CALLS.
// These are excluded from route matching by default.
var defaultExcludePaths = []string{
	"/health",
	"/healthz",
	"/ready",
	"/readyz",
	"/metrics",
	"/favicon.ico",
}

// isExternalDomain checks if a domain is a well-known external API.
func isExternalDomain(domain string) bool {
	domain = strings.ToLower(domain)
	for _, ext := range externalDomains {
		if domain == ext || strings.HasSuffix(domain, "."+ext) {
			return true
		}
	}
	return false
}

// ExtractURLPaths finds URL path segments from text (exported for use by pipeline).
func ExtractURLPaths(text string) []string {
	return extractURLPaths(text)
}

// filesystemPathPrefixes are POSIX directory prefixes that indicate
// a quoted path literal is a filesystem path, not an HTTP URL path.
// pathRe (`["'](/[a-zA-Z0-9_/:.\-]{2,})["']`) matches anything starting
// with `/`, which over-matches strings like `"/home/user/.ssh/id_rsa"`
// in functions that also contain `requests.get` (e.g. a function that
// reads a key file then sends an HTTP request). PSM 2026-05-07 baseline:
// 2 of 3 HTTP_CALLS edges in an 80K-node graph were filesystem-path
// FPs. The fix is conservative — these prefixes are unambiguous on
// Linux/macOS and have no overlap with sensible URL path roots.
var filesystemPathPrefixes = []string{
	"/home/",
	"/root/",
	"/var/",
	"/etc/",
	"/tmp/",
	"/usr/",
	"/opt/",
	"/dev/",
	"/proc/",
	"/sys/",
	"/mnt/",
	"/media/",
	"/srv/",
	"/lib/",
	"/lib64/",
	"/boot/",
	"/run/",
	"/bin/",
	"/sbin/",
}

// isFilesystemPath returns true if p has the shape of a Unix
// filesystem path (e.g. /home/user/file). Used by extractURLPaths to
// reject FPs from quoted path literals in functions that mix HTTP
// client calls with filesystem reads.
func isFilesystemPath(p string) bool {
	for _, prefix := range filesystemPathPrefixes {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// extractURLPaths finds URL path segments from text.
func extractURLPaths(text string) []string {
	seen := map[string]bool{}
	var paths []string

	// Full URLs: extract domain and path, skip external domains
	for _, m := range urlRe.FindAllStringSubmatch(text, -1) {
		domain := m[1]
		p := m[2]
		if isExternalDomain(domain) {
			continue
		}
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}

	// Quoted path literals — filter out filesystem-path FPs
	for _, m := range pathRe.FindAllStringSubmatch(text, -1) {
		p := m[1]
		if isFilesystemPath(p) {
			continue
		}
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}

	// Try to extract URLs from embedded JSON strings (e.g., Cloud Tasks payloads)
	for _, p := range extractJSONStringPaths(text) {
		if isFilesystemPath(p) {
			continue
		}
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}

	// C1 (Phase C): Rust format!() macro paths.
	// `format!("{}/api/users", base)` carries `/api/users` inside the
	// format string but pathRe never sees it (the literal starts with
	// `{`, not `/`). Scan format strings for /path-shaped substrings.
	//
	// Trailing-slash policy: format!("{}/api/users/{}", base, id) emits
	// `/api/users/` because pathInFormatRe greedily consumes through
	// the slash before the next placeholder. Trim the trailing slash
	// so the canonical form matches what pathRe produces for ordinary
	// quoted paths (and what matchAndLink's normalizePath expects).
	for _, m := range formatMacroRe.FindAllStringSubmatch(text, -1) {
		fmtStr := m[1]
		for _, raw := range pathInFormatRe.FindAllString(fmtStr, -1) {
			p := strings.TrimRight(raw, "/")
			if p == "" {
				continue
			}
			if isFilesystemPath(p) {
				continue
			}
			if !seen[p] {
				seen[p] = true
				paths = append(paths, p)
			}
		}
	}

	// C2 (Phase C): JS/TS template literals.
	// `fetch(\`/api/users/${id}\`)` carries `/api/users/` inside backticks
	// that pathRe never matches. Same /path extraction + trailing-slash
	// trim as the format!() branch above. Note: this fires regardless of
	// language because backticks-as-template-literals are JS/TS-specific
	// in our supported stacks; on languages that don't use backticks
	// for strings the regex matches nothing and the loop is a no-op.
	for _, m := range templateLiteralRe.FindAllStringSubmatch(text, -1) {
		tmplStr := m[1]
		for _, raw := range pathInFormatRe.FindAllString(tmplStr, -1) {
			p := strings.TrimRight(raw, "/")
			if p == "" {
				continue
			}
			if isFilesystemPath(p) {
				continue
			}
			if !seen[p] {
				seen[p] = true
				paths = append(paths, p)
			}
		}
	}

	return paths
}

// extractJSONStringPaths tries to JSON-parse the text (or substrings that look
// like JSON) and extract URL paths from string values within.
func extractJSONStringPaths(text string) []string {
	seen := make(map[string]bool)
	var paths []string

	// Find JSON-like substrings: {...} or [...]
	for _, bounds := range findJSONBounds(text) {
		var parsed any
		if err := json.Unmarshal([]byte(bounds), &parsed); err != nil {
			continue
		}
		var raw []string
		walkJSONForURLs(parsed, &raw)
		for _, p := range raw {
			if !seen[p] {
				seen[p] = true
				paths = append(paths, p)
			}
		}
	}

	return paths
}

// findJSONBounds extracts substrings that look like JSON objects or arrays.
func findJSONBounds(text string) []string {
	results := make([]string, 0, 4)
	for _, opener := range []byte{'{', '['} {
		closer := byte('}')
		if opener == '[' {
			closer = ']'
		}
		results = append(results, scanJSONBlocks(text, opener, closer)...)
	}
	return results
}

// scanJSONBlocks scans text for balanced JSON blocks delimited by opener/closer.
func scanJSONBlocks(text string, opener, closer byte) []string {
	var results []string
	start := strings.IndexByte(text, opener)
	for start >= 0 && start < len(text) {
		end, ok := findBalancedEnd(text, start, opener, closer)
		if !ok {
			break
		}
		candidate := text[start : end+1]
		if len(candidate) > 5 {
			results = append(results, candidate)
		}
		start = end + 1
		next := strings.IndexByte(text[start:], opener)
		if next < 0 {
			break
		}
		start += next
	}
	return results
}

// findBalancedEnd finds the index of the closing bracket that balances the opener at start.
// Returns the index and true if found, or 0 and false if unbalanced.
func findBalancedEnd(text string, start int, opener, closer byte) (int, bool) {
	depth := 0
	inStr := false
	for i := start; i < len(text); i++ {
		ch := text[i]
		if inStr {
			if ch == '\\' {
				i++ // skip escaped char
				continue
			}
			if ch == '"' {
				inStr = false
			}
			continue
		}
		switch ch {
		case '"':
			inStr = true
		case opener:
			depth++
		case closer:
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// walkJSONForURLs recursively walks parsed JSON and extracts URL paths.
func walkJSONForURLs(v any, out *[]string) {
	switch val := v.(type) {
	case map[string]any:
		for _, child := range val {
			walkJSONForURLs(child, out)
		}
	case []any:
		for _, child := range val {
			walkJSONForURLs(child, out)
		}
	case string:
		// Check if value is a URL or path
		for _, m := range urlRe.FindAllStringSubmatch(val, -1) {
			if !isExternalDomain(m[1]) {
				*out = append(*out, m[2])
			}
		}
		for _, m := range pathRe.FindAllStringSubmatch(`"`+val+`"`, -1) {
			*out = append(*out, m[1])
		}
	}
}

// routeDeclarerNames are the last-segment names commonly used for the
// FUNCTION CONTAINING the route declarations (router builder, server
// startup, main). When resolveHandlerNode would fall back to the legacy
// route-declarer lookup, an edge pointing at one of these names is
// almost always a misroute — the route was declared, not handled, by
// that function. Phase H1 (2026-05-09): drop the edge instead of
// emitting it pointing at the route-declarer.
//
// PSM 2026-05-07 baseline showed 5 generic-target misroutes to
// `run_http_server`, `main`, and `router` patterns. These are the
// failure mode: rh.HandlerRef was empty / name lookup found nothing;
// legacy fallback pointed at the route-declaring function which is
// definitionally NOT the handler.
var routeDeclarerNames = map[string]bool{
	"run_http_server": true,
	"build_router":    true,
	"router":          true,
	"main":            true,
	"setup_routes":    true,
	"register_routes": true,
	"new_router":      true,
	"create_router":   true,
	"start_server":    true,
}

// isRouteDeclarerNode returns true if the node's bare-name (last segment
// of qualified_name) is a route-declarer pattern. Used by Phase H1's
// drop-on-misroute rule.
func isRouteDeclarerNode(n *store.Node) bool {
	if n == nil {
		return false
	}
	qn := n.QualifiedName
	// Last segment after . or ::
	last := qn
	if idx := strings.LastIndex(qn, "."); idx >= 0 {
		last = qn[idx+1:]
	}
	if idx := strings.LastIndex(last, "::"); idx >= 0 {
		last = last[idx+2:]
	}
	return routeDeclarerNames[last]
}

// resolveHandlerNode returns the graph node that the route's handler
// most likely refers to. Phase D1 (2026-05-08) rework: prefer the
// handler symbol the route literally points at (rh.HandlerRef) over
// the route-declaring function (rh.QualifiedName).
//
// Why: extractAxumRoutes / extractGoRoutes / extractExpressRoutes set
// rh.QualifiedName to the function CONTAINING the .route(...) /
// .GET(...) / app.get(...) call (e.g. `build_router`). For matchAndLink
// to attach an HTTP_CALLS edge to the actual handler (e.g.
// `handle_orders`), it has to look up the handler symbol by name.
// Pre-D1 the lookup used rh.QualifiedName, which produced edges that
// pointed AT the route-declaring function — visible in PSM as
// `loadDoomperStatus → router`, `fetchPowerStatus → run_http_server`,
// `BatteryIndicator → main`.
//
// Phase H1 (2026-05-09): when falling back to the legacy route-
// declarer lookup, drop the edge if the legacy node's name is a
// known route-declarer pattern (run_http_server, build_router, main,
// router, etc.). Better no edge than an edge pointing at the route
// declaration itself.
//
// Resolution order:
//  1. If rh.HandlerRef is empty, fall back to legacy QualifiedName
//     lookup; if legacy is a route-declarer, return nil (drop edge).
//  2. Strip any receiver/module prefix from HandlerRef ("h.create_user"
//     and "routes::list_users" both become the bare last-segment name).
//  3. Look up by name via FindNodesByName. If exactly one match, use it.
//  4. If multiple matches, pick the candidate whose qualified name shares
//     the longest common-prefix with rh.QualifiedName — that's the
//     handler in the same crate / module as the route-declaring function.
//     Falls back to the first candidate if all share zero prefix (which
//     shouldn't happen in practice — every node shares the project-name
//     prefix).
//  5. If no name match, fall back to legacy QualifiedName lookup; if
//     legacy is a route-declarer, return nil (drop edge) instead.
func (l *Linker) resolveHandlerNode(rh RouteHandler) *store.Node {
	legacy, _ := l.store.FindNodeByQN(l.project, rh.QualifiedName)

	// Phase H1: legacy fallback that targets a route-declarer is a
	// known misroute. Drop the edge.
	legacyOrNil := func(n *store.Node) *store.Node {
		if isRouteDeclarerNode(n) {
			return nil
		}
		return n
	}

	if rh.HandlerRef == "" {
		return legacyOrNil(legacy)
	}

	name := rh.HandlerRef
	// Strip any leading receiver/module path: "h.CreateOrder" → "CreateOrder",
	// "routes::list_users" → "list_users".
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		name = name[idx+1:]
	}
	if idx := strings.LastIndex(name, "::"); idx >= 0 {
		name = name[idx+2:]
	}
	if name == "" {
		return legacy
	}

	candidates, err := l.store.FindNodesByName(l.project, name)
	if err != nil || len(candidates) == 0 {
		return legacyOrNil(legacy)
	}

	// Filter to Function/Method nodes only — Variable / Class / etc.
	// would never be a route handler, and including them inflates the
	// crate-locality tiebreak with noise.
	filtered := make([]*store.Node, 0, len(candidates))
	for _, c := range candidates {
		if c.Label == "Function" || c.Label == "Method" {
			filtered = append(filtered, c)
		}
	}
	if len(filtered) == 0 {
		return legacyOrNil(legacy)
	}
	if len(filtered) == 1 {
		return filtered[0]
	}

	// Multiple same-name candidates: pick the one with the longest
	// common QN-prefix with rh.QualifiedName. That candidate lives in
	// the same crate / module path as the route declaration, which is
	// the standard layout (router and handlers both under the same
	// crate). On collision (e.g. two crates with `fn list_users` where
	// neither is the router's crate) the first-seen candidate wins —
	// not ideal, but avoids dropping the edge entirely.
	var best *store.Node
	bestLen := -1
	for _, c := range filtered {
		n := commonPrefixLen(rh.QualifiedName, c.QualifiedName)
		if n > bestLen {
			bestLen = n
			best = c
		}
	}
	return best
}

// commonPrefixLen returns the number of leading bytes a and b share.
// Used by resolveHandlerNode to break ties between same-name handler
// candidates by crate-locality (longer shared prefix → same crate).
func commonPrefixLen(a, b string) int {
	n := 0
	max := len(a)
	if len(b) < max {
		max = len(b)
	}
	for n < max && a[n] == b[n] {
		n++
	}
	return n
}

// matchAndLink matches call site paths to route handler paths and creates edges.
// Uses multi-signal probabilistic scoring (path Jaccard, depth, method, source type).
// Only creates edges above the confidence threshold.
func (l *Linker) matchAndLink(routes []RouteHandler, callSites []HTTPCallSite) []HTTPLink {
	var links []HTTPLink

	for _, cs := range callSites {
		for _, rh := range routes {
			if sameService(cs.SourceQualifiedName, rh.QualifiedName) {
				continue
			}

			// Skip noisy utility endpoints
			if isPathExcluded(rh.Path, l.config.AllExcludePaths()) {
				continue
			}

			// Multi-signal confidence scoring
			pathScore := pathMatchScore(cs.Path, rh.Path)
			if pathScore == 0 {
				continue
			}

			weight := sourceWeight(cs.SourceLabel)
			mBonus := methodBonus(cs.Method, rh.Method)
			score := pathScore*weight + mBonus
			minConfidence := l.config.EffectiveMinConfidence()
			if score < minConfidence {
				// Phase B2 (2026-05-08): gated debug slog of rejected pairs.
				// Set CODE_GRAPH_MATCH_TRACE=1 to surface which (caller, route)
				// pairs scored above 0 but below threshold. Useful when tuning
				// pathMatchScore for the partial-match-FP class (Phase E1).
				if os.Getenv("CODE_GRAPH_MATCH_TRACE") != "" {
					slog.Debug(
						"matchAndLink.reject_below_threshold",
						"caller", cs.SourceQualifiedName,
						"caller_url", cs.Path,
						"route_path", rh.Path,
						"route_qn", rh.QualifiedName,
						"path_score", pathScore,
						"source_weight", weight,
						"method_bonus", mBonus,
						"final_score", score,
						"min_confidence", minConfidence,
					)
				}
				continue
			}
			if os.Getenv("CODE_GRAPH_MATCH_TRACE") != "" {
				slog.Debug(
					"matchAndLink.accept",
					"caller", cs.SourceQualifiedName,
					"caller_url", cs.Path,
					"route_path", rh.Path,
					"route_qn", rh.QualifiedName,
					"path_score", pathScore,
					"source_weight", weight,
					"method_bonus", mBonus,
					"final_score", score,
				)
			}
			if score > 1.0 {
				score = 1.0
			}

			// Create edge with confidence score
			edgeType := "HTTP_CALLS"
			if cs.IsAsync {
				edgeType = "ASYNC_CALLS"
			}

			callerNode, _ := l.store.FindNodeByQN(l.project, cs.SourceQualifiedName)
			handlerNode := l.resolveHandlerNode(rh)
			if callerNode != nil && handlerNode != nil {
				// Skip self-loop edges: when caller and handler live in
				// the same source file, the matched call is almost
				// always intra-file routing (a function that documents
				// a route via a path constant, then handles it locally),
				// not a cross-service HTTP call. PSM 2026-05-07 baseline:
				// the JS Route → Route self-loop (mithrandir component
				// matched by an Express extractor against itself) was
				// 1 of 3 false-positive HTTP_CALLS edges. Self-loops at
				// the node level are also filtered (caller == handler).
				if callerNode.ID == handlerNode.ID {
					continue
				}
				if callerNode.FilePath != "" && callerNode.FilePath == handlerNode.FilePath {
					continue
				}
				band := confidenceBand(score)
				props := map[string]any{
					"url_path":        cs.Path,
					"confidence":      score,
					"confidence_band": band,
					"confidence_tier": store.ConfidenceInferred,
				}
				if rh.Method != "" {
					props["method"] = rh.Method
				}
				_, _ = l.store.InsertEdge(&store.Edge{
					Project:    l.project,
					SourceID:   callerNode.ID,
					TargetID:   handlerNode.ID,
					Type:       edgeType,
					Properties: props,
				})
			}

			handlerQN := rh.QualifiedName
			if handlerNode != nil {
				handlerQN = handlerNode.QualifiedName
			}
			links = append(links, HTTPLink{
				CallerQN:    cs.SourceQualifiedName,
				CallerLabel: cs.SourceLabel,
				HandlerQN:   handlerQN,
				URLPath:     cs.Path,
				EdgeType:    edgeType,
			})
		}
	}

	return links
}

// normalizePath normalizes a URL path for comparison.
// numericSegmentRe matches path segments that are pure numeric IDs.
var numericSegmentRe = regexp.MustCompile(`/\d+(/|$)`)

// uuidSegmentRe matches path segments that are UUIDs.
var uuidSegmentRe = regexp.MustCompile(`/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}(/|$)`)

func normalizePath(path string) string {
	path = strings.TrimRight(path, "/")
	path = colonParamRe.ReplaceAllString(path, "*")
	path = braceParamRe.ReplaceAllString(path, "*")
	// Normalize UUIDs and numeric IDs to wildcards for better matching
	path = uuidSegmentRe.ReplaceAllString(path, "/*$1")
	path = numericSegmentRe.ReplaceAllString(path, "/*$1")
	return strings.ToLower(path)
}

// matchConfidenceThreshold is the minimum score for an HTTP_CALLS edge.
// Lowered from 0.45 to 0.25 to include speculative matches with confidence bands.
const matchConfidenceThreshold = 0.25

// pathMatchScore returns a confidence score (0.0–1.0) for how well callPath
// matches routePath. Returns 0 if no match.
//
// Multi-signal scoring (inspired by RAD/Code2DFD research):
//
//	confidence = matchBase × (0.5 × jaccard + 0.5 × depthFactor)
//
// Where:
//
//	matchBase:   exact=0.95, suffix=0.75, wildcard=0.55
//	jaccard:     segment Jaccard similarity (non-wildcard segments)
//	depthFactor: min(matched_segments / 3.0, 1.0) — longer paths = more specific
func pathMatchScore(callPath, routePath string) float64 {
	normCall := normalizePath(callPath)
	normRoute := normalizePath(routePath)

	if normCall == "" || normRoute == "" {
		return 0
	}

	// Determine structural match type
	var matchBase float64
	var matchedCallSegs, matchedRouteSegs []string

	switch {
	case normCall == normRoute:
		matchBase = 0.95
		matchedCallSegs = splitSegments(normCall)
		matchedRouteSegs = splitSegments(normRoute)
	case strings.HasSuffix(normCall, normRoute):
		matchBase = 0.75
		matchedCallSegs = splitSegments(normRoute) // use the route portion that matched
		matchedRouteSegs = splitSegments(normRoute)
	default:
		// Segment-by-segment wildcard matching
		callParts := strings.Split(normCall, "/")
		routeParts := strings.Split(normRoute, "/")
		if len(callParts) != len(routeParts) {
			return 0
		}
		for i := range callParts {
			if callParts[i] != routeParts[i] && callParts[i] != "*" && routeParts[i] != "*" {
				return 0
			}
		}
		matchBase = 0.55
		matchedCallSegs = splitSegments(normCall)
		matchedRouteSegs = splitSegments(normRoute)
	}

	// Jaccard similarity on non-empty, non-wildcard segments
	jaccard := segmentJaccard(matchedCallSegs, matchedRouteSegs)

	// Depth factor: more segments = more specific match
	totalSegs := len(matchedRouteSegs)
	depthFactor := float64(totalSegs) / 3.0
	if depthFactor > 1.0 {
		depthFactor = 1.0
	}

	score := matchBase * (0.5*jaccard + 0.5*depthFactor)
	if score > 1.0 {
		score = 1.0
	}
	return score
}

// splitSegments splits a normalized path into non-empty segments.
func splitSegments(path string) []string {
	var segs []string
	for _, s := range strings.Split(path, "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	return segs
}

// segmentJaccard computes Jaccard similarity on non-wildcard path segments.
// Wildcards (*) are excluded from both sets since they match anything.
func segmentJaccard(segsA, segsB []string) float64 {
	setA := make(map[string]bool)
	setB := make(map[string]bool)
	for _, s := range segsA {
		if s != "*" {
			setA[s] = true
		}
	}
	for _, s := range segsB {
		if s != "*" {
			setB[s] = true
		}
	}

	if len(setA) == 0 && len(setB) == 0 {
		return 0
	}

	intersection := 0
	for k := range setA {
		if setB[k] {
			intersection++
		}
	}

	union := len(setA)
	for k := range setB {
		if !setA[k] {
			union++
		}
	}

	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// methodBonus returns a confidence adjustment based on HTTP method matching.
//
//	+0.10 if both methods are known and match
//	 0.00 if one or both methods are unknown
//	-0.15 if both methods are known and mismatch
func methodBonus(callMethod, routeMethod string) float64 {
	if callMethod == "" || routeMethod == "" {
		return 0
	}
	if strings.EqualFold(callMethod, routeMethod) {
		return 0.10
	}
	return -0.15
}

// sourceWeight returns a confidence multiplier based on call site type.
// Function/Method sources are higher confidence (HTTP client in source code)
// than Module sources (URL in constants — may be config, not a call).
func sourceWeight(label string) float64 {
	switch label {
	case "Function", "Method":
		return 1.0
	default:
		return 0.85
	}
}

// resolveFastAPIPrefixes resolves include_router prefixes for FastAPI routes.
// Scans Python Module files for app.include_router(var, prefix="/prefix") calls
// and prepends the prefix to routes from the imported module.
func (l *Linker) resolveFastAPIPrefixes(routes []RouteHandler, rootPath string) {
	modules, err := l.store.FindNodesByLabel(l.project, "Module")
	if err != nil {
		return
	}

	for _, mod := range modules {
		if !strings.HasSuffix(mod.FilePath, ".py") {
			continue
		}

		source, readErr := os.ReadFile(filepath.Join(rootPath, mod.FilePath))
		if readErr != nil {
			continue
		}
		srcStr := string(source)

		includes := fastAPIIncludeRe.FindAllStringSubmatch(srcStr, -1)
		if len(includes) == 0 {
			continue
		}

		// Build import map: var_name → dotted module path
		imports := map[string]string{}
		for _, m := range pyImportRe.FindAllStringSubmatch(srcStr, -1) {
			imports[m[2]] = m[1] // var_name → module.path
		}

		for _, inc := range includes {
			varName := inc[1]
			prefix := inc[2]

			modulePath, ok := imports[varName]
			if !ok {
				continue
			}

			// Convert dotted module path to file path fragment
			fileFrag := strings.ReplaceAll(modulePath, ".", "/")
			normalizedPrefix := strings.TrimRight(prefix, "/")

			prefixed := 0
			for i := range routes {
				if strings.HasPrefix(routes[i].Path, normalizedPrefix) {
					continue
				}
				// Match routes whose QN contains the imported module path
				if strings.Contains(routes[i].QualifiedName, fileFrag+".py") ||
					strings.Contains(routes[i].QualifiedName, fileFrag+"/") {
					routes[i].Path = normalizedPrefix + "/" + strings.TrimLeft(routes[i].Path, "/")
					prefixed++
				}
			}
			if prefixed > 0 {
				slog.Info("httplink.fastapi_prefix", "prefix", prefix, "module", modulePath, "routes", prefixed)
			}
		}
	}
}

// resolveExpressPrefixes resolves app.use("/prefix", router) for Express routes.
// Scans JS/TS Module files for .use("/prefix", routerVar) calls and prepends
// the prefix to routes from the imported module.
func (l *Linker) resolveExpressPrefixes(routes []RouteHandler, rootPath string) {
	modules, err := l.store.FindNodesByLabel(l.project, "Module")
	if err != nil {
		return
	}

	for _, mod := range modules {
		if !isJSTSModule(mod.FilePath) {
			continue
		}

		source, readErr := os.ReadFile(filepath.Join(rootPath, mod.FilePath))
		if readErr != nil {
			continue
		}
		srcStr := string(source)

		uses := expressUseRe.FindAllStringSubmatch(srcStr, -1)
		if len(uses) == 0 {
			continue
		}

		imports := buildJSImportMap(srcStr)
		l.applyExpressUsePrefixes(routes, uses, imports)
	}
}

// isJSTSModule returns true if the file path is a JS/TS module file.
func isJSTSModule(filePath string) bool {
	return strings.HasSuffix(filePath, ".js") || strings.HasSuffix(filePath, ".ts") ||
		strings.HasSuffix(filePath, ".mjs") || strings.HasSuffix(filePath, ".tsx")
}

// buildJSImportMap builds a map of var_name to module path from require/import statements.
func buildJSImportMap(src string) map[string]string {
	imports := map[string]string{}
	for _, m := range jsRequireRe.FindAllStringSubmatch(src, -1) {
		imports[m[1]] = m[2]
	}
	for _, m := range jsImportRe.FindAllStringSubmatch(src, -1) {
		imports[m[1]] = m[2]
	}
	return imports
}

// applyExpressUsePrefixes applies .use("/prefix", routerVar) prefix resolution to routes.
func (l *Linker) applyExpressUsePrefixes(routes []RouteHandler, uses [][]string, imports map[string]string) {
	for _, use := range uses {
		prefix := use[1]
		varName := use[2]

		modulePath, ok := imports[varName]
		if !ok {
			continue
		}

		// Strip leading ./ from relative import
		fileFrag := strings.TrimPrefix(modulePath, "./")
		fileFrag = strings.TrimPrefix(fileFrag, "../")
		normalizedPrefix := strings.TrimRight(prefix, "/")

		prefixed := 0
		for i := range routes {
			if strings.HasPrefix(routes[i].Path, normalizedPrefix) {
				continue
			}
			if strings.Contains(routes[i].QualifiedName, fileFrag+".js") ||
				strings.Contains(routes[i].QualifiedName, fileFrag+".ts") ||
				strings.Contains(routes[i].QualifiedName, fileFrag+"/") {
				routes[i].Path = normalizedPrefix + "/" + strings.TrimLeft(routes[i].Path, "/")
				prefixed++
			}
		}
		if prefixed > 0 {
			slog.Info("httplink.express_prefix", "prefix", prefix, "module", modulePath, "routes", prefixed)
		}
	}
}

// pathsMatch is a convenience wrapper for tests — returns true if score >= threshold.
func pathsMatch(callPath, routePath string) bool {
	return pathMatchScore(callPath, routePath) >= matchConfidenceThreshold
}

// sameService checks if two qualified names share the same directory path.
// It strips the last 2 segments (module file + function/method name) from each
// QN and compares the remaining directory prefix. If the prefixes are identical,
// the nodes are in the same deployable unit.
//
// Example: "myapp.docker-images.cloud-runs.svcA.module.func" → dir prefix "myapp.docker-images.cloud-runs.svcA"
//
//	"myapp.docker-images.cloud-runs.svcB.routes.handler" → dir prefix "myapp.docker-images.cloud-runs.svcB"
//	→ different prefix → different service → returns false
func sameService(qn1, qn2 string) bool {
	parts1 := strings.Split(qn1, ".")
	parts2 := strings.Split(qn2, ".")

	// Strip last 2 segments (module + name) to get directory path
	const strip = 2
	if len(parts1) <= strip || len(parts2) <= strip {
		return false
	}
	dir1 := strings.Join(parts1[:len(parts1)-strip], ".")
	dir2 := strings.Join(parts2[:len(parts2)-strip], ".")
	return dir1 == dir2
}

// crossFileGroupRe matches patterns like: funcName(something.Group("/prefix"))
// Captures the function name being called and the group prefix.
var crossFileGroupRe = regexp.MustCompile(`(\w+)\s*\(\s*\w+\.Group\s*\(\s*"([^"]+)"\s*\)`)

// crossFileGroupVarRe matches the variable-based pattern:
// varName := something.Group("/prefix")
// ... (next line)
// funcName(varName)
var crossFileGroupVarRe = regexp.MustCompile(`(\w+)\s*:?=\s*\w+\.Group\s*\(\s*"([^"]+)"\s*\)`)

// resolveCrossFileGroupPrefixes resolves Group() prefixes from caller functions
// for routes that were registered without a group prefix within their own function.
func (l *Linker) resolveCrossFileGroupPrefixes(routes []RouteHandler, rootPath string) {
	for funcQN, indices := range l.routesByFunc {
		funcNode, _ := l.store.FindNodeByQN(l.project, funcQN)
		if funcNode == nil {
			continue
		}

		callerEdges, _ := l.store.FindEdgesByTargetAndType(funcNode.ID, "CALLS")
		if len(callerEdges) == 0 {
			continue
		}

		l.resolveCallerGroupPrefixes(routes, indices, callerEdges, funcNode.Name, rootPath)
	}
}

// resolveCallerGroupPrefixes checks each caller's source for Group() prefix passing
// and prepends the prefix to the routes at the given indices.
func (l *Linker) resolveCallerGroupPrefixes(routes []RouteHandler, indices []int, callerEdges []*store.Edge, funcName, rootPath string) {
	for _, edge := range callerEdges {
		callerNode, _ := l.store.FindNodeByID(edge.SourceID)
		if callerNode == nil || callerNode.FilePath == "" || callerNode.StartLine <= 0 {
			continue
		}

		callerSource := readSourceLines(rootPath, callerNode.FilePath, callerNode.StartLine, callerNode.EndLine)
		if callerSource == "" {
			continue
		}

		// Pattern 1: direct â RegisterRoutes(router.Group("/api"))
		for _, m := range crossFileGroupRe.FindAllStringSubmatch(callerSource, -1) {
			if m[1] == funcName {
				l.prependPrefixToRoutes(routes, indices, m[2])
				break
			}
		}

		// Pattern 2: variable-based â v1 := router.Group("/api"); RegisterRoutes(v1)
		l.resolveVarGroupPrefix(routes, indices, callerSource, funcName)
	}
}

// resolveVarGroupPrefix resolves Group() prefixes passed via intermediate variables.
func (l *Linker) resolveVarGroupPrefix(routes []RouteHandler, indices []int, callerSource, funcName string) {
	varPrefixes := map[string]string{}
	for _, m := range crossFileGroupVarRe.FindAllStringSubmatch(callerSource, -1) {
		varPrefixes[m[1]] = m[2]
	}
	if len(varPrefixes) == 0 {
		return
	}
	callRe := regexp.MustCompile(regexp.QuoteMeta(funcName) + `\s*\(\s*(\w+)`)
	for _, cm := range callRe.FindAllStringSubmatch(callerSource, -1) {
		if prefix, ok := varPrefixes[cm[1]]; ok {
			l.prependPrefixToRoutes(routes, indices, prefix)
			break
		}
	}
}

// prependPrefixToRoutes prepends a group prefix to routes at the given indices,
// but only if the route path doesn't already start with the prefix.
func (l *Linker) prependPrefixToRoutes(routes []RouteHandler, indices []int, prefix string) {
	for _, idx := range indices {
		rh := &routes[idx]
		normalizedPrefix := strings.TrimRight(prefix, "/")
		if !strings.HasPrefix(rh.Path, normalizedPrefix) {
			rh.Path = normalizedPrefix + "/" + strings.TrimLeft(rh.Path, "/")
		}
	}
	slog.Info("httplink.cross_file_prefix", "prefix", prefix, "routes", len(indices))
}

// createRegistrationCallEdges creates CALLS edges from route-registering functions
// to the handler functions they reference (e.g. .POST("/path", h.CreateOrder)).
func (l *Linker) createRegistrationCallEdges(routes []RouteHandler) {
	count := 0
	for i := range routes {
		rh := &routes[i]
		if rh.HandlerRef == "" {
			continue
		}

		// Find the registering function node
		registrar, _ := l.store.FindNodeByQN(l.project, rh.QualifiedName)
		if registrar == nil {
			continue
		}

		// Resolve handler reference — try method name (strip receiver prefix like "h.")
		handlerName := rh.HandlerRef
		if idx := strings.LastIndex(handlerName, "."); idx >= 0 {
			handlerName = handlerName[idx+1:]
		}

		// Search for the handler function/method by name
		handlerNodes, _ := l.store.FindNodesByName(l.project, handlerName)
		if len(handlerNodes) == 0 {
			continue
		}

		// Use the first match (typically unique within a project)
		handler := handlerNodes[0]

		// Propagate resolved handler QN back to route for insertRouteNodes
		rh.ResolvedHandlerQN = handler.QualifiedName

		_, _ = l.store.InsertEdge(&store.Edge{
			Project:  l.project,
			SourceID: registrar.ID,
			TargetID: handler.ID,
			Type:     "CALLS",
			Properties: map[string]any{
				"via":                 "route_registration",
				"resolution_strategy": "route_registration",
				"confidence_tier":     store.ConfidenceInferred,
			},
		})
		count++
	}
	if count > 0 {
		slog.Info("httplink.registration_edges", "count", count)
	}
}

// resolveRustConstURLs scans rootPath/relPath for top-level Rust const
// URL bindings (`const FOO: &str = "https://..."`, etc.) and returns
// the URL literals of those bindings whose names appear inside
// funcSource. Returned as a newline-joined block of quoted literals
// suitable for appending to the function source — extractURLPaths
// then picks them up via the existing urlRe / pathRe loops.
//
// Scoping rules:
//   - matches whole-word identifier references (`\bNAME\b`) so
//     `BASE_URL` doesn't match a local var `MY_BASE_URL_PREFIX`.
//   - case-insensitive identifier-name regex match would over-include;
//     we keep it case-sensitive to mirror Rust's identifier rules.
//   - returns "" if either the file can't be read or no referenced
//     consts are found, so callers can no-op safely.
func resolveRustConstURLs(rootPath, relPath, funcSource string) string {
	fileSource := readSourceFull(rootPath, relPath)
	if fileSource == "" {
		return ""
	}
	matches := rustConstUrlRe.FindAllStringSubmatch(fileSource, -1)
	if len(matches) == 0 {
		return ""
	}
	var extras []string
	for _, m := range matches {
		name := m[1]
		url := m[2]
		// Defensive cap: very long URLs are unlikely to be real and
		// expanding them blows up downstream regex cost.
		if len(url) > 512 {
			continue
		}
		if !regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`).MatchString(funcSource) {
			continue
		}
		extras = append(extras, `"`+url+`"`)
	}
	return strings.Join(extras, "\n")
}

// readSourceLines reads specific lines from a file on disk.
// readSourceFull reads the entire file content as a string.
func readSourceFull(rootPath, relPath string) string {
	data, err := os.ReadFile(filepath.Join(rootPath, relPath))
	if err != nil {
		return ""
	}
	return string(data)
}

func readSourceLines(rootPath, relPath string, startLine, endLine int) string {
	absPath := filepath.Join(rootPath, relPath)
	f, err := os.Open(absPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if lineNum >= startLine && lineNum <= endLine {
			lines = append(lines, scanner.Text())
		}
		if lineNum > endLine {
			break
		}
	}
	return strings.Join(lines, "\n")
}
