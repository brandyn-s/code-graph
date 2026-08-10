package evidence

import "testing"

func TestSymbolRefCrossEngineVector(t *testing.T) {
	ref := NewSymbolRef(
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"./src/auth.py",
		"Method",
		"Auth.verify",
		10,
		20,
	)
	const expected = "sym:v1:228a411de5eb1f52b61bd1abb05f9b7b4d20680cd35163f3e8934a47ce6952a0"
	if ref.ID != expected {
		t.Fatalf("cross-engine symbol id mismatch: got %s want %s", ref.ID, expected)
	}
	if ref.RelativePath != "src/auth.py" {
		t.Fatalf("path not canonical: %q", ref.RelativePath)
	}
	if ref.SymbolKind != "method" {
		t.Fatalf("kind not canonical: %q", ref.SymbolKind)
	}
}

func TestEvidenceRefCrossEngineVector(t *testing.T) {
	symbol := NewSymbolRef(
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"src/auth.py",
		"method",
		"Auth.verify",
		10,
		20,
	)
	evidence := NewEvidenceRef(
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		"src/auth.py",
		10,
		20,
		"hybrid_match",
		&symbol,
	)
	const expected = "ev:v1:539e903153197f55fc473f9e63299b0014937be16e183076a34208b11dd95914"
	if evidence.ID != expected {
		t.Fatalf("cross-engine evidence id mismatch: got %s want %s", evidence.ID, expected)
	}
}

func TestEvidenceRefChangesWithGeneration(t *testing.T) {
	symbol := NewSymbolRef("repo", "rev", "src/auth.py", "method", "Auth.verify", 10, 20)
	first := NewEvidenceRef("repo", "rev", "generation-a", "src/auth.py", 10, 20, "semantic_match", &symbol)
	second := NewEvidenceRef("repo", "rev", "generation-b", "src/auth.py", 10, 20, "semantic_match", &symbol)
	if first.ID == second.ID {
		t.Fatal("evidence id must be bound to index generation")
	}
}
