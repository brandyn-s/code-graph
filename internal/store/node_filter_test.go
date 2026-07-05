package store

import "testing"

// TestIsSurfaceableCodeNode pins the shared exclusion predicate used by
// code_localize, rank_by_query, and query_security_surfaces to keep
// Community pseudo-nodes and external stubs (empty file_path) out of
// result lists — the pollution observed live 2026-07-04.
func TestIsSurfaceableCodeNode(t *testing.T) {
	cases := []struct {
		name     string
		label    string
		filePath string
		want     bool
	}{
		{"first-party function", "Function", "internal/store/search.go", true},
		{"first-party module", "Module", "internal/store", true},
		{"community pseudo-node", "Community", "", false},
		{"community with stray path", "Community", "x", false},
		{"external stub (empty path)", "Function", "", false},
		{"external method stub", "Method", "", false},
	}
	for _, c := range cases {
		if got := IsSurfaceableCodeNode(c.label, c.filePath); got != c.want {
			t.Errorf("%s: IsSurfaceableCodeNode(%q,%q)=%v, want %v", c.name, c.label, c.filePath, got, c.want)
		}
	}
}
