package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/brandyn-s/code-graph/internal/store"
)

// TestSyncProjectBusyReturnsError pins the skip contract: when another
// index operation holds indexMu, syncProject must return errSyncBusy — a
// nil return made the watcher commit its snapshot and permanently mark the
// detected change as synced without any reindex having run.
func TestSyncProjectBusyReturnsError(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("router: %v", err)
	}
	t.Cleanup(router.CloseAll)
	srv := NewServer(router)

	srv.indexMu.Lock()
	defer srv.indexMu.Unlock()

	err = srv.syncProject(context.Background(), "p", t.TempDir())
	if !errors.Is(err, errSyncBusy) {
		t.Fatalf("expected errSyncBusy while indexMu is held, got %v", err)
	}
}

func TestRunAutoIndexPreservesStatusWhenIndexLockIsBusy(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("router: %v", err)
	}
	t.Cleanup(router.CloseAll)
	const project = "busy-auto-index"
	if _, err := router.ForProject(project); err != nil {
		t.Fatalf("create existing project store: %v", err)
	}

	srv := NewServer(router)
	srv.sessionProject = project
	srv.sessionRoot = t.TempDir()
	srv.indexStatus.Store("ready")
	srv.indexMu.Lock()
	defer srv.indexMu.Unlock()

	err = srv.runAutoIndex(true)
	if !errors.Is(err, errSyncBusy) {
		t.Fatalf("expected errSyncBusy while indexMu is held, got %v", err)
	}
	status, _ := srv.indexStatus.Load().(string)
	if status != "ready" {
		t.Fatalf("auto-index status = %q, want preserved previous status %q", status, "ready")
	}
}
