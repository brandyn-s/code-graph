package main

import (
	"strings"
	"testing"
)

func TestEmbeddingMode(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		wantEnabled bool
		wantSubstr  string
	}{
		{"no key, no override → disabled", map[string]string{}, false, "embeddings disabled (set VOYAGE_API_KEY"},
		{"key present → voyage", map[string]string{"VOYAGE_API_KEY": "k"}, true, "embeddings: voyage"},
		{"explicit skip wins over key", map[string]string{"VOYAGE_API_KEY": "k", "CODE_GRAPH_SKIP_EMBEDDINGS": "1"}, false, "CODE_GRAPH_SKIP_EMBEDDINGS set"},
		{"explicit skip=true", map[string]string{"CODE_GRAPH_SKIP_EMBEDDINGS": "true"}, false, "CODE_GRAPH_SKIP_EMBEDDINGS set"},
		{"explicit enable without key", map[string]string{"CODE_GRAPH_SKIP_EMBEDDINGS": "0"}, true, "VOYAGE_API_KEY is unset"},
		{"explicit enable with key", map[string]string{"CODE_GRAPH_SKIP_EMBEDDINGS": "false", "VOYAGE_API_KEY": "k"}, true, "embeddings: voyage"},
		{"blank key counts as unset", map[string]string{"VOYAGE_API_KEY": "  "}, false, "embeddings disabled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(k string) string { return tt.env[k] }
			enabled, status := embeddingMode(getenv)
			if enabled != tt.wantEnabled {
				t.Fatalf("enabled = %v, want %v (status %q)", enabled, tt.wantEnabled, status)
			}
			if !strings.Contains(status, tt.wantSubstr) {
				t.Fatalf("status %q does not contain %q", status, tt.wantSubstr)
			}
			if !strings.HasPrefix(status, "code-graph: ") {
				t.Fatalf("status %q must be prefixed with the binary name", status)
			}
		})
	}
}
