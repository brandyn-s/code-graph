package locagent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brandyn-s/code-graph/internal/store"
)

// withStubRunOnce temporarily replaces runOnceFn with a stub for the
// duration of the test and restores the original on cleanup. Returns a
// pointer to a counter the stub increments per call so tests can assert
// dispatch counts.
func withStubRunOnce(t *testing.T, stub func(ctx context.Context, st *store.Store, project, issue string, topK int) (*Result, error)) *atomic.Int64 {
	t.Helper()
	original := runOnceFn
	var calls atomic.Int64
	runOnceFn = func(ctx context.Context, st *store.Store, project, issue string, topK int) (*Result, error) {
		calls.Add(1)
		return stub(ctx, st, project, issue, topK)
	}
	t.Cleanup(func() {
		runOnceFn = original
	})
	return &calls
}

// TestT3D2_ParallelDispatchesAllIterations — Plan 4 D2 falsifier:
// LOCAGENT_PARALLEL=1 must dispatch all N iterations concurrently, not
// serially. Verified via timing: a stub that sleeps S ms per call should
// complete N calls in ~S ms when parallel and ~N*S ms when serial.
func TestT3D2_ParallelDispatchesAllIterations(t *testing.T) {
	const sleepPerIter = 50 * time.Millisecond
	const iters = 3

	stub := func(ctx context.Context, st *store.Store, project, issue string, topK int) (*Result, error) {
		time.Sleep(sleepPerIter)
		return &Result{
			Entities: []LocalizedEntity{{QualifiedName: "stub.fn", FilePath: "stub.go"}},
		}, nil
	}
	calls := withStubRunOnce(t, stub)

	// Parallel mode.
	t.Setenv("LOCAGENT_PARALLEL", "1")
	startP := time.Now()
	rP, err := runWithConsistency(context.Background(), nil, "p", "issue", 10, iters)
	parallelWall := time.Since(startP)
	if err != nil {
		t.Fatalf("parallel: %v", err)
	}
	if got := calls.Load(); got != iters {
		t.Errorf("parallel: expected %d stub calls, got %d", iters, got)
	}
	if len(rP.Iterations) != iters {
		t.Errorf("parallel: expected %d Iterations slots, got %d", iters, len(rP.Iterations))
	}

	// Serial mode (no env var).
	calls.Store(0)
	t.Setenv("LOCAGENT_PARALLEL", "")
	startS := time.Now()
	rS, err := runWithConsistency(context.Background(), nil, "p", "issue", 10, iters)
	serialWall := time.Since(startS)
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	if got := calls.Load(); got != iters {
		t.Errorf("serial: expected %d stub calls, got %d", iters, got)
	}
	if len(rS.Iterations) != iters {
		t.Errorf("serial: expected %d Iterations slots, got %d", iters, len(rS.Iterations))
	}

	// Timing assertion: parallel should be ~sleepPerIter (with goroutine
	// scheduling slack); serial should be ~N*sleepPerIter. Conservative
	// floors prevent flake on slow CI: parallel < (N-1)*sleepPerIter
	// (i.e., at least 1 iteration's worth faster than full serialization).
	expectedSerialMin := time.Duration(iters-1) * sleepPerIter
	if parallelWall >= serialWall {
		t.Errorf("parallel (%s) should be faster than serial (%s)", parallelWall, serialWall)
	}
	if parallelWall >= expectedSerialMin {
		t.Errorf("parallel wall %s should be << serial-bound %s — looks like dispatch is NOT actually concurrent",
			parallelWall, expectedSerialMin)
	}
	t.Logf("D2 timing: parallel=%s, serial=%s (%.1fx speedup at N=%d)",
		parallelWall, serialWall,
		float64(serialWall)/float64(parallelWall), iters)
}

// TestT3D2_ParallelPartialErrorReturnsAggregate — when one iteration
// errors but others succeed, parallel mode should return the aggregate
// of successful iterations with stop_reason=partial_consistency,
// matching serial-mode semantics.
func TestT3D2_ParallelPartialErrorReturnsAggregate(t *testing.T) {
	var callIdx atomic.Int64
	stub := func(ctx context.Context, st *store.Store, project, issue string, topK int) (*Result, error) {
		idx := callIdx.Add(1)
		// Make the second call error out; first and third succeed.
		if idx == 2 {
			return nil, errors.New("simulated failure")
		}
		return &Result{
			Entities: []LocalizedEntity{{QualifiedName: "ok", FilePath: "ok.go"}},
		}, nil
	}
	withStubRunOnce(t, stub)

	t.Setenv("LOCAGENT_PARALLEL", "1")
	r, err := runWithConsistency(context.Background(), nil, "p", "issue", 10, 3)
	if err != nil {
		t.Fatalf("parallel partial: unexpected error %v", err)
	}
	if r.StopReason != "partial_consistency" {
		t.Errorf("expected stop_reason=partial_consistency, got %q", r.StopReason)
	}
	// 2 iterations succeeded.
	if len(r.Iterations) != 2 {
		t.Errorf("expected 2 successful iterations, got %d", len(r.Iterations))
	}
	if len(r.Entities) == 0 {
		t.Errorf("expected non-empty entities from successful iterations")
	}
}

// TestT3D2_ParallelTotalFailureReturnsError — when ALL iterations
// error, parallel mode should return the aggregate plus the first
// error (matches serial semantics on iter=0 failure).
func TestT3D2_ParallelTotalFailureReturnsError(t *testing.T) {
	stub := func(ctx context.Context, st *store.Store, project, issue string, topK int) (*Result, error) {
		return nil, errors.New("all fail")
	}
	withStubRunOnce(t, stub)

	t.Setenv("LOCAGENT_PARALLEL", "1")
	_, err := runWithConsistency(context.Background(), nil, "p", "issue", 10, 3)
	if err == nil {
		t.Errorf("expected error when all iterations fail")
	}
}
