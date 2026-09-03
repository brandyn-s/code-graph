package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brandyn-s/code-graph/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestParseSearchCodeParamsRejectsInvalidPagination(t *testing.T) {
	tests := []struct {
		name      string
		arguments string
		wantField string
	}{
		{"negative max results", `{"pattern":"x","max_results":-1}`, "max_results"},
		{"zero max results", `{"pattern":"x","max_results":0}`, "max_results"},
		{"excessive max results", `{"pattern":"x","max_results":1001}`, "max_results"},
		{"fractional max results", `{"pattern":"x","max_results":1.5}`, "max_results"},
		{"non numeric max results", `{"pattern":"x","max_results":"10"}`, "max_results"},
		{"excessive offset", `{"pattern":"x","offset":1000001}`, "offset"},
		{"fractional offset", `{"pattern":"x","offset":1.5}`, "offset"},
		{"non numeric offset", `{"pattern":"x","offset":"1"}`, "offset"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := &mcp.CallToolRequest{
				Params: &mcp.CallToolParamsRaw{
					Name:      "search_code",
					Arguments: json.RawMessage(test.arguments),
				},
			}

			params, result := parseSearchCodeParams(req)
			if params != nil {
				t.Fatalf("invalid pagination produced params: %+v", params)
			}
			if result == nil || !result.IsError {
				t.Fatalf("invalid pagination must return a structured error, got %+v", result)
			}
			if len(result.Content) == 0 {
				t.Fatal("structured error has no content")
			}
			text, ok := result.Content[0].(*mcp.TextContent)
			if !ok {
				t.Fatalf("structured error content is %T, want TextContent", result.Content[0])
			}
			if !strings.Contains(text.Text, test.wantField) {
				t.Fatalf("error %q does not identify invalid field %q", text.Text, test.wantField)
			}
		})
	}
}

func TestSearchCodeSchemaDeclaresPaginationBounds(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)

	srv := NewServer(router)
	srv.sessionOnce.Do(func() {})
	srv.updateOnce.Do(func() {})

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.MCPServer().Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(
		&mcp.Implementation{Name: "schema-test", Version: "dev"},
		&mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}},
	)
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	list, err := clientSession.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	var searchCode *mcp.Tool
	for _, tool := range list.Tools {
		if tool.Name == "search_code" {
			searchCode = tool
			break
		}
	}
	if searchCode == nil {
		t.Fatal("search_code not advertised")
		return
	}

	schema, ok := searchCode.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("input schema is %T, want map", searchCode.InputSchema)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties are %T, want map", schema["properties"])
	}
	assertBounds := func(field string, wantMin, wantMax float64) {
		t.Helper()
		property, ok := properties[field].(map[string]any)
		if !ok {
			t.Fatalf("%s schema is %T, want map", field, properties[field])
		}
		if got := property["minimum"]; got != wantMin {
			t.Errorf("%s minimum = %v, want %v", field, got, wantMin)
		}
		if got := property["maximum"]; got != wantMax {
			t.Errorf("%s maximum = %v, want %v", field, got, wantMax)
		}
	}
	assertBounds("max_results", 1, maxSearchCodeResults)
	assertBounds("offset", 0, maxSearchCodeOffset)
}

func TestSearchCodePaginationReportsExactTotal(t *testing.T) {
	root := t.TempDir()
	content := strings.Join([]string{
		"needle one",
		"needle two",
		"needle three",
		"needle four",
		"needle five",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)
	st, err := router.ForProject("test")
	if err != nil {
		t.Fatalf("ForProject: %v", err)
	}
	if err := st.UpsertProject("test", root); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	if _, err := st.UpsertNode(&store.Node{
		Project: "test", Label: "File", Name: "sample.go", FilePath: "sample.go",
	}); err != nil {
		t.Fatalf("insert file node: %v", err)
	}

	srv := NewServer(router)
	srv.sessionOnce.Do(func() {})
	srv.updateOnce.Do(func() {})

	tests := []struct {
		name        string
		offset      int
		limit       int
		wantLines   []int
		wantHasMore bool
	}{
		{
			name:        "exact final page",
			offset:      3,
			limit:       2,
			wantLines:   []int{4, 5},
			wantHasMore: false,
		},
		{
			name:        "one extra match",
			offset:      0,
			limit:       4,
			wantLines:   []int{1, 2, 3, 4},
			wantHasMore: true,
		},
		{
			name:        "later page",
			offset:      2,
			limit:       2,
			wantLines:   []int{3, 4},
			wantHasMore: true,
		},
		{
			name:        "empty page",
			offset:      5,
			limit:       2,
			wantLines:   nil,
			wantHasMore: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := metadataResponseFromHandler(
				t,
				srv.handleSearchCode,
				"search_code",
				map[string]any{
					"pattern":     "needle",
					"project":     "test",
					"offset":      tt.offset,
					"max_results": tt.limit,
				},
			)

			total, ok := response["total"].(float64)
			if !ok {
				t.Fatalf("total is %T, want number", response["total"])
			}
			if got := int(total); got != 5 {
				t.Errorf("total = %d, want exact total 5", got)
			}
			if got, _ := response["has_more"].(bool); got != tt.wantHasMore {
				t.Errorf("has_more = %t, want %t", got, tt.wantHasMore)
			}
			rawMatches := toSlice(response["matches"])
			if len(rawMatches) != len(tt.wantLines) {
				t.Fatalf("matches = %d, want %d: %v", len(rawMatches), len(tt.wantLines), rawMatches)
			}
			for i, raw := range rawMatches {
				match, ok := raw.(map[string]any)
				if !ok {
					t.Fatalf("match[%d] is %T, want map", i, raw)
				}
				line, ok := match["line"].(float64)
				if !ok {
					t.Fatalf("match[%d].line is %T, want number", i, match["line"])
				}
				if got := int(line); got != tt.wantLines[i] {
					t.Errorf("match[%d].line = %d, want %d", i, got, tt.wantLines[i])
				}
			}
		})
	}
}

func TestSearchCodeCountsMatchOnLineLargerThanScannerLimit(t *testing.T) {
	root := t.TempDir()
	const oldScannerLimit = 1024 * 1024
	content := strings.Repeat("x", oldScannerLimit+128) + " needle\n"
	if err := os.WriteFile(filepath.Join(root, "large.go"), []byte(content), 0o600); err != nil {
		t.Fatalf("write large-line fixture: %v", err)
	}

	srv, _ := newSearchCodeTestServer(t, root, "large.go")
	response := metadataResponseFromHandler(
		t,
		srv.handleSearchCode,
		"search_code",
		map[string]any{
			"pattern": "needle",
			"project": "test",
		},
	)

	total, ok := response["total"].(float64)
	if !ok {
		t.Fatalf("total is %T, want number", response["total"])
	}
	if got := int(total); got != 1 {
		t.Errorf("total = %d, want exact total 1 for a match on a line larger than 1 MiB", got)
	}
	matches := toSlice(response["matches"])
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1: %v", len(matches), matches)
	}
}

func TestSearchCodeReturnsErrorWhenIndexedFileCannotBeCompletelyRead(t *testing.T) {
	tests := []struct {
		name        string
		indexedPath string
		setup       func(*testing.T, string)
	}{
		{
			name:        "open failure",
			indexedPath: "missing.go",
			setup:       func(*testing.T, string) {},
		},
		{
			name:        "read failure",
			indexedPath: "source-dir",
			setup: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(root, "source-dir"), 0o700); err != nil {
					t.Fatalf("create directory fixture: %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			tt.setup(t, root)
			srv, _ := newSearchCodeTestServer(t, root, tt.indexedPath)

			result := callSearchCodeForTest(t, srv, map[string]any{
				"pattern": "needle",
				"project": "test",
			})
			assertSearchCodeError(t, result, tt.indexedPath)
		})
	}
}

func TestSearchCodeReturnsErrorWhenIndexedFilesCannotBeListed(t *testing.T) {
	root := t.TempDir()
	srv, st := newSearchCodeTestServer(t, root, "sample.go")

	// Resolve the root from the session fields so the closed-store failure
	// occurs specifically while collecting indexed file paths.
	srv.sessionRoot = root
	srv.sessionProject = "test"
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	result := callSearchCodeForTest(t, srv, map[string]any{
		"pattern": "needle",
	})
	assertSearchCodeError(t, result, "collect files")
}

func newSearchCodeTestServer(t *testing.T, root string, filePaths ...string) (*Server, *store.Store) {
	t.Helper()

	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)
	st, err := router.ForProject("test")
	if err != nil {
		t.Fatalf("ForProject: %v", err)
	}
	if err := st.UpsertProject("test", root); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	for _, filePath := range filePaths {
		if _, err := st.UpsertNode(&store.Node{
			Project: "test", Label: "File", Name: filepath.Base(filePath), FilePath: filePath,
		}); err != nil {
			t.Fatalf("insert file node %q: %v", filePath, err)
		}
	}

	srv := NewServer(router)
	srv.sessionOnce.Do(func() {})
	srv.updateOnce.Do(func() {})
	return srv, st
}

func callSearchCodeForTest(t *testing.T, srv *Server, args map[string]any) *mcp.CallToolResult {
	t.Helper()

	arguments, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal search arguments: %v", err)
	}
	result, err := srv.handleSearchCode(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "search_code",
			Arguments: arguments,
		},
	})
	if err != nil {
		t.Fatalf("search_code handler error: %v", err)
	}
	if result == nil {
		t.Fatal("search_code returned nil result")
	}
	return result
}

func assertSearchCodeError(t *testing.T, result *mcp.CallToolResult, wantText string) {
	t.Helper()

	if !result.IsError {
		t.Fatalf("search_code must return a structured error when exact totals cannot be computed: %+v", result)
	}
	if len(result.Content) == 0 {
		t.Fatal("structured search error has no content")
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("structured search error content is %T, want TextContent", result.Content[0])
	}
	if !strings.Contains(strings.ToLower(text.Text), strings.ToLower(wantText)) {
		t.Fatalf("structured search error %q does not contain %q", text.Text, wantText)
	}
}
