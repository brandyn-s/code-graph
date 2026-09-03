package tools

import (
	"testing"

	"github.com/brandyn-s/code-graph/internal/store"
)

// testIDs holds node IDs keyed by name for a test graph.
type testIDs map[string]int64

// setupSecurityGraph creates an in-memory graph with security-tagged nodes
// for testing taint analysis, security surfaces, and data flow tools.
//
// Graph structure:
//
//	handle_request (input_entry_point, http_handler) — src/handler.rs
//	  └─ CALLS → authenticate (auth_boundary) — src/auth.rs
//	       └─ CALLS → validate_jwt (sanitizer) — src/auth.rs
//	       └─ CALLS → get_user (sensitive_sink, database) — src/db.rs
//	  └─ CALLS → write_log (audit_logging) — src/logging.rs
//	  └─ CALLS → execute_sql (sensitive_sink, database) — src/db.rs
//
//	main (input_entry_point) — src/main.rs
//	  └─ CALLS → setup (infra callee, filtered by isInfraCallee)
//	  └─ CALLS → handle_request
//
//	parse_config (crypto_operation, encryption) — src/config.rs
//	test_auth (test function) — tests/auth_test.rs
//	  └─ TESTS → authenticate
//
//	config.rs AppConfig ─ FILE_CHANGES_WITH → auth.rs authenticate (score 0.7)
//
//	EnvVar: DATABASE_URL ← READS_ENV ─ get_user
func setupSecurityGraph(t *testing.T) (*store.Store, testIDs) {
	t.Helper()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}

	if err := st.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}

	nodes := []*store.Node{
		{
			Project: "test", Label: "Function", Name: "handle_request",
			QualifiedName: "test.handler.handle_request", FilePath: "svc-api/src/handler.rs",
			StartLine: 1, EndLine: 30,
			Properties: map[string]any{"security_role": "input_entry_point", "security_subtype": "http_handler"},
		},
		{
			Project: "test", Label: "Function", Name: "authenticate",
			QualifiedName: "test.auth.authenticate", FilePath: "svc-api/src/auth.rs",
			StartLine: 10, EndLine: 40,
			Properties: map[string]any{"security_role": "auth_boundary"},
		},
		{
			Project: "test", Label: "Function", Name: "validate_jwt",
			QualifiedName: "test.auth.validate_jwt", FilePath: "svc-api/src/auth.rs",
			StartLine: 45, EndLine: 70,
			Properties: map[string]any{"security_role": "sanitizer"},
		},
		{
			Project: "test", Label: "Function", Name: "get_user",
			QualifiedName: "test.db.get_user", FilePath: "svc-api/src/db.rs",
			StartLine: 1, EndLine: 25,
			Properties: map[string]any{"security_role": "sensitive_sink", "security_subtype": "database"},
		},
		{
			Project: "test", Label: "Function", Name: "execute_sql",
			QualifiedName: "test.db.execute_sql", FilePath: "svc-api/src/db.rs",
			StartLine: 30, EndLine: 50,
			Properties: map[string]any{"security_role": "sensitive_sink", "security_subtype": "database"},
		},
		{
			Project: "test", Label: "Function", Name: "write_log",
			QualifiedName: "test.logging.write_log", FilePath: "svc-api/src/logging.rs",
			StartLine: 1, EndLine: 15,
			Properties: map[string]any{"security_role": "audit_logging"},
		},
		{
			Project: "test", Label: "Function", Name: "main",
			QualifiedName: "test.main.main", FilePath: "svc-api/src/main.rs",
			StartLine: 1, EndLine: 20,
			Properties: map[string]any{"security_role": "input_entry_point"},
		},
		{
			Project: "test", Label: "Function", Name: "setup",
			QualifiedName: "test.main.setup", FilePath: "svc-api/src/main.rs",
			StartLine: 25, EndLine: 35,
		},
		{
			Project: "test", Label: "Function", Name: "parse_config",
			QualifiedName: "test.config.parse_config", FilePath: "svc-api/src/config.rs",
			StartLine: 1, EndLine: 30,
			Properties: map[string]any{"security_role": "crypto_operation", "security_subtype": "encryption"},
		},
		{
			Project: "test", Label: "Function", Name: "test_auth",
			QualifiedName: "test.auth_test.test_auth", FilePath: "svc-api/tests/auth_test.rs",
			StartLine: 1, EndLine: 20,
		},
		{
			Project: "test", Label: "Struct", Name: "AppConfig",
			QualifiedName: "test.config.AppConfig", FilePath: "svc-api/src/config.rs",
			StartLine: 35, EndLine: 50,
		},
		{
			Project: "test", Label: "EnvVar", Name: "DATABASE_URL",
			QualifiedName: "test.env.DATABASE_URL", FilePath: "",
			StartLine: 0, EndLine: 0,
		},
		// Second service for service-level tests
		{
			Project: "test", Label: "Function", Name: "process_order",
			QualifiedName: "test.orders.process_order", FilePath: "svc-orders/src/handler.rs",
			StartLine: 1, EndLine: 40,
			Properties: map[string]any{"security_role": "input_entry_point", "security_subtype": "http_handler"},
		},
		{
			Project: "test", Label: "Function", Name: "send_notification",
			QualifiedName: "test.notify.send_notification", FilePath: "svc-notify/src/sender.rs",
			StartLine: 1, EndLine: 30,
		},
	}

	ids := make(testIDs)
	for _, n := range nodes {
		id, upsertErr := st.UpsertNode(n)
		if upsertErr != nil {
			t.Fatalf("UpsertNode %s: %v", n.Name, upsertErr)
		}
		ids[n.Name] = id
	}

	edges := []struct {
		src, tgt, typ string
		props         map[string]any
	}{
		// handle_request calls
		{"handle_request", "authenticate", "CALLS", nil},
		{"handle_request", "write_log", "CALLS", nil},
		{"handle_request", "execute_sql", "CALLS", nil},
		// authenticate calls
		{"authenticate", "validate_jwt", "CALLS", nil},
		{"authenticate", "get_user", "CALLS", nil},
		// main calls
		{"main", "setup", "CALLS", nil},
		{"main", "handle_request", "CALLS", nil},
		// test coverage
		{"test_auth", "authenticate", "TESTS", nil},
		// env var
		{"get_user", "DATABASE_URL", "READS_ENV", nil},
		// change coupling
		{"AppConfig", "authenticate", "FILE_CHANGES_WITH", map[string]any{"coupling_score": 0.7}},
		// cross-service call
		{"process_order", "send_notification", "HTTP_CALLS", nil},
		// cross-crate coupling (svc-api <-> svc-orders)
		{"handle_request", "process_order", "FILE_CHANGES_WITH", map[string]any{"coupling_score": 0.5}},
	}

	for _, e := range edges {
		srcID, ok := ids[e.src]
		if !ok {
			t.Fatalf("unknown source node: %s", e.src)
		}
		tgtID, ok := ids[e.tgt]
		if !ok {
			t.Fatalf("unknown target node: %s", e.tgt)
		}
		_, insertErr := st.InsertEdge(&store.Edge{
			Project:    "test",
			SourceID:   srcID,
			TargetID:   tgtID,
			Type:       e.typ,
			Properties: e.props,
		})
		if insertErr != nil {
			t.Fatalf("InsertEdge %s->%s (%s): %v", e.src, e.tgt, e.typ, insertErr)
		}
	}

	return st, ids
}
