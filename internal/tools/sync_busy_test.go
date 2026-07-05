package tools

import (
	"context"
	"errors"
	"testing"

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
