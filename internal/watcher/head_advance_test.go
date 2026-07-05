package watcher

import (
	"context"
	"errors"
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// TestGitHeadAdvanceDeferredToIndexOutcome pins the watcher-durability
// contract for git-strategy projects: a detected change must survive a
// failed (or skipped) reindex. Before this fix, checkSentinel advanced
// lastGitHead unconditionally, so a HEAD move whose reindex failed was
// never re-detected — git strategy never forces full snapshots, and the
// missed content stayed unindexed until the next commit.
func TestGitHeadAdvanceDeferredToIndexOutcome(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	initGitRepo(t, dir)
	mustWriteFile(t, dir+"/a.go", []byte("package a\n"))
	gitCommitAll(t, dir, "baseline")

	r, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("router: %v", err)
	}
	defer r.CloseAll()

	indexErr := errors.New("boom")
	indexCalls := 0
	w := New(r, func(_ context.Context, _, _ string) error {
		indexCalls++
		if indexErr != nil {
			return indexErr
		}
		return nil
	})
	w.ctx = context.Background()

	proj := &store.Project{Name: "p", RootPath: dir}
	state := &projectState{strategy: strategyGit}
	baseHead, err := gitHead(context.Background(), dir)
	if err != nil {
		t.Fatalf("gitHead: %v", err)
	}
	state.lastGitHead = baseHead
	state.snapshot, err = captureSnapshot(context.Background(), dir)
	if err != nil {
		t.Fatalf("captureSnapshot: %v", err)
	}

	// Real content change + commit: HEAD moves.
	mustWriteFile(t, dir+"/a.go", []byte("package a\n\nfunc A() {}\n"))
	gitCommitAll(t, dir, "change")

	changed, failed := w.checkSentinel(proj, state)
	if !changed || failed {
		t.Fatalf("expected changed=true failed=false, got %v %v", changed, failed)
	}
	if state.lastGitHead != baseHead {
		t.Fatalf("checkSentinel must not advance lastGitHead on a change")
	}

	// Reindex FAILS: snapshot and head must stay put so the next poll retries.
	w.fullSnapshotAndIndex(proj, state)
	if indexCalls != 1 {
		t.Fatalf("expected 1 index attempt, got %d", indexCalls)
	}
	if state.lastGitHead != baseHead {
		t.Fatalf("failed index must not advance lastGitHead")
	}
	if changed, _ := w.checkSentinel(proj, state); !changed {
		t.Fatalf("change must be re-detected after a failed index")
	}

	// Reindex SUCCEEDS: snapshot commits, head advances, change clears.
	indexErr = nil
	w.fullSnapshotAndIndex(proj, state)
	if indexCalls != 2 {
		t.Fatalf("expected 2 index attempts, got %d", indexCalls)
	}
	if state.lastGitHead == baseHead {
		t.Fatalf("successful index must advance lastGitHead")
	}
	if changed, _ := w.checkSentinel(proj, state); changed {
		t.Fatalf("no change expected after successful index")
	}
}

// TestGitHeadAdvancesOnContentNeutralChange pins the no-op branch: a HEAD
// move that leaves the file snapshot identical (e.g. an empty commit) must
// advance the sentinel — otherwise the same no-op is re-detected forever.
func TestGitHeadAdvancesOnContentNeutralChange(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	initGitRepo(t, dir)
	mustWriteFile(t, dir+"/a.go", []byte("package a\n"))
	gitCommitAll(t, dir, "baseline")

	r, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("router: %v", err)
	}
	defer r.CloseAll()

	indexCalls := 0
	w := New(r, func(_ context.Context, _, _ string) error {
		indexCalls++
		return nil
	})
	w.ctx = context.Background()

	proj := &store.Project{Name: "p", RootPath: dir}
	state := &projectState{strategy: strategyGit}
	baseHead, _ := gitHead(context.Background(), dir)
	state.lastGitHead = baseHead
	state.snapshot, _ = captureSnapshot(context.Background(), dir)

	gitRun(t, dir, "commit", "--allow-empty", "-m", "empty")

	changed, _ := w.checkSentinel(proj, state)
	if !changed {
		t.Fatalf("HEAD move should register as changed")
	}
	w.fullSnapshotAndIndex(proj, state)
	if indexCalls != 0 {
		t.Fatalf("content-neutral change must not trigger an index, got %d calls", indexCalls)
	}
	if state.lastGitHead == baseHead {
		t.Fatalf("content-neutral change must still advance lastGitHead")
	}
	if changed, _ := w.checkSentinel(proj, state); changed {
		t.Fatalf("no-op must not be re-detected after head advance")
	}
}
