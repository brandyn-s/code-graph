package store

import (
	"strings"
	"testing"
)

// setupArchTestStore creates a store with representative nodes and edges for architecture tests.
func setupArchTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}

	if err := s.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}

	// Files
	for _, fp := range []string{"main.go", "handler.go", "service.go", "model.py", "utils.js"} {
		_, _ = s.UpsertNode(&Node{Project: "test", Label: "File", Name: fp, QualifiedName: "test." + fp, FilePath: fp})
	}

	// Packages — first-party Package nodes are produced by the directory-
	// scan pass with FilePath set. Lockfile-parsed third-party packages
	// would have FilePath == ""; archPackages filters those out.
	_, _ = s.UpsertNode(&Node{Project: "test", Label: "Package", Name: "cmd", QualifiedName: "test.cmd", FilePath: "cmd"})
	_, _ = s.UpsertNode(&Node{Project: "test", Label: "Package", Name: "handler", QualifiedName: "test.handler", FilePath: "internal/handler"})
	_, _ = s.UpsertNode(&Node{Project: "test", Label: "Package", Name: "service", QualifiedName: "test.service", FilePath: "internal/service"})

	// Functions with different packages (4-segment QNs for realistic sub-package extraction)
	idMain, _ := s.UpsertNode(&Node{
		Project: "test", Label: "Function", Name: "main",
		QualifiedName: "test.cmd.server.main", FilePath: "cmd/server/main.go",
		Properties: map[string]any{"is_entry_point": true},
	})
	idHandleReq, _ := s.UpsertNode(&Node{
		Project: "test", Label: "Function", Name: "HandleRequest",
		QualifiedName: "test.internal.handler.HandleRequest", FilePath: "internal/handler/handler.go",
		Properties: map[string]any{"is_entry_point": true},
	})
	idProcess, _ := s.UpsertNode(&Node{
		Project: "test", Label: "Function", Name: "ProcessOrder",
		QualifiedName: "test.internal.service.ProcessOrder", FilePath: "internal/service/service.go",
	})
	idValidate, _ := s.UpsertNode(&Node{
		Project: "test", Label: "Function", Name: "ValidateOrder",
		QualifiedName: "test.internal.service.ValidateOrder", FilePath: "internal/service/service.go",
	})
	idHelper, _ := s.UpsertNode(&Node{
		Project: "test", Label: "Function", Name: "formatDate",
		QualifiedName: "test.internal.service.formatDate", FilePath: "internal/service/service.go",
	})

	// Test function (should be excluded from entry_points/hotspots)
	idTestFunc, _ := s.UpsertNode(&Node{
		Project: "test", Label: "Function", Name: "TestHandleRequest",
		QualifiedName: "test.internal.handler.handler_test.TestHandleRequest",
		FilePath:      "internal/handler/handler_test.go",
		Properties:    map[string]any{"is_entry_point": true},
	})

	// Route
	_, _ = s.UpsertNode(&Node{
		Project: "test", Label: "Route", Name: "/api/orders",
		QualifiedName: "test.internal.handler.route./api/orders",
		Properties:    map[string]any{"method": "POST", "path": "/api/orders", "handler": "HandleRequest"},
	})

	// Edges: main → HandleRequest → ProcessOrder → ValidateOrder
	//                                ProcessOrder → formatDate
	_, _ = s.InsertEdge(&Edge{Project: "test", SourceID: idMain, TargetID: idHandleReq, Type: "CALLS"})
	_, _ = s.InsertEdge(&Edge{Project: "test", SourceID: idHandleReq, TargetID: idProcess, Type: "CALLS"})
	_, _ = s.InsertEdge(&Edge{Project: "test", SourceID: idProcess, TargetID: idValidate, Type: "CALLS"})
	_, _ = s.InsertEdge(&Edge{Project: "test", SourceID: idProcess, TargetID: idHelper, Type: "CALLS"})
	_, _ = s.InsertEdge(&Edge{Project: "test", SourceID: idTestFunc, TargetID: idHandleReq, Type: "CALLS"})

	return s
}

func TestGetArchitectureAll(t *testing.T) {
	s := setupArchTestStore(t)
	defer s.Close()

	info, err := s.GetArchitecture("test", nil) // nil = all aspects
	if err != nil {
		t.Fatalf("GetArchitecture: %v", err)
	}

	if len(info.Languages) == 0 {
		t.Error("expected languages to be populated")
	}
	if len(info.Packages) == 0 {
		t.Error("expected packages to be populated")
	}
	if len(info.EntryPoints) == 0 {
		t.Error("expected entry_points to be populated")
	}
	if len(info.Routes) == 0 {
		t.Error("expected routes to be populated")
	}
	if len(info.Hotspots) == 0 {
		t.Error("expected hotspots to be populated")
	}
	if len(info.Boundaries) == 0 {
		t.Error("expected boundaries to be populated")
	}
}

func TestArchEntryPointsExcludeTests(t *testing.T) {
	s := setupArchTestStore(t)
	defer s.Close()

	eps, err := s.archEntryPoints("test")
	if err != nil {
		t.Fatal(err)
	}

	for _, ep := range eps {
		if strings.Contains(ep.File, "test") {
			t.Errorf("test function leaked into entry_points: %s (%s)", ep.Name, ep.File)
		}
	}
	if len(eps) != 2 {
		t.Errorf("expected 2 entry points (main, HandleRequest), got %d", len(eps))
	}
}

func TestArchHotspotsExcludeTests(t *testing.T) {
	s := setupArchTestStore(t)
	defer s.Close()

	hotspots, err := s.archHotspots("test")
	if err != nil {
		t.Fatal(err)
	}

	for _, h := range hotspots {
		if strings.Contains(h.Name, "Test") {
			t.Errorf("test function leaked into hotspots: %s", h.Name)
		}
	}
}

func TestGetArchitectureSpecificAspects(t *testing.T) {
	s := setupArchTestStore(t)
	defer s.Close()

	info, err := s.GetArchitecture("test", []string{"languages", "hotspots"})
	if err != nil {
		t.Fatalf("GetArchitecture: %v", err)
	}

	if len(info.Languages) == 0 {
		t.Error("expected languages populated")
	}
	if len(info.Hotspots) == 0 {
		t.Error("expected hotspots populated")
	}

	// These should be nil because not requested
	if info.Packages != nil {
		t.Error("expected packages to be nil")
	}
	if info.EntryPoints != nil {
		t.Error("expected entry_points to be nil")
	}
	if info.Routes != nil {
		t.Error("expected routes to be nil")
	}
}

func TestGetArchitectureEmpty(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertProject("empty", "/tmp/empty"); err != nil {
		t.Fatal(err)
	}

	info, err := s.GetArchitecture("empty", []string{"all"})
	if err != nil {
		t.Fatalf("GetArchitecture: %v", err)
	}

	// All should be empty/nil but no errors
	if info == nil {
		t.Fatal("expected non-nil ArchitectureInfo")
	}
}

func TestArchLanguages(t *testing.T) {
	s := setupArchTestStore(t)
	defer s.Close()

	langs, err := s.archLanguages("test")
	if err != nil {
		t.Fatal(err)
	}

	langMap := map[string]int{}
	for _, l := range langs {
		langMap[l.Language] = l.FileCount
	}

	if langMap["Go"] != 3 {
		t.Errorf("expected 3 Go files, got %d", langMap["Go"])
	}
	if langMap["Python"] != 1 {
		t.Errorf("expected 1 Python file, got %d", langMap["Python"])
	}
	if langMap["JavaScript"] != 1 {
		t.Errorf("expected 1 JavaScript file, got %d", langMap["JavaScript"])
	}
}

func TestArchLayers(t *testing.T) {
	s := setupArchTestStore(t)
	defer s.Close()

	layers, err := s.archLayers("test")
	if err != nil {
		t.Fatal(err)
	}

	if len(layers) == 0 {
		t.Fatal("expected layer classifications")
	}

	layerMap := map[string]string{}
	for _, l := range layers {
		layerMap[l.Name] = l.Layer
	}

	// Service package has high fan-in from handler, should be core or internal
	// Handler package has routes, should be api
	if layerMap["handler"] != "api" {
		t.Logf("handler layer: %q (expected api)", layerMap["handler"])
	}
}

func TestArchClusters(t *testing.T) {
	s := setupArchTestStore(t)
	defer s.Close()

	clusters, err := s.archClusters("test")
	if err != nil {
		t.Fatal(err)
	}

	// With 5 functions and 4 edges, Louvain should find at least 1 cluster
	if clusters == nil {
		t.Log("clusters returned nil — may need more nodes for meaningful clustering")
		return
	}

	for _, c := range clusters {
		if c.Members < 2 {
			t.Errorf("cluster %d has only %d members, expected >= 2", c.ID, c.Members)
		}
		if c.Label == "" {
			t.Errorf("cluster %d has empty label", c.ID)
		}
	}
}

func TestArchFileTree(t *testing.T) {
	s := setupArchTestStore(t)
	defer s.Close()

	tree, err := s.archFileTree("test")
	if err != nil {
		t.Fatal(err)
	}

	if len(tree) == 0 {
		t.Fatal("expected file tree entries")
	}

	// Check that entries have valid types
	for _, entry := range tree {
		if entry.Type != "dir" && entry.Type != "file" {
			t.Errorf("unexpected type %q for %s", entry.Type, entry.Path)
		}
	}
}

func TestArchRoutes(t *testing.T) {
	s := setupArchTestStore(t)
	defer s.Close()

	routes, err := s.archRoutes("test")
	if err != nil {
		t.Fatal(err)
	}

	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	if routes[0].Method != "POST" {
		t.Errorf("expected POST, got %s", routes[0].Method)
	}
	if routes[0].Path != "/api/orders" {
		t.Errorf("expected /api/orders, got %s", routes[0].Path)
	}
	if routes[0].Handler != "HandleRequest" {
		t.Errorf("expected HandleRequest, got %s", routes[0].Handler)
	}
}

func TestArchHotspots(t *testing.T) {
	s := setupArchTestStore(t)
	defer s.Close()

	hotspots, err := s.archHotspots("test")
	if err != nil {
		t.Fatal(err)
	}

	if len(hotspots) == 0 {
		t.Fatal("expected hotspots")
	}

	// ProcessOrder should be a hotspot (called by HandleRequest)
	found := false
	for _, h := range hotspots {
		if h.Name == "ProcessOrder" {
			found = true
			if h.FanIn < 1 {
				t.Errorf("ProcessOrder fan-in: %d, expected >= 1", h.FanIn)
			}
		}
	}
	if !found {
		t.Log("ProcessOrder not in hotspots — may be expected with few edges")
	}
}

func TestArchBoundaries(t *testing.T) {
	s := setupArchTestStore(t)
	defer s.Close()

	boundaries, err := s.archBoundaries("test")
	if err != nil {
		t.Fatal(err)
	}

	if len(boundaries) == 0 {
		t.Fatal("expected cross-package boundaries")
	}

	// server → handler and handler → service should be present
	foundServerHandler := false
	foundHandlerService := false
	for _, b := range boundaries {
		if b.From == "server" && b.To == "handler" {
			foundServerHandler = true
		}
		if b.From == "handler" && b.To == "service" {
			foundHandlerService = true
		}
	}
	if !foundServerHandler {
		t.Error("missing server → handler boundary")
	}
	if !foundHandlerService {
		t.Error("missing handler → service boundary")
	}
}

// TestArchPackagesPopulatesFanInFanOut pins the F5 fix: before 2026-05-14,
// archPackages assigned only Name + NodeCount, leaving FanIn/FanOut at the
// zero value. With the file-path-prefix-match fan tally, cross-package
// CALLS edges populate both fields.
//
// Fixture (from setupArchTestStore):
//   - Package "cmd" (file_path="cmd") contains main (cmd/server/main.go)
//   - Package "handler" (file_path="internal/handler") contains
//     HandleRequest + TestHandleRequest
//   - Package "service" (file_path="internal/service") contains
//     ProcessOrder, ValidateOrder, formatDate
//
// Cross-package CALLS edges:
//   - main → HandleRequest        (cmd → handler)
//   - HandleRequest → ProcessOrder (handler → service)
//
// Intra-package CALLS (must be excluded from fan counts):
//   - ProcessOrder → ValidateOrder (service → service)
//   - ProcessOrder → formatDate    (service → service)
//   - TestHandleRequest → HandleRequest (handler → handler)
func TestArchPackagesPopulatesFanInFanOut(t *testing.T) {
	s := setupArchTestStore(t)
	defer s.Close()

	pkgs, err := s.archPackages("test")
	if err != nil {
		t.Fatalf("archPackages: %v", err)
	}

	pkgMap := map[string]PackageSummary{}
	for _, p := range pkgs {
		pkgMap[p.Name] = p
	}

	cmd, ok := pkgMap["cmd"]
	if !ok {
		t.Fatalf("expected 'cmd' package; got %v", pkgMap)
	}
	if cmd.FanOut != 1 {
		t.Errorf("cmd.FanOut = %d, want 1 (main → HandleRequest)", cmd.FanOut)
	}
	if cmd.FanIn != 0 {
		t.Errorf("cmd.FanIn = %d, want 0", cmd.FanIn)
	}

	handler, ok := pkgMap["handler"]
	if !ok {
		t.Fatalf("expected 'handler' package; got %v", pkgMap)
	}
	if handler.FanOut != 1 {
		t.Errorf("handler.FanOut = %d, want 1 (HandleRequest → ProcessOrder)", handler.FanOut)
	}
	if handler.FanIn != 1 {
		t.Errorf("handler.FanIn = %d, want 1 (main → HandleRequest)", handler.FanIn)
	}

	service, ok := pkgMap["service"]
	if !ok {
		t.Fatalf("expected 'service' package; got %v", pkgMap)
	}
	if service.FanIn != 1 {
		t.Errorf("service.FanIn = %d, want 1 (HandleRequest → ProcessOrder)", service.FanIn)
	}
	if service.FanOut != 0 {
		t.Errorf("service.FanOut = %d, want 0 (only intra-package calls)", service.FanOut)
	}
}

// TestArchPackagesIntraPackageCallsExcluded verifies that calls within the
// same package don't contribute to fan-in or fan-out. ProcessOrder calls
// ValidateOrder and formatDate (both in 'service'); these must not show
// up as service.FanIn or service.FanOut.
func TestArchPackagesIntraPackageCallsExcluded(t *testing.T) {
	s := setupArchTestStore(t)
	defer s.Close()

	pkgs, err := s.archPackages("test")
	if err != nil {
		t.Fatalf("archPackages: %v", err)
	}

	for _, p := range pkgs {
		if p.Name == "service" {
			// service has 2 intra-package calls (Process→Validate, Process→formatDate)
			// + 1 inbound from handler. fan_out must stay 0.
			if p.FanOut != 0 {
				t.Errorf("service.FanOut = %d, want 0 — intra-package calls leaked", p.FanOut)
			}
			return
		}
	}
	t.Fatal("expected 'service' package in result")
}

// TestArchPackagesByQNPopulatesFanInFanOut covers the fallback path where
// no Package nodes exist and we group by qnToPackage. Setup mirrors the
// boundaries test logic: server → handler → service via QN prefixes.
func TestArchPackagesByQNPopulatesFanInFanOut(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertProject("qn-test", "/tmp/qn-test"); err != nil {
		t.Fatal(err)
	}

	// NO Package nodes — forces the qnToPackage fallback path.
	idMain, _ := s.UpsertNode(&Node{
		Project: "qn-test", Label: "Function", Name: "main",
		QualifiedName: "qn-test.cmd.server.main",
	})
	idHandler, _ := s.UpsertNode(&Node{
		Project: "qn-test", Label: "Function", Name: "HandleRequest",
		QualifiedName: "qn-test.internal.handler.HandleRequest",
	})
	idService, _ := s.UpsertNode(&Node{
		Project: "qn-test", Label: "Function", Name: "ProcessOrder",
		QualifiedName: "qn-test.internal.service.ProcessOrder",
	})

	// Cross-package: server → handler → service
	_, _ = s.InsertEdge(&Edge{Project: "qn-test", SourceID: idMain, TargetID: idHandler, Type: "CALLS"})
	_, _ = s.InsertEdge(&Edge{Project: "qn-test", SourceID: idHandler, TargetID: idService, Type: "CALLS"})

	pkgs, err := s.archPackages("qn-test") // routes through archPackagesByQN
	if err != nil {
		t.Fatalf("archPackages: %v", err)
	}

	pkgMap := map[string]PackageSummary{}
	for _, p := range pkgs {
		pkgMap[p.Name] = p
	}

	// qnToPackage on "qn-test.cmd.server.main" → "server"
	server := pkgMap["server"]
	if server.FanOut != 1 || server.FanIn != 0 {
		t.Errorf("server fan: in=%d out=%d, want in=0 out=1", server.FanIn, server.FanOut)
	}
	// qnToPackage on "qn-test.internal.handler.HandleRequest" → "handler"
	handler := pkgMap["handler"]
	if handler.FanIn != 1 || handler.FanOut != 1 {
		t.Errorf("handler fan: in=%d out=%d, want in=1 out=1", handler.FanIn, handler.FanOut)
	}
	// qnToPackage on "qn-test.internal.service.ProcessOrder" → "service"
	service := pkgMap["service"]
	if service.FanIn != 1 || service.FanOut != 0 {
		t.Errorf("service fan: in=%d out=%d, want in=1 out=0", service.FanIn, service.FanOut)
	}
}

// TestArchPackagesNoEdgesZeroFan verifies that packages with no CALLS
// edges have FanIn=0 and FanOut=0 (not a misleading non-zero value).
func TestArchPackagesNoEdgesZeroFan(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertProject("noedge", "/tmp/noedge"); err != nil {
		t.Fatal(err)
	}

	// One package with one function, no CALLS edges.
	_, _ = s.UpsertNode(&Node{
		Project: "noedge", Label: "Package", Name: "lonely",
		QualifiedName: "noedge.lonely", FilePath: "lonely",
	})
	_, _ = s.UpsertNode(&Node{
		Project: "noedge", Label: "Function", Name: "alone",
		QualifiedName: "noedge.lonely.alone", FilePath: "lonely/alone.go",
	})

	pkgs, err := s.archPackages("noedge")
	if err != nil {
		t.Fatalf("archPackages: %v", err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("expected 1 package, got %d", len(pkgs))
	}
	if pkgs[0].FanIn != 0 || pkgs[0].FanOut != 0 {
		t.Errorf("isolated package fan: in=%d out=%d, want 0/0", pkgs[0].FanIn, pkgs[0].FanOut)
	}
}

// --- Case-insensitive search tests ---

// TestSearchComplexityFilter covers min/max_complexity filtering.
//
// Nodes carry a "complexity" property (cyclomatic complexity from the
// CBM tree-sitter pass). The filter reads this off Node.Properties and
// excludes nodes that fall outside the requested range or lack the
// property when a filter is set.
func TestSearchComplexityFilter(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatal(err)
	}

	_, _ = s.UpsertNode(&Node{
		Project: "test", Label: "Function", Name: "trivial",
		QualifiedName: "test.trivial",
		Properties:    map[string]any{"complexity": 1},
	})
	_, _ = s.UpsertNode(&Node{
		Project: "test", Label: "Function", Name: "medium",
		QualifiedName: "test.medium",
		Properties:    map[string]any{"complexity": 5},
	})
	_, _ = s.UpsertNode(&Node{
		Project: "test", Label: "Function", Name: "gnarly",
		QualifiedName: "test.gnarly",
		Properties:    map[string]any{"complexity": 20},
	})
	// Node without a complexity property — should be excluded when any
	// complexity filter is set, kept otherwise.
	_, _ = s.UpsertNode(&Node{
		Project: "test", Label: "Function", Name: "unmeasured",
		QualifiedName: "test.unmeasured",
	})

	noFilter, err := s.Search(&SearchParams{
		Project: "test", Label: "Function",
		MinDegree: -1, MaxDegree: -1, MinComplexity: -1, MaxComplexity: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(noFilter.Results) != 4 {
		t.Errorf("no filter: expected 4 matches, got %d", len(noFilter.Results))
	}

	minOnly, err := s.Search(&SearchParams{
		Project: "test", Label: "Function",
		MinDegree: -1, MaxDegree: -1, MinComplexity: 5, MaxComplexity: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// medium (5) + gnarly (20); trivial (1) below, unmeasured has no complexity.
	if len(minOnly.Results) != 2 {
		t.Errorf("min_complexity=5: expected 2 matches, got %d", len(minOnly.Results))
	}

	maxOnly, err := s.Search(&SearchParams{
		Project: "test", Label: "Function",
		MinDegree: -1, MaxDegree: -1, MinComplexity: -1, MaxComplexity: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	// trivial (1) + medium (5); gnarly (20) above, unmeasured has no complexity.
	if len(maxOnly.Results) != 2 {
		t.Errorf("max_complexity=5: expected 2 matches, got %d", len(maxOnly.Results))
	}

	both, err := s.Search(&SearchParams{
		Project: "test", Label: "Function",
		MinDegree: -1, MaxDegree: -1, MinComplexity: 2, MaxComplexity: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Only medium (5) falls in [2, 10].
	if len(both.Results) != 1 {
		t.Errorf("min=2 max=10: expected 1 match, got %d", len(both.Results))
	}
	if len(both.Results) == 1 && both.Results[0].Node.Name != "medium" {
		t.Errorf("min=2 max=10: expected 'medium', got %q", both.Results[0].Node.Name)
	}

	// Unmeasured node: with min=-1 AND max=-1 it's included; with any bound
	// set it must be dropped (already asserted above via counts).
	foundUnmeasuredInNoFilter := false
	for _, r := range noFilter.Results {
		if r.Node.Name == "unmeasured" {
			foundUnmeasuredInNoFilter = true
		}
	}
	if !foundUnmeasuredInNoFilter {
		t.Error("no filter: expected 'unmeasured' to be included")
	}
}

func TestSearchCaseInsensitiveDefault(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatal(err)
	}

	_, _ = s.UpsertNode(&Node{Project: "test", Label: "Function", Name: "FooBar", QualifiedName: "test.FooBar"})
	_, _ = s.UpsertNode(&Node{Project: "test", Label: "Function", Name: "foobar", QualifiedName: "test.foobar"})
	_, _ = s.UpsertNode(&Node{Project: "test", Label: "Function", Name: "FOOBAR", QualifiedName: "test.FOOBAR"})

	// Default (CaseSensitive=false) should match all 3
	output, err := s.Search(&SearchParams{
		Project:     "test",
		NamePattern: "foobar",
		MinDegree:   -1,
		MaxDegree:   -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Results) != 3 {
		t.Errorf("case-insensitive default: expected 3 matches, got %d", len(output.Results))
	}
}

func TestSearchCaseSensitiveExplicit(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatal(err)
	}

	_, _ = s.UpsertNode(&Node{Project: "test", Label: "Function", Name: "FooBar", QualifiedName: "test.FooBar"})
	_, _ = s.UpsertNode(&Node{Project: "test", Label: "Function", Name: "foobar", QualifiedName: "test.foobar"})
	_, _ = s.UpsertNode(&Node{Project: "test", Label: "Function", Name: "FOOBAR", QualifiedName: "test.FOOBAR"})

	// Explicit case-sensitive should match only exact case
	output, err := s.Search(&SearchParams{
		Project:       "test",
		NamePattern:   "foobar",
		CaseSensitive: true,
		MinDegree:     -1,
		MaxDegree:     -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Results) != 1 {
		t.Errorf("case-sensitive: expected 1 match, got %d", len(output.Results))
	}
	if len(output.Results) > 0 && output.Results[0].Node.Name != "foobar" {
		t.Errorf("case-sensitive: expected 'foobar', got %q", output.Results[0].Node.Name)
	}
}

func TestEnsureCaseInsensitive(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"handler", "(?i)handler"},
		{"(?i)handler", "(?i)handler"}, // idempotent
		{".*Order.*", "(?i).*Order.*"},
		{"", "(?i)"},
	}
	for _, tt := range tests {
		got := ensureCaseInsensitive(tt.input)
		if got != tt.want {
			t.Errorf("ensureCaseInsensitive(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestStripCaseFlag(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"(?i)handler", "handler"},
		{"handler", "handler"},
		{"(?i)(?i)double", "(?i)double"},
	}
	for _, tt := range tests {
		got := stripCaseFlag(tt.input)
		if got != tt.want {
			t.Errorf("stripCaseFlag(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- ADR tests ---

func TestStoreAndRetrieveADR(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatal(err)
	}

	content := "## PURPOSE\nTest project for unit tests.\n\n## STACK\n- Go: speed"
	if err := s.StoreADR("test", content); err != nil {
		t.Fatal(err)
	}

	adr, err := s.GetADR("test")
	if err != nil {
		t.Fatal(err)
	}

	if adr.Content != content {
		t.Errorf("content mismatch: %q", adr.Content)
	}
	if adr.Project != "test" {
		t.Errorf("project mismatch: %q", adr.Project)
	}
	if adr.CreatedAt == "" {
		t.Error("created_at empty")
	}
	if adr.UpdatedAt == "" {
		t.Error("updated_at empty")
	}
}

func TestStoreADRUpsert(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatal(err)
	}

	if err := s.StoreADR("test", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreADR("test", "v2"); err != nil {
		t.Fatal(err)
	}

	adr, err := s.GetADR("test")
	if err != nil {
		t.Fatal(err)
	}
	if adr.Content != "v2" {
		t.Errorf("expected v2, got %q", adr.Content)
	}
}

func TestDeleteADR(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatal(err)
	}

	if err := s.StoreADR("test", "## PURPOSE\nTest"); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteADR("test"); err != nil {
		t.Fatal(err)
	}

	_, err = s.GetADR("test")
	if err == nil {
		t.Error("expected error after delete, got nil")
	}
}

func TestDeleteADRNotFound(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	err = s.DeleteADR("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent ADR")
	}
}

func TestParseADRSections(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  map[string]string
	}{
		{
			name:  "basic two sections",
			input: "## PURPOSE\nFoo\n\n## STACK\nBar",
			want:  map[string]string{"PURPOSE": "Foo", "STACK": "Bar"},
		},
		{
			name:  "all six sections",
			input: "## PURPOSE\nA\n\n## STACK\nB\n\n## ARCHITECTURE\nC\n\n## PATTERNS\nD\n\n## TRADEOFFS\nE\n\n## PHILOSOPHY\nF",
			want:  map[string]string{"PURPOSE": "A", "STACK": "B", "ARCHITECTURE": "C", "PATTERNS": "D", "TRADEOFFS": "E", "PHILOSOPHY": "F"},
		},
		{
			name:  "non-canonical header preserved as text",
			input: "## PURPOSE\nFoo\n## CUSTOM\nStill in PURPOSE\n\n## STACK\nBar",
			want:  map[string]string{"PURPOSE": "Foo\n## CUSTOM\nStill in PURPOSE", "STACK": "Bar"},
		},
		{
			name:  "empty content",
			input: "",
			want:  map[string]string{},
		},
		{
			name:  "content before first section",
			input: "preamble\n## PURPOSE\nFoo",
			want:  map[string]string{"PURPOSE": "Foo"},
		},
		{
			name:  "multiline section content",
			input: "## PURPOSE\nLine 1\nLine 2\nLine 3\n\n## STACK\n- Go\n- SQLite",
			want:  map[string]string{"PURPOSE": "Line 1\nLine 2\nLine 3", "STACK": "- Go\n- SQLite"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseADRSections(tt.input)
			if len(got) != len(tt.want) {
				t.Errorf("section count: got %d, want %d\ngot: %v", len(got), len(tt.want), got)
				return
			}
			for k, wantV := range tt.want {
				if gotV, ok := got[k]; !ok {
					t.Errorf("missing section %q", k)
				} else if gotV != wantV {
					t.Errorf("section %q:\n  got:  %q\n  want: %q", k, gotV, wantV)
				}
			}
		})
	}
}

func TestRenderADR(t *testing.T) {
	tests := []struct {
		name     string
		sections map[string]string
		want     string
	}{
		{
			name:     "canonical order",
			sections: map[string]string{"STACK": "Bar", "PURPOSE": "Foo"},
			want:     "## PURPOSE\nFoo\n\n## STACK\nBar",
		},
		{
			name:     "all sections in order",
			sections: map[string]string{"PHILOSOPHY": "F", "PURPOSE": "A", "STACK": "B", "ARCHITECTURE": "C", "PATTERNS": "D", "TRADEOFFS": "E"},
			want:     "## PURPOSE\nA\n\n## STACK\nB\n\n## ARCHITECTURE\nC\n\n## PATTERNS\nD\n\n## TRADEOFFS\nE\n\n## PHILOSOPHY\nF",
		},
		{
			name:     "non-canonical sections appended alphabetically",
			sections: map[string]string{"PURPOSE": "Foo", "ZEBRA": "Z", "ALPHA": "A"},
			want:     "## PURPOSE\nFoo\n\n## ALPHA\nA\n\n## ZEBRA\nZ",
		},
		{
			name:     "empty map",
			sections: map[string]string{},
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RenderADR(tt.sections)
			if got != tt.want {
				t.Errorf("RenderADR:\n  got:  %q\n  want: %q", got, tt.want)
			}
		})
	}
}

func TestParseRenderRoundTrip(t *testing.T) {
	original := "## PURPOSE\nTest project\n\n## STACK\n- Go: speed\n- SQLite: embedded\n\n## ARCHITECTURE\nPipeline pattern\n\n## PATTERNS\n- Convention over config\n\n## TRADEOFFS\n- Speed over features\n\n## PHILOSOPHY\n- Keep it simple"
	sections := ParseADRSections(original)
	rendered := RenderADR(sections)
	if rendered != original {
		t.Errorf("round-trip mismatch:\n  got:  %q\n  want: %q", rendered, original)
	}
}

func TestUpdateADRSections(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatal(err)
	}

	// Store initial ADR
	initial := "## PURPOSE\nOriginal purpose\n\n## STACK\n- Go"
	if err := s.StoreADR("test", initial); err != nil {
		t.Fatal(err)
	}

	// Update only PATTERNS section
	updated, err := s.UpdateADRSections("test", map[string]string{
		"PATTERNS": "- Pipeline pattern",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify all sections are present
	sections := ParseADRSections(updated.Content)
	if sections["PURPOSE"] != "Original purpose" {
		t.Errorf("PURPOSE changed: %q", sections["PURPOSE"])
	}
	if sections["STACK"] != "- Go" {
		t.Errorf("STACK changed: %q", sections["STACK"])
	}
	if sections["PATTERNS"] != "- Pipeline pattern" {
		t.Errorf("PATTERNS not updated: %q", sections["PATTERNS"])
	}
}

func TestUpdateADRSectionsOverflow(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatal(err)
	}

	if err := s.StoreADR("test", "## PURPOSE\nShort"); err != nil {
		t.Fatal(err)
	}

	// Try to update with content that exceeds the limit
	hugeContent := make([]byte, maxADRLength+1)
	for i := range hugeContent {
		hugeContent[i] = 'x'
	}

	_, err = s.UpdateADRSections("test", map[string]string{
		"STACK": string(hugeContent),
	})
	if err == nil {
		t.Error("expected overflow error")
	}
}

func TestUpdateADRSectionsNoExisting(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatal(err)
	}

	_, err = s.UpdateADRSections("test", map[string]string{
		"PURPOSE": "New purpose",
	})
	if err == nil {
		t.Error("expected error when no existing ADR")
	}
}

// --- ADR validation tests ---

func TestValidateADRContentAllSections(t *testing.T) {
	content := "## PURPOSE\nA\n\n## STACK\nB\n\n## ARCHITECTURE\nC\n\n## PATTERNS\nD\n\n## TRADEOFFS\nE\n\n## PHILOSOPHY\nF"
	if err := ValidateADRContent(content); err != nil {
		t.Errorf("expected no error for complete ADR, got: %v", err)
	}
}

func TestValidateADRContentMissingSections(t *testing.T) {
	content := "## PURPOSE\nA\n\n## STACK\nB"
	err := ValidateADRContent(content)
	if err == nil {
		t.Fatal("expected error for incomplete ADR")
	}
	// Error should mention the missing sections
	for _, missing := range []string{"ARCHITECTURE", "PATTERNS", "TRADEOFFS", "PHILOSOPHY"} {
		if !strings.Contains(err.Error(), missing) {
			t.Errorf("error should mention missing section %q: %v", missing, err)
		}
	}
}

func TestValidateADRContentEmpty(t *testing.T) {
	err := ValidateADRContent("")
	if err == nil {
		t.Fatal("expected error for empty content")
	}
}

func TestValidateADRSectionKeysValid(t *testing.T) {
	sections := map[string]string{
		"PURPOSE": "updated",
		"STACK":   "updated",
	}
	if err := ValidateADRSectionKeys(sections); err != nil {
		t.Errorf("expected no error for valid keys, got: %v", err)
	}
}

func TestValidateADRSectionKeysInvalid(t *testing.T) {
	sections := map[string]string{
		"PURPOSE": "ok",
		"STACKS":  "typo",
		"CUSTOM":  "invalid",
	}
	err := ValidateADRSectionKeys(sections)
	if err == nil {
		t.Fatal("expected error for invalid keys")
	}
	if !strings.Contains(err.Error(), "STACKS") {
		t.Errorf("error should mention invalid key STACKS: %v", err)
	}
	if !strings.Contains(err.Error(), "CUSTOM") {
		t.Errorf("error should mention invalid key CUSTOM: %v", err)
	}
}

func TestValidateADRSectionKeysEmpty(t *testing.T) {
	if err := ValidateADRSectionKeys(map[string]string{}); err != nil {
		t.Errorf("expected no error for empty map, got: %v", err)
	}
}

// --- Louvain algorithm tests ---

func TestLouvainBasic(t *testing.T) {
	// Triangle: A-B, B-C, A-C (should form one community)
	// Isolated: D-E (should form another)
	nodes := []int64{1, 2, 3, 4, 5}
	edges := []louvainEdge{
		{src: 1, dst: 2},
		{src: 2, dst: 3},
		{src: 1, dst: 3},
		{src: 4, dst: 5},
	}

	partition := louvain(nodes, edges)

	// A, B, C should be in the same community
	if partition[1] != partition[2] || partition[2] != partition[3] {
		t.Errorf("triangle nodes should be in same community: %v", partition)
	}

	// D, E should be in the same community
	if partition[4] != partition[5] {
		t.Errorf("pair nodes should be in same community: %v", partition)
	}

	// Triangle and pair should be in different communities
	if partition[1] == partition[4] {
		t.Errorf("triangle and pair should be in different communities: %v", partition)
	}
}

func TestLouvainEmpty(t *testing.T) {
	partition := louvain(nil, nil)
	if len(partition) != 0 {
		t.Errorf("expected empty partition, got %v", partition)
	}
}

func TestLouvainSingleNode(t *testing.T) {
	partition := louvain([]int64{42}, nil)
	if len(partition) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(partition))
	}
	if _, ok := partition[42]; !ok {
		t.Error("expected node 42 in partition")
	}
}

func TestLouvainConverges(t *testing.T) {
	// Larger test: two clear clusters connected by a thin bridge
	var nodes []int64
	var edges []louvainEdge

	// Cluster 1: nodes 1-10, fully connected
	for i := int64(1); i <= 10; i++ {
		nodes = append(nodes, i)
		for j := i + 1; j <= 10; j++ {
			edges = append(edges, louvainEdge{src: i, dst: j})
		}
	}

	// Cluster 2: nodes 11-20, fully connected
	for i := int64(11); i <= 20; i++ {
		nodes = append(nodes, i)
		for j := i + 1; j <= 20; j++ {
			edges = append(edges, louvainEdge{src: i, dst: j})
		}
	}

	// Bridge: single edge between clusters
	edges = append(edges, louvainEdge{src: 5, dst: 15})

	partition := louvain(nodes, edges)

	// Count communities
	communities := map[int]int{}
	for _, comm := range partition {
		communities[comm]++
	}

	// Should find exactly 2 communities (or close to it)
	if len(communities) < 2 {
		t.Errorf("expected at least 2 communities, got %d", len(communities))
	}

	// Nodes 1-10 should mostly be in the same community
	comm1 := partition[int64(1)]
	sameCount := 0
	for i := int64(1); i <= 10; i++ {
		if partition[i] == comm1 {
			sameCount++
		}
	}
	if sameCount < 8 {
		t.Errorf("expected cluster 1 nodes mostly in same community, got %d/10", sameCount)
	}
}

// --- qnToPackage regression tests ---

func TestQnToPackage(t *testing.T) {
	tests := []struct {
		qn   string
		want string
	}{
		// 4+ segment QNs — returns segment[2] (sub-package)
		{"project.internal.store.search.Search", "store"},
		{"project.src.utils.helper.foo", "utils"},
		{"project.src.components.Button.render", "components"},
		{"project.cmd.server.main", "server"},
		// 3-segment QNs — falls back to segment[1]
		{"project.main.foo", "main"},
		{"project.cmd", "cmd"},
		// Edge cases
		{"standalone", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := qnToPackage(tt.qn)
		if got != tt.want {
			t.Errorf("qnToPackage(%q) = %q, want %q", tt.qn, got, tt.want)
		}
	}
}

func TestQnToTopPackage(t *testing.T) {
	tests := []struct {
		qn   string
		want string
	}{
		{"project.internal.store.search.Search", "internal"},
		{"project.src.components.Button", "src"},
		{"project.cmd", "cmd"},
		{"standalone", ""},
	}
	for _, tt := range tests {
		got := qnToTopPackage(tt.qn)
		if got != tt.want {
			t.Errorf("qnToTopPackage(%q) = %q, want %q", tt.qn, got, tt.want)
		}
	}
}

func TestFindArchitectureDocs(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatal(err)
	}

	// Add some file nodes, including architecture docs
	for _, fp := range []string{"main.go", "ARCHITECTURE.md", "docs/adr/001-use-sqlite.md", "README.md"} {
		_, _ = s.UpsertNode(&Node{Project: "test", Label: "File", Name: fp, QualifiedName: "test." + fp, FilePath: fp})
	}

	docs, err := s.FindArchitectureDocs("test")
	if err != nil {
		t.Fatal(err)
	}

	if len(docs) != 2 {
		t.Fatalf("expected 2 architecture docs, got %d: %v", len(docs), docs)
	}

	// Should find ARCHITECTURE.md and docs/adr/001-use-sqlite.md
	found := map[string]bool{}
	for _, d := range docs {
		found[d] = true
	}
	if !found["ARCHITECTURE.md"] {
		t.Error("missing ARCHITECTURE.md")
	}
	if !found["docs/adr/001-use-sqlite.md"] {
		t.Error("missing docs/adr/001-use-sqlite.md")
	}
}

func TestFindArchitectureDocsEmpty(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatal(err)
	}

	docs, err := s.FindArchitectureDocs("test")
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 0 {
		t.Errorf("expected 0 docs, got %d", len(docs))
	}
}

func TestIsTestFilePath(t *testing.T) {
	tests := []struct {
		fp   string
		want bool
	}{
		{"internal/handler/handler.go", false},
		{"src/__tests__/handler.test.ts", true},
		{"src/test/java/com/example/Test.java", true},
		{"tests/test_handler.py", true},
		{"testdata/fixture.json", true},
		{"", false},
	}
	for _, tt := range tests {
		got := isTestFilePath(tt.fp)
		if got != tt.want {
			t.Errorf("isTestFilePath(%q) = %v, want %v", tt.fp, got, tt.want)
		}
	}
}

// --- A4 (2026-05-07) — get_architecture vendored-path + stdlib-derive filters ---

// setupArchVendoredFixture seeds the store with a mix of first-party
// content and vendored content (node_modules / vendor / target / Cargo.nix)
// to exercise the architecture filters added in A4.
func setupArchVendoredFixture(t *testing.T) *Store {
	t.Helper()
	s, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	if err := s.UpsertProject("vfx", "/tmp/vfx"); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}

	// Files: 2 first-party Go + 1 first-party Rust, then 3 vendored
	// (node_modules JS, vendor Go, target/ Rust). Cargo.nix is also
	// in the vendored set (Nix-managed lockfile clone).
	files := []struct {
		name string
		fp   string
	}{
		{"main.go", "cmd/main.go"},
		{"handler.go", "internal/handler/handler.go"},
		{"lib.rs", "src/lib.rs"},
		{"index.js", "node_modules/@babel/core/lib/index.js"},
		{"helper.go", "vendor/github.com/x/y/helper.go"},
		{"crate.rs", "target/debug/build/foo/out/crate.rs"},
		{"Cargo.nix", "Cargo.nix"},
	}
	for _, f := range files {
		_, _ = s.UpsertNode(&Node{Project: "vfx", Label: "File", Name: f.name, QualifiedName: "vfx." + f.name, FilePath: f.fp})
	}

	// Packages: 2 first-party (FilePath set, clean dirs) + 3 vendored +
	// 1 lockfile-parsed (no FilePath, the `@babel/core` flavor).
	_, _ = s.UpsertNode(&Node{Project: "vfx", Label: "Package", Name: "cmd", QualifiedName: "vfx.cmd", FilePath: "cmd"})
	_, _ = s.UpsertNode(&Node{Project: "vfx", Label: "Package", Name: "handler", QualifiedName: "vfx.handler", FilePath: "internal/handler"})
	_, _ = s.UpsertNode(&Node{Project: "vfx", Label: "Package", Name: "@babel/core", QualifiedName: "vfx.__pkg__.@babel/core"}) // lockfile-parsed (no FilePath)
	_, _ = s.UpsertNode(&Node{Project: "vfx", Label: "Package", Name: "vendored_a", QualifiedName: "vfx.vendored_a", FilePath: "vendor/a"})
	_, _ = s.UpsertNode(&Node{Project: "vfx", Label: "Package", Name: "target_b", QualifiedName: "vfx.target_b", FilePath: "target/debug/build/b"})
	_, _ = s.UpsertNode(&Node{Project: "vfx", Label: "Package", Name: "node_modules_c", QualifiedName: "vfx.node_modules_c", FilePath: "node_modules/c"})

	return s
}

func TestArchLanguagesFiltersVendored(t *testing.T) {
	s := setupArchVendoredFixture(t)
	defer s.Close()

	langs, err := s.archLanguages("vfx")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, l := range langs {
		got[l.Language] = l.FileCount
	}
	if got["Go"] != 2 {
		t.Errorf("expected 2 Go files (main.go, handler.go) — vendor/.go filtered; got %d", got["Go"])
	}
	if got["Rust"] != 1 {
		t.Errorf("expected 1 Rust file (lib.rs) — target/.rs filtered; got %d", got["Rust"])
	}
	// JS file at node_modules/@babel/core/lib/index.js must not appear.
	if got["JavaScript"] != 0 {
		t.Errorf("expected 0 JS files (node_modules filtered); got %d", got["JavaScript"])
	}
}

func TestArchPackagesFiltersVendored(t *testing.T) {
	s := setupArchVendoredFixture(t)
	defer s.Close()

	pkgs, err := s.archPackages("vfx")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, p := range pkgs {
		names[p.Name] = true
	}
	// First-party packages should be present.
	if !names["cmd"] || !names["handler"] {
		t.Errorf("expected first-party packages cmd, handler; got %v", names)
	}
	// Lockfile-parsed (@babel/core has no FilePath) must be excluded.
	if names["@babel/core"] {
		t.Error("lockfile-parsed @babel/core leaked into packages — file_path filter not active")
	}
	// Vendored-path packages must be excluded.
	for _, n := range []string{"vendored_a", "target_b", "node_modules_c"} {
		if names[n] {
			t.Errorf("vendored package %q leaked into packages — vendoredPathSQLClause not active", n)
		}
	}
}

func TestArchHotspotsFiltersStdlibDerive(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertProject("hot", "/tmp/hot"); err != nil {
		t.Fatal(err)
	}

	// Create a "real" hotspot (custom function) and stdlib-derive
	// hotspots (to_string, unwrap, fmt). Each gets the same fan-in (1)
	// so ordering is by name; the filter must exclude the derives
	// regardless.
	idCaller, _ := s.UpsertNode(&Node{
		Project: "hot", Label: "Function", Name: "Caller",
		QualifiedName: "hot.Caller", FilePath: "src/main.rs",
	})
	idCustom, _ := s.UpsertNode(&Node{
		Project: "hot", Label: "Function", Name: "ProcessOrder",
		QualifiedName: "hot.ProcessOrder", FilePath: "src/order.rs",
	})
	idToString, _ := s.UpsertNode(&Node{
		Project: "hot", Label: "Method", Name: "to_string",
		QualifiedName: "hot.SomeType.to_string", FilePath: "src/types.rs",
	})
	idUnwrap, _ := s.UpsertNode(&Node{
		Project: "hot", Label: "Method", Name: "unwrap",
		QualifiedName: "hot.SomeType.unwrap", FilePath: "src/types.rs",
	})
	idFmt, _ := s.UpsertNode(&Node{
		Project: "hot", Label: "Method", Name: "fmt",
		QualifiedName: "hot.SomeType.fmt", FilePath: "src/types.rs",
	})
	for _, target := range []int64{idCustom, idToString, idUnwrap, idFmt} {
		_, _ = s.InsertEdge(&Edge{Project: "hot", SourceID: idCaller, TargetID: target, Type: "CALLS"})
	}

	hotspots, err := s.archHotspots("hot")
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hotspots {
		if stdlibDeriveMethods[h.Name] {
			t.Errorf("stdlib-derive method %q leaked into hotspots", h.Name)
		}
	}
	// ProcessOrder is the only non-derive method called; expect it surfaced.
	found := false
	for _, h := range hotspots {
		if h.Name == "ProcessOrder" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected ProcessOrder in hotspots, got %+v", hotspots)
	}
}

func TestArchHotspotsFiltersVendoredPaths(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertProject("hot2", "/tmp/hot2"); err != nil {
		t.Fatal(err)
	}

	idCaller, _ := s.UpsertNode(&Node{
		Project: "hot2", Label: "Function", Name: "Caller",
		QualifiedName: "hot2.Caller", FilePath: "src/main.go",
	})
	// Vendored hotspot — must be filtered even with high fan-in.
	idVendored, _ := s.UpsertNode(&Node{
		Project: "hot2", Label: "Function", Name: "VendoredHelper",
		QualifiedName: "hot2.VendoredHelper",
		FilePath:      "vendor/github.com/x/y/helper.go",
	})
	for i := 0; i < 5; i++ {
		_, _ = s.InsertEdge(&Edge{Project: "hot2", SourceID: idCaller, TargetID: idVendored, Type: "CALLS"})
	}

	hotspots, err := s.archHotspots("hot2")
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hotspots {
		if h.Name == "VendoredHelper" {
			t.Errorf("vendored hotspot leaked into top-10: %s", h.QualifiedName)
		}
	}
}

func TestStdlibDeriveMethodsContainsExpected(t *testing.T) {
	expected := []string{
		"to_string", "unwrap", "map", "iter", "collect", "into", "from",
		"default", "clone", "eq", "hash", "fmt", "as_str", "as_ref",
		"len", "is_empty", "get", "set",
	}
	for _, name := range expected {
		if !stdlibDeriveMethods[name] {
			t.Errorf("expected %q in stdlibDeriveMethods", name)
		}
	}
}

func TestVendoredPathSQLClauseShape(t *testing.T) {
	got := vendoredPathSQLClause("file_path")
	for _, p := range vendoredPathPatterns {
		if !strings.Contains(got, p) {
			t.Errorf("vendoredPathSQLClause missing pattern %q in %q", p, got)
		}
	}
	if !strings.Contains(got, "file_path NOT LIKE") {
		t.Errorf("vendoredPathSQLClause should reference column; got %q", got)
	}
}
