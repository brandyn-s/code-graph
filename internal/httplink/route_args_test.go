package httplink

import (
	"testing"

	"github.com/brandyn-s/code-graph/internal/store"
)

// Inputs ported from upstream codebase-memory-mcp 592894a4 (handler is the
// LAST argument; middleware sits between path and handler) and c36b4fbc (a
// Spring/JAX-RS path attribute may appear anywhere in the annotation).

func TestExpressHandlerIsTheLastArgumentNotTheFirstMiddleware(t *testing.T) {
	node := &store.Node{Name: "setup", QualifiedName: "proj.routes.setup"}
	cases := []struct {
		name, line, wantHandler string
	}{
		{"two middlewares", `router.get("/users", requireAuth, rateLimit, listUsers);`, "listUsers"},
		{"single handler", `app.post('/orders', createOrder)`, "createOrder"},
		{"member handler after middleware", `router.put("/items/:id", requireAuth, items.update);`, "items.update"},
		{"inline arrow middlewares", `router.delete("/x", (req, res, next) => next(), (req, res, next) => next(), (req, res, next) => next(), removeX);`, "removeX"},
		{"inline handler", `app.get('/health', (req, res) => res.send('ok'))`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			routes := extractExpressRoutes(node, tc.line)
			if len(routes) != 1 {
				t.Fatalf("routes = %d, want 1: %+v", len(routes), routes)
			}
			if routes[0].HandlerRef != tc.wantHandler {
				t.Fatalf("handler = %q, want %q", routes[0].HandlerRef, tc.wantHandler)
			}
		})
	}
}

func TestGinHandlerIsTheLastArgumentAfterMiddleware(t *testing.T) {
	node := &store.Node{Name: "setup", QualifiedName: "proj.routes.setup"}
	cases := []struct {
		name, line, wantHandler string
	}{
		{"middleware then handler", `r.GET("/users", authRequired(), rateLimit(), h.ListUsers)`, "h.ListUsers"},
		{"single handler", `r.POST("/orders", h.CreateOrder)`, "h.CreateOrder"},
		{"group with middleware", `api.DELETE("/items/:id", requireAdmin, deleteItem)`, "deleteItem"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			routes := extractGoRoutes(node, tc.line)
			if len(routes) != 1 {
				t.Fatalf("routes = %d, want 1: %+v", len(routes), routes)
			}
			if routes[0].HandlerRef != tc.wantHandler {
				t.Fatalf("handler = %q, want %q", routes[0].HandlerRef, tc.wantHandler)
			}
		})
	}
}

func TestSpringPathAttributeAnywhereInTheAnnotation(t *testing.T) {
	cases := []struct {
		name, decorator, wantPath, wantMethod string
	}{
		{"path first", `@RequestMapping(path = "/orders", method = RequestMethod.GET)`, "/orders", ""},
		{"path last", `@RequestMapping(method = RequestMethod.GET, produces = MediaType.APPLICATION_JSON_VALUE, consumes = MediaType.APPLICATION_JSON_VALUE, path = "/orders")`, "/orders", ""},
		{"value last", `@GetMapping(produces = "application/json", value = "/users/{id}")`, "/users/{id}", "GET"},
		{"positional", `@PostMapping("/users")`, "/users", "POST"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := &store.Node{Name: "handler", QualifiedName: "proj.Ctl.handler", Properties: map[string]any{"decorators": []any{tc.decorator}}}
			routes := extractJavaRoutes(node)
			if len(routes) != 1 {
				t.Fatalf("routes = %d, want 1: %+v", len(routes), routes)
			}
			if routes[0].Path != tc.wantPath || routes[0].Method != tc.wantMethod {
				t.Fatalf("got %s %q, want %s %q", routes[0].Method, routes[0].Path, tc.wantMethod, tc.wantPath)
			}
		})
	}
}
