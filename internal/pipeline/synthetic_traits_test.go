package pipeline

import (
	"strings"
	"testing"
)

// TestSyntheticTraits_StdlibCoverage pins the curated registry's stdlib
// coverage. PR #265 baseline sampled 20 common-trait-names; 17 are
// external (not in PSM Interface nodes). All 17 must be in this map.
func TestSyntheticTraits_StdlibCoverage(t *testing.T) {
	requiredStdlibNames := []string{
		"From", "TryFrom", "Display", "Debug", "Iterator",
		"Default", "Clone", "Serialize", "Deserialize", "Error",
		"Future", "AsRef", "AsMut", "Deref", "DerefMut",
		"Drop", "Send", "Sync",
	}
	for _, name := range requiredStdlibNames {
		qn, ok := lookupSyntheticTrait(name)
		if !ok {
			t.Errorf("Required stdlib trait %q missing from syntheticTraits map", name)
			continue
		}
		if !strings.HasPrefix(qn, "_external.") {
			t.Errorf("Synthetic QN for %q must start with `_external.`, got %q", name, qn)
		}
	}
}

// TestSyntheticTraits_ReservedPrefix ensures every synthetic QN uses the
// `_external.` namespace prefix, so they cannot collide with real Rust
// node QNs (which start with project / package names).
func TestSyntheticTraits_ReservedPrefix(t *testing.T) {
	for name, qn := range syntheticTraits {
		if !strings.HasPrefix(qn, "_external.") {
			t.Errorf("trait %q has non-reserved-prefix QN: %q", name, qn)
		}
		if strings.Contains(qn, "::") {
			t.Errorf("trait %q QN contains `::` (should use `.` separators): %q",
				name, qn)
		}
	}
}

// TestSyntheticTraits_NotInRegistryReturnsFalse pins the negative path:
// names not in the curated map return ok=false.
func TestSyntheticTraits_NotInRegistryReturnsFalse(t *testing.T) {
	for _, name := range []string{
		"MyCustomTrait", "FooBar", "definitely_not_a_trait",
		"", "lowercase",
	} {
		qn, ok := lookupSyntheticTrait(name)
		if ok {
			t.Errorf("name %q unexpectedly matched: qn=%q", name, qn)
		}
		if qn != "" {
			t.Errorf("name %q got non-empty qn for ok=false: %q", name, qn)
		}
	}
}

// TestSyntheticTraits_StrictNameMatch ensures we don't accidentally match
// substrings. "FromIterator" must NOT match "From" — they have separate
// entries.
func TestSyntheticTraits_StrictNameMatch(t *testing.T) {
	fromQN, fromOk := lookupSyntheticTrait("From")
	fromIterQN, fromIterOk := lookupSyntheticTrait("FromIterator")
	if !fromOk || !fromIterOk {
		t.Fatal("both From and FromIterator must be in registry")
	}
	if fromQN == fromIterQN {
		t.Errorf("From and FromIterator have IDENTICAL QNs (%q) — names "+
			"must map to distinct synthetic targets", fromQN)
	}
	// A made-up extension must NOT match a real one.
	_, ok := lookupSyntheticTrait("FromXyz")
	if ok {
		t.Error("substring match on `FromXyz` — must be exact-match only")
	}
}
