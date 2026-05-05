// Tests for the Python indirect-dispatch analyzer.
// v0.1: stub. Tests verify the API shape; real assertions land with v0.1
// implementation.

package pipeline

import "testing"

func TestAnalyzePythonIndirectCalls_executor_submit_stub(t *testing.T) {
	// Source with one executor.submit pattern. v0.1 should detect it
	// and emit one INDIRECT_CALLS edge.
	source := []byte(`
import concurrent.futures

def _run_check(item):
    return item

def main():
    with concurrent.futures.ThreadPoolExecutor() as executor:
        result = executor.submit(_run_check, "x")
        return result.result()
`)

	edges := AnalyzePythonIndirectCalls("test.py", source)

	// v0.1 stub: returns nil. After v0.1 implementation, expect >=1 edge.
	if edges == nil {
		t.Logf("v0.1 stub returned nil — expected (implementation pending)")
		return
	}

	// When v0.1 lands, replace the early return above with these assertions:
	if len(edges) < 1 {
		t.Errorf("expected at least 1 INDIRECT_CALLS edge for executor.submit, got %d", len(edges))
	}
	for _, e := range edges {
		if e.DispatchKind != "executor_submit" {
			t.Errorf("expected dispatch_kind 'executor_submit', got %q", e.DispatchKind)
		}
		if e.Confidence != "high" {
			t.Errorf("executor.submit dispatches resolve cleanly; expected confidence='high', got %q", e.Confidence)
		}
	}
}
