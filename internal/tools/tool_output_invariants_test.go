package tools

// End-to-end tool-output invariants.
//
// This test indexes a real fixture repo through the full pipeline, then
// calls the MCP tool handlers and asserts output-quality invariants that
// unit tests miss because the bugs live in the SEAMS between the pipeline,
// the store, and the tool layer. Each invariant corresponds to a defect
// found live 2026-07-04 by running the tools and reading their output
// (PRs #396/#398/#401/#402/#403). The per-fix unit tests pin each bug in
// isolation; this test is the cross-cutting guard that catches the NEXT
// bug of the same shape — a capability/filter/fix that exists in one code
// path but not its sibling.
//
// Keyless invariants (#401/#402/#403) run in every CI. Embedding-dependent
// invariants (#396/#398) run only when VOYAGE_API_KEY is set and skip
// cleanly otherwise; their logic is also pinned keyless by the pipeline
// package's pass_embeddings_test.go.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brandyn-s/code-graph/internal/pipeline"
	"github.com/brandyn-s/code-graph/internal/store"
)

// invariantFixtureMarker is a distinctive token embedded in the fixture
// project's PATH via a dot ("proj.<marker>"). Project names are derived
// from the path and preserve dots, so a correct QN parser strips the whole
// project prefix and this token NEVER appears in a package name; the #401
// bug (splitting the project name on its dot) leaked it into every package.
const invariantFixtureMarker = "invariantmarker"

// writeInvariantFixture writes a small multi-file Go repo whose PATH
// contains a dot (reproducing #401's trigger on any host), whose sources
// call external stdlib functions (producing empty-file_path CALLS_EXTERNAL
// stubs — including os.WriteFile, which the security tagger marks a
// sensitive_sink, for #402) and form a call graph dense enough for Louvain
// to emit Community pseudo-nodes (also #402), plus one >1MB generated file
// skipped by the pipeline (so #403's missing_from_index check is
// non-vacuous). Go is deliberate: the codebase's own language, where
// CALLS_EXTERNAL stubs and security tags reliably form (a Python fixture
// produced zero external stubs — the #402 assertion would have been
// vacuous). Returns the absolute repo path and its canonical project name.
func writeInvariantFixture(t *testing.T) (repoAbs, projectName string) {
	t.Helper()
	// MkdirTemp under the package dir (not t.TempDir → /var, which the
	// forbidden-index-path check rejects). The "proj.<marker>" prefix puts a
	// literal dot in the path so the derived project name is dotted.
	repo, err := os.MkdirTemp(".", "proj."+invariantFixtureMarker+"-")
	if err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(repo) })

	mustWrite := func(rel, content string) {
		p := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	mustWrite("go.mod", "module invariantfixture\n\ngo 1.24\n")

	// Nested package under svc/ → QN "<proj>.svc.<...>.HandleRequest", so the
	// package is a real path segment ("svc"). os.WriteFile / os.Getpid /
	// exec.Command are external → empty-file_path CALLS_EXTERNAL stubs;
	// os.WriteFile is name-matched as a sensitive_sink. The functions
	// cross-call to give Louvain a cluster to detect.
	mustWrite("svc/handler.go", `package svc

import (
	"os"
	"os/exec"
)

func HandleRequest(cmd string) error {
	if !Validate(cmd) {
		return nil
	}
	Audit(cmd)
	return os.WriteFile("/tmp/out", []byte(cmd), 0o600)
}

func Validate(cmd string) bool {
	return len(cmd) > 0 && Normalize(cmd) != ""
}

func Normalize(cmd string) string {
	return cmd
}

func Audit(cmd string) {
	_ = os.Getpid()
	Record(cmd)
}

func Record(cmd string) {
	_ = cmd
}

func Run(cmd string) error {
	return exec.Command("sh", "-c", cmd).Run()
}
`)
	mustWrite("app/main.go", `package main

import "invariantfixture/svc"

func dispatch(cmd string) error {
	return svc.HandleRequest(cmd)
}

func runAll(cmds []string) {
	for _, c := range cmds {
		_ = dispatch(c)
		_ = svc.Run(c)
	}
}

func main() {
	runAll([]string{"ls", "pwd"})
}
`)

	// One >1MB generated file: the pipeline skips it (full-mode 1MB cutoff),
	// so a correct index_health excludes it from "missing"; the pre-#403 code
	// discovered with no limit and reported it as missing_from_index. Content
	// need not compile — it is size-skipped before tree-sitter ever parses it.
	big := "package gen\n\nconst Data = \"" + strings.Repeat("x", 1_100_000) + "\"\n"
	mustWrite("gen/generated_big.go", big)

	repoAbs, err = filepath.Abs(repo)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return repoAbs, pipeline.ProjectNameFromPath(repoAbs)
}

// TestToolOutputInvariants indexes a fixture and asserts the keyless
// output-quality invariants (#401/#402/#403) that the live-2026-07-04 bugs
// violated. Runs in every CI (no API key required).
func TestToolOutputInvariants(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)
	srv := NewServer(router)
	repo, proj := writeInvariantFixture(t)

	idxResp := metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository",
		map[string]any{"repo_path": repo, "skip_report": true})
	if nodes, _ := idxResp["nodes"].(float64); nodes <= 0 {
		t.Fatalf("index produced no nodes (resp=%v)", idxResp)
	}

	// ---- #401: explain_symbol package must not carry the project prefix ----
	// The dotted project path would leak the marker into the package if the
	// QN parser split on "." instead of stripping the known prefix. Probe
	// Validate (internal-only — HandleRequest also exists as a cross-package
	// external stub, and explain_symbol resolves the ambiguous name to the
	// stub, whose empty-QN yields an empty package that would make this check
	// vacuous). Validate resolves to the real node: QN
	// "<proj>.svc.handler.Validate", package "svc.handler".
	exp := metadataResponseFromHandler(t, srv.handleExplainSymbol, "explain_symbol",
		map[string]any{"name": "Validate", "project": proj})
	pkg, _ := exp["package"].(string)
	if pkg == "" {
		t.Fatalf("#401: explain_symbol returned an empty package for Validate — check is vacuous, fixture/resolution changed")
	}
	if strings.Contains(pkg, invariantFixtureMarker) {
		t.Errorf("#401 regression: explain_symbol package %q contains the project-path marker %q — QN parser is not stripping the dotted project prefix",
			pkg, invariantFixtureMarker)
	}
	if !strings.HasPrefix(pkg, "svc") {
		t.Errorf("#401: expected a clean 'svc'-rooted package for svc/handler.go, got %q", pkg)
	}

	// ---- #402: no result surface may include Community pseudo-nodes or ----
	// ----       external stubs (empty file_path).                        ----
	assertNoUnsurfaceable := func(tool string, matches []any) {
		for _, m := range matches {
			e, ok := m.(map[string]any)
			if !ok {
				continue
			}
			label, _ := e["label"].(string)
			fp, _ := e["file_path"].(string)
			if label == "Community" {
				t.Errorf("#402 regression: %s returned a Community pseudo-node (%v)", tool, e["qualified_name"])
			}
			if fp == "" {
				t.Errorf("#402 regression: %s returned a node with empty file_path (%v) — external stub not excluded", tool, e["qualified_name"])
			}
		}
	}

	loc := metadataResponseFromHandler(t, srv.handleCodeLocalize, "code_localize",
		map[string]any{"issue_description": "handle request validate run", "project": proj, "seed_strategy": "substring", "top_k": 25})
	assertNoUnsurfaceable("code_localize", toSlice(loc["matches"]))

	rank := metadataResponseFromHandler(t, srv.handleRankByQuery, "rank_by_query",
		map[string]any{"query": "handle request validate run", "project": proj, "top_k": 25})
	assertNoUnsurfaceable("rank_by_query", toSlice(rank["matches"]))

	// query_security_surfaces groups entries under a role→[]entry map.
	sec := metadataResponseFromHandler(t, srv.handleQuerySecuritySurfaces, "query_security_surfaces",
		map[string]any{"project": proj, "mode": "surfaces"})
	if surfaces, ok := sec["surfaces"].(map[string]any); ok {
		for role, v := range surfaces {
			assertNoUnsurfaceable("query_security_surfaces["+role+"]", toSlice(v))
		}
	}

	// ---- #403: a fresh full index has no "missing" files; the >1MB file ----
	// ----       is skipped by BOTH the pipeline and (fixed) health.      ----
	health := metadataResponseFromHandler(t, srv.handleIndexHealth, "index_health",
		map[string]any{"project": proj})
	if missing, ok := health["missing_from_index"].(float64); ok && missing != 0 {
		files, _ := health["missing_files"].([]any)
		t.Errorf("#403 regression: index_health reports missing_from_index=%v on a fresh index (files=%v) — discovery cutoff not aligned with the pipeline's",
			missing, files)
	}
}

// TestToolOutputInvariantsSemantic asserts the embedding-dependent
// invariants (#398 provenance model, #396 embeddings survive an incremental
// reindex). Requires VOYAGE_API_KEY; skips cleanly without it. The #396
// backfill logic is also pinned keyless in pipeline/pass_embeddings_test.go.
func TestToolOutputInvariantsSemantic(t *testing.T) {
	if os.Getenv("VOYAGE_API_KEY") == "" {
		t.Skip("VOYAGE_API_KEY not set — semantic invariants (#398/#396) require live embeddings")
	}
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)
	srv := NewServer(router)
	repo, proj := writeInvariantFixture(t)

	metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository",
		map[string]any{"repo_path": repo, "skip_report": true})

	// ---- #398: provenance.model must be the model actually used, not a ----
	// ----       hardcoded "voyage-4-large".                              ----
	expectModel := os.Getenv("VOYAGE_EMBED_MODEL")
	if expectModel == "" {
		expectModel = "voyage-code-3" // client default (voyage_client.go)
	}
	sem := metadataResponseFromHandler(t, srv.handleSearchCodeSemantic, "search_code_semantic",
		map[string]any{"query": "handle an incoming request", "project": proj, "limit": 5})
	if md, ok := sem["_metadata"].(map[string]any); ok {
		if prov, ok := md["provenance"].(map[string]any); ok {
			if got, _ := prov["model"].(string); got != expectModel {
				t.Errorf("#398 regression: search_code_semantic provenance.model = %q, want %q (the model actually used)", got, expectModel)
			}
		}
	}

	// ---- #396: embeddings must survive an incremental reindex ----
	embCount := func() int {
		st, release, err := router.AcquireStore(proj)
		if err != nil {
			t.Fatalf("acquire store: %v", err)
		}
		defer release()
		n, err := st.EmbeddingCount(proj)
		if err != nil {
			t.Fatalf("embedding count: %v", err)
		}
		return n
	}
	if embCount() == 0 {
		t.Fatalf("#396 precondition: full index produced no embeddings")
	}
	// Change a source file, then reindex (takes the incremental path).
	if err := os.WriteFile(filepath.Join(repo, "svc", "handler.py"),
		[]byte("import os\n\n\ndef handle_request(cmd):\n    return os.system(cmd)\n\n\ndef new_helper():\n    return 42\n"), 0o600); err != nil {
		t.Fatalf("modify fixture: %v", err)
	}
	metadataResponseFromHandler(t, srv.handleIndexRepository, "index_repository",
		map[string]any{"repo_path": repo, "skip_report": true})
	if got := embCount(); got == 0 {
		t.Error("#396 regression: incremental reindex left the project with zero embeddings (cascade-delete not backfilled)")
	}
}

// toSlice coerces a decoded JSON array (or nil) to []any.
func toSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
