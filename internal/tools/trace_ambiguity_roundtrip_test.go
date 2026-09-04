package tools

// Round-trip property test for trace_call_path's ambiguity guard.
//
// The guard (trace.go) returns status="ambiguous" plus a list of candidate
// `qualified_name` values, with a message instructing the caller to "re-call
// trace_call_path with a fully-qualified name from the suggestions below to
// disambiguate". That is a CONTRACT: every QN the tool emits must be accepted
// on the retry.
//
// It was not held. findNodeAcrossProjects looked candidates up via
// FindNodesByName, which matches the `name` column; a dotted QN never appears
// there, so the advertised retry returned "function not found" for every
// ambiguous symbol. Reproduced live on a Go service repo (2026-07-27) for both
// `AppConfig.validate` (3 candidates) and `make_token` (5 candidates) using
// the tool's own verbatim suggestions.
//
// Why no existing test caught it: the ambiguity guard had coverage for
// EMITTING suggestions, and the trace path had coverage for resolving SHORT
// names. Nothing asserted the two compose. A self-consistent check ("does the
// guard fire?") cannot see a defect in the hand-off it advertises — the same
// shape as a regenerate-and-compare drift check certifying a deterministic bug.
// The durable fix is this property: emit → feed back → must resolve.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/brandyn-s/code-graph/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// seedAmbiguousFunctions adds two Functions that share the bare name
// "validate" in different modules, plus a caller for each so the trace has an
// inbound edge to walk. Mirrors the real-world shape (one repo's `validate` x3,
// `make_token` x5) that the guard exists to disambiguate.
func seedAmbiguousFunctions(t *testing.T, st *store.Store, projectName string) {
	t.Helper()
	nodes := []*store.Node{
		{Project: projectName, Label: "Function", Name: "validate",
			QualifiedName: projectName + ".config.validate", FilePath: "src/config.rs",
			StartLine: 10, EndLine: 20},
		{Project: projectName, Label: "Function", Name: "validate",
			QualifiedName: projectName + ".schema.validate", FilePath: "src/schema.rs",
			StartLine: 5, EndLine: 15},
		{Project: projectName, Label: "Function", Name: "load_config",
			QualifiedName: projectName + ".config.load_config", FilePath: "src/config.rs",
			StartLine: 30, EndLine: 45},
		{Project: projectName, Label: "Function", Name: "check_schema",
			QualifiedName: projectName + ".schema.check_schema", FilePath: "src/schema.rs",
			StartLine: 25, EndLine: 35},
	}
	ids := make(map[string]int64, len(nodes))
	for _, n := range nodes {
		id, err := st.UpsertNode(n)
		if err != nil {
			t.Fatalf("UpsertNode %s: %v", n.QualifiedName, err)
		}
		ids[n.QualifiedName] = id
	}
	// One caller per validate, so an inbound trace on either QN is non-empty.
	edges := []struct{ src, tgt string }{
		{projectName + ".config.load_config", projectName + ".config.validate"},
		{projectName + ".schema.check_schema", projectName + ".schema.validate"},
	}
	for _, e := range edges {
		// No confidence property: trace.go keeps edges with confidence=0
		// ("no confidence set"), so the fixture survives the default
		// min_confidence=0.45 filter without pinning a magic number here.
		if _, err := st.InsertEdge(&store.Edge{
			Project:  projectName,
			SourceID: ids[e.src],
			TargetID: ids[e.tgt],
			Type:     "CALLS",
		}); err != nil {
			t.Fatalf("InsertEdge %s->%s: %v", e.src, e.tgt, err)
		}
	}
}

// traceResponse invokes handleTraceCallPath and parses the JSON body.
func traceResponse(t *testing.T, s *Server, args map[string]any) map[string]any {
	t.Helper()
	argBytes, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	res, err := s.handleTraceCallPath(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "trace_call_path", Arguments: argBytes},
	})
	if err != nil {
		t.Fatalf("handleTraceCallPath error: %v", err)
	}
	if res == nil || len(res.Content) == 0 {
		t.Fatalf("trace_call_path returned nil/empty result")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("Content[0] is not TextContent: %T", res.Content[0])
	}
	// errResult returns a BARE STRING, not JSON. Surface that as the error it
	// is rather than letting json.Unmarshal report a confusing parse failure —
	// "function not found: <qn>" is the exact production symptom this test
	// exists to catch, and it should read that way in the failure output.
	if !strings.HasPrefix(strings.TrimSpace(tc.Text), "{") {
		return map[string]any{"error": strings.TrimSpace(tc.Text)}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(tc.Text), &out); err != nil {
		t.Fatalf("response not valid JSON: %v\nbody: %.300s", err, tc.Text)
	}
	return out
}

func newServerWithAmbiguousProject(t *testing.T) *Server {
	t.Helper()
	s, router := newServerWithRouter(t)
	const projectName = "ambig"
	upsertTestProject(t, router, projectName, "/tmp/ambig")
	st, err := router.ForProject(projectName)
	if err != nil {
		t.Fatalf("ForProject: %v", err)
	}
	seedAmbiguousFunctions(t, st, projectName)
	s.sessionProject = projectName
	return s
}

// TestTraceCallPath_AmbiguousSuggestionsAreResolvable is the regression test
// for the disambiguation round-trip. It asserts the contract end to end:
// a bare ambiguous name yields status="ambiguous" with suggestions, and EVERY
// suggested qualified_name resolves when passed straight back.
func TestTraceCallPath_AmbiguousSuggestionsAreResolvable(t *testing.T) {
	s := newServerWithAmbiguousProject(t)

	resp := traceResponse(t, s, map[string]any{
		"function_name": "validate",
		"project":       "ambig",
		"direction":     "inbound",
	})

	if got := resp["status"]; got != "ambiguous" {
		t.Fatalf("bare ambiguous name: status = %v, want \"ambiguous\" (response keys=%v)",
			got, mapKeys(resp))
	}

	raw, ok := resp["suggestions"].([]any)
	if !ok || len(raw) < 2 {
		t.Fatalf("expected >=2 suggestions, got %v", resp["suggestions"])
	}

	// The load-bearing assertion: feed each emitted QN back verbatim.
	for _, item := range raw {
		sugg, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("suggestion is not an object: %T", item)
		}
		qn, ok := sugg["qualified_name"].(string)
		if !ok || qn == "" {
			t.Fatalf("suggestion missing qualified_name: %v", sugg)
		}

		retry := traceResponse(t, s, map[string]any{
			"function_name": qn,
			"project":       "ambig",
			"direction":     "inbound",
		})

		// Any of these three means the advertised retry did not work.
		if status, present := retry["status"]; present {
			if status == "ambiguous" {
				t.Errorf("retry with QN %q was still ambiguous — the QN did not disambiguate", qn)
				continue
			}
			if status == "not_found" {
				t.Errorf("retry with QN %q returned not_found; message=%v", qn, retry["message"])
				continue
			}
		}
		if errMsg, present := retry["error"]; present {
			t.Errorf("retry with QN %q errored: %v", qn, errMsg)
			continue
		}

		// Positive confirmation: it resolved to the node we asked for, not
		// some other same-named candidate (the nodes[0] guess this guard
		// was introduced to prevent).
		root, ok := retry["root"].(map[string]any)
		if !ok {
			t.Errorf("retry with QN %q returned no root node; keys=%v", qn, mapKeys(retry))
			continue
		}
		if gotQN := root["qualified_name"]; gotQN != qn {
			t.Errorf("retry with QN %q resolved to the wrong node: root.qualified_name = %v", qn, gotQN)
		}
	}
}

// TestTraceCallPath_ShortNameStillResolvesWhenUnambiguous guards the other
// direction: adding the QN lookup must not break plain short-name traces,
// which are the common case.
func TestTraceCallPath_ShortNameStillResolvesWhenUnambiguous(t *testing.T) {
	s := newServerWithAmbiguousProject(t)

	resp := traceResponse(t, s, map[string]any{
		"function_name": "load_config", // unique in the fixture
		"project":       "ambig",
		"direction":     "outbound",
	})

	if status, present := resp["status"]; present && status != "ok" {
		t.Fatalf("unambiguous short name returned status=%v; message=%v", status, resp["message"])
	}
	root, ok := resp["root"].(map[string]any)
	if !ok {
		t.Fatalf("no root node for unambiguous short name; keys=%v", mapKeys(resp))
	}
	if root["name"] != "load_config" {
		t.Errorf("root.name = %v, want load_config", root["name"])
	}
}
