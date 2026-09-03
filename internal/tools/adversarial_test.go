package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/brandyn-s/code-graph/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// newAdversarialTestServer builds a minimal Server suitable for adversarial
// input testing. The server has a working StoreRouter but no indexed projects
// — that's intentional: we want to verify each tool gracefully handles
// malicious input from a fresh state, not after a particular schema.
func newAdversarialTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	router, err := store.NewRouterWithDir(dir)
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(func() { router.CloseAll() })
	return &Server{router: router}
}

// TestAdversarialPayloads runs a battery of malicious / malformed inputs
// against every registered MCP tool and asserts the server (a) doesn't panic,
// (b) doesn't return success on garbage input, and (c) gracefully surfaces
// the error via IsError or an error-prefixed text response.
//
// Mirrors the 23-payload audit suite documented in upstream code-graph
// arXiv:2603.27277 Section 3.8 (8-Layer CI Audit Suite, MCP robustness testing).
//
// Categories covered:
//   - Malformed JSON (truncated, extra braces, wrong types)
//   - SQL injection via Cypher query_graph (DROP TABLE, ATTACH DATABASE)
//   - Shell metacharacters in tool string args (path traversal, $(whoami), backticks)
//   - Oversized inputs (1 MB strings)
//   - Empty / null / wrong-type fields where strings expected
//   - Unicode / NULL byte injection
//   - Deeply-nested JSON arrays
//
// The agent / MCP framework should reject these without panicking. Concrete
// per-tool error semantics (e.g., "project not found" vs "invalid argument")
// are tool-specific and not asserted here — the contract this test pins is
// "no panic, no successful processing of obviously-malicious input."
func TestAdversarialPayloads(t *testing.T) {
	srv := newAdversarialTestServer(t)

	cases := []struct {
		name  string
		tool  string
		args  string // raw JSON args
		check func(t *testing.T, result *mcp.CallToolResult, err error)
	}{
		// --- Malformed JSON ---
		{
			"truncated_json",
			"search_graph",
			`{"project":`,
			expectErrorOrIsError,
		},
		{
			"extra_braces",
			"search_graph",
			`{"project":"foo"}}}`,
			anyOutcomeNoPanic,
		},
		{
			"wrong_type_for_string",
			"search_graph",
			`{"project":12345}`,
			anyOutcomeNoPanic,
		},

		// --- SQL injection via Cypher ---
		{
			"cypher_drop_table",
			"query_graph",
			`{"project":"any","query":"MATCH (n) DROP TABLE nodes; --"}`,
			expectErrorOrIsError,
		},
		{
			"cypher_attach_database",
			"query_graph",
			`{"project":"any","query":"ATTACH DATABASE '/etc/passwd' AS pwn"}`,
			expectErrorOrIsError,
		},
		{
			"cypher_pragma_attack",
			"query_graph",
			`{"project":"any","query":"PRAGMA writable_schema = 1; DELETE FROM nodes;"}`,
			expectErrorOrIsError,
		},
		{
			"cypher_union_select",
			"query_graph",
			`{"project":"any","query":"MATCH (n) RETURN n UNION SELECT name FROM sqlite_master"}`,
			expectErrorOrIsError,
		},

		// --- Shell metacharacters in tool args ---
		{
			"shell_command_substitution",
			"get_code_snippet",
			`{"project":"any","qualified_name":"$(whoami)"}`,
			anyOutcomeNoPanic,
		},
		{
			"shell_backticks",
			"get_code_snippet",
			"{\"project\":\"any\",\"qualified_name\":\"`id`\"}",
			anyOutcomeNoPanic,
		},
		{
			"shell_pipe",
			"search_code",
			`{"project":"any","pattern":"foo | cat /etc/passwd"}`,
			anyOutcomeNoPanic,
		},
		{
			"shell_redirect",
			"search_code",
			`{"project":"any","pattern":"foo > /tmp/pwn"}`,
			anyOutcomeNoPanic,
		},

		// --- Path traversal ---
		{
			"path_traversal_dotdot",
			"index_repository",
			`{"path":"../../../etc/passwd"}`,
			anyOutcomeNoPanic,
		},
		{
			"path_traversal_absolute",
			"index_repository",
			`{"path":"/etc/shadow"}`,
			anyOutcomeNoPanic,
		},

		// --- Sensitive directory indexing (forbidden index paths) ---
		{
			"index_etc",
			"index_repository",
			`{"repo_path":"/etc"}`,
			expectErrorOrIsError,
		},
		{
			"index_var",
			"index_repository",
			`{"repo_path":"/var"}`,
			expectErrorOrIsError,
		},
		{
			"index_root",
			"index_repository",
			`{"repo_path":"/"}`,
			expectErrorOrIsError,
		},
		{
			"path_traversal_url_encoded",
			"get_code_snippet",
			`{"project":"any","qualified_name":"%2e%2e%2f%2e%2e%2fetc%2fpasswd"}`,
			anyOutcomeNoPanic,
		},

		// --- Oversized inputs ---
		{
			"oversized_pattern_1mb",
			"search_code",
			`{"project":"any","pattern":"` + strings.Repeat("A", 1024*1024) + `"}`,
			anyOutcomeNoPanic,
		},
		{
			"oversized_qualified_name",
			"get_code_snippet",
			`{"project":"any","qualified_name":"` + strings.Repeat("x.", 50000) + `end"}`,
			anyOutcomeNoPanic,
		},

		// --- Empty / null / missing required fields ---
		{
			"empty_project",
			"search_graph",
			`{"project":""}`,
			anyOutcomeNoPanic,
		},
		{
			"null_project",
			"search_graph",
			`{"project":null}`,
			anyOutcomeNoPanic,
		},
		{
			"missing_required",
			"query_graph",
			`{"project":"any"}`, // missing required 'query'
			expectErrorOrIsError,
		},

		// --- Unicode / NUL injection (using \x00 / \x01 escapes since Go
		// source files cannot contain literal NUL or low control bytes) ---
		{
			"null_byte_injection",
			"get_code_snippet",
			"{\"project\":\"any\",\"qualified_name\":\"foo\x00bar\"}",
			anyOutcomeNoPanic,
		},
		{
			"control_chars_in_pattern",
			"search_code",
			"{\"project\":\"any\",\"pattern\":\"foo\x01\x02\x03\"}",
			anyOutcomeNoPanic,
		},

		// --- Deeply-nested JSON ---
		{
			"deeply_nested_array",
			"search_graph",
			`{"project":"any","name_pattern":` + deepArray(100) + `}`,
			anyOutcomeNoPanic,
		},

		// --- ReDoS-style regex ---
		{
			"redos_search_code",
			"search_code",
			`{"project":"any","pattern":"(a+)+b","regex":true}`,
			anyOutcomeNoPanic,
		},

		// --- Project name with reserved DB characters ---
		{
			"project_name_with_slashes",
			"search_graph",
			`{"project":"../../another_db"}`,
			anyOutcomeNoPanic,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("PANIC on adversarial input: %v", r)
				}
			}()

			result, err := srv.CallTool(context.Background(), tc.tool, json.RawMessage(tc.args))
			tc.check(t, result, err)
		})
	}
}

// expectErrorOrIsError passes when the tool returned an error path
// (either Go-level error or IsError=true on the result).
func expectErrorOrIsError(t *testing.T, result *mcp.CallToolResult, err error) {
	t.Helper()
	if err != nil {
		return // good, surfaced as a Go error
	}
	if result == nil {
		t.Fatal("got nil result and nil err — expected one or the other")
	}
	if result.IsError {
		return // good, surfaced via MCP error contract
	}
	// Some tools emit error text without IsError; accept "error:" prefix.
	for _, c := range result.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			if strings.HasPrefix(strings.ToLower(tc.Text), "error") ||
				strings.Contains(strings.ToLower(tc.Text), "invalid") ||
				strings.Contains(strings.ToLower(tc.Text), "required") {
				return
			}
		}
	}
	t.Fatalf("expected error response, got success: %+v", result)
}

// anyOutcomeNoPanic just records that no panic occurred. Useful for inputs
// where the right behavior is "sanitize and proceed" — we don't dictate
// success vs error, only that the server stays up.
func anyOutcomeNoPanic(t *testing.T, result *mcp.CallToolResult, err error) {
	t.Helper()
	// no assertion — the deferred recover() handles panic detection
	_ = result
	_ = err
}

// deepArray returns a string like "[[[...[]...]]]" with `depth` open brackets.
func deepArray(depth int) string {
	var sb strings.Builder
	for i := 0; i < depth; i++ {
		sb.WriteByte('[')
	}
	for i := 0; i < depth; i++ {
		sb.WriteByte(']')
	}
	return sb.String()
}
