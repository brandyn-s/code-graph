package cbm

import (
	"testing"

	"github.com/brandyn-s/code-graph/internal/lang"
)

// Upstream 47116b8e / cb7cb444: Go struct fields live one level below the
// struct_type body and must be extracted; the blank identifier `_` (padding
// fields in generated code) must not become a node.
func TestGoStructFieldsAreExtractedAndBlankIdentifierIsSkipped(t *testing.T) {
	src := `package p

type Config struct {
	Name    string
	Retries int
	_       [4]byte
	inner   *Config
}

type Runner interface {
	Run() error
}
`
	res, err := ExtractFile([]byte(src), lang.Go, "sf", "config.go")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	seen := map[string]string{}
	for _, d := range res.Definitions {
		seen[d.Name] = d.Label
	}
	for _, want := range []string{"Name", "Retries", "inner"} {
		if _, ok := seen[want]; !ok {
			t.Errorf("struct field %q not extracted; got %v", want, seen)
		}
	}
	if label, ok := seen["_"]; ok {
		t.Errorf("blank identifier field was extracted as %s", label)
	}
	if _, ok := seen["Run"]; !ok {
		t.Errorf("interface method Run not extracted; got %v", seen)
	}
}
