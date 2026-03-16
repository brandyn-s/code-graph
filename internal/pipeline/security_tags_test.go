package pipeline

import (
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

func TestClassifySecurityRole(t *testing.T) {
	tests := []struct {
		name     string
		node     *store.Node
		wantRole string
	}{
		{
			name:     "auth middleware by name",
			node:     &store.Node{Name: "requireAuth", Label: "Function", FilePath: "middleware/auth.go"},
			wantRole: "auth_boundary",
		},
		{
			name:     "auth decorator",
			node:     &store.Node{Name: "getUser", Label: "Function", Properties: map[string]any{"decorators": []any{"@login_required"}}},
			wantRole: "auth_boundary",
		},
		{
			name:     "HTTP handler by decorator",
			node:     &store.Node{Name: "createOrder", Label: "Function", Properties: map[string]any{"decorators": []any{"@app.post"}}},
			wantRole: "input_entry_point",
		},
		{
			name:     "route handler by label",
			node:     &store.Node{Name: "/api/orders", Label: "Route"},
			wantRole: "input_entry_point",
		},
		{
			name:     "main function",
			node:     &store.Node{Name: "main", Label: "Function", FilePath: "cmd/server/main.go"},
			wantRole: "input_entry_point",
		},
		{
			name:     "database write function",
			node:     &store.Node{Name: "executeQuery", Label: "Function", FilePath: "db/queries.go"},
			wantRole: "sensitive_sink",
		},
		{
			name:     "crypto function",
			node:     &store.Node{Name: "encryptPayload", Label: "Function", FilePath: "crypto/aes.rs"},
			wantRole: "crypto_operation",
		},
		{
			name:     "rsa standalone matches crypto",
			node:     &store.Node{Name: "rsa_encrypt", Label: "Function", FilePath: "security/keys.py"},
			wantRole: "crypto_operation",
		},
		{
			name:     "rsa substring should NOT match crypto",
			node:     &store.Node{Name: "conversations_search_messages", Label: "Function", FilePath: "slack/slack_mcp.py"},
			wantRole: "",
		},
		{
			name:     "aes substring should NOT match crypto",
			node:     &store.Node{Name: "diseases_lookup", Label: "Function", FilePath: "health/api.py"},
			wantRole: "",
		},
		{
			name:     "hmac standalone matches crypto",
			node:     &store.Node{Name: "hmac_sign", Label: "Function", FilePath: "auth/tokens.py"},
			wantRole: "crypto_operation",
		},
		{
			name:     "ordinary function",
			node:     &store.Node{Name: "formatDate", Label: "Function", FilePath: "util/time.go"},
			wantRole: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.node.Properties == nil {
				tt.node.Properties = map[string]any{}
			}
			got := classifySecurityRole(tt.node)
			if got != tt.wantRole {
				t.Errorf("classifySecurityRole(%s) = %q, want %q", tt.node.Name, got, tt.wantRole)
			}
		})
	}
}

func TestDecoratorEntryPoints(t *testing.T) {
	tests := []struct {
		name     string
		node     *store.Node
		wantRole string
	}{
		{
			name: "click command",
			node: &store.Node{Name: "deploy", Label: "Function", Properties: map[string]any{
				"decorators": []any{"@cli.command()"},
			}},
			wantRole: "input_entry_point",
		},
		{
			name: "typer command",
			node: &store.Node{Name: "run_scan", Label: "Function", Properties: map[string]any{
				"decorators": []any{"@app.command()"},
			}},
			wantRole: "input_entry_point",
		},
		{
			name: "celery task",
			node: &store.Node{Name: "process_batch", Label: "Function", Properties: map[string]any{
				"decorators": []any{"@celery.task"},
			}},
			wantRole: "input_entry_point",
		},
		{
			name: "pytest fixture",
			node: &store.Node{Name: "db_session", Label: "Function", Properties: map[string]any{
				"decorators": []any{"@pytest.fixture"},
			}},
			wantRole: "input_entry_point",
		},
		{
			name: "django signal",
			node: &store.Node{Name: "on_user_created", Label: "Function", Properties: map[string]any{
				"decorators": []any{"@receiver(post_save)"},
			}},
			wantRole: "input_entry_point",
		},
		{
			name: "celery shared_task",
			node: &store.Node{Name: "send_email", Label: "Function", Properties: map[string]any{
				"decorators": []any{"@shared_task"},
			}},
			wantRole: "input_entry_point",
		},
		{
			name: "starlette on_event",
			node: &store.Node{Name: "startup", Label: "Function", Properties: map[string]any{
				"decorators": []any{`@app.on_event("startup")`},
			}},
			wantRole: "input_entry_point",
		},
		// Negative test: @task_runner.something should NOT match @task
		{
			name: "task_runner should not match task",
			node: &store.Node{Name: "cleanup", Label: "Function", Properties: map[string]any{
				"decorators": []any{"@task_runner.schedule"},
			}},
			wantRole: "",
		},
		// Negative test: @task as substring in a longer word should not match
		{
			name: "tasker should not match task",
			node: &store.Node{Name: "do_work", Label: "Function", Properties: map[string]any{
				"decorators": []any{"@tasker"},
			}},
			wantRole: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifySecurityRole(tt.node)
			if got != tt.wantRole {
				t.Errorf("classifySecurityRole() = %q, want %q", got, tt.wantRole)
			}
		})
	}
}

func TestNewSecurityRoles(t *testing.T) {
	tests := []struct {
		name     string
		node     *store.Node
		wantRole string
	}{
		{"setuid wrapper", &store.Node{Name: "setuid_wrapper", Label: "Function"}, "privilege_escalation"},
		{"assume role", &store.Node{Name: "assume_role", Label: "Function"}, "privilege_escalation"},
		{"create session", &store.Node{Name: "create_session", Label: "Function"}, "session_management"},
		{"revoke token", &store.Node{Name: "revoke_token", Label: "Function"}, "session_management"},
		{"session file", &store.Node{Name: "handler", Label: "Function", FilePath: "auth/sessions/manager.py"}, "session_management"},
		{"audit log", &store.Node{Name: "write_audit_log", Label: "Function"}, "audit_logging"},
		{"compliance log", &store.Node{Name: "compliance_log", Label: "Function"}, "audit_logging"},
		{"audit file", &store.Node{Name: "record_event", Label: "Function", FilePath: "audit/events.py"}, "audit_logging"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifySecurityRole(tt.node)
			if got != tt.wantRole {
				t.Errorf("classifySecurityRole() = %q, want %q", got, tt.wantRole)
			}
		})
	}
}
