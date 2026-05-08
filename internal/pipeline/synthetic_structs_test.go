package pipeline

import (
	"strings"
	"testing"
)

// TestSyntheticStructs_StdlibCoverage pins the curated registry's coverage
// of the most common foreign-impl substrate names. Mirror of
// TestSyntheticTraits_StdlibCoverage for the struct side.
func TestSyntheticStructs_StdlibCoverage(t *testing.T) {
	requiredStdlibNames := []string{
		"Vec", "HashMap", "BTreeMap", "HashSet",
		"Box", "Rc", "Arc", "RefCell", "Mutex",
		"Option", "Result", "String", "PathBuf",
		"Duration", "Uuid",
	}
	for _, name := range requiredStdlibNames {
		qn, ok := lookupSyntheticStruct(name)
		if !ok {
			t.Errorf("Required external struct %q missing from syntheticStructs map", name)
			continue
		}
		if !strings.HasPrefix(qn, "_external.") {
			t.Errorf("Synthetic QN for %q must start with `_external.`, got %q", name, qn)
		}
	}
}

// TestSyntheticStructs_ReservedPrefix ensures every synthetic QN uses the
// `_external.` namespace prefix.
func TestSyntheticStructs_ReservedPrefix(t *testing.T) {
	for name, qn := range syntheticStructs {
		if !strings.HasPrefix(qn, "_external.") {
			t.Errorf("struct %q has non-reserved-prefix QN: %q", name, qn)
		}
		if strings.Contains(qn, "::") {
			t.Errorf("struct %q QN contains `::` (should use `.` separators): %q",
				name, qn)
		}
	}
}

// TestSyntheticStructs_NotInRegistryReturnsFalse pins the negative path.
func TestSyntheticStructs_NotInRegistryReturnsFalse(t *testing.T) {
	for _, name := range []string{
		"MyCustomStruct", "FooBar", "definitely_not_a_struct",
		"", "lowercase",
	} {
		qn, ok := lookupSyntheticStruct(name)
		if ok {
			t.Errorf("name %q unexpectedly matched: qn=%q", name, qn)
		}
		if qn != "" {
			t.Errorf("name %q got non-empty qn for ok=false: %q", name, qn)
		}
	}
}
