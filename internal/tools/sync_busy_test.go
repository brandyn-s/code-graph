package tools

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
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
	srv := NewServer(router)

	srv.indexMu.Lock()
	defer srv.indexMu.Unlock()

	err = srv.syncProject(context.Background(), "p", t.TempDir())
	if !errors.Is(err, errSyncBusy) {
		t.Fatalf("expected errSyncBusy while indexMu is held, got %v", err)
	}
}

func TestStartAutoIndexPreservesStatusWhenIndexLockIsBusy(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("router: %v", err)
	}
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

	srv.startAutoIndex()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status, _ := srv.indexStatus.Load().(string)
		if status == "ready" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	status, _ := srv.indexStatus.Load().(string)
	t.Fatalf("auto-index status = %q, want preserved previous status %q", status, "ready")
}
