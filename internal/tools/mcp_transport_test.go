package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPTransport_ContentExtraction pins the contract that the MCP transport
// path (used by Claude Code stdio + any MCP-compatible agent) receives the
// real tool response even when an update notice is prepended.
//
// Companion to:
//   - cmd/codebase-memory-mcp/main.go CLI-extraction fix (banner-skip)
//   - bench/research/agent-effectiveness/ category 1 questions
//
// The bug we shipped 2026-05-12: CLI extraction read `result.Content[0]` and
// got the banner, dropping the actual JSON. The MCP transport doesn't have
// the same bug because the agent receives the full Content[] slice — both
// the banner block AND the JSON block. This test pins that invariant: when
// an update notice is set, the result.Content slice MUST contain both
// blocks, and the JSON block MUST be reachable.
//
// If a future refactor changes addUpdateNotice to OVERWRITE Content[0]
// instead of prepending, this test will catch it.
// If a future refactor removes the JSON block when an update is set, this
// test will catch it.
func TestMCPTransport_ContentExtraction(t *testing.T) {
	s := newServerWithSeededProject(t)

	// Force an update notice into the server state. The notice is set
	// by checkForUpdate() when a newer release is found; we simulate
	// that condition here without needing network.
	s.updateNotice.Store("⚡ Update available: vtest → v0.6.1 — run: codebase-memory-mcp update")

	// Use search_graph: it calls addUpdateNotice on its success path and
	// always returns at least 1 JSON Content block when a project is
	// seeded.
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "search_graph",
			Arguments: json.RawMessage(`{"project":"test","limit":3}`),
		},
	}
	result, err := s.handleSearchGraph(context.Background(), req)
	if err != nil {
		t.Fatalf("search_graph call failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.IsError {
		// Print the error text for diagnosis
		if len(result.Content) > 0 {
			if tc, ok := result.Content[0].(*mcp.TextContent); ok {
				t.Fatalf("expected success, got IsError: %s", tc.Text)
			}
		}
		t.Fatalf("expected success, got IsError")
	}
	if len(result.Content) < 2 {
		t.Fatalf("expected ≥2 Content blocks (banner + real response), got %d. "+
			"This means the banner displaced the real response — the MCP "+
			"transport's contract is broken.", len(result.Content))
	}

	// Block 0 should be the banner.
	first, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected first content block to be TextContent, got %T", result.Content[0])
	}
	if !strings.HasPrefix(first.Text, "⚡ Update available") {
		t.Errorf("expected first block to be the update banner; got %q", first.Text[:tmin(80, len(first.Text))])
	}

	// Block 1+ should contain the real JSON response.
	var foundJSON bool
	for i, c := range result.Content[1:] {
		tc, ok := c.(*mcp.TextContent)
		if !ok {
			continue
		}
		trimmed := strings.TrimSpace(tc.Text)
		if strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{") {
			foundJSON = true
			break
		}
		t.Logf("Content[%d] text: %q", i+1, tc.Text[:tmin(80, len(tc.Text))])
	}
	if !foundJSON {
		t.Error("expected a JSON-shaped Content block after the banner; none found. " +
			"The MCP-transport agent sees only the banner — the real response is gone.")
	}
}

// TestMCPTransport_NoBannerWhenNoNotice verifies the inverse: when no update
// notice is pending, the response should NOT have a banner prefix. Content[0]
// should be the real JSON directly.
func TestMCPTransport_NoBannerWhenNoNotice(t *testing.T) {
	s := newServerWithSeededProject(t)
	s.updateNotice.Store("")

	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "search_graph",
			Arguments: json.RawMessage(`{"project":"test","limit":3}`),
		},
	}
	result, err := s.handleSearchGraph(context.Background(), req)
	if err != nil {
		t.Fatalf("search_graph call failed: %v", err)
	}
	if result == nil || result.IsError {
		t.Fatalf("expected success result; got %+v", result)
	}
	if len(result.Content) == 0 {
		t.Fatal("expected ≥1 Content block in normal response")
	}
	first, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected first content block to be TextContent, got %T", result.Content[0])
	}
	if strings.HasPrefix(first.Text, "⚡ Update available") {
		t.Errorf("Content[0] starts with banner even though no notice was set: %q",
			first.Text[:tmin(80, len(first.Text))])
	}
}

// TestMCPTransport_BannerPlusRealResponse_OnAllNoticeCallers sweeps the
// handlers that call addUpdateNotice and confirms each returns ≥2 Content
// blocks when a notice is pending: the banner at [0], and the real response
// at [1+]. If any handler returns just the banner (or just the response,
// dropping the banner), this test surfaces it.
func TestMCPTransport_BannerPlusRealResponse_OnAllNoticeCallers(t *testing.T) {
	cases := []struct {
		name    string
		handler func(s *Server) func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error)
		args    string
	}{
		{
			"search_graph",
			func(s *Server) func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return s.handleSearchGraph
			},
			`{"project":"test","limit":3}`,
		},
		{
			"query_graph",
			func(s *Server) func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return s.handleQueryGraph
			},
			`{"project":"test","query":"MATCH (n) RETURN n LIMIT 3"}`,
		},
		{
			"get_architecture",
			func(s *Server) func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return s.handleGetArchitecture
			},
			`{"project":"test"}`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s := newServerWithSeededProject(t)
			s.updateNotice.Store("⚡ Update available: vtest → v0.6.1 — run: codebase-memory-mcp update")
			req := &mcp.CallToolRequest{
				Params: &mcp.CallToolParamsRaw{
					Name:      tc.name,
					Arguments: json.RawMessage(tc.args),
				},
			}
			result, err := tc.handler(s)(context.Background(), req)
			if err != nil {
				t.Fatalf("%s: tool err: %v", tc.name, err)
			}
			if result == nil {
				t.Fatalf("%s: nil result", tc.name)
			}
			if result.IsError {
				if len(result.Content) > 0 {
					if textC, ok := result.Content[0].(*mcp.TextContent); ok {
						t.Fatalf("%s: result.IsError; first content: %s", tc.name, textC.Text)
					}
				}
				t.Fatalf("%s: result.IsError with no content", tc.name)
			}
			if len(result.Content) < 2 {
				t.Fatalf("%s: expected ≥2 Content blocks under update-pending state; got %d. "+
					"The banner-prepend is the canonical 2-block contract — if this drops to 1, "+
					"either the banner is missing or the real response was displaced.",
					tc.name, len(result.Content))
			}
			first, ok := result.Content[0].(*mcp.TextContent)
			if !ok {
				t.Fatalf("%s: expected TextContent at [0]; got %T", tc.name, result.Content[0])
			}
			if !strings.HasPrefix(first.Text, "⚡ Update available") {
				t.Errorf("%s: expected banner at Content[0]; got %q",
					tc.name, first.Text[:tmin(80, len(first.Text))])
			}
		})
	}
}

func tmin(a, b int) int {
	if a < b {
		return a
	}
	return b
}
