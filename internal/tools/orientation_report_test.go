package tools

import (
	"strings"
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// TestRenderOrientationReport_RealisticFixture asserts that the markdown
// renderer produces every section we care about from a representative
// ArchitectureInfo fixture. The goal is not to pin exact markdown — only to
// catch silent section-drop regressions.
func TestRenderOrientationReport_RealisticFixture(t *testing.T) {
	arch := &store.ArchitectureInfo{
		Languages: []store.LanguageCount{
			{Language: "Python", FileCount: 80},
			{Language: "YAML", FileCount: 47},
		},
		Hotspots: []store.HotspotFunction{
			{Name: "_j", QualifiedName: "pkg.foo._j", FanIn: 105},
			{Name: "_post", QualifiedName: "pkg.foo._post", FanIn: 95},
		},
		Clusters: []store.ClusterInfo{
			{ID: 1, Label: "tailscale_mcp", Members: 76, Cohesion: 0.84, TopNodes: []string{"_j", "_safe_path", "_post"}},
			{ID: 2, Label: "tenable_mcp", Members: 63, Cohesion: 0.89, TopNodes: []string{"_j"}},
		},
		Boundaries: []store.CrossPkgBoundary{
			{From: "tests", To: "backend_pool", CallCount: 25},
		},
		Routes: []store.RouteInfo{
			{Method: "GET", Path: "/health", Handler: "app.handle_health"},
		},
		EntryPoints: []store.EntryPointInfo{
			{Name: "main", QualifiedName: "cmd.main", File: "cmd/main.go"},
		},
	}

	md := renderOrientationReport("myrepo", 6210, 12462, arch)

	required := []string{
		"# myrepo",
		"6210 nodes",
		"12462 edges",
		"Python (80)",
		"## Start Here",
		"## God Nodes",
		"`_j`",
		"105",
		"## Communities",
		"tailscale_mcp",
		"0.84",
		"## Cross-Package Boundaries",
		"tests",
		"backend_pool",
		"## Entry Points",
		"### HTTP Routes",
		"/health",
		"### Process Entry Points",
		"cmd/main.go",
		"## Suggested Questions",
		"trace_call_path",
	}
	for _, needle := range required {
		if !strings.Contains(md, needle) {
			t.Errorf("rendered report missing expected section/content %q", needle)
		}
	}
}

// TestRenderOrientationReport_EmptyGraph covers the no-data case — a freshly
// indexed empty repo should still produce a well-formed (if sparse) report
// rather than crashing or emitting malformed markdown.
func TestRenderOrientationReport_EmptyGraph(t *testing.T) {
	md := renderOrientationReport("empty-repo", 0, 0, &store.ArchitectureInfo{})

	if !strings.Contains(md, "# empty-repo") {
		t.Error("missing header")
	}
	if !strings.Contains(md, "## Start Here") {
		t.Error("Start Here must render even for empty graphs")
	}
	if !strings.Contains(md, "## Suggested Questions") {
		t.Error("Suggested Questions fallback must render even without hotspots")
	}
	if strings.Contains(md, "## God Nodes") {
		t.Error("God Nodes section should be suppressed when no hotspots")
	}
}

// TestRenderOrientationReport_NilArch covers the defensive path where
// GetArchitecture returns nil (shouldn't happen today but we guard against
// a future change silently crashing the indexer's report step).
func TestRenderOrientationReport_NilArch(t *testing.T) {
	md := renderOrientationReport("nil-arch", 42, 100, nil)

	if !strings.Contains(md, "# nil-arch") {
		t.Error("header should render with nil arch")
	}
	if !strings.Contains(md, "42 nodes") {
		t.Error("node count should render with nil arch")
	}
}

// TestRenderOrientationReport_PipeInLabelEscaped checks that table cells
// carrying a `|` in an identifier don't break the markdown table syntax.
// We locate the God Nodes table row and assert the pipe is escaped inside
// the row (so pandoc/GitHub render it as a single cell, not split).
func TestRenderOrientationReport_PipeInLabelEscaped(t *testing.T) {
	arch := &store.ArchitectureInfo{
		Hotspots: []store.HotspotFunction{
			{Name: "weird|name", QualifiedName: "pkg.weird|name", FanIn: 3},
		},
	}
	md := renderOrientationReport("r", 1, 1, arch)

	// Find the data row under God Nodes. Table rows start with `| \``.
	// The literal escaped form `weird\|name` must appear in that row.
	var dataRow string
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(line, "| `") && strings.Contains(line, "weird") {
			dataRow = line
			break
		}
	}
	if dataRow == "" {
		t.Fatalf("could not find God Nodes data row in report:\n%s", md)
	}
	if !strings.Contains(dataRow, `weird\|name`) {
		t.Errorf("expected `weird\\|name` (escaped pipe) in table row, got: %q", dataRow)
	}
}
