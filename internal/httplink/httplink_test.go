package httplink

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/api/orders/", "/api/orders"},
		{"/api/orders", "/api/orders"},
		{"/api/orders/:id", "/api/orders/*"},
		{"/api/orders/{order_id}", "/api/orders/*"},
		{"/API/Orders", "/api/orders"},
		{"/api/:version/items/:id", "/api/*/items/*"},
		{"/api/{version}/items/{id}", "/api/*/items/*"},
		{"/", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := normalizePath(tt.input)
		if got != tt.want {
			t.Errorf("normalizePath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestPathsMatch(t *testing.T) {
	tests := []struct {
		callPath  string
		routePath string
		want      bool
	}{
		// Exact match
		{"/api/orders", "/api/orders", true},
		{"/api/orders/", "/api/orders", true},

		// Case insensitive
		{"/API/Orders", "/api/orders", true},

		// Suffix match (call has host prefix, route is just path)
		{"https://example.com/api/orders", "/api/orders", true},
		{"/api/orders", "/api/orders", true},

		// Wildcard params
		{"/api/orders/:id", "/api/orders/{order_id}", true},
		{"/api/orders/123", "/api/orders/:id", true}, // 123 matches * (normalized :id)

		// Segment wildcard: :version normalizes to *, matches any segment
		{"/api/:version/items", "/api/v1/items", true},

		// Different lengths
		{"/api/orders", "/api/orders/detail", false},
		{"/api", "/api/orders", false},

		// Both have wildcards
		{"/api/*/items", "/api/*/items", true},

		// No match
		{"/api/users", "/api/orders", false},
	}
	for _, tt := range tests {
		got := pathsMatch(tt.callPath, tt.routePath)
		if got != tt.want {
			t.Errorf("pathsMatch(%q, %q) = %v, want %v", tt.callPath, tt.routePath, got, tt.want)
		}
	}
}

func TestPathsMatchSuffix(t *testing.T) {
	// Suffix match: normalized call path ends with normalized route path
	got := pathsMatch("/host/prefix/api/orders", "/api/orders")
	if !got {
		t.Error("expected suffix match for /host/prefix/api/orders -> /api/orders")
	}
}

func TestPathMatchScore(t *testing.T) {
	tests := []struct {
		call  string
		route string
		min   float64
		max   float64
	}{
		// Exact matches: matchBase=0.95, confidence = 0.95 × (0.5×jaccard + 0.5×depthFactor)
		{"/api/orders", "/api/orders", 0.78, 0.82},                   // jaccard=1.0, depth=2/3=0.667 → 0.95×0.833 ≈ 0.79
		{"/integrate", "/integrate", 0.60, 0.67},                     // jaccard=1.0, depth=1/3=0.333 → 0.95×0.667 ≈ 0.63
		{"/api/v1/orders/items", "/api/v1/orders/items", 0.93, 0.96}, // jaccard=1.0, depth=4/3→1.0 → 0.95×1.0 = 0.95

		// Suffix matches: matchBase=0.75
		{"https://host/api/orders", "/api/orders", 0.60, 0.66}, // jaccard=1.0, depth=0.667 → 0.75×0.833 ≈ 0.625

		// Numeric IDs normalized to wildcard → exact match with :id (also normalized to *)
		{"/api/orders/123", "/api/orders/:id", 0.90, 0.96}, // both normalize to /api/orders/* → exact match

		// No match
		{"/api/users", "/api/orders", 0.0, 0.0},
		{"/", "/api/orders", 0.0, 0.0}, // empty normalized
		{"", "/api/orders", 0.0, 0.0},
	}
	for _, tt := range tests {
		got := pathMatchScore(tt.call, tt.route)
		if got < tt.min || got > tt.max {
			t.Errorf("pathMatchScore(%q, %q) = %.2f, want [%.2f, %.2f]", tt.call, tt.route, got, tt.min, tt.max)
		}
	}
}

func TestSameService(t *testing.T) {
	tests := []struct {
		qn1  string
		qn2  string
		want bool
	}{
		// Full directory comparison: strip last 2 segments (module+name), compare rest
		// "a.b.c.mod.func" → dir="a.b.c", so same dir = same service
		{"a.b.c.mod.Func1", "a.b.c.mod.Func2", true},     // same dir (a.b.c)
		{"a.b.c.mod.Func1", "a.b.x.mod.Func2", false},    // different dir (a.b.c vs a.b.x)
		{"a.b.c.d.mod.Func", "a.b.c.d.mod.Other", true},  // same deep dir (a.b.c.d)
		{"a.b.c.d.mod.Func", "a.b.c.e.mod.Other", false}, // different deep dir
		{"short.x", "short.y", false},                    // only 2 segments → strip leaves empty → false
		{"a.b", "a.b", false},                            // 2 segments → not enough to determine
		{"a.b.c", "a.b.c", true},                         // 3 segments: dir="a", same
		{"a.b.c", "x.b.c", false},                        // 3 segments: dir="a" vs "x"
		// Realistic multi-service QN patterns
		{"myapp.docker-images.cloud-runs.order-service.main.Func", "myapp.docker-images.cloud-runs.order-service.handlers.Other", true},
		{"myapp.docker-images.cloud-runs.order-service.main.Func", "myapp.docker-images.cloud-runs.notification-service.main.health_check", false},
		{"myapp.docker-images.cloud-runs.svcA.sub.mod.Func", "myapp.docker-images.cloud-runs.svcA.sub.mod.Other", true},
		{"myapp.docker-images.cloud-runs.svcA.sub.mod.Func", "myapp.docker-images.cloud-runs.svcB.sub.mod.Other", false},
	}
	for _, tt := range tests {
		got := sameService(tt.qn1, tt.qn2)
		if got != tt.want {
			t.Errorf("sameService(%q, %q) = %v, want %v", tt.qn1, tt.qn2, got, tt.want)
		}
	}
}

func TestExtractURLPaths(t *testing.T) {
	tests := []struct {
		text string
		want int // expected number of paths
	}{
		{`URL = "https://example.com/api/orders"`, 1},
		{`fetch("http://host/api/v1/items")`, 1},
		{`path = "/api/orders"`, 1},
		{`no urls here`, 0},
		{`both = "https://a.com/api/x" and "/api/y"`, 2},
	}
	for _, tt := range tests {
		got := extractURLPaths(tt.text)
		if len(got) != tt.want {
			t.Errorf("extractURLPaths(%q) returned %d paths, want %d: %v", tt.text, len(got), tt.want, got)
		}
	}
}

// A1 (2026-05-07): filesystem paths are not URL paths.
func TestExtractURLPaths_FiltersFilesystemPaths(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "ssh_key_path_alone",
			text: `key = "/home/redacted/.ssh/id_ecdsa"`,
			want: nil,
		},
		{
			name: "ssh_key_alongside_real_url",
			text: `key = "/home/user/.ssh/id_rsa"; url = "https://service/api/orders"`,
			want: []string{"/api/orders"},
		},
		{
			name: "etc_config_path",
			text: `cfg := "/etc/redacted/config.toml"`,
			want: nil,
		},
		{
			name: "var_log",
			text: `log_path = "/var/log/sartv.log"`,
			want: nil,
		},
		{
			name: "tmp_socket",
			text: `socket = "/tmp/runtime.sock"`,
			want: nil,
		},
		{
			name: "real_api_path_passes",
			text: `path = "/api/v1/orders"`,
			want: []string{"/api/v1/orders"},
		},
		{
			name: "var_in_path_should_pass",
			text: `path = "/api/var/orders"`, // /api/var/... is fine — only directory PREFIXES are filtered
			want: []string{"/api/var/orders"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractURLPaths(tt.text)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d paths %v, want %d %v", len(got), got, len(tt.want), tt.want)
			}
			for i, p := range got {
				if p != tt.want[i] {
					t.Errorf("path[%d] = %q, want %q", i, p, tt.want[i])
				}
			}
		})
	}
}

// C1 (Phase C, 2026-05-08): Rust format!() macro path extraction.
// `format!("{}/api/users", base)` carries `/api/users` inside the
// format string but pathRe never sees it (the literal starts with
// `{`, not `/`). Without this, reqwest call sites that compute their
// URL via format!() emit no HTTP_CALLS edge.
func TestExtractURLPaths_FormatMacro(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "format_with_leading_brace_then_path",
			text: `let url = format!("{}/api/users", base);`,
			want: []string{"/api/users"},
		},
		{
			name: "format_with_path_then_id_slot",
			text: `let url = format!("{}/api/users/{}", base, id);`,
			want: []string{"/api/users"},
		},
		{
			name: "format_no_leading_base_just_path_template",
			text: `let url = format!("/api/orders/{}", id);`,
			want: []string{"/api/orders"},
		},
		{
			name: "no_format_no_extraction",
			text: `let url = String::from("plain"); /* /api/skipped */`,
			want: nil,
		},
		{
			name: "format_with_filesystem_path_filtered",
			text: `let p = format!("{}/var/log/app.log", base);`,
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractURLPaths(tt.text)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d paths %v, want %d %v", len(got), got, len(tt.want), tt.want)
			}
			for i, p := range got {
				if p != tt.want[i] {
					t.Errorf("path[%d] = %q, want %q", i, p, tt.want[i])
				}
			}
		})
	}
}

// C2 (Phase C, 2026-05-08): JS/TS template literal path extraction.
// `fetch(`/api/users/${id}`)` carries `/api/users/` inside backticks
// that pathRe never sees. Without this, the call emits no HTTP_CALLS
// edge and the URL is invisible to matchAndLink.
func TestExtractURLPaths_TemplateLiteral(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "fetch_template_with_id_slot",
			text: "const r = await fetch(`/api/users/${id}`);",
			want: []string{"/api/users"},
		},
		{
			name: "fetch_template_static_prefix_then_dynamic",
			text: "fetch(`${baseUrl}/api/orders`)",
			want: []string{"/api/orders"},
		},
		{
			name: "fetch_template_simple_path_no_interp",
			text: "fetch(`/api/items`)",
			want: []string{"/api/items"},
		},
		{
			name: "no_backticks_no_extraction",
			text: "fetch(/* /api/skipped */)",
			want: nil,
		},
		{
			name: "template_with_filesystem_path_filtered",
			text: "const p = `${root}/var/log/app.log`;",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractURLPaths(tt.text)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d paths %v, want %d %v", len(got), got, len(tt.want), tt.want)
			}
			for i, p := range got {
				if p != tt.want[i] {
					t.Errorf("path[%d] = %q, want %q", i, p, tt.want[i])
				}
			}
		})
	}
}

// C1 (Phase C, 2026-05-08): rustConstUrlRe shape coverage.
// Top-level Rust const URL definitions take many shapes. Pin the
// regex on the exact subset extractFunctionCallSites depends on.
func TestRustConstUrlRe(t *testing.T) {
	tests := []struct {
		name string
		text string
		want [][2]string // [name, url]
	}{
		{
			name: "const_with_str_type",
			text: `const BASE_URL: &str = "https://api.example.com/v1";`,
			want: [][2]string{{"BASE_URL", "https://api.example.com/v1"}},
		},
		{
			name: "static_with_static_lifetime",
			text: `static BASE_URL: &'static str = "https://api.example.com/v2";`,
			want: [][2]string{{"BASE_URL", "https://api.example.com/v2"}},
		},
		{
			name: "let_binding_no_type",
			text: `let endpoint = "/api/users";`,
			want: [][2]string{{"endpoint", "/api/users"}},
		},
		{
			name: "pub_const",
			text: `pub const URL: &str = "https://x.com/y";`,
			want: [][2]string{{"URL", "https://x.com/y"}},
		},
		{
			name: "non_url_string_literal_ignored",
			text: `const NAME: &str = "alice";`,
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rustConstUrlRe.FindAllStringSubmatch(tt.text, -1)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d matches %v, want %d %v", len(got), got, len(tt.want), tt.want)
			}
			for i, m := range got {
				if m[1] != tt.want[i][0] {
					t.Errorf("name[%d] = %q, want %q", i, m[1], tt.want[i][0])
				}
				if m[2] != tt.want[i][1] {
					t.Errorf("url[%d] = %q, want %q", i, m[2], tt.want[i][1])
				}
			}
		})
	}
}

func TestIsFilesystemPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		// Filesystem paths
		{"/home/user/.ssh/id_rsa", true},
		{"/root/.bashrc", true},
		{"/var/log/app.log", true},
		{"/etc/hosts", true},
		{"/tmp/socket", true},
		{"/usr/local/bin/foo", true},
		{"/opt/redacted/bin", true},
		{"/dev/null", true},
		{"/proc/cpuinfo", true},
		{"/sys/fs/cgroup", true},
		{"/mnt/data", true},
		{"/media/cdrom", true},
		{"/srv/www", true},
		{"/lib/x86_64-linux-gnu/libc.so.6", true},
		{"/boot/vmlinuz", true},
		{"/run/lock", true},
		// API paths
		{"/api/orders", false},
		{"/v1/users/123", false},
		{"/api/var/x", false}, // "var" inside path is fine
		{"/health", false},
		{"/", false},
	}
	for _, tt := range tests {
		got := isFilesystemPath(tt.path)
		if got != tt.want {
			t.Errorf("isFilesystemPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestExtractPythonRoutes(t *testing.T) {
	node := &store.Node{
		Name:          "create_order",
		QualifiedName: "proj.api.routes.create_order",
		Properties: map[string]any{
			"decorators": []any{
				`@app.post("/api/orders")`,
			},
		},
	}

	routes := extractPythonRoutes(node)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	if routes[0].Path != "/api/orders" {
		t.Errorf("path = %q, want /api/orders", routes[0].Path)
	}
	if routes[0].Method != "POST" {
		t.Errorf("method = %q, want POST", routes[0].Method)
	}
	if routes[0].QualifiedName != "proj.api.routes.create_order" {
		t.Errorf("qn = %q, want proj.api.routes.create_order", routes[0].QualifiedName)
	}
}

func TestExtractPythonRoutesMultiple(t *testing.T) {
	node := &store.Node{
		Name:          "handler",
		QualifiedName: "proj.api.handler",
		Properties: map[string]any{
			"decorators": []any{
				`@router.get("/api/items/{item_id}")`,
				`@router.post("/api/items")`,
			},
		},
	}

	routes := extractPythonRoutes(node)
	if len(routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(routes))
	}
}

func TestExtractPythonRoutesNoDecorators(t *testing.T) {
	node := &store.Node{
		Name:          "helper",
		QualifiedName: "proj.utils.helper",
		Properties:    map[string]any{},
	}

	routes := extractPythonRoutes(node)
	if len(routes) != 0 {
		t.Errorf("expected 0 routes, got %d", len(routes))
	}
}

func TestExtractGoRoutes(t *testing.T) {
	source := `
		r.POST("/api/orders", h.CreateOrder)
		r.GET("/api/orders/:id", h.GetOrder)
	`
	node := &store.Node{
		Name:          "RegisterRoutes",
		QualifiedName: "proj.api.RegisterRoutes",
	}

	routes := extractGoRoutes(node, source)
	if len(routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(routes))
	}
	if routes[0].Path != "/api/orders" {
		t.Errorf("route[0].Path = %q, want /api/orders", routes[0].Path)
	}
	if routes[0].Method != "POST" {
		t.Errorf("route[0].Method = %q, want POST", routes[0].Method)
	}
	if routes[1].Path != "/api/orders/:id" {
		t.Errorf("route[1].Path = %q, want /api/orders/:id", routes[1].Path)
	}
}

func TestReadSourceLines(t *testing.T) {
	dir, err := os.MkdirTemp("", "httplink-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	content := "line1\nline2\nline3\nline4\nline5\n"
	path := filepath.Join(dir, "test.go")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	got := readSourceLines(dir, "test.go", 2, 4)
	want := "line2\nline3\nline4"
	if got != want {
		t.Errorf("readSourceLines = %q, want %q", got, want)
	}
}

func TestReadSourceLinesMissingFile(t *testing.T) {
	got := readSourceLines("/nonexistent", "missing.go", 1, 10)
	if got != "" {
		t.Errorf("expected empty string for missing file, got %q", got)
	}
}

func TestLinkerRun(t *testing.T) {
	// Set up a temp directory with a Python route handler and a Go caller
	dir, err := os.MkdirTemp("", "httplink-e2e-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// Write a Go file that contains a URL constant
	goDir := filepath.Join(dir, "caller")
	if err := os.MkdirAll(goDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goDir, "client.go"), []byte(`package caller
const OrderURL = "https://api.example.com/api/orders"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	project := "testproj"
	if err := s.UpsertProject(project, dir); err != nil {
		t.Fatal(err)
	}

	// Create a Module node with constants containing a URL
	callerID, _ := s.UpsertNode(&store.Node{
		Project:       project,
		Label:         "Module",
		Name:          "client.go",
		QualifiedName: "testproj.caller.client",
		FilePath:      "caller/client.go",
		Properties: map[string]any{
			"constants": []any{`OrderURL = "https://api.example.com/api/orders"`},
		},
	})

	// Create a Function node with a Python route decorator
	handlerID, _ := s.UpsertNode(&store.Node{
		Project:       project,
		Label:         "Function",
		Name:          "create_order",
		QualifiedName: "testproj.handler.routes.create_order",
		FilePath:      "handler/routes.py",
		Properties: map[string]any{
			"decorators": []any{`@app.post("/api/orders")`},
		},
	})

	if callerID == 0 || handlerID == 0 {
		t.Fatal("failed to create test nodes")
	}

	linker := New(s, project)
	links, err := linker.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(links) == 0 {
		t.Fatal("expected at least 1 HTTP link, got 0")
	}

	// Verify the link
	found := false
	for _, link := range links {
		if link.CallerQN == "testproj.caller.client" && link.HandlerQN == "testproj.handler.routes.create_order" {
			found = true
			t.Logf("link: %s -> %s (path=%s)", link.CallerQN, link.HandlerQN, link.URLPath)
		}
	}
	if !found {
		t.Error("expected link from testproj.caller.client to testproj.handler.routes.create_order")
		for _, link := range links {
			t.Logf("  got: %s -> %s", link.CallerQN, link.HandlerQN)
		}
	}

	// Verify edge was created in store
	callerNode, _ := s.FindNodeByQN(project, "testproj.caller.client")
	if callerNode == nil {
		t.Fatal("caller node not found")
	}
	edges, _ := s.FindEdgesBySourceAndType(callerNode.ID, "HTTP_CALLS")
	if len(edges) == 0 {
		t.Error("expected HTTP_CALLS edge in store, got 0")
	}
}

func TestExtractJSONStringPaths(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{
			name: "JSON object with URL",
			text: `BODY = '{"target": "https://api.internal.com/api/orders", "method": "POST"}'`,
			want: 1, // /api/orders
		},
		{
			name: "JSON object with path",
			text: `CONFIG = {"endpoint": "/api/v1/process", "timeout": 30}`,
			want: 1, // /api/v1/process
		},
		{
			name: "no JSON",
			text: `plain string without json`,
			want: 0,
		},
		{
			name: "nested JSON with URL",
			text: `{"services": [{"url": "https://svc.example.com/api/health"}]}`,
			want: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJSONStringPaths(tt.text)
			if len(got) != tt.want {
				t.Errorf("extractJSONStringPaths(%q) returned %d paths, want %d: %v", tt.text, len(got), tt.want, got)
			}
		})
	}
}

func TestRouteNodesCreated(t *testing.T) {
	dir, err := os.MkdirTemp("", "httplink-route-nodes-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	s, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	project := "testproj"
	if err := s.UpsertProject(project, dir); err != nil {
		t.Fatal(err)
	}

	// Create a Function node with a Python route decorator
	_, _ = s.UpsertNode(&store.Node{
		Project:       project,
		Label:         "Function",
		Name:          "create_order",
		QualifiedName: "testproj.handler.routes.create_order",
		FilePath:      "handler/routes.py",
		Properties: map[string]any{
			"decorators": []any{`@app.post("/api/orders")`},
		},
	})

	linker := New(s, project)
	_, err = linker.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Verify Route node was created
	routeNodes, _ := s.FindNodesByLabel(project, "Route")
	if len(routeNodes) != 1 {
		t.Fatalf("expected 1 Route node, got %d", len(routeNodes))
	}
	rn := routeNodes[0]
	if rn.FilePath != "handler/routes.py" {
		t.Errorf("Route file_path = %q, want 'handler/routes.py'", rn.FilePath)
	}
	if rn.Name != "POST /api/orders" {
		t.Errorf("Route name = %q, want 'POST /api/orders'", rn.Name)
	}
	if rn.Properties["method"] != "POST" {
		t.Errorf("Route method = %v, want POST", rn.Properties["method"])
	}
	if rn.Properties["path"] != "/api/orders" {
		t.Errorf("Route path = %v, want /api/orders", rn.Properties["path"])
	}

	// Verify HANDLES edge from handler → Route
	handlerNode, _ := s.FindNodeByQN(project, "testproj.handler.routes.create_order")
	if handlerNode == nil {
		t.Fatal("handler node not found")
	}
	edges, _ := s.FindEdgesBySourceAndType(handlerNode.ID, "HANDLES")
	if len(edges) != 1 {
		t.Errorf("expected 1 HANDLES edge, got %d", len(edges))
	}

	// Verify handler marked as entry point
	if handlerNode.Properties["is_entry_point"] != true {
		t.Error("expected handler to be marked as is_entry_point")
	}
}

func TestCrossFileGroupPrefix(t *testing.T) {
	dir, err := os.MkdirTemp("", "httplink-crossfile-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// Write a two-file Go project: main.go calls RegisterRoutes(v1.Group("/api"))
	if err := os.MkdirAll(filepath.Join(dir, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "routes"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "cmd", "main.go"), []byte(`package main

func setup(r *gin.Engine) {
	RegisterRoutes(r.Group("/api"))
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "routes", "routes.go"), []byte(`package routes

func RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/orders", ListOrders)
	rg.POST("/orders", CreateOrder)
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	project := "testproj"
	if err := s.UpsertProject(project, dir); err != nil {
		t.Fatal(err)
	}

	// Create function nodes that simulate what the pipeline would create
	setupID, _ := s.UpsertNode(&store.Node{
		Project:       project,
		Label:         "Function",
		Name:          "setup",
		QualifiedName: "testproj.cmd.main.setup",
		FilePath:      "cmd/main.go",
		StartLine:     3,
		EndLine:       5,
	})

	regID, _ := s.UpsertNode(&store.Node{
		Project:       project,
		Label:         "Function",
		Name:          "RegisterRoutes",
		QualifiedName: "testproj.routes.routes.RegisterRoutes",
		FilePath:      "routes/routes.go",
		StartLine:     3,
		EndLine:       6,
	})

	// Create CALLS edge: setup -> RegisterRoutes (as pipeline pass3 would)
	if _, err := s.InsertEdge(&store.Edge{
		Project:  project,
		SourceID: setupID,
		TargetID: regID,
		Type:     "CALLS",
	}); err != nil {
		t.Fatal(err)
	}

	linker := New(s, project)
	_, err = linker.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Verify Route nodes have the cross-file prefix /api prepended
	routeNodes, _ := s.FindNodesByLabel(project, "Route")
	if len(routeNodes) != 2 {
		t.Fatalf("expected 2 Route nodes, got %d", len(routeNodes))
	}

	foundPaths := map[string]bool{}
	for _, rn := range routeNodes {
		path, _ := rn.Properties["path"].(string)
		foundPaths[path] = true
		t.Logf("Route: %s (path=%s)", rn.Name, path)
	}

	if !foundPaths["/api/orders"] {
		t.Error("expected route path /api/orders with cross-file prefix")
	}
}

func TestCrossFileGroupPrefixVariable(t *testing.T) {
	dir, err := os.MkdirTemp("", "httplink-crossfile-var-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	if err := os.MkdirAll(filepath.Join(dir, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "routes"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Variable-based pattern: v1 := r.Group("/api"); RegisterRoutes(v1)
	if err := os.WriteFile(filepath.Join(dir, "cmd", "main.go"), []byte(`package main

func setup(r *gin.Engine) {
	v1 := r.Group("/api")
	RegisterRoutes(v1)
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "routes", "routes.go"), []byte(`package routes

func RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/items", ListItems)
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	project := "testproj"
	if err := s.UpsertProject(project, dir); err != nil {
		t.Fatal(err)
	}

	setupID, _ := s.UpsertNode(&store.Node{
		Project: project, Label: "Function", Name: "setup",
		QualifiedName: "testproj.cmd.main.setup",
		FilePath:      "cmd/main.go", StartLine: 3, EndLine: 6,
	})

	regID, _ := s.UpsertNode(&store.Node{
		Project: project, Label: "Function", Name: "RegisterRoutes",
		QualifiedName: "testproj.routes.routes.RegisterRoutes",
		FilePath:      "routes/routes.go", StartLine: 3, EndLine: 5,
	})

	if _, err := s.InsertEdge(&store.Edge{
		Project: project, SourceID: setupID, TargetID: regID, Type: "CALLS",
	}); err != nil {
		t.Fatal(err)
	}

	linker := New(s, project)
	if _, runErr := linker.Run(); runErr != nil {
		t.Fatal(runErr)
	}

	routeNodes, _ := s.FindNodesByLabel(project, "Route")
	if len(routeNodes) != 1 {
		t.Fatalf("expected 1 Route node, got %d", len(routeNodes))
	}

	path, _ := routeNodes[0].Properties["path"].(string)
	if path != "/api/items" {
		t.Errorf("expected /api/items, got %s", path)
	}
}

func TestRouteRegistrationCallEdges(t *testing.T) {
	dir, err := os.MkdirTemp("", "httplink-reg-edges-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	if err := os.MkdirAll(filepath.Join(dir, "routes"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "routes", "routes.go"), []byte(`package routes

func RegisterRoutes(rg *gin.RouterGroup) {
	rg.POST("/orders", h.CreateOrder)
	rg.GET("/orders/:id", h.GetOrder)
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	project := "testproj"
	if err := s.UpsertProject(project, dir); err != nil {
		t.Fatal(err)
	}

	// Create the registering function
	if _, err := s.UpsertNode(&store.Node{
		Project: project, Label: "Function", Name: "RegisterRoutes",
		QualifiedName: "testproj.routes.routes.RegisterRoutes",
		FilePath:      "routes/routes.go", StartLine: 3, EndLine: 6,
	}); err != nil {
		t.Fatal(err)
	}

	// Create handler functions (as pipeline would)
	if _, err := s.UpsertNode(&store.Node{
		Project: project, Label: "Method", Name: "CreateOrder",
		QualifiedName: "testproj.handlers.handler.CreateOrder",
		FilePath:      "handlers/handler.go", StartLine: 10, EndLine: 30,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertNode(&store.Node{
		Project: project, Label: "Method", Name: "GetOrder",
		QualifiedName: "testproj.handlers.handler.GetOrder",
		FilePath:      "handlers/handler.go", StartLine: 32, EndLine: 50,
	}); err != nil {
		t.Fatal(err)
	}

	linker := New(s, project)
	if _, runErr := linker.Run(); runErr != nil {
		t.Fatal(runErr)
	}

	// Verify CALLS edges from RegisterRoutes to handlers
	regNode, _ := s.FindNodeByQN(project, "testproj.routes.routes.RegisterRoutes")
	if regNode == nil {
		t.Fatal("RegisterRoutes node not found")
	}

	edges, _ := s.FindEdgesBySourceAndType(regNode.ID, "CALLS")
	if len(edges) < 2 {
		t.Errorf("expected at least 2 CALLS edges from RegisterRoutes, got %d", len(edges))
	}

	// Verify that CreateOrder is a target
	createNode, _ := s.FindNodeByQN(project, "testproj.handlers.handler.CreateOrder")
	if createNode == nil {
		t.Fatal("CreateOrder not found")
	}
	foundCreate := false
	for _, e := range edges {
		if e.TargetID == createNode.ID {
			foundCreate = true
			// Check the via property
			if via, ok := e.Properties["via"]; ok {
				if via != "route_registration" {
					t.Errorf("expected via=route_registration, got %v", via)
				}
			}
		}
	}
	if !foundCreate {
		t.Error("expected CALLS edge from RegisterRoutes to CreateOrder")
	}
}

func TestAsyncDispatchKeywords(t *testing.T) {
	dir, err := os.MkdirTemp("", "httplink-async-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	writeTestFile(t, dir, "taskworker", "dispatch.go", `package taskworker

func DispatchOrder(orderID string) {
	url := "https://api.internal.com/api/orders"
	client.CreateTask(ctx, url, payload)
}
`)
	writeTestFile(t, dir, "synccaller", "caller.go", `package synccaller

func CallOrder() {
	url := "https://api.internal.com/api/orders"
	requests.post(url, data)
}
`)
	writeTestFile(t, dir, "bothcaller", "both.go", `package bothcaller

func CallAndDispatch() {
	url := "https://api.internal.com/api/orders"
	requests.post(url, data)
	client.CreateTask(ctx, url, payload)
}
`)

	s, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	project := "testproj"
	if err := s.UpsertProject(project, dir); err != nil {
		t.Fatal(err)
	}

	createTestNode(t, s, project, "DispatchOrder", "testproj.taskworker.dispatch.DispatchOrder", "taskworker/dispatch.go", 3, 6)
	createTestNode(t, s, project, "CallOrder", "testproj.synccaller.caller.CallOrder", "synccaller/caller.go", 3, 6)
	createTestNode(t, s, project, "CallAndDispatch", "testproj.bothcaller.both.CallAndDispatch", "bothcaller/both.go", 3, 7)

	_, _ = s.UpsertNode(&store.Node{
		Project: project, Label: "Function", Name: "create_order",
		QualifiedName: "testproj.handler.routes.create_order",
		FilePath:      "handler/routes.py",
		Properties:    map[string]any{"decorators": []any{`@app.post("/api/orders")`}},
	})

	linker := New(s, project)
	links, err := linker.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	edgeTypes := map[string]string{}
	for _, link := range links {
		edgeTypes[link.CallerQN] = link.EdgeType
	}

	assertEdgeType(t, edgeTypes, "testproj.taskworker.dispatch.DispatchOrder", "ASYNC_CALLS")
	assertEdgeType(t, edgeTypes, "testproj.synccaller.caller.CallOrder", "HTTP_CALLS")
	assertEdgeType(t, edgeTypes, "testproj.bothcaller.both.CallAndDispatch", "HTTP_CALLS")

	assertStoredEdgeCounts(t, s, project, "testproj.taskworker.dispatch.DispatchOrder", 1, 0)
	assertStoredEdgeCounts(t, s, project, "testproj.synccaller.caller.CallOrder", 0, 1)
}

func writeTestFile(t *testing.T, dir, subdir, filename, content string) {
	t.Helper()
	d := filepath.Join(dir, subdir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, filename), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func createTestNode(t *testing.T, s *store.Store, project, name, qn, filePath string, startLine, endLine int) {
	t.Helper()
	_, err := s.UpsertNode(&store.Node{
		Project: project, Label: "Function", Name: name,
		QualifiedName: qn, FilePath: filePath,
		StartLine: startLine, EndLine: endLine,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertEdgeType(t *testing.T, edgeTypes map[string]string, qn, wantType string) {
	t.Helper()
	et, ok := edgeTypes[qn]
	if !ok {
		t.Errorf("expected link from %s", qn)
		return
	}
	if et != wantType {
		t.Errorf("%s edge type = %q, want %q", qn, et, wantType)
	}
}

func assertStoredEdgeCounts(t *testing.T, s *store.Store, project, qn string, wantAsync, wantHTTP int) {
	t.Helper()
	node, _ := s.FindNodeByQN(project, qn)
	if node == nil {
		t.Errorf("node not found: %s", qn)
		return
	}
	asyncEdges, _ := s.FindEdgesBySourceAndType(node.ID, "ASYNC_CALLS")
	if len(asyncEdges) != wantAsync {
		t.Errorf("%s: ASYNC_CALLS edges = %d, want %d", qn, len(asyncEdges), wantAsync)
	}
	httpEdges, _ := s.FindEdgesBySourceAndType(node.ID, "HTTP_CALLS")
	if len(httpEdges) != wantHTTP {
		t.Errorf("%s: HTTP_CALLS edges = %d, want %d", qn, len(httpEdges), wantHTTP)
	}
}

func TestExtractFunctionCallSitesAsync(t *testing.T) {
	// Test extractFunctionCallSites directly with a temp file containing async keywords.
	dir, err := os.MkdirTemp("", "httplink-extract-async-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// Write a Go file with CreateTask and a URL
	if err := os.MkdirAll(filepath.Join(dir, "worker"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "worker", "task.go"), []byte(`package worker

func EnqueueJob(ctx context.Context) {
	url := "https://backend.internal.com/api/process"
	client.CreateTask(ctx, &taskspb.CreateTaskRequest{
		HttpRequest: &taskspb.HttpRequest{Url: url},
	})
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	node := &store.Node{
		Project: "testproj", Label: "Function", Name: "EnqueueJob",
		QualifiedName: "testproj.worker.task.EnqueueJob",
		FilePath:      "worker/task.go", StartLine: 3, EndLine: 7,
	}

	sites := extractFunctionCallSites(node, dir)
	if len(sites) == 0 {
		t.Fatal("expected at least 1 call site, got 0")
	}

	foundAsync := false
	for _, s := range sites {
		if s.IsAsync {
			foundAsync = true
			if s.Path != "/api/process" {
				t.Errorf("async site path = %q, want /api/process", s.Path)
			}
		}
	}
	if !foundAsync {
		t.Error("expected at least one call site with IsAsync=true")
	}

	// Also test that a function with only sync keywords gets IsAsync=false
	if err := os.WriteFile(filepath.Join(dir, "worker", "sync.go"), []byte(`package worker

func SyncCall(ctx context.Context) {
	url := "https://backend.internal.com/api/process"
	requests.post(url, data)
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	syncNode := &store.Node{
		Project: "testproj", Label: "Function", Name: "SyncCall",
		QualifiedName: "testproj.worker.sync.SyncCall",
		FilePath:      "worker/sync.go", StartLine: 3, EndLine: 6,
	}

	syncSites := extractFunctionCallSites(syncNode, dir)
	if len(syncSites) == 0 {
		t.Fatal("expected at least 1 sync call site, got 0")
	}
	for _, s := range syncSites {
		if s.IsAsync {
			t.Errorf("sync call site should have IsAsync=false, got true (path=%s)", s.Path)
		}
	}
}

func TestLinkerSkipsSameService(t *testing.T) {
	s, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	dir, err := os.MkdirTemp("", "httplink-same-svc-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	project := "testproj"
	if err := s.UpsertProject(project, dir); err != nil {
		t.Fatal(err)
	}

	// Both in the same service (same first 4 QN segments: testproj.cat.sub.svcA)
	_, _ = s.UpsertNode(&store.Node{
		Project:       project,
		Label:         "Module",
		Name:          "client.py",
		QualifiedName: "testproj.cat.sub.svcA.internal.client",
		FilePath:      "cat/sub/svcA/internal/client.py",
		Properties: map[string]any{
			"constants": []any{`URL = "https://localhost/api/orders"`},
		},
	})

	_, _ = s.UpsertNode(&store.Node{
		Project:       project,
		Label:         "Function",
		Name:          "handle_orders",
		QualifiedName: "testproj.cat.sub.svcA.internal.handle_orders",
		FilePath:      "cat/sub/svcA/internal/routes.py",
		Properties: map[string]any{
			"decorators": []any{`@app.get("/api/orders")`},
		},
	})

	linker := New(s, project)
	links, err := linker.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(links) != 0 {
		t.Errorf("expected 0 links (same service), got %d", len(links))
		for _, l := range links {
			t.Logf("  %s -> %s", l.CallerQN, l.HandlerQN)
		}
	}
}

func TestDetectProtocol(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{"go websocket upgrade", `err := websocket.Upgrade(w, r, nil, 1024, 1024)`, "ws"},
		{"go websocket accept", `conn, err := websocket.Accept(w, r, nil)`, "ws"},
		{"go upgrader", `conn, err := upgrader.Upgrade(w, r, nil)`, "ws"},
		{"js ws", `ws.on("connection", func)`, "ws"},
		{"js socketio", `io.on("connection", handler)`, "ws"},
		{"sse content type", `w.Header().Set("Content-Type", "text/event-stream")`, "sse"},
		{"python sse", `return EventSourceResponse(generate())`, "sse"},
		{"java sse emitter", `SseEmitter emitter = new SseEmitter()`, "sse"},
		{"java sse event", `ServerSentEvent event = ServerSentEvent.builder()`, "sse"},
		{"no protocol", `return json.Marshal(result)`, ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectProtocol(tt.source)
			if got != tt.want {
				t.Errorf("detectProtocol() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractPythonWSRoutes(t *testing.T) {
	node := &store.Node{
		Name:          "ws_handler",
		QualifiedName: "proj.api.ws_handler",
		Properties: map[string]any{
			"decorators": []any{
				`@app.websocket("/ws/chat")`,
			},
		},
	}

	routes := extractPythonRoutes(node)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	if routes[0].Path != "/ws/chat" {
		t.Errorf("path = %q, want /ws/chat", routes[0].Path)
	}
	if routes[0].Method != "WS" {
		t.Errorf("method = %q, want WS", routes[0].Method)
	}
	if routes[0].Protocol != "ws" {
		t.Errorf("protocol = %q, want ws", routes[0].Protocol)
	}
}

func TestExtractSpringWSRoutes(t *testing.T) {
	node := &store.Node{
		Name:          "handleChat",
		QualifiedName: "proj.ChatController.handleChat",
		Properties: map[string]any{
			"decorators": []any{
				`@MessageMapping("/chat")`,
			},
		},
	}

	routes := extractJavaRoutes(node)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	if routes[0].Path != "/chat" {
		t.Errorf("path = %q, want /chat", routes[0].Path)
	}
	if routes[0].Method != "WS" {
		t.Errorf("method = %q, want WS", routes[0].Method)
	}
	if routes[0].Protocol != "ws" {
		t.Errorf("protocol = %q, want ws", routes[0].Protocol)
	}
}

func TestExtractKtorWSRoutes(t *testing.T) {
	source := `
	webSocket("/chat") {
		for (frame in incoming) {
			send(frame)
		}
	}
	get("/api/health") {
		call.respond("ok")
	}
`
	node := &store.Node{
		Name:          "configureRouting",
		QualifiedName: "proj.Routing.configureRouting",
	}

	routes := extractKtorRoutes(node, source)
	if len(routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(routes))
	}

	// Check WS route
	wsFound := false
	httpFound := false
	for _, r := range routes {
		if r.Protocol == "ws" && r.Path == "/chat" && r.Method == "WS" {
			wsFound = true
		}
		if r.Path == "/api/health" && r.Method == "GET" {
			httpFound = true
		}
	}
	if !wsFound {
		t.Error("expected WS route for /chat")
	}
	if !httpFound {
		t.Error("expected HTTP route for /api/health")
	}
}

func TestChiPrefix(t *testing.T) {
	source := `
func SetupRoutes(r chi.Router) {
	r.Route("/api", func(r chi.Router) {
		r.Get("/health", healthHandler)
		r.Route("/users", func(r chi.Router) {
			r.Get("/", listUsers)
			r.Post("/{id}", updateUser)
		})
	})
}
`
	node := &store.Node{
		Name:          "SetupRoutes",
		QualifiedName: "proj.SetupRoutes",
	}

	routes := extractGoRoutes(node, source)

	expectedPaths := map[string]string{
		"/api/health":     "GET",
		"/api/users":      "GET",
		"/api/users/{id}": "POST",
	}

	if len(routes) != len(expectedPaths) {
		t.Fatalf("expected %d routes, got %d", len(expectedPaths), len(routes))
		for _, r := range routes {
			t.Logf("  %s %s", r.Method, r.Path)
		}
	}

	for _, r := range routes {
		wantMethod, ok := expectedPaths[r.Path]
		if !ok {
			t.Errorf("unexpected route: %s %s", r.Method, r.Path)
			continue
		}
		if r.Method != wantMethod {
			t.Errorf("route %s: method = %q, want %q", r.Path, r.Method, wantMethod)
		}
	}
}

func TestChiPrefixMixedWithGin(t *testing.T) {
	// When no chi Route() blocks, gin group resolution should still work
	source := `
func RegisterRoutes(r *gin.RouterGroup) {
	orders := r.Group("/orders")
	orders.GET("/:id", getOrder)
	orders.POST("", createOrder)
}
`
	node := &store.Node{
		Name:          "RegisterRoutes",
		QualifiedName: "proj.RegisterRoutes",
	}

	routes := extractGoRoutes(node, source)
	if len(routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(routes))
	}

	for _, r := range routes {
		if !strings.HasPrefix(r.Path, "/orders") {
			t.Errorf("expected /orders prefix, got %s", r.Path)
		}
	}
}

func TestFastAPIPrefix(t *testing.T) {
	dir, err := os.MkdirTemp("", "httplink-fastapi-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// Write Python files: main.py with include_router, orders/routes.py with routes
	if err := os.MkdirAll(filepath.Join(dir, "orders"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte(`
from orders.routes import order_router

app = FastAPI()
app.include_router(order_router, prefix="/api/v1/orders")
`), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	project := "testproj"
	if err := s.UpsertProject(project, dir); err != nil {
		t.Fatal(err)
	}

	// Create Module for main.py (has the include_router call)
	_, _ = s.UpsertNode(&store.Node{
		Project: project, Label: "Module", Name: "main.py",
		QualifiedName: "testproj//main.py",
		FilePath:      "main.py",
	})

	// Create Function with route in the orders module
	_, _ = s.UpsertNode(&store.Node{
		Project: project, Label: "Function", Name: "list_orders",
		QualifiedName: "testproj//orders/routes.py/list_orders",
		FilePath:      "orders/routes.py",
		Properties: map[string]any{
			"decorators": []any{`@order_router.get("/")`},
		},
	})

	linker := New(s, project)
	_, err = linker.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	routeNodes, _ := s.FindNodesByLabel(project, "Route")
	if len(routeNodes) != 1 {
		t.Fatalf("expected 1 Route node, got %d", len(routeNodes))
	}

	path, _ := routeNodes[0].Properties["path"].(string)
	if path != "/api/v1/orders/" {
		t.Errorf("expected /api/v1/orders/, got %s", path)
	}
}

func TestExpressPrefix(t *testing.T) {
	dir, err := os.MkdirTemp("", "httplink-express-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	if err := os.MkdirAll(filepath.Join(dir, "routes"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "app.js"), []byte(`
const orderRouter = require('./routes/orders');
app.use("/api/orders", orderRouter);
`), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	project := "testproj"
	if err := s.UpsertProject(project, dir); err != nil {
		t.Fatal(err)
	}

	// Module for app.js
	_, _ = s.UpsertNode(&store.Node{
		Project: project, Label: "Module", Name: "app.js",
		QualifiedName: "testproj//app.js",
		FilePath:      "app.js",
	})

	// Function with route in orders module
	_, _ = s.UpsertNode(&store.Node{
		Project: project, Label: "Function", Name: "getOrder",
		QualifiedName: "testproj//routes/orders.js/getOrder",
		FilePath:      "routes/orders.js",
		StartLine:     1, EndLine: 5,
	})

	// Write the routes file with Express route
	if err := os.WriteFile(filepath.Join(dir, "routes", "orders.js"), []byte(`
router.get("/:id", function(req, res) {
	res.json({id: req.params.id});
});
`), 0o600); err != nil {
		t.Fatal(err)
	}

	linker := New(s, project)
	_, err = linker.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	routeNodes, _ := s.FindNodesByLabel(project, "Route")
	if len(routeNodes) != 1 {
		t.Fatalf("expected 1 Route node, got %d", len(routeNodes))
	}

	path, _ := routeNodes[0].Properties["path"].(string)
	if path != "/api/orders/:id" {
		t.Errorf("expected /api/orders/:id, got %s", path)
	}
}

func TestExpressRouteFiltering(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantMatch  bool
		wantMethod string
		wantPath   string
	}{
		// Should match (allowlisted receivers)
		{"app.get", `app.get('/api/users', handler)`, true, "GET", "/api/users"},
		{"router.post", `router.post('/orders', handler)`, true, "POST", "/orders"},
		{"server.put", `server.put('/items', handler)`, true, "PUT", "/items"},
		{"api.delete", `api.delete('/users/:id', handler)`, true, "DELETE", "/users/:id"},
		{"routes.patch", `routes.patch('/items/:id', handler)`, true, "PATCH", "/items/:id"},
		// Should NOT match (not in allowlist)
		{"req.get", `req.get('Content-Type')`, false, "", ""},
		{"res.get", `res.get('key')`, false, "", ""},
		{"this.get", `this.get('property')`, false, "", ""},
		{"map.get", `map.get('key')`, false, "", ""},
		{"model.delete", `model.delete('record')`, false, "", ""},
		{"params.get", `params.get('id')`, false, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := &store.Node{
				Name:          "testFunc",
				QualifiedName: "proj.test.testFunc",
			}
			routes := extractExpressRoutes(node, tt.line)
			if tt.wantMatch {
				if len(routes) == 0 {
					t.Errorf("expected route match, got 0 routes")
					return
				}
				if routes[0].Method != tt.wantMethod {
					t.Errorf("method = %q, want %q", routes[0].Method, tt.wantMethod)
				}
				if routes[0].Path != tt.wantPath {
					t.Errorf("path = %q, want %q", routes[0].Path, tt.wantPath)
				}
			} else if len(routes) > 0 {
				t.Errorf("expected no match, got %d routes: %v", len(routes), routes[0].Path)
			}
		})
	}
}

func TestLaravelModuleLevelRoutes(t *testing.T) {
	dir, err := os.MkdirTemp("", "httplink-laravel-module-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// Create a Laravel-style route file with module-level Route:: calls
	if err := os.MkdirAll(filepath.Join(dir, "routes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "routes", "api.php"), []byte(`<?php

use App\Http\Controllers\OrderController;

Route::get('/api/orders', [OrderController::class, 'index']);
Route::post('/api/orders', [OrderController::class, 'store']);
Route::get('/api/orders/{id}', [OrderController::class, 'show']);
`), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	project := "testproj"
	if err := s.UpsertProject(project, dir); err != nil {
		t.Fatal(err)
	}

	// Create Module node for the PHP route file (as pipeline would)
	// No Function/Method nodes — routes are at module level
	_, _ = s.UpsertNode(&store.Node{
		Project:       project,
		Label:         "Module",
		Name:          "api.php",
		QualifiedName: "testproj.routes.api",
		FilePath:      "routes/api.php",
	})

	linker := New(s, project)
	_, err = linker.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	routeNodes, _ := s.FindNodesByLabel(project, "Route")
	if len(routeNodes) < 3 {
		t.Fatalf("expected at least 3 Route nodes from module-level Laravel routes, got %d", len(routeNodes))
	}

	foundPaths := map[string]bool{}
	for _, rn := range routeNodes {
		path, _ := rn.Properties["path"].(string)
		foundPaths[path] = true
		t.Logf("Route: %s (path=%s)", rn.Name, path)
	}

	for _, wantPath := range []string{"/api/orders", "/api/orders/{id}"} {
		if !foundPaths[wantPath] {
			t.Errorf("expected route path %s", wantPath)
		}
	}
}

func TestPythonDictGetNotRoute(t *testing.T) {
	// Python dict.get(), session.get(), params.delete() etc. must NOT create Route nodes.
	// The Express/Go/Ktor source-based extractors should only run on their own file types.
	dir, err := os.MkdirTemp("", "httplink-pydict-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// Write a Python file with common dict/object .get()/.delete() calls
	if err := os.MkdirAll(filepath.Join(dir, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	pySource := `def configure(app, config, router, session, params):
    db_url = config.get("database_url")
    secret = app.get("secret_key")
    prefix = router.get("api_prefix")
    token = session.get("auth_token")
    params.delete("old_param")
    api.get("setting")
    server.post("event_name")
`
	if err := os.WriteFile(filepath.Join(dir, "app", "settings.py"), []byte(pySource), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	project := "testproj"
	if err := s.UpsertProject(project, dir); err != nil {
		t.Fatal(err)
	}

	// Create a Function node in a .py file with source lines matching Express/Go/Ktor patterns
	_, _ = s.UpsertNode(&store.Node{
		Project:       project,
		Label:         "Function",
		Name:          "configure",
		QualifiedName: "testproj.app.settings.configure",
		FilePath:      "app/settings.py",
		StartLine:     1,
		EndLine:       8,
		Properties:    map[string]any{},
	})

	linker := New(s, project)
	_, err = linker.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	routeNodes, _ := s.FindNodesByLabel(project, "Route")
	if len(routeNodes) != 0 {
		for _, rn := range routeNodes {
			t.Logf("unexpected Route: %s (path=%v, method=%v)", rn.Name, rn.Properties["path"], rn.Properties["method"])
		}
		t.Fatalf("expected 0 Route nodes from Python dict.get() calls, got %d", len(routeNodes))
	}
}

func TestIsTestNodeFiltering(t *testing.T) {
	tests := []struct {
		filePath string
		isTest   bool
		expected bool
	}{
		{"src/routes/api.js", false, false},
		{"test/app.get.js", false, true},
		{"__tests__/routes.test.ts", false, true},
		{"src/routes/api.js", true, true},
		{"lib/router/index.js", false, false},
		{"tests/fixtures/server.js", false, true},
		{"app/controllers/orders_controller.rb", false, false},
	}

	for _, tt := range tests {
		n := &store.Node{
			FilePath:   tt.filePath,
			Properties: map[string]any{"is_test": tt.isTest},
		}
		got := isTestNode(n)
		if got != tt.expected {
			t.Errorf("isTestNode(%q, is_test=%v) = %v, want %v", tt.filePath, tt.isTest, got, tt.expected)
		}
	}
}

// A4 (2026-05-07): isServerJSFile excludes .jsx/.tsx (React client)
// from Express route extraction.
func TestIsServerJSFileExcludesReactExtensions(t *testing.T) {
	tests := []struct {
		path string
		want bool
		why  string
	}{
		{"server/index.js", true, "plain server JS"},
		{"src/api.ts", true, "server-side TS"},
		{"lib/server.mjs", true, "ESM server"},
		{"lib/server.cjs", true, "CommonJS server"},
		{"src/api.mts", true, "ESM TypeScript"},
		{"src/api.cts", true, "CommonJS TypeScript"},
		// React client extensions — must be rejected.
		{"src/components/Button.jsx", false, "React JSX component"},
		{"src/pages/Home.tsx", false, "React TSX page"},
		// Other extensions
		{"main.go", false, "non-JS"},
		{"main.py", false, "non-JS"},
		{"", false, "empty path"},
	}
	for _, tt := range tests {
		got := isServerJSFile(tt.path)
		if got != tt.want {
			t.Errorf("isServerJSFile(%q) = %v, want %v (%s)", tt.path, got, tt.want, tt.why)
		}
	}
}

// isJSFile is used outside the route extractor (general file
// classification). It MUST still recognize .jsx / .tsx so non-route
// graph passes work unchanged.
func TestIsJSFileStillIncludesReactExtensions(t *testing.T) {
	if !isJSFile("src/Button.jsx") {
		t.Error("isJSFile should still match .jsx for non-route uses")
	}
	if !isJSFile("src/Page.tsx") {
		t.Error("isJSFile should still match .tsx for non-route uses")
	}
}

// --- Phase D2 (2026-05-07) — Axum route extraction ---

func TestExtractAxumRoutes_BasicGet(t *testing.T) {
	source := `pub fn build_router() -> Router {
    Router::new()
        .route("/api/users", get(list_users))
        .route("/api/users/:id", get(get_user))
}`
	f := &store.Node{Name: "build_router", QualifiedName: "myproj.api.build_router", FilePath: "src/api.rs"}
	routes := extractAxumRoutes(f, source)
	if len(routes) != 2 {
		t.Fatalf("expected 2 routes, got %d: %+v", len(routes), routes)
	}
	if routes[0].Path != "/api/users" || routes[0].Method != "GET" || routes[0].HandlerRef != "list_users" {
		t.Errorf("route[0]: %+v, want path=/api/users method=GET handler=list_users", routes[0])
	}
	if routes[1].Path != "/api/users/:id" || routes[1].HandlerRef != "get_user" {
		t.Errorf("route[1]: %+v, want path=/api/users/:id handler=get_user", routes[1])
	}
}

func TestExtractAxumRoutes_AllMethods(t *testing.T) {
	source := `Router::new()
    .route("/g", get(h_get))
    .route("/p", post(h_post))
    .route("/u", put(h_put))
    .route("/d", delete(h_delete))
    .route("/pa", patch(h_patch))
    .route("/h", head(h_head))
    .route("/o", options(h_options))`
	f := &store.Node{Name: "router", QualifiedName: "p.router", FilePath: "src/r.rs"}
	routes := extractAxumRoutes(f, source)
	if len(routes) != 7 {
		t.Fatalf("expected 7 routes, got %d", len(routes))
	}
	wantMethods := []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"}
	for i, want := range wantMethods {
		if routes[i].Method != want {
			t.Errorf("route[%d].Method = %q, want %q", i, routes[i].Method, want)
		}
	}
}

func TestExtractAxumRoutes_StripsHandlerModulePrefix(t *testing.T) {
	source := `.route("/admin", post(admin::create_user))`
	f := &store.Node{Name: "x", QualifiedName: "p.x", FilePath: "src/x.rs"}
	routes := extractAxumRoutes(f, source)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	if routes[0].HandlerRef != "create_user" {
		t.Errorf("HandlerRef = %q, want create_user (module prefix stripped)", routes[0].HandlerRef)
	}
}

func TestExtractAxumRoutes_NoMatchOnNonAxumSource(t *testing.T) {
	// Actix-style attributes shouldn't match the axum regex (they're
	// extracted via decorators, not source-line scan).
	source := `#[get("/api/users")]
async fn list_users() -> impl Responder {
    HttpResponse::Ok().body("users")
}`
	f := &store.Node{Name: "list_users", QualifiedName: "p.list_users", FilePath: "src/api.rs"}
	routes := extractAxumRoutes(f, source)
	if len(routes) != 0 {
		t.Errorf("expected 0 routes (actix shape), got %d: %+v", len(routes), routes)
	}
}

func TestExtractAxumRoutes_EmptySource(t *testing.T) {
	f := &store.Node{Name: "x", QualifiedName: "p.x", FilePath: "src/x.rs"}
	routes := extractAxumRoutes(f, "")
	if len(routes) != 0 {
		t.Errorf("empty source should produce 0 routes, got %d", len(routes))
	}
}

// Phase C (2026-05-08): actix-web BUILDER route extractor.
// Pattern: `.route(PATH, web::METHOD().to(HANDLER))` with optional
// `web::scope("/prefix")` nesting. Pin the major shapes PSM's
// sysmanager uses.

func TestExtractActixBuilderRoutes_FlatRoute(t *testing.T) {
	f := &store.Node{Name: "configure", QualifiedName: "p.configure", FilePath: "src/routes.rs"}
	source := `cfg.service(
		web::scope("/api/v1")
			.route("/users", web::get().to(controllers::user::list))
	);`
	routes := extractActixBuilderRoutes(f, source)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d: %v", len(routes), routes)
	}
	r := routes[0]
	if r.Path != "/api/v1/users" {
		t.Errorf("path = %q, want /api/v1/users", r.Path)
	}
	if r.Method != "GET" {
		t.Errorf("method = %q, want GET", r.Method)
	}
	if r.HandlerRef != "list" {
		t.Errorf("handler = %q, want list (stripped from controllers::user::list)", r.HandlerRef)
	}
}

func TestExtractActixBuilderRoutes_NestedScopes(t *testing.T) {
	f := &store.Node{Name: "configure", QualifiedName: "p.configure", FilePath: "src/routes.rs"}
	source := `cfg.service(
		web::scope("/api/v1")
			.service(
				web::scope("/device")
					.route("", web::post().to(controllers::device::create))
					.route("/timeline", web::get().to(controllers::device::timeline))
			)
			.service(
				web::scope("/auth")
					.route("/login", web::post().to(controllers::auth::login))
			)
	);`
	routes := extractActixBuilderRoutes(f, source)
	if len(routes) != 3 {
		t.Fatalf("expected 3 routes, got %d: %v", len(routes), routes)
	}
	expected := []struct {
		path, method, handler string
	}{
		{"/api/v1/device", "POST", "create"},
		{"/api/v1/device/timeline", "GET", "timeline"},
		{"/api/v1/auth/login", "POST", "login"},
	}
	for i, want := range expected {
		got := routes[i]
		if got.Path != want.path || got.Method != want.method || got.HandlerRef != want.handler {
			t.Errorf("routes[%d] = %v, want path=%s method=%s handler=%s",
				i, got, want.path, want.method, want.handler)
		}
	}
}

func TestExtractActixBuilderRoutes_PathParameter(t *testing.T) {
	f := &store.Node{Name: "configure", QualifiedName: "p.configure", FilePath: "src/routes.rs"}
	source := `web::scope("/api")
		.route("/users/{id}", web::get().to(handlers::get_user))`
	routes := extractActixBuilderRoutes(f, source)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	if routes[0].Path != "/api/users/{id}" {
		t.Errorf("path = %q, want /api/users/{id}", routes[0].Path)
	}
}

func TestExtractActixBuilderRoutes_DoesNotMatchAxum(t *testing.T) {
	// Axum's pattern uses bare `get(handler)` without `web::` prefix and
	// without `.to()`. Must NOT match.
	f := &store.Node{Name: "configure", QualifiedName: "p.configure", FilePath: "src/routes.rs"}
	source := `Router::new().route("/users", get(list_users))`
	routes := extractActixBuilderRoutes(f, source)
	if len(routes) != 0 {
		t.Errorf("axum source should produce 0 actix-builder routes, got %d: %v", len(routes), routes)
	}
}

func TestExtractActixBuilderRoutes_NoWebPrefix(t *testing.T) {
	// Without the `web::` keyword, the file isn't actix — early return.
	f := &store.Node{Name: "x", QualifiedName: "p.x", FilePath: "src/x.rs"}
	source := `fn main() { println!("hello"); }`
	routes := extractActixBuilderRoutes(f, source)
	if len(routes) != 0 {
		t.Errorf("non-actix source should produce 0 routes, got %d", len(routes))
	}
}

func TestExtractActixBuilderRoutes_EmptySource(t *testing.T) {
	f := &store.Node{Name: "x", QualifiedName: "p.x", FilePath: "src/x.rs"}
	routes := extractActixBuilderRoutes(f, "")
	if len(routes) != 0 {
		t.Errorf("empty source should produce 0 routes, got %d", len(routes))
	}
}

// D1 (Phase D, 2026-05-08): commonPrefixLen — the crate-locality
// tie-breaker for handler resolution. Pin the byte-level behavior
// so refactors don't silently change it.
func TestCommonPrefixLen(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"proj.server.list_users", "proj.server.routes.build_router", len("proj.server.")},
		{"proj.decoy.list_users", "proj.server.routes.build_router", len("proj.")},
		{"identical", "identical", len("identical")},
		{"", "anything", 0},
		{"anything", "", 0},
		{"abc", "abd", 2},
	}
	for _, tt := range tests {
		got := commonPrefixLen(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("commonPrefixLen(%q,%q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestIsRustFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"src/main.rs", true},
		{"foo.RS", true}, // case-insensitive
		{"src/main.go", false},
		{"src/main.py", false},
		{"foo.rsx", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isRustFile(tt.path); got != tt.want {
			t.Errorf("isRustFile(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

// --- 2026-05-08: SVG xmlns FP class ---

func TestExtractURLPaths_FiltersSVGXmlns(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "svg_xmlns_alone_filtered",
			text: `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">`,
			want: nil,
		},
		{
			name: "svg_xmlns_alongside_real_url",
			text: `<svg xmlns="http://www.w3.org/2000/svg"></svg>; fetch("/api/items")`,
			want: []string{"/api/items"},
		},
		{
			name: "w3_org_no_www_filtered",
			text: `<svg xmlns="http://w3.org/2000/svg">`,
			want: nil,
		},
		{
			name: "json_schema_org_filtered",
			text: `const schema = {"$schema": "http://json-schema.org/draft-07/schema#"};`,
			want: nil,
		},
		{
			name: "w3_tr_pages_filtered",
			text: `link to https://www.w3.org/TR/html5/`,
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractURLPaths(tt.text)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d paths %v, want %d %v", len(got), got, len(tt.want), tt.want)
			}
			for i, p := range got {
				if p != tt.want[i] {
					t.Errorf("path[%d] = %q, want %q", i, p, tt.want[i])
				}
			}
		})
	}
}

func TestIsExternalDomain_NamespaceURIs(t *testing.T) {
	for _, d := range []string{
		"w3.org", "www.w3.org",
		"xmlns.com",
		"json-schema.org",
		"schemas.microsoft.com",
		"WWW.W3.ORG", // case-insensitive
	} {
		if !isExternalDomain(d) {
			t.Errorf("isExternalDomain(%q) should be true (XML namespace / schema URI)", d)
		}
	}
	// Internal-shaped domains must NOT be filtered.
	for _, d := range []string{"localhost", "myapp.internal", "api.example.com"} {
		if isExternalDomain(d) {
			t.Errorf("isExternalDomain(%q) should be false", d)
		}
	}
}
