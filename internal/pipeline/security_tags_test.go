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
