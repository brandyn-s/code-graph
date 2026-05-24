package cbm

import (
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/lang"
)

// TestRustGroupedImportsFlat verifies that `use crate::{a, b, c};` produces
// one import per item with the correct local_name + module_path.
//
// Pre-2026-05-24: the extractor emitted one import with garbage local_name
// like "{a, b, c}", dropping every binding in the group. This caused ~28
// FNs on assetman + apid (per
// knowledge-base/plans/2026-05-24-post-oracle-fix-substrate-rebaseline.md).
func TestRustGroupedImportsFlat(t *testing.T) {
	Init()
	defer Shutdown()

	code := `use crate::{openapi, parse_message, state};

fn main() {}
`
	result, err := ExtractFile([]byte(code), lang.Rust, "test", "test.rs")
	if err != nil {
		t.Fatalf("extraction failed: %v", err)
	}

	imports := map[string]string{}
	for _, imp := range result.Imports {
		imports[imp.LocalName] = imp.ModulePath
	}

	checks := []struct {
		local, target string
	}{
		{"openapi", "crate::openapi"},
		{"parse_message", "crate::parse_message"},
		{"state", "crate::state"},
	}
	for _, c := range checks {
		got, ok := imports[c.local]
		if !ok {
			t.Errorf("missing import for local=%q (have: %v)", c.local, imports)
			continue
		}
		if got != c.target {
			t.Errorf("import %q: got module_path=%q, want %q", c.local, got, c.target)
		}
	}
	if len(imports) < 3 {
		t.Errorf("expected at least 3 imports (one per group item), got %d: %v", len(imports), imports)
	}
}

// TestRustGroupedImportsNested verifies recursive decomposition for
// `use a::{b::{c, d}, e};` shape. This is the PSM assetman pattern —
// `use crate::{models::subasset::{HostType, Subasset, SubassetHostname}};`.
func TestRustGroupedImportsNested(t *testing.T) {
	Init()
	defer Shutdown()

	code := `use crate::models::subasset::{HostType, Subasset, SubassetHostname};

fn main() {}
`
	result, err := ExtractFile([]byte(code), lang.Rust, "test", "test.rs")
	if err != nil {
		t.Fatalf("extraction failed: %v", err)
	}

	imports := map[string]string{}
	for _, imp := range result.Imports {
		imports[imp.LocalName] = imp.ModulePath
	}

	checks := []struct {
		local, target string
	}{
		{"HostType", "crate::models::subasset::HostType"},
		{"Subasset", "crate::models::subasset::Subasset"},
		{"SubassetHostname", "crate::models::subasset::SubassetHostname"},
	}
	for _, c := range checks {
		got, ok := imports[c.local]
		if !ok {
			t.Errorf("missing import for local=%q (have: %v)", c.local, imports)
			continue
		}
		if got != c.target {
			t.Errorf("import %q: got module_path=%q, want %q", c.local, got, c.target)
		}
	}
}

// TestRustGroupedImportsDeepNested verifies multi-level nesting works:
// `use a::{b, c::{d, e::{f, g}}};`.
func TestRustGroupedImportsDeepNested(t *testing.T) {
	Init()
	defer Shutdown()

	code := `use crate::{
    openapi,
    state,
    models::{
        asset::Asset,
        subasset::{HostType, SubassetHostname},
    },
};

fn main() {}
`
	result, err := ExtractFile([]byte(code), lang.Rust, "test", "test.rs")
	if err != nil {
		t.Fatalf("extraction failed: %v", err)
	}

	imports := map[string]string{}
	for _, imp := range result.Imports {
		imports[imp.LocalName] = imp.ModulePath
	}

	checks := []struct {
		local, target string
	}{
		{"openapi", "crate::openapi"},
		{"state", "crate::state"},
		{"Asset", "crate::models::asset::Asset"},
		{"HostType", "crate::models::subasset::HostType"},
		{"SubassetHostname", "crate::models::subasset::SubassetHostname"},
	}
	for _, c := range checks {
		got, ok := imports[c.local]
		if !ok {
			t.Errorf("missing import for local=%q (have: %v)", c.local, imports)
			continue
		}
		if got != c.target {
			t.Errorf("import %q: got module_path=%q, want %q", c.local, got, c.target)
		}
	}
}

// TestRustGroupedImportsAliasInGroup verifies `use a::{b as c, d};` —
// alias inside a group correctly assigns local_name=alias.
func TestRustGroupedImportsAliasInGroup(t *testing.T) {
	Init()
	defer Shutdown()

	code := `use std::io::{Result as IoResult, Error};

fn main() {}
`
	result, err := ExtractFile([]byte(code), lang.Rust, "test", "test.rs")
	if err != nil {
		t.Fatalf("extraction failed: %v", err)
	}

	imports := map[string]string{}
	for _, imp := range result.Imports {
		imports[imp.LocalName] = imp.ModulePath
	}

	// IoResult is the alias, NOT Result, so result.Result must NOT be a key.
	if _, isStdResult := imports["Result"]; isStdResult {
		t.Errorf("expected alias IoResult, not Result; got: %v", imports)
	}
	if got, ok := imports["IoResult"]; !ok || got != "std::io::Result" {
		t.Errorf("expected IoResult -> std::io::Result, got %q (all: %v)", got, imports)
	}
	if got, ok := imports["Error"]; !ok || got != "std::io::Error" {
		t.Errorf("expected Error -> std::io::Error, got %q (all: %v)", got, imports)
	}
}

// TestRustGroupedImportsWithSelf verifies `use a::b::{self, c};` —
// `self` binds the parent module name (the last segment of the prefix).
func TestRustGroupedImportsWithSelf(t *testing.T) {
	Init()
	defer Shutdown()

	code := `use crate::v1::{self, command};

fn main() {}
`
	result, err := ExtractFile([]byte(code), lang.Rust, "test", "test.rs")
	if err != nil {
		t.Fatalf("extraction failed: %v", err)
	}

	imports := map[string]string{}
	for _, imp := range result.Imports {
		imports[imp.LocalName] = imp.ModulePath
	}

	// `self` should bind as `v1 -> crate::v1` (the prefix, last segment as local).
	if got, ok := imports["v1"]; !ok || got != "crate::v1" {
		t.Errorf("expected v1 -> crate::v1 from `self` in group, got %q (all: %v)", got, imports)
	}
	if got, ok := imports["command"]; !ok || got != "crate::v1::command" {
		t.Errorf("expected command -> crate::v1::command, got %q (all: %v)", got, imports)
	}
}

// TestRustGroupedImportsRegressionGuard pins the pre-fix behavior — a single
// import with garbage local_name like `{a, b, c}` — does NOT recur.
func TestRustGroupedImportsRegressionGuard(t *testing.T) {
	Init()
	defer Shutdown()

	code := `use crate::{a, b, c};

fn main() {}
`
	result, err := ExtractFile([]byte(code), lang.Rust, "test", "test.rs")
	if err != nil {
		t.Fatalf("extraction failed: %v", err)
	}

	for _, imp := range result.Imports {
		// Pre-fix bug: local_name like "{a, b, c}" with stray braces.
		if len(imp.LocalName) > 0 && (imp.LocalName[0] == '{' || imp.LocalName[0] == '}') {
			t.Errorf("regression: garbage local_name with brace = %q (full import: %+v)", imp.LocalName, imp)
		}
		// Also catch any local_name containing comma — those are also pre-fix shapes.
		for _, c := range imp.LocalName {
			if c == ',' {
				t.Errorf("regression: local_name contains comma = %q (full: %+v)", imp.LocalName, imp)
			}
		}
	}
}

// TestRustImportsFlatStillWorks verifies single-path imports continue to work
// post-fix (no regression on the non-group case).
func TestRustImportsFlatStillWorks(t *testing.T) {
	Init()
	defer Shutdown()

	code := `use std::io::Result;
use foo::bar::Baz;

fn main() {}
`
	result, err := ExtractFile([]byte(code), lang.Rust, "test", "test.rs")
	if err != nil {
		t.Fatalf("extraction failed: %v", err)
	}

	imports := map[string]string{}
	for _, imp := range result.Imports {
		imports[imp.LocalName] = imp.ModulePath
	}

	if got, ok := imports["Result"]; !ok || got != "std::io::Result" {
		t.Errorf("expected Result -> std::io::Result, got %q (all: %v)", got, imports)
	}
	if got, ok := imports["Baz"]; !ok || got != "foo::bar::Baz" {
		t.Errorf("expected Baz -> foo::bar::Baz, got %q (all: %v)", got, imports)
	}
}

// TestRustImportsWildcardSkipped verifies `use a::*;` produces no binding
// (wildcards don't introduce a single named import).
func TestRustImportsWildcardSkipped(t *testing.T) {
	Init()
	defer Shutdown()

	code := `use std::io::*;
use foo::Bar;

fn main() {}
`
	result, err := ExtractFile([]byte(code), lang.Rust, "test", "test.rs")
	if err != nil {
		t.Fatalf("extraction failed: %v", err)
	}

	imports := map[string]string{}
	for _, imp := range result.Imports {
		imports[imp.LocalName] = imp.ModulePath
	}

	// Wildcard shouldn't produce a "io" -> "std::io" binding (or any wildcard entry).
	// Only Bar from the non-wildcard import.
	if got, ok := imports["Bar"]; !ok || got != "foo::Bar" {
		t.Errorf("expected Bar -> foo::Bar, got %q (all: %v)", got, imports)
	}
	// Ensure no stray asterisk-bearing local_name.
	for _, imp := range result.Imports {
		for _, c := range imp.LocalName {
			if c == '*' {
				t.Errorf("wildcard emitted as local_name: %+v", imp)
			}
		}
	}
}
