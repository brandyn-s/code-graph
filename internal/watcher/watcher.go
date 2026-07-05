package watcher

import (
	"bytes"
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/DeusData/codebase-memory-mcp/internal/discover"
	"github.com/DeusData/codebase-memory-mcp/internal/safegit"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// watchStrategy determines how the watcher detects file changes for a project.
type watchStrategy int

const (
	strategyAuto     watchStrategy = iota // auto-detect at first poll
	strategyGit                           // git status + HEAD tracking
	strategyFSNotify                      // fsnotify event-driven
	strategyDirMtime                      // directory mtime polling (fallback)
)

func (s watchStrategy) String() string {
	switch s {
	case strategyGit:
		return "git"
	case strategyFSNotify:
		return "fsnotify"
	case strategyDirMtime:
		return "dirmtime"
	default:
		return "auto"
	}
}

const (
	baseInterval         = 5 * time.Second
	maxInterval          = 60 * time.Second
	fullSnapshotInterval = 5 // polls between forced full snapshots
	// upgradeAttemptInterval bounds the recovery window for a downgraded
	// project. After a `git → fsnotify → dirmtime` downgrade the watcher
	// is stuck on the lower tier until this many polls elapse, at which
	// point it probes whether the higher tier is available again (e.g.
	// .git was restored, fsnotify init now succeeds). With baseInterval=5s
	// and adaptive intervals up to maxInterval=60s, 60 polls is roughly
	// 5–60 minutes — long enough to avoid spam, short enough to recover
	// within a reasonable operator window.
	upgradeAttemptInterval = 60
)

type fileSnapshot struct {
	modTime time.Time
	size    int64
}

type projectState struct {
	snapshot       map[string]fileSnapshot
	pollsSinceFull int
	// pollsSinceUpgradeCheck counts polls since the last attempted
	// strategy upgrade. Reset to 0 after every upgrade probe (success
	// or fail); resets to upgradeAttemptInterval-1 on downgrade so we
	// don't try to immediately re-upgrade after a failure.
	pollsSinceUpgradeCheck int
	interval               time.Duration
	nextPoll               time.Time

	// Strategy (set during baseline, may downgrade and re-upgrade at runtime).
	strategy watchStrategy

	// Git strategy state.
	lastGitHead string

	// FSNotify strategy state.
	fsWatcher *fsnotify.Watcher
	fsChanged atomic.Bool
	fsCancel  context.CancelFunc
	fsDone    chan struct{} // closed when drainFSEvents exits

	// Dir-mtime strategy state.
	dirMtimes map[string]time.Time
}

// close releases per-project resources (fsnotify watcher, goroutines).
func (ps *projectState) close() {
	if ps.fsCancel != nil {
		ps.fsCancel()
	}
	if ps.fsWatcher != nil {
		ps.fsWatcher.Close()
	}
	if ps.fsDone != nil {
		<-ps.fsDone // wait for drain goroutine to exit
	}
	ps.fsCancel = nil
	ps.fsWatcher = nil
	ps.fsDone = nil
}

// IndexFunc is the callback signature for triggering a re-index.
type IndexFunc func(ctx context.Context, projectName, rootPath string) error

// watchEntry tracks a project in the explicit watch list.
type watchEntry struct {
	rootPath  string
	touchedAt time.Time
}

// Watcher polls indexed projects for file changes and triggers re-indexing.
// Change detection uses a 3-tier strategy per project:
//
//  1. Git — git status + HEAD tracking (for git repos)
//  2. FSNotify — event-driven via OS file notifications (for non-git dirs)
//  3. Dir-mtime — directory mtime polling (fallback if fsnotify setup fails)
type Watcher struct {
	router   *store.StoreRouter
	indexFn  IndexFunc
	projects map[string]*projectState
	ctx      context.Context

	// Explicit watch list — only watched projects get polled.
	// strategies mirrors the current change-detection strategy per project
	// so operators can query without parsing logs. Guarded by mu so external
	// callers can read it concurrently with the Run goroutine.
	mu         sync.Mutex
	watchList  map[string]watchEntry
	strategies map[string]string

	// testStrategy overrides auto-detection when non-zero (for tests).
	testStrategy watchStrategy
}

// New creates a Watcher. indexFn is called when file changes are detected.
func New(r *store.StoreRouter, indexFn IndexFunc) *Watcher {
	return &Watcher{
		router:     r,
		indexFn:    indexFn,
		projects:   make(map[string]*projectState),
		watchList:  make(map[string]watchEntry),
		strategies: make(map[string]string),
		ctx:        context.Background(),
	}
}

// Strategies returns a snapshot of the current change-detection strategy
// per watched project. Useful for surfacing silent degradations (e.g., a
// project that probed as "git" but downgraded to "dirmtime" after git
// failures). Returned map is safe to mutate.
func (w *Watcher) Strategies() map[string]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]string, len(w.strategies))
	for k, v := range w.strategies {
		out[k] = v
	}
	return out
}

// setStrategy records the current strategy for project. Called whenever
// state.strategy is assigned so Strategies() stays in sync.
func (w *Watcher) setStrategy(project string, s watchStrategy) {
	w.mu.Lock()
	w.strategies[project] = s.String()
	w.mu.Unlock()
}

// forgetStrategy drops the record for a project that's no longer watched.
func (w *Watcher) forgetStrategy(project string) {
	w.mu.Lock()
	delete(w.strategies, project)
	w.mu.Unlock()
}

// Watch adds a project to the watch list. Called after successful index.
func (w *Watcher) Watch(name, rootPath string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.watchList[name] = watchEntry{rootPath: rootPath, touchedAt: time.Now()}
	slog.Debug("watcher.watch", "project", name, "path", rootPath)
}

// Unwatch removes a project from the watch list. Called on delete.
func (w *Watcher) Unwatch(name string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.watchList, name)
	slog.Debug("watcher.unwatch", "project", name)
}

// TouchProject refreshes a project's timestamp in the watch list.
// If the project isn't watched yet, adds it (looks up rootPath from DB).
func (w *Watcher) TouchProject(name string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if e, ok := w.watchList[name]; ok {
		e.touchedAt = time.Now()
		w.watchList[name] = e
		return
	}
	// Not yet watched — look up rootPath from DB.
	st, release, err := w.router.AcquireStore(name)
	if err != nil {
		return
	}
	proj, projErr := st.GetProject(name)
	release()
	if projErr != nil || proj == nil || proj.RootPath == "" {
		return
	}
	w.watchList[name] = watchEntry{rootPath: proj.RootPath, touchedAt: time.Now()}
	slog.Debug("watcher.touch_add", "project", name, "path", proj.RootPath)
}

// Run blocks until ctx is cancelled. Ticks at baseInterval, polling each
// project only when its adaptive interval has elapsed.
func (w *Watcher) Run(ctx context.Context) {
	w.ctx = ctx
	ticker := time.NewTicker(baseInterval)
	defer ticker.Stop()
	defer w.closeAll()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.pollAll()
		}
	}
}

// closeAll releases resources for all tracked projects.
func (w *Watcher) closeAll() {
	for _, state := range w.projects {
		state.close()
	}
}

// pollAll iterates the explicit watch list and polls each project that is due.
// Prunes watcher state for unwatched projects.
func (w *Watcher) pollAll() {
	w.mu.Lock()
	// Copy watch list under lock to avoid holding lock during poll.
	entries := make(map[string]watchEntry, len(w.watchList))
	for k, v := range w.watchList {
		entries[k] = v
	}
	w.mu.Unlock()

	// Prune projectState for unwatched projects.
	for name, state := range w.projects {
		if _, ok := entries[name]; !ok {
			slog.Info("watcher.prune", "project", name)
			state.close()
			delete(w.projects, name)
			w.forgetStrategy(name)
		}
	}

	now := time.Now()
	for name, entry := range entries {
		state, exists := w.projects[name]
		if exists && now.Before(state.nextPoll) {
			continue
		}

		st, release, stErr := w.router.AcquireStore(name)
		if stErr != nil {
			continue
		}
		proj, projErr := st.GetProject(name)
		release()
		if projErr != nil || proj == nil {
			continue
		}

		// Use rootPath from watch list (most current).
		if entry.rootPath != "" {
			proj.RootPath = entry.rootPath
		}

		if !exists {
			state = &projectState{}
			w.projects[name] = state
		}

		w.pollProject(proj, state)
	}
}

// --- Strategy probing ---

// probeStrategy determines the best change detection strategy for a project.
// Order: git → fsnotify → dirmtime.
func (w *Watcher) probeStrategy(ctx context.Context, rootPath string) watchStrategy {
	if w.testStrategy != strategyAuto {
		return w.testStrategy
	}
	if isGitRepo(ctx, rootPath) {
		return strategyGit
	}
	// FSNotify is the intent; actual setup may fail → falls back in initBaseline.
	return strategyFSNotify
}

// --- Core poll logic ---

func (w *Watcher) pollProject(proj *store.Project, state *projectState) {
	if _, err := os.Stat(proj.RootPath); err != nil {
		slog.Warn("watcher.root_gone", "project", proj.Name, "path", proj.RootPath)
		state.nextPoll = time.Now().Add(maxInterval)
		return
	}

	// First poll — capture baseline and select strategy.
	if state.snapshot == nil {
		w.initBaseline(proj, state)
		return
	}

	state.pollsSinceFull++
	state.pollsSinceUpgradeCheck++

	// Check sentinel based on strategy.
	changed, strategyFailed := w.checkSentinel(proj, state)

	if strategyFailed {
		w.downgradeStrategy(proj, state)
		changed, _ = w.checkSentinel(proj, state)
	} else if state.strategy != strategyGit && state.pollsSinceUpgradeCheck >= upgradeAttemptInterval {
		// Probe whether a higher-tier strategy is now available. Only
		// fires when the current strategy is BELOW git (the top tier)
		// and only when an upgrade interval has elapsed. Recovers from
		// transient downgrade causes (.git removed then restored,
		// fsnotify file-descriptor exhaustion that subsequently
		// cleared, etc.) without operator intervention.
		state.pollsSinceUpgradeCheck = 0
		w.tryUpgradeStrategy(proj, state)
	}

	// Git sentinel catches everything — no forced full snapshot needed.
	// FSNotify + dir-mtime may miss in-place edits, so force periodically.
	forceFull := state.strategy != strategyGit && state.pollsSinceFull >= fullSnapshotInterval

	if !changed && !forceFull {
		state.nextPoll = time.Now().Add(state.interval)
		return
	}

	w.fullSnapshotAndIndex(proj, state)
}

func (w *Watcher) initBaseline(proj *store.Project, state *projectState) {
	snap, err := captureSnapshot(w.ctx, proj.RootPath)
	if err != nil {
		slog.Warn("watcher.snapshot", "project", proj.Name, "err", err)
		state.nextPoll = time.Now().Add(baseInterval)
		return
	}

	state.snapshot = snap
	state.pollsSinceFull = 0
	state.interval = pollInterval(len(snap))
	state.nextPoll = time.Now().Add(state.interval)

	// Select and initialize strategy.
	state.strategy = w.probeStrategy(w.ctx, proj.RootPath)

	switch state.strategy {
	case strategyGit:
		head, _ := gitHead(w.ctx, proj.RootPath)
		state.lastGitHead = head
		slog.Debug("watcher.baseline", "project", proj.Name, "strategy", "git", "files", len(snap))

	case strategyFSNotify:
		if err := w.initFSNotify(state, proj.RootPath); err != nil {
			slog.Debug("watcher.fsnotify.fallback", "project", proj.Name, "err", err)
			state.strategy = strategyDirMtime
			state.dirMtimes, _ = checkDirMtimes(w.ctx, proj.RootPath)
			slog.Debug("watcher.baseline", "project", proj.Name, "strategy", "dirmtime", "files", len(snap), "dirs", len(state.dirMtimes))
		} else {
			slog.Debug("watcher.baseline", "project", proj.Name, "strategy", "fsnotify", "files", len(snap))
		}

	case strategyDirMtime:
		state.dirMtimes, _ = checkDirMtimes(w.ctx, proj.RootPath)
		slog.Debug("watcher.baseline", "project", proj.Name, "strategy", "dirmtime", "files", len(snap), "dirs", len(state.dirMtimes))
	}

	w.setStrategy(proj.Name, state.strategy)
}

// checkSentinel returns (changed, strategyFailed).
func (w *Watcher) checkSentinel(proj *store.Project, state *projectState) (changed, strategyFailed bool) {
	switch state.strategy {
	case strategyGit:
		changed, newHead, err := gitSentinel(w.ctx, proj.RootPath, state.lastGitHead)
		if err != nil {
			slog.Warn("watcher.git.err", "project", proj.Name, "err", err)
			return false, true
		}
		// Advance the sentinel only when nothing changed. On a change, the
		// head advances after the reindex outcome is known (see
		// fullSnapshotAndIndex). Advancing here made a failed or skipped
		// reindex permanent: git strategy never forces full snapshots, so
		// the next poll compared head==lastGitHead, saw no change, and the
		// missed content stayed unindexed until the next commit.
		if !changed {
			state.lastGitHead = newHead
		}
		return changed, false

	case strategyFSNotify:
		return state.fsChanged.CompareAndSwap(true, false), false

	case strategyDirMtime:
		dirMtimes, _ := checkDirMtimes(w.ctx, proj.RootPath)
		changed := !dirMtimesEqual(state.dirMtimes, dirMtimes)
		state.dirMtimes = dirMtimes
		return changed, false

	default:
		return false, true
	}
}

// downgradeStrategy moves to the next fallback tier.
func (w *Watcher) downgradeStrategy(proj *store.Project, state *projectState) {
	old := state.strategy
	switch old {
	case strategyGit:
		// Try fsnotify, then dir-mtime.
		state.strategy = strategyFSNotify
		if err := w.initFSNotify(state, proj.RootPath); err != nil {
			state.strategy = strategyDirMtime
			state.dirMtimes, _ = checkDirMtimes(w.ctx, proj.RootPath)
		}
	case strategyFSNotify:
		state.close()
		state.strategy = strategyDirMtime
		state.dirMtimes, _ = checkDirMtimes(w.ctx, proj.RootPath)
	default:
		return // already at bottom tier
	}
	// Warn, not Info: a downgrade means the preferred strategy stopped
	// working. Operators need to see this without filtering Info logs.
	slog.Warn("watcher.strategy.downgrade", "project", proj.Name, "from", old.String(), "to", state.strategy.String())
	// Reset the upgrade-check counter so we don't immediately re-probe
	// the higher tier (which would race against whatever made it fail).
	state.pollsSinceUpgradeCheck = 0
	w.setStrategy(proj.Name, state.strategy)
}

// tryUpgradeStrategy probes whether a higher-tier change-detection
// strategy is available and switches to it when so. Preference order
// matches the original probe ladder: git > fsnotify > dirmtime.
//
// Called every upgradeAttemptInterval polls when the current strategy
// is below git. On success, fsnotify state is closed before switching
// to git (otherwise the fsnotify goroutine continues consuming events
// nobody reads). The function is best-effort — failures are silent so
// we just wait for the next interval.
//
// Test override: when testStrategy is set we skip the upgrade probe
// entirely so tests that pin a strategy don't see auto-promotion.
func (w *Watcher) tryUpgradeStrategy(proj *store.Project, state *projectState) {
	if w.testStrategy != strategyAuto {
		return
	}
	old := state.strategy
	// Prefer git — it's the highest-signal strategy.
	if isGitRepo(w.ctx, proj.RootPath) {
		head, _ := gitHead(w.ctx, proj.RootPath)
		if state.strategy == strategyFSNotify {
			state.close()
		}
		state.lastGitHead = head
		state.strategy = strategyGit
		slog.Info("watcher.strategy.upgrade",
			"project", proj.Name,
			"from", old.String(),
			"to", state.strategy.String(),
			"trigger", "git_now_available",
		)
		w.setStrategy(proj.Name, state.strategy)
		return
	}
	// Else try fsnotify if we're at the bottom.
	if state.strategy == strategyDirMtime {
		if err := w.initFSNotify(state, proj.RootPath); err == nil {
			state.strategy = strategyFSNotify
			slog.Info("watcher.strategy.upgrade",
				"project", proj.Name,
				"from", old.String(),
				"to", state.strategy.String(),
				"trigger", "fsnotify_now_available",
			)
			w.setStrategy(proj.Name, state.strategy)
		}
	}
}

func (w *Watcher) fullSnapshotAndIndex(proj *store.Project, state *projectState) {
	snap, err := captureSnapshot(w.ctx, proj.RootPath)
	if err != nil {
		slog.Warn("watcher.snapshot", "project", proj.Name, "err", err)
		state.nextPoll = time.Now().Add(state.interval)
		return
	}

	interval := pollInterval(len(snap))
	state.pollsSinceFull = 0

	if snapshotsEqual(state.snapshot, snap) {
		// Content-neutral change (e.g. a commit of already-indexed content
		// only touches .git). Advance the git sentinel so the same no-op
		// isn't re-detected every poll — checkSentinel deliberately leaves
		// lastGitHead un-advanced on changed=true.
		w.advanceGitHead(proj, state)
		state.interval = interval
		state.nextPoll = time.Now().Add(interval)
		return
	}

	slog.Info("watcher.changed", "project", proj.Name, "strategy", state.strategy.String(), "files", len(snap))
	if err := w.indexFn(w.ctx, proj.Name, proj.RootPath); err != nil {
		// Snapshot and git head stay un-advanced: the next poll re-detects
		// the same change and retries the index.
		slog.Warn("watcher.index", "project", proj.Name, "err", err)
		state.nextPoll = time.Now().Add(interval)
		return
	}

	state.snapshot = snap
	state.interval = pollInterval(len(snap))
	state.nextPoll = time.Now().Add(state.interval)

	// Update git HEAD only now that the index succeeded.
	w.advanceGitHead(proj, state)
}

// advanceGitHead refreshes lastGitHead for git-strategy projects. Call only
// after a detected change has been fully handled — indexed successfully, or
// proven content-neutral — never before (a pre-advance turns a failed
// reindex into a permanently missed change).
func (w *Watcher) advanceGitHead(proj *store.Project, state *projectState) {
	if state.strategy != strategyGit {
		return
	}
	if head, err := gitHead(w.ctx, proj.RootPath); err == nil {
		state.lastGitHead = head
	}
}

// --- Git sentinel ---

// isGitRepo checks if rootPath is inside a git repository.
func isGitRepo(ctx context.Context, rootPath string) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := safegit.Command(ctx, "-C", rootPath, "rev-parse", "--git-dir")
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// gitHead returns the current HEAD commit hash.
func gitHead(ctx context.Context, rootPath string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := safegit.Command(ctx, "-C", rootPath, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// gitSentinel checks for working tree changes or HEAD movement since lastHead.
func gitSentinel(ctx context.Context, rootPath, lastHead string) (changed bool, newHead string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	head, err := gitHead(ctx, rootPath)
	if err != nil {
		return false, "", err
	}
	if lastHead != "" && head != lastHead {
		return true, head, nil // HEAD moved (commit, checkout, pull)
	}

	// Check working tree.
	cmd := safegit.Command(ctx, "--no-optional-locks", "-C", rootPath,
		"status", "--porcelain", "--untracked-files=normal")
	out, err := cmd.Output()
	if err != nil {
		return false, head, err
	}
	return len(bytes.TrimSpace(out)) > 0, head, nil
}

// --- FSNotify sentinel ---

// initFSNotify sets up an fsnotify watcher for all directories under rootPath.
// Starts a drain goroutine that sets state.fsChanged on events.
func (w *Watcher) initFSNotify(state *projectState, rootPath string) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	walkErr := filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, walkDirErr error) error {
		if walkDirErr != nil {
			return walkDirErr
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if name == ".git" || name == "node_modules" || name == "__pycache__" || name == ".venv" || name == "vendor" {
			return filepath.SkipDir
		}
		if addErr := fsw.Add(path); addErr != nil {
			return addErr // FD limit or other OS error → fall back
		}
		return nil
	})
	if walkErr != nil {
		fsw.Close()
		return walkErr
	}

	ctx, cancel := context.WithCancel(w.ctx)
	done := make(chan struct{})
	state.fsWatcher = fsw
	state.fsCancel = cancel
	state.fsDone = done
	go drainFSEvents(ctx, fsw, &state.fsChanged, done)
	return nil
}

// drainFSEvents reads fsnotify events and sets the changed flag.
// Exits when ctx is cancelled or the watcher is closed.
func drainFSEvents(ctx context.Context, fsw *fsnotify.Watcher, changed *atomic.Bool, done chan struct{}) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-fsw.Events:
			if !ok {
				return
			}
			changed.Store(true)
			// Watch newly created directories for future events.
			if ev.Has(fsnotify.Create) {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					_ = fsw.Add(ev.Name)
				}
			}
		case _, ok := <-fsw.Errors:
			if !ok {
				return
			}
		}
	}
}

// --- Dir-mtime sentinel ---

// checkDirMtimes walks only directories under rootPath and records their mtimes.
// Cost: ~200 syscalls for a project with 200 dirs (vs 10K+ for full file walk).
func checkDirMtimes(ctx context.Context, rootPath string) (map[string]time.Time, error) {
	mtimes := make(map[string]time.Time, 256)
	err := filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, walkDirErr error) error {
		if walkDirErr != nil {
			return walkDirErr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if name == ".git" || name == "node_modules" || name == "__pycache__" || name == ".venv" || name == "vendor" {
			return filepath.SkipDir
		}
		info, statErr := d.Info()
		if statErr != nil {
			return statErr
		}
		mtimes[path] = info.ModTime()
		return nil
	})
	return mtimes, err
}

// dirMtimesEqual returns true if two dir mtime maps are identical.
func dirMtimesEqual(a, b map[string]time.Time) bool {
	if len(a) != len(b) {
		return false
	}
	for path, aTime := range a {
		if bTime, ok := b[path]; !ok || !aTime.Equal(bTime) {
			return false
		}
	}
	return true
}

// --- Snapshot functions ---

// captureSnapshot walks the file tree using discover.Discover and captures
// mtime+size for each file.
func captureSnapshot(ctx context.Context, rootPath string) (map[string]fileSnapshot, error) {
	files, err := discover.Discover(ctx, rootPath, nil)
	if err != nil {
		return nil, err
	}
	snap := make(map[string]fileSnapshot, len(files))
	for _, f := range files {
		info, statErr := os.Stat(f.Path)
		if statErr != nil {
			continue
		}
		snap[f.RelPath] = fileSnapshot{
			modTime: info.ModTime(),
			size:    info.Size(),
		}
	}
	return snap, nil
}

// snapshotsEqual returns true if two snapshots have identical files with same mtime+size.
func snapshotsEqual(a, b map[string]fileSnapshot) bool {
	if len(a) != len(b) {
		return false
	}
	for path, aSnap := range a {
		bSnap, ok := b[path]
		if !ok {
			return false
		}
		if !aSnap.modTime.Equal(bSnap.modTime) || aSnap.size != bSnap.size {
			return false
		}
	}
	return true
}

// pollInterval computes the adaptive interval from file count.
// 5s base + 1s per 500 files, capped at 60s.
func pollInterval(fileCount int) time.Duration {
	ms := 5000 + (fileCount/500)*1000
	if ms > 60000 {
		ms = 60000
	}
	return time.Duration(ms) * time.Millisecond
}
