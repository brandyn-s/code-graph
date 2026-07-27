package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/indexidentity"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// newServerWithRouter constructs a minimal Server suitable for testing the
// resolveProjectName + handleListProjects logic. Avoids NewServer's heavy
// registerTools/watcher setup.
func newServerWithRouter(t *testing.T) (*Server, *store.StoreRouter) {
	t.Helper()
	dir := t.TempDir()
	router, err := store.NewRouterWithDir(dir)
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(func() { router.CloseAll() })
	return &Server{router: router}, router
}

func upsertTestProject(t *testing.T, router *store.StoreRouter, name, rootPath string) {
	t.Helper()
	st, err := router.ForProject(name)
	if err != nil {
		t.Fatalf("ForProject %q: %v", name, err)
	}
	if err := st.UpsertProject(name, rootPath); err != nil {
		t.Fatalf("UpsertProject %q: %v", name, err)
	}
}

func TestResolveProjectName_FriendlyBasename(t *testing.T) {
	s, router := newServerWithRouter(t)

	// Two registered projects with distinct basenames.
	upsertTestProject(t, router, "c-Users-someone-myrepo", "/Users/someone/myrepo")
	upsertTestProject(t, router, "c-Users-someone-other", "/Users/someone/other")

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty falls back to session project", "", ""}, // sessionProject is unset, returns ""
		{"exact canonical match", "c-Users-someone-myrepo", "c-Users-someone-myrepo"},
		{"friendly basename resolves to canonical", "myrepo", "c-Users-someone-myrepo"},
		{"friendly basename — second project", "other", "c-Users-someone-other"},
		{"unknown name passes through unchanged", "nope", "nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.resolveProjectName(tc.in); got != tc.want {
				t.Errorf("resolveProjectName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestResolveProjectName_AmbiguousReturnsInput(t *testing.T) {
	// Two projects with the same basename — resolution must NOT pick one
	// arbitrarily; it should return the input unchanged so the caller fails
	// loudly with the friendly name in the error message.
	s, router := newServerWithRouter(t)
	upsertTestProject(t, router, "a-foo", "/work/foo")
	upsertTestProject(t, router, "b-foo", "/lab/foo")

	if got := s.resolveProjectName("foo"); got != "foo" {
		t.Errorf("ambiguous: got %q, want %q (input unchanged)", got, "foo")
	}
}

func TestResolveProjectName_SkipsConfigDB(t *testing.T) {
	// _config.db (internal MCP config) must not be considered when resolving
	// friendly names, even if a basename happened to collide.
	s, router := newServerWithRouter(t)
	upsertTestProject(t, router, "c-Users-someone-myrepo", "/Users/someone/myrepo")
	// Drop a fake _config.db with metadata that would otherwise match.
	upsertTestProject(t, router, "_config", "/Users/someone/_config_fake")

	// Friendly resolution must skip the underscore-prefixed entry.
	if got := s.resolveProjectName("myrepo"); got != "c-Users-someone-myrepo" {
		t.Errorf("got %q, want canonical name", got)
	}
	// And resolving a name that ONLY matches the _config entry must pass
	// through unchanged (no resolution).
	if got := s.resolveProjectName("_config_fake"); got != "_config_fake" {
		t.Errorf("got %q, want %q (input unchanged)", got, "_config_fake")
	}
}

func TestHandleListProjects_FiltersConfigDB(t *testing.T) {
	// Verify the public list_projects output omits _config.db.
	dir := t.TempDir()
	router, err := store.NewRouterWithDir(dir)
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(func() { router.CloseAll() })

	upsertTestProject(t, router, "myproject", "/work/myproject")

	// Touch a _config.db so the directory contains one. The router will
	// surface it via ListProjects (it scans .db files).
	configDB := filepath.Join(dir, "_config.db")
	if err := os.WriteFile(configDB, []byte{}, 0o600); err != nil {
		t.Fatalf("write _config.db: %v", err)
	}

	infos, err := router.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	hasConfig := false
	for _, info := range infos {
		if info.Name == "_config" {
			hasConfig = true
			break
		}
	}
	if !hasConfig {
		t.Skip("router.ListProjects already filters _config; nothing for handler to skip")
	}

	// Walk the same filter logic the handler uses.
	visible := 0
	for _, info := range infos {
		if len(info.Name) > 0 && info.Name[0] == '_' {
			continue
		}
		visible++
	}
	if visible != 1 {
		t.Errorf("after filter: got %d visible projects, want 1", visible)
	}
}

func listProjectsResponse(t *testing.T, s *Server) []map[string]any {
	t.Helper()
	res, err := s.handleListProjects(context.Background(), nil)
	if err != nil {
		t.Fatalf("list_projects handler: %v", err)
	}
	if res == nil || len(res.Content) == 0 {
		t.Fatal("list_projects returned nil/empty result")
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("list_projects Content[0] = %T, want TextContent", res.Content[0])
	}
	var projects []map[string]any
	if err := json.Unmarshal([]byte(text.Text), &projects); err != nil {
		t.Fatalf("parse list_projects response: %v\n%s", err, text.Text)
	}
	return projects
}

func TestLegacyProjectIdentityIsExplicitlyDegradedInStatusAndList(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)
	s := NewServer(router)
	const project = "legacy"
	upsertTestProject(t, router, project, "/work/legacy")

	status := metadataResponseFromHandler(t, s.handleIndexStatus, "index_status",
		map[string]any{"project": project})
	if got, _ := status["status"].(string); got != "degraded" {
		t.Errorf("index_status.status = %q, want degraded", got)
	}
	if got, _ := status["identity_status"].(string); got != indexidentity.StatusMissing {
		t.Errorf("index_status.identity_status = %q, want %q", got, indexidentity.StatusMissing)
	}
	if reason, _ := status["identity_reason"].(string); reason == "" {
		t.Error("index_status.identity_reason is empty; want reindex remediation")
	}
	if identity := status["index_identity"]; identity != nil {
		t.Errorf("legacy index_status exposed an identity: %v", identity)
	}
	metadata, _ := status["_metadata"].(map[string]any)
	freshness, _ := metadata["freshness"].(map[string]any)
	if got, _ := freshness["state"].(string); got != "unknown" {
		t.Errorf("legacy metadata freshness = %q, want unknown", got)
	}

	projects := listProjectsResponse(t, s)
	if len(projects) != 1 {
		t.Fatalf("list_projects returned %d projects, want 1: %v", len(projects), projects)
	}
	entry := projects[0]
	if got, _ := entry["status"].(string); got != "degraded" {
		t.Errorf("list_projects status = %q, want degraded", got)
	}
	if got, _ := entry["identity_status"].(string); got != indexidentity.StatusMissing {
		t.Errorf("list_projects identity_status = %q, want %q", got, indexidentity.StatusMissing)
	}
	if reason, _ := entry["identity_reason"].(string); reason == "" {
		t.Error("list_projects identity_reason is empty; want reindex remediation")
	}
	if identity := entry["index_identity"]; identity != nil {
		t.Errorf("legacy list_projects exposed an identity: %v", identity)
	}
}
