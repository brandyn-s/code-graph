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

func TestSymbolAndEvidenceRefsCanonicalizeWindowsSeparatorsOnAnyHost(t *testing.T) {
	windowsSymbol := NewSymbolRef(
		"repo",
		"rev",
		`.\src\auth.py`,
		"method",
		"Auth.verify",
		10,
		20,
	)
	posixSymbol := NewSymbolRef(
		"repo",
		"rev",
		"./src/auth.py",
		"method",
		"Auth.verify",
		10,
		20,
	)
	if windowsSymbol.RelativePath != "src/auth.py" {
		t.Fatalf("Windows path not canonical: %q", windowsSymbol.RelativePath)
	}
	if windowsSymbol.ID != posixSymbol.ID {
		t.Fatalf("separator-dependent symbol IDs: %s != %s", windowsSymbol.ID, posixSymbol.ID)
	}

	windowsEvidence := NewEvidenceRef(
		"repo",
		"rev",
		"generation",
		`src\auth.py`,
		10,
		20,
		"semantic_match",
		&windowsSymbol,
	)
	posixEvidence := NewEvidenceRef(
		"repo",
		"rev",
		"generation",
		"src/auth.py",
		10,
		20,
		"semantic_match",
		&posixSymbol,
	)
	if windowsEvidence.RelativePath != "src/auth.py" {
		t.Fatalf("Windows evidence path not canonical: %q", windowsEvidence.RelativePath)
	}
	if windowsEvidence.ID != posixEvidence.ID {
		t.Fatalf("separator-dependent evidence IDs: %s != %s", windowsEvidence.ID, posixEvidence.ID)
	}
}

func TestEvidenceAndObservationCrossEngineVectors(t *testing.T) {
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
	const expectedEvidence = "ev:v1:539e903153197f55fc473f9e63299b0014937be16e183076a34208b11dd95914"
	if evidence.ID != expectedEvidence {
		t.Fatalf("cross-engine evidence id mismatch: got %s want %s", evidence.ID, expectedEvidence)
	}
	observation := NewObservationRef(
		evidence,
		"support",
		"code-search",
		"semantic_match",
		"medium",
	)
	const expectedObservation = "obs:v1:1c84066e266be0053c388febc5141805b30e592d1a741307e922a04240f88c10"
	if observation.ID != expectedObservation {
		t.Fatalf("cross-engine observation id mismatch: got %s want %s", observation.ID, expectedObservation)
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

func TestClaimRefCrossEngineVector(t *testing.T) {
	claim := NewClaimRef(
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"Security_Invariant",
		"All  admin routes\nrequire authorization.",
	)
	const expected = "claim:v1:99154bd6d5d47db3acc040eb14345f914a852105cfc998bc6e171df523c881a5"
	if claim.ID != expected {
		t.Fatalf("cross-engine claim id mismatch: got %s want %s", claim.ID, expected)
	}
	if claim.ClaimText != "All admin routes require authorization." {
		t.Fatalf("claim text not canonical: %q", claim.ClaimText)
	}
	if claim.ClaimKind != "security_invariant" {
		t.Fatalf("claim kind not canonical: %q", claim.ClaimKind)
	}
}
