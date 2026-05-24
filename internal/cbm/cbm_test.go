package cbm

import (
	"fmt"
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/lang"
)

func TestPythonDocstring(t *testing.T) {
	source := []byte("def compute(x, y):\n    \"\"\"Compute the sum of x and y.\"\"\"\n    return x + y\n")
	result, err := ExtractFile(source, lang.Python, "test", "test.py")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("Defs: %d\n", len(result.Definitions))
	for _, d := range result.Definitions {
		fmt.Printf("  Name=%q Label=%q QN=%q Doc=%q Sig=%q\n", d.Name, d.Label, d.QualifiedName, d.Docstring, d.Signature)
	}
	if len(result.Definitions) == 0 {
		t.Fatal("no definitions extracted")
	}
	found := false
	for _, d := range result.Definitions {
		if d.Name == "compute" {
			found = true
			if d.Docstring == "" {
				t.Error("docstring is empty for compute")
			}
			t.Logf("docstring: %q", d.Docstring)
		}
	}
	if !found {
		t.Error("compute function not found")
	}
}

func TestGoFunctionExtraction(t *testing.T) {
	source := []byte(`package main

// Greet returns a greeting.
func Greet(name string) string {
	return "Hello, " + name
}

func main() {
	Greet("world")
}
`)
	result, err := ExtractFile(source, lang.Go, "test", "main.go")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("Defs: %d, Calls: %d, Imports: %d\n", len(result.Definitions), len(result.Calls), len(result.Imports))
	for _, d := range result.Definitions {
		fmt.Printf("  Name=%q Label=%q QN=%q Sig=%q Doc=%q\n", d.Name, d.Label, d.QualifiedName, d.Signature, d.Docstring)
	}
	for _, c := range result.Calls {
		fmt.Printf("  Call: callee=%q enclosing=%q\n", c.CalleeName, c.EnclosingFuncQN)
	}
}

func TestPythonFastAPIDependsTracked(t *testing.T) {
	source := []byte(`
from fastapi import Depends

def get_db():
    return Database()

def get_current_user(db = Depends(get_db)):
    return db.get_user()

@app.get("/items")
def list_items(user = Depends(get_current_user)):
    return items
`)
	result, err := ExtractFile(source, lang.Python, "test", "app/routes.py")
	if err != nil {
		t.Fatal(err)
	}

	// Verify Depends() arguments appear as calls
	dependsCalls := map[string]bool{}
	for _, c := range result.Calls {
		t.Logf("Call: callee=%q enclosing=%q", c.CalleeName, c.EnclosingFuncQN)
		if c.CalleeName == "get_db" || c.CalleeName == "get_current_user" {
			dependsCalls[c.CalleeName] = true
		}
	}
	if !dependsCalls["get_db"] {
		t.Errorf("get_db not found in calls via Depends(). All calls: %v", result.Calls)
	}
	if !dependsCalls["get_current_user"] {
		t.Errorf("get_current_user not found in calls via Depends(). All calls: %v", result.Calls)
	}
}

// TestPythonExecutorSubmitTracked pins INDIRECT_CALLS v0.1: when a
// caller invokes <pool>.submit(fn, ...), the Python extractor emits
// fn as a call target alongside the .submit call itself. Closes the
// largest source of confidence_band: "speculative" traces on Python
// codebases — concurrent.futures dispatch sites that previously
// produced 0 CALLS edges from the calling function to fn.
func TestPythonExecutorSubmitTracked(t *testing.T) {
	source := []byte(`
from concurrent.futures import ThreadPoolExecutor

def background_task():
    return 42

def another_task():
    return "ok"

def run_jobs():
    with ThreadPoolExecutor(max_workers=4) as executor:
        f1 = executor.submit(background_task)
        f2 = executor.submit(another_task, "arg")
        return f1.result() + f2.result()
`)
	result, err := ExtractFile(source, lang.Python, "test", "app/jobs.py")
	if err != nil {
		t.Fatal(err)
	}

	submitTargets := map[string]bool{}
	for _, c := range result.Calls {
		t.Logf("Call: callee=%q enclosing=%q", c.CalleeName, c.EnclosingFuncQN)
		if c.CalleeName == "background_task" || c.CalleeName == "another_task" {
			submitTargets[c.CalleeName] = true
		}
	}
	if !submitTargets["background_task"] {
		t.Errorf("background_task not found in calls via executor.submit(). All calls: %v", result.Calls)
	}
	if !submitTargets["another_task"] {
		t.Errorf("another_task not found in calls via executor.submit(). All calls: %v", result.Calls)
	}
}

// TestPythonNonSubmitDoesNotEmitFirstArg pins the negative case: a
// callee NOT ending in ".submit" should not have its first arg
// emitted as a separate call. Catches over-broad pattern matching.
func TestPythonNonSubmitDoesNotEmitFirstArg(t *testing.T) {
	source := []byte(`
def helper():
    return "h"

def other():
    return "o"

def run():
    foo = some_function(helper, other)
    return foo
`)
	result, err := ExtractFile(source, lang.Python, "test", "app/non_submit.py")
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range result.Calls {
		// some_function should appear, but helper/other should NOT
		// be emitted as separate calls (they're being passed as args
		// to a non-submit, non-Depends function).
		if c.CalleeName == "helper" || c.CalleeName == "other" {
			t.Errorf("first arg %q was incorrectly emitted as a call when callee is %q (expected only via .submit/Depends pattern). All calls: %v",
				c.CalleeName, "some_function", result.Calls)
		}
	}
}

// TestPythonGetattrDispatchTracked pins INDIRECT_CALLS v0.2: when a
// caller invokes getattr(obj, "method")(...), the Python extractor
// emits "method" as a call target. Common in plugin/registry dispatch.
// Variable-name dispatch (getattr(obj, name_var)) is NOT handled —
// extracting the variable name would emit phantom edges. v0.2 only
// handles string-literal method names (precise extraction).
func TestPythonGetattrDispatchTracked(t *testing.T) {
	source := []byte(`
class Plugin:
    def handler_a(self):
        return "a"
    def handler_b(self):
        return "b"

def dispatch(plugin, action):
    return getattr(plugin, "handler_a")()

def variable_dispatch(plugin, name):
    return getattr(plugin, name)()
`)
	result, err := ExtractFile(source, lang.Python, "test", "app/dispatch.py")
	if err != nil {
		t.Fatal(err)
	}

	gotHandlerA := false
	for _, c := range result.Calls {
		t.Logf("Call: callee=%q enclosing=%q", c.CalleeName, c.EnclosingFuncQN)
		if c.CalleeName == "handler_a" && c.EnclosingFuncQN == "test.app.dispatch.dispatch" {
			gotHandlerA = true
		}
		// Negative: variable "name" should NOT be emitted as a call.
		if c.CalleeName == "name" && c.EnclosingFuncQN == "test.app.dispatch.variable_dispatch" {
			t.Errorf("variable 'name' should not be emitted as a call target. All calls: %v", result.Calls)
		}
	}
	if !gotHandlerA {
		t.Errorf("handler_a not found via getattr() dispatch. All calls: %v", result.Calls)
	}
}

func TestJSArrowFunction(t *testing.T) {
	source := []byte(`const greet = (name) => {
  return "Hello " + name;
};

const result = greet("world");
`)
	result, err := ExtractFile(source, lang.JavaScript, "test", "app.js")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("Defs: %d\n", len(result.Definitions))
	for _, d := range result.Definitions {
		fmt.Printf("  Name=%q Label=%q QN=%q\n", d.Name, d.Label, d.QualifiedName)
	}
}

// TestPythonExecutorSubmitDispatchKind verifies that calls synthesized
// from the .submit(fn) pattern carry DispatchKind="executor_submit",
// while direct calls (the .submit invocation itself, regular function
// calls) carry no dispatch_kind. This is the metadata the pipeline
// reads to label INDIRECT_CALLS edges in trace_call_path.
func TestPythonExecutorSubmitDispatchKind(t *testing.T) {
	source := []byte(`
from concurrent.futures import ThreadPoolExecutor

def bg():
    return 1

def run():
    with ThreadPoolExecutor() as executor:
        executor.submit(bg)
`)
	result, err := ExtractFile(source, lang.Python, "test", "app/d.py")
	if err != nil {
		t.Fatal(err)
	}

	var bgCall, submitCall *Call
	for i := range result.Calls {
		c := &result.Calls[i]
		switch c.CalleeName {
		case "bg":
			bgCall = c
		case "executor.submit":
			submitCall = c
		}
	}
	if bgCall == nil {
		t.Fatalf("expected synthesized bg call, got %v", result.Calls)
	}
	if bgCall.DispatchKind != "executor_submit" {
		t.Errorf("bg call: expected DispatchKind=executor_submit, got %q", bgCall.DispatchKind)
	}
	if submitCall == nil {
		t.Fatalf("expected direct executor.submit call entry")
	}
	if submitCall.DispatchKind != "" {
		t.Errorf("direct executor.submit call: expected empty DispatchKind, got %q", submitCall.DispatchKind)
	}
}

// TestPythonDependsDispatchKind verifies the Depends() variant.
func TestPythonDependsDispatchKind(t *testing.T) {
	source := []byte(`
def get_db(): pass

def handler(db = Depends(get_db)):
    pass
`)
	result, err := ExtractFile(source, lang.Python, "test", "app/dep.py")
	if err != nil {
		t.Fatal(err)
	}

	var getDB, depends *Call
	for i := range result.Calls {
		c := &result.Calls[i]
		switch c.CalleeName {
		case "get_db":
			getDB = c
		case "Depends":
			depends = c
		}
	}
	if getDB == nil {
		t.Fatalf("expected synthesized get_db call from Depends pattern, calls=%v", result.Calls)
	}
	if getDB.DispatchKind != "depends" {
		t.Errorf("get_db call: expected DispatchKind=depends, got %q", getDB.DispatchKind)
	}
	if depends != nil && depends.DispatchKind != "" {
		t.Errorf("direct Depends call: expected empty DispatchKind, got %q", depends.DispatchKind)
	}
}

// TestPythonBeforeRequestRegisterTracked pins INDIRECT_CALLS v0.3
// Pattern A: when a caller invokes app.before_request(fn) (or any
// of the seven Flask hook-registrar methods), the extractor emits
// fn as a synthesized call with dispatch_kind set to the per-
// registrar label. Closes the flask-tiny F004 baseline gap.
//
// The function reference at the registration site is a deterministic
// Name; only the dispatch is indirect (Flask invokes from a
// registered-hook list at request time). Without this emission, the
// registered function has 0 inbound CALLS edges and trace_call_path
// inbound returns confidence_band=high + unresolved_call_count=0
// (the extractor never saw a call into it).
//
// Negative: a non-allowlist method call ending in something other
// than the seven Flask suffixes (e.g. "obj.register(fn)") must NOT
// emit a synthesized call.
func TestPythonBeforeRequestRegisterTracked(t *testing.T) {
	source := []byte(`
from flask import Flask

def log_request():
    pass

def log_response():
    pass

def cleanup():
    pass

def handle_404():
    pass

def inject_user():
    pass

def warm_cache():
    pass

def close_db():
    pass

def unrelated_register():
    pass

def create_app():
    app = Flask(__name__)
    app.before_request(log_request)
    app.after_request(log_response)
    app.teardown_request(cleanup)
    app.errorhandler(handle_404)
    app.context_processor(inject_user)
    app.before_first_request(warm_cache)
    app.teardown_appcontext(close_db)
    # Negative: not on the allowlist, must NOT emit synthetic call.
    app.register(unrelated_register)
    return app
`)
	result, err := ExtractFile(source, lang.Python, "test", "app/main.py")
	if err != nil {
		t.Fatal(err)
	}

	// Map of synthesized hook name → expected dispatch_kind. Each
	// must appear exactly once with the right label and with
	// enclosing_func_qn = the create_app QN.
	want := map[string]string{
		"log_request":  "before_request_hook",
		"log_response": "after_request_hook",
		"cleanup":      "teardown_request_hook",
		"close_db":     "teardown_appcontext_hook",
		"handle_404":   "errorhandler_hook",
		"inject_user":  "context_processor_hook",
		"warm_cache":   "before_first_request_hook",
	}
	const wantEnclosing = "test.app.main.create_app"

	got := map[string]*Call{}
	for i := range result.Calls {
		c := &result.Calls[i]
		t.Logf("Call: callee=%q enclosing=%q dispatch=%q",
			c.CalleeName, c.EnclosingFuncQN, c.DispatchKind)
		if _, expected := want[c.CalleeName]; expected && c.DispatchKind != "" {
			got[c.CalleeName] = c
		}
		// Negative: unrelated_register must NOT be emitted as a
		// synthesized call. The .register(...) suffix is not on the
		// allowlist; only the seven Flask hook-registrar names are.
		if c.CalleeName == "unrelated_register" && c.DispatchKind != "" {
			t.Errorf("unrelated_register was synthesized as call with "+
				"dispatch_kind=%q; .register is NOT on the v0.3 "+
				"allowlist. All calls: %v", c.DispatchKind, result.Calls)
		}
	}

	for name, label := range want {
		c, ok := got[name]
		if !ok {
			t.Errorf("expected synthesized call to %q with "+
				"dispatch_kind=%q; not found. All calls: %v",
				name, label, result.Calls)
			continue
		}
		if c.DispatchKind != label {
			t.Errorf("%s: dispatch_kind = %q, want %q",
				name, c.DispatchKind, label)
		}
		if c.EnclosingFuncQN != wantEnclosing {
			t.Errorf("%s: enclosing_func_qn = %q, want %q",
				name, c.EnclosingFuncQN, wantEnclosing)
		}
	}
}
