package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brandyn-s/code-graph/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPClientEndToEnd drives the server the way a real MCP client does:
// initialize, tools/list, an indexing round trip, a relationship query, an
// evidence query, and a stale-source refusal after the checkout changes. It
// uses the go-sdk client over an in-memory transport, so the only thing it
// skips compared to a stdio client is the pipe itself.
func TestMCPClientEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end MCP client test skipped in -short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required to build the checkout-backed fixture")
	}

	repo := gitBackedFixtureCopy(t, filepath.Join("..", "..", "bench", "accuracy", "synthetic", "go-minimal"))

	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)
	cfg, err := store.OpenConfigInDir(t.TempDir())
	if err != nil {
		t.Fatalf("OpenConfigInDir: %v", err)
	}
	t.Cleanup(func() { _ = cfg.Close() })

	srv := NewServer(router, WithConfig(cfg))
	// The initialized notification would otherwise probe session roots and
	// start background indexing against the test process's working directory.
	srv.sessionOnce.Do(func() {})
	srv.updateOnce.Do(func() {})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.MCPServer().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "code-graph-e2e", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect (initialize): %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	t.Run("tools_list", func(t *testing.T) { checkToolsList(ctx, t, session, srv) })

	project := indexFixture(ctx, t, session, repo)
	entryQN, leafQN := lookupEntryAndLeaf(ctx, t, session, project)
	t.Run("relationship_evidence", func(t *testing.T) { checkRelationshipEvidence(ctx, t, session, project, entryQN, leafQN) })
	t.Run("stale_source_refusal", func(t *testing.T) { checkStaleRefusal(ctx, t, session, repo, project, entryQN) })
}

// checkToolsList asserts tools/list agrees with the registered definitions the
// schema snapshot is generated from and that the core toolset names only
// registered tools.
func checkToolsList(ctx context.Context, t *testing.T, session *mcp.ClientSession, srv *Server) {
	t.Helper()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	listedNames := map[string]bool{}
	for _, tool := range listed.Tools {
		listedNames[tool.Name] = true
	}
	advertised := srv.AdvertisedToolNames()
	for _, name := range advertised {
		if !listedNames[name] {
			t.Errorf("tools/list is missing advertised tool %q", name)
		}
	}
	if len(listedNames) != len(advertised) {
		t.Fatalf("tools/list returned %d tools, server advertises %d under toolset %q", len(listedNames), len(advertised), srv.Toolset())
	}
	registry := map[string]bool{}
	for _, name := range registeredToolNames(t) {
		registry[name] = true
	}
	for _, name := range CoreToolNames() {
		if !registry[name] {
			t.Errorf("core toolset names unregistered tool %q", name)
		}
	}

}

// indexFixture runs index_repository against the fixture and returns the
// project name.
func indexFixture(ctx context.Context, t *testing.T, session *mcp.ClientSession, repo string) string {
	t.Helper()
	indexed := callToolJSON(ctx, t, session, "index_repository", map[string]any{
		"repo_path":   repo,
		"skip_report": true,
	})
	project, _ := indexed["project"].(string)
	if project == "" {
		t.Fatalf("index_repository returned no project: %v", indexed)
	}
	if status, _ := indexed["status"].(string); status == "degraded" {
		t.Fatalf("index_repository degraded: %v", indexed)
	}

	return project
}

// lookupEntryAndLeaf resolves the qualified names of the fixture's entry and
// leaf functions through search_graph.
func lookupEntryAndLeaf(ctx context.Context, t *testing.T, session *mcp.ClientSession, project string) (entryQN, leafQN string) {
	t.Helper()
	callers := callToolJSON(ctx, t, session, "search_graph", map[string]any{
		"project":        project,
		"label":          "Function",
		"name_pattern":   "entry",
		"include_source": true,
	})
	entry := firstSearchResult(t, callers)
	entryQN = requireStringValue(t, entry["qualified_name"], "entry qualified_name")
	leafResult := firstSearchResult(t, callToolJSON(ctx, t, session, "search_graph", map[string]any{
		"project":      project,
		"label":        "Function",
		"name_pattern": "leaf",
	}))
	leafQN = requireStringValue(t, leafResult["qualified_name"], "leaf qualified_name")

	return entryQN, leafQN
}

// checkRelationshipEvidence asserts the entry -> leaf CALLS edge carries
// resolver, tier, band, and a generation-bound relationship_ref.
func checkRelationshipEvidence(ctx context.Context, t *testing.T, session *mcp.ClientSession, project, entryQN, leafQN string) {
	t.Helper()
	evidence := callToolJSON(ctx, t, session, "get_relationship_evidence", map[string]any{
		"project":                project,
		"qualified_name":         entryQN,
		"direction":              "outbound",
		"relationship_types":     []any{"CALLS"},
		"related_qualified_name": leafQN,
	})
	relationships, ok := evidence["relationships"].([]any)
	if !ok || len(relationships) == 0 {
		t.Fatalf("no relationships in evidence response: %v", evidence)
	}
	rel := requireMapValue(t, relationships[0], "relationship")
	for _, field := range []string{"resolution_source", "confidence_tier", "confidence_band"} {
		if text, _ := rel[field].(string); text == "" {
			t.Errorf("relationship evidence missing %s", field)
		}
	}
	ref, ok := rel["relationship_ref"].(map[string]any)
	if !ok {
		t.Fatalf("relationship evidence missing relationship_ref")
	}
	for _, field := range []string{"id", "source_revision", "index_generation", "repository_id"} {
		if text, _ := ref[field].(string); text == "" {
			t.Errorf("relationship_ref missing %s", field)
		}
	}
	if id, _ := ref["id"].(string); !strings.HasPrefix(id, "rel:v1:") {
		t.Errorf("relationship_ref.id = %q, want rel:v1: prefix", id)
	}
	if relType, _ := rel["relation_type"].(string); !strings.EqualFold(relType, "calls") {
		t.Errorf("relation_type = %q, want calls", relType)
	}

}

// checkStaleRefusal edits the checkout without reindexing and asserts evidence
// fails closed.
func checkStaleRefusal(ctx context.Context, t *testing.T, session *mcp.ClientSession, repo, project, entryQN string) {
	t.Helper()
	mainGo := filepath.Join(repo, "main.go")
	original, err := os.ReadFile(mainGo)
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if err := os.WriteFile(mainGo, append(original, []byte("\n// edited after indexing\n")...), 0o600); err != nil {
		t.Fatalf("mutate main.go: %v", err)
	}
	stale := callToolJSON(ctx, t, session, "get_relationship_evidence", map[string]any{
		"project":        project,
		"qualified_name": entryQN,
		"direction":      "outbound",
	})
	if status, _ := stale["identity_status"].(string); status != "stale_source" {
		t.Fatalf("identity_status after edit = %q, want stale_source: %v", status, stale)
	}
	if rels, _ := stale["relationships"].([]any); len(rels) != 0 {
		t.Fatalf("stale response still carried %d relationships", len(rels))
	}
	metadata := requireMapValue(t, stale["_metadata"], "_metadata")
	refs := requireMapValue(t, metadata["evidence_refs"], "evidence_refs")
	if refs["emitted"] != false {
		t.Fatalf("stale evidence_refs = %v, want emitted=false", refs)
	}
}

// registeredToolNames returns the sorted tool names from the same registry the
// Python schema snapshot is generated from.
func registeredToolNames(t *testing.T) []string {
	t.Helper()
	raw, err := RegisteredToolDefinitionsJSON()
	if err != nil {
		t.Fatalf("RegisteredToolDefinitionsJSON: %v", err)
	}
	var asList []map[string]any
	if err := json.Unmarshal(raw, &asList); err == nil {
		names := make([]string, 0, len(asList))
		for _, def := range asList {
			if name, _ := def["name"].(string); name != "" {
				names = append(names, name)
			}
		}
		return names
	}
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatalf("registered tool definitions are neither a list nor a map: %v", err)
	}
	names := make([]string, 0, len(asMap))
	for name := range asMap {
		names = append(names, name)
	}
	return names
}

// callToolJSON calls one tool over the client session and decodes its text
// content as a JSON object, failing the test on transport or tool errors.
func callToolJSON(ctx context.Context, t *testing.T, session *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: transport error: %v", name, err)
	}
	var text strings.Builder
	for _, content := range result.Content {
		if tc, ok := content.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	if result.IsError {
		t.Fatalf("%s returned an error result: %s", name, text.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(text.String()), &decoded); err != nil {
		t.Fatalf("%s returned non-JSON text: %v\n%s", name, err, text.String())
	}
	return decoded
}

// gitBackedFixtureCopy copies a fixture directory into a fresh git repository
// with one commit so index identity has a revision and a clean fingerprint.
func gitBackedFixtureCopy(t *testing.T, fixture string) string {
	t.Helper()
	src, err := filepath.Abs(fixture)
	if err != nil {
		t.Fatalf("abs fixture: %v", err)
	}
	// Not t.TempDir(): on macOS that resolves under /var, which the indexer
	// refuses as a system directory. The repo-root _fixtures directory is
	// gitignored and approved, matching writeFixtureRepo.
	fixtureRoot, err := filepath.Abs(filepath.Join("..", "..", "_fixtures"))
	if err != nil {
		t.Fatalf("resolve fixture root: %v", err)
	}
	rootExisted := true
	if _, statErr := os.Stat(fixtureRoot); os.IsNotExist(statErr) {
		rootExisted = false
	}
	if err := os.MkdirAll(fixtureRoot, 0o750); err != nil {
		t.Fatalf("mkdir fixture root: %v", err)
	}
	dst, err := os.MkdirTemp(fixtureRoot, "e2e-go-minimal-")
	if err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dst)
		if !rootExisted {
			_ = os.Remove(fixtureRoot)
		}
	})
	if err := copyTree(src, dst); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=e2e", "-c", "user.email=e2e@example.invalid", "add", "."},
		{"-c", "user.name=e2e", "-c", "user.email=e2e@example.invalid", "commit", "-q", "-m", "fixture"},
	} {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dst
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dst
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
}
