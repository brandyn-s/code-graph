package fqn

import "testing"

// TestCompute_GoIndexFile is the regression test for the CBM Method QN bug:
// `internal/tools/index.go`'s single Method (Server.handleIndexRepository)
// was being stored without the `.index.` file segment because the JS/TS
// `index`-strip rule fired unconditionally regardless of file extension.
// After the fix, only JS/TS-family extensions trigger the strip; Go's
// `index.go` keeps the `index` segment.
func TestCompute_GoIndexFile(t *testing.T) {
	got := Compute("proj", "internal/tools/index.go", "Server.handleIndexRepository")
	want := "proj.internal.tools.index.Server.handleIndexRepository"
	if got != want {
		t.Errorf("Compute Go index.go method:\n  got:  %s\n  want: %s", got, want)
	}
}

// TestCompute_JsIndexFile pins the original behavior: JS/TS `index.js`
// files DO get their `index` segment stripped (the rule's intended scope).
func TestCompute_JsIndexFile(t *testing.T) {
	got := Compute("proj", "src/utils/index.js", "helper")
	want := "proj.src.utils.helper"
	if got != want {
		t.Errorf("Compute JS index.js:\n  got:  %s\n  want: %s", got, want)
	}
}

// TestCompute_TsIndexFile likewise for TypeScript.
func TestCompute_TsIndexFile(t *testing.T) {
	got := Compute("proj", "src/utils/index.ts", "helper")
	want := "proj.src.utils.helper"
	if got != want {
		t.Errorf("Compute TS index.ts:\n  got:  %s\n  want: %s", got, want)
	}
}

// TestCompute_PythonInit pins __init__ stripping to Python only.
func TestCompute_PythonInit(t *testing.T) {
	got := Compute("proj", "pkg/__init__.py", "helper")
	want := "proj.pkg.helper"
	if got != want {
		t.Errorf("Compute Python __init__.py:\n  got:  %s\n  want: %s", got, want)
	}
}

// TestCompute_GoInitNotStripped — hypothetical `__init__.go` retains the
// `__init__` segment because __init__ stripping is Python-only.
func TestCompute_GoInitNotStripped(t *testing.T) {
	got := Compute("proj", "pkg/__init__.go", "helper")
	want := "proj.pkg.__init__.helper"
	if got != want {
		t.Errorf("Compute Go __init__.go:\n  got:  %s\n  want: %s", got, want)
	}
}

// TestCompute_RustIndexFile — Rust `index.rs` keeps the `index` segment.
func TestCompute_RustIndexFile(t *testing.T) {
	got := Compute("proj", "src/index.rs", "helper")
	want := "proj.src.index.helper"
	if got != want {
		t.Errorf("Compute Rust index.rs:\n  got:  %s\n  want: %s", got, want)
	}
}

// TestModuleQN_GoIndexFile — Module QN for index.go keeps the segment too.
func TestModuleQN_GoIndexFile(t *testing.T) {
	got := ModuleQN("proj", "internal/tools/index.go")
	want := "proj.internal.tools.index"
	if got != want {
		t.Errorf("ModuleQN Go index.go:\n  got:  %s\n  want: %s", got, want)
	}
}

// TestModuleQN_JsIndexFile — JS index.js Module QN drops the index segment.
func TestModuleQN_JsIndexFile(t *testing.T) {
	got := ModuleQN("proj", "src/utils/index.js")
	want := "proj.src.utils"
	if got != want {
		t.Errorf("ModuleQN JS index.js:\n  got:  %s\n  want: %s", got, want)
	}
}
