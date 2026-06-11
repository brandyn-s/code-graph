package store

// Evictor behavior tests — added with the 2026-06-11 mid-index eviction
// fix. Prior to this file the evictor had zero test coverage, which is
// how "ForProject's doc comment claims a ref is held but no ref is held"
// shipped: every index_repository call crossing the 30s idleTimeout had
// its *sql.DB closed mid-run by evictIdle. The data still committed (the
// in-flight transaction's connection survives pool closure), but every
// post-commit read failed and the tool response reported nodes=0/edges=0
// with action_outcome="created" and a fabricated indexed_at.
//
// These tests pin the two halves of the contract the fix relies on:
//   1. evictIdle DOES close bare (un-ref'd) stores past idleTimeout —
//      the baseline that makes long-running use of ForProject unsafe.
//   2. evictIdle does NOT close a store while an AcquireStore ref is
//      held, and DOES close it after release — the protection that
//      handleIndexRepository and code_localize_agent now depend on.

import (
	"testing"
	"time"
)

// backdateEntry moves a store entry's lastUsed past the idle timeout so
// the next evictIdle pass treats it as evictable (refs permitting).
func backdateEntry(t *testing.T, r *StoreRouter, name string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[name]
	if !ok {
		t.Fatalf("no router entry for %q", name)
	}
	e.lastUsed = time.Now().Add(-r.idleTimeout - time.Second)
}

func TestEvictIdleClosesBareStore(t *testing.T) {
	r, err := NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	const proj = "evict-bare"

	st, err := r.ForProject(proj)
	if err != nil {
		t.Fatalf("ForProject: %v", err)
	}
	if _, err := st.CountNodes(proj); err != nil {
		t.Fatalf("CountNodes before eviction: %v", err)
	}

	backdateEntry(t, r, proj)
	r.evictIdle()

	r.mu.Lock()
	_, stillPresent := r.entries[proj]
	r.mu.Unlock()
	if stillPresent {
		t.Fatal("expected idle un-ref'd store to be evicted")
	}

	// The handle the caller kept is now backed by a closed pool — this is
	// exactly what a >30s index_repository run experienced mid-pipeline.
	if _, err := st.CountNodes(proj); err == nil {
		t.Fatal("expected query on evicted store to fail, got nil error")
	}
}

func TestEvictIdleSkipsAcquiredStoreUntilRelease(t *testing.T) {
	r, err := NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	const proj = "evict-held"

	st, release, err := r.AcquireStore(proj)
	if err != nil {
		t.Fatalf("AcquireStore: %v", err)
	}

	// Even far past the idle timeout, a held ref blocks eviction.
	backdateEntry(t, r, proj)
	r.evictIdle()

	r.mu.Lock()
	_, stillPresent := r.entries[proj]
	r.mu.Unlock()
	if !stillPresent {
		t.Fatal("store evicted while AcquireStore ref was held")
	}
	if _, err := st.CountNodes(proj); err != nil {
		t.Fatalf("query on ref-held store failed: %v", err)
	}

	// After release the same idle entry is reclaimable again.
	release()
	backdateEntry(t, r, proj)
	r.evictIdle()

	r.mu.Lock()
	_, stillPresent = r.entries[proj]
	r.mu.Unlock()
	if stillPresent {
		t.Fatal("expected store to be evicted after release")
	}
}
