package cbm

import (
	"strings"
	"testing"

	"github.com/brandyn-s/code-graph/internal/lang"
)

// Regression inputs ported in spirit from upstream codebase-memory-mcp
// 174e56b4 (#668: >4096 top-level definitions silently dropped), 40f2722d
// (fixed-size traversal stacks) and da046da5 (GLR stack-merge recursion).
// Our walkers were recursive, so the failure mode here was a C-stack overflow
// rather than truncation; the fix is an iterative heap stack for walk_defs and
// a bounded depth guard for every other recursive walker.

func countLabel(defs []Definition, label string) int {
	n := 0
	for _, d := range defs {
		if d.Label == label {
			n++
		}
	}
	return n
}

func TestManyTopLevelDefinitionsAreAllExtracted(t *testing.T) {
	const want = 5000
	var b strings.Builder
	for i := 0; i < want; i++ {
		b.WriteString("def f_")
		b.WriteString(itoa(i))
		b.WriteString("():\n    return ")
		b.WriteString(itoa(i))
		b.WriteString("\n\n")
	}
	res, err := ExtractFile([]byte(b.String()), lang.Python, "deep", "many.py")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if got := countLabel(res.Definitions, "Function"); got != want {
		t.Fatalf("functions extracted = %d, want %d (definitions must not be truncated)", got, want)
	}
	if res.DepthCapped {
		t.Fatal("a wide, shallow file must not be reported as depth-capped")
	}
}

func TestDeeplyNestedExpressionsDoNotCrash(t *testing.T) {
	cases := []struct {
		name  string
		lang  lang.Language
		file  string
		src   string
		capOK bool // depth guard may legitimately trigger
	}{
		{
			name:  "js nested calls",
			lang:  lang.JavaScript,
			file:  "deep.js",
			src:   "const x = " + strings.Repeat("f(", 20000) + "1" + strings.Repeat(")", 20000) + ";\n",
			capOK: true,
		},
		{
			name:  "js nested arrays",
			lang:  lang.JavaScript,
			file:  "arrays.js",
			src:   "const a = " + strings.Repeat("[", 6000) + "0" + strings.Repeat("]", 6000) + ";\n",
			capOK: true,
		},
		{
			name:  "python nested parens",
			lang:  lang.Python,
			file:  "deep.py",
			src:   "x = " + strings.Repeat("(", 3000) + "1" + strings.Repeat(")", 3000) + "\n",
			capOK: true,
		},
		{
			name:  "go nested blocks",
			lang:  lang.Go,
			file:  "blocks.go",
			src:   "package p\n\nfunc f() {\n" + strings.Repeat("{\n", 5000) + strings.Repeat("}\n", 5000) + "}\n",
			capOK: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A parse timeout is an acceptable outcome; a crash is not. The test
			// process surviving is the assertion.
			res, err := ExtractFile([]byte(tc.src), tc.lang, "deep", tc.file)
			if err != nil {
				t.Logf("extract returned error (acceptable for pathological input): %v", err)
				return
			}
			if res.DepthCapped && !tc.capOK {
				t.Fatalf("unexpected depth cap for %s", tc.name)
			}
		})
	}
}

func TestDepthGuardFlagsOnlyPathologicalFiles(t *testing.T) {
	normal := "def f(a):\n    return [x for x in a if x]\n\nclass C:\n    def m(self):\n        return f([1, 2])\n"
	res, err := ExtractFile([]byte(normal), lang.Python, "deep", "normal.py")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if res.DepthCapped {
		t.Fatal("normal file reported depth-capped")
	}

	// 6000 nested array literals exceed the default 4096-frame ceiling for the
	// recursive walkers (calls, usages, type refs); the file must still yield a
	// result and carry the flag so the pipeline can log it.
	deep := "const a = " + strings.Repeat("[", 6000) + "0" + strings.Repeat("]", 6000) + ";\n"
	res, err = ExtractFile([]byte(deep), lang.JavaScript, "deep", "arrays.js")
	if err != nil {
		t.Skipf("parser refused pathological input before walkers ran: %v", err)
	}
	if !res.DepthCapped {
		t.Fatal("6000-deep nesting should trip the depth guard and set DepthCapped")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
