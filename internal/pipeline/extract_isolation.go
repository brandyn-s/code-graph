package pipeline

import (
	"context"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brandyn-s/code-graph/internal/cbm"
	"github.com/brandyn-s/code-graph/internal/cbm/isolate"
	"github.com/brandyn-s/code-graph/internal/config"
	"github.com/brandyn-s/code-graph/internal/lang"
	"github.com/brandyn-s/code-graph/internal/store"
)

// Extraction isolation: tree-sitter extraction runs in supervised worker
// processes so a file that crashes or hangs the native extractor is skipped
// and reported instead of killing the indexer (upstream codebase-memory-mcp
// 9b9638e1 / fb334f78 / e242ce1e). CODE_GRAPH_EXTRACT_ISOLATION selects the
// mode; CODE_GRAPH_EXTRACT_FILE_TIMEOUT_S bounds one file.

var (
	isolationOnce    sync.Once
	isolationPool    *isolate.Pool
	isolationFactory isolate.CommandFactory
	isolationMu      sync.Mutex
)

// SetIsolationCommandFactory overrides how worker processes are launched.
// Tests use it to run the test binary as the worker. It must be called before
// the first extraction in the process.
func SetIsolationCommandFactory(f isolate.CommandFactory) {
	isolationMu.Lock()
	defer isolationMu.Unlock()
	isolationFactory = f
}

// IsolationEnabled resolves CODE_GRAPH_EXTRACT_ISOLATION: "on" and "off" are
// explicit; "auto" (default) is on for the real binary, because a native crash
// inside an MCP server or a long index run is the failure mode this exists
// for. Under `go test` (a *.test executable) auto resolves to off: the test
// binary cannot serve as its own worker, and tests that exercise isolation
// opt in with "on" plus SetIsolationCommandFactory.
func IsolationEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(config.Get(config.ExtractIsolation))) {
	case "off", "0", "false", "no":
		return false
	case "on", "1", "true", "yes":
		return true
	default:
		return !strings.HasSuffix(os.Args[0], ".test") && !strings.HasSuffix(os.Args[0], ".test.exe")
	}
}

// IsolationFileTimeout resolves CODE_GRAPH_EXTRACT_FILE_TIMEOUT_S (default 30s).
func IsolationFileTimeout() time.Duration {
	if raw := strings.TrimSpace(config.Get(config.ExtractFileTimeoutSec)); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v > 0 {
			return time.Duration(v * float64(time.Second))
		}
	}
	return 30 * time.Second
}

func isolationWorkers() int {
	n := runtime.NumCPU()
	if n > 8 {
		n = 8
	}
	if n < 1 {
		n = 1
	}
	return n
}

func getIsolationPool() *isolate.Pool {
	isolationOnce.Do(func() {
		isolationMu.Lock()
		factory := isolationFactory
		isolationMu.Unlock()
		if factory == nil {
			f, err := isolate.DefaultCommandFactory()
			if err != nil {
				slog.Warn("cbm.isolation.disabled", "err", err, "hint", "falling back to in-process extraction")
				return
			}
			factory = f
		}
		isolationPool = isolate.NewPool(isolationWorkers(), IsolationFileTimeout(), factory)
	})
	return isolationPool
}

// extractFile is the single entry point the pipeline uses for tree-sitter
// extraction. With isolation on it goes through the supervisor pool; with
// isolation off, or when no worker can be launched, it runs in-process.
func extractFile(ctx context.Context, source []byte, language lang.Language, project, relPath string) (*cbm.FileResult, error) {
	if IsolationEnabled() {
		if pool := getIsolationPool(); pool != nil {
			if ctx == nil {
				ctx = context.Background()
			}
			return pool.Extract(ctx, isolate.Request{Source: source, Language: language, Project: project, RelPath: relPath})
		}
	}
	return cbm.ExtractFile(source, language, project, relPath)
}

// IsolationStats reports supervisor counters for doctor/health output; zero
// values when isolation never started.
func IsolationStats() isolate.Stats {
	if isolationPool == nil {
		return isolate.Stats{}
	}
	return isolationPool.Stats()
}

// recordSkip remembers a supervisor skip for the sidecar written at the end of
// the run. Non-skip errors are ignored.
func (p *Pipeline) recordSkip(relPath string, err error) bool {
	reason, ok := isolate.IsSkip(err)
	if !ok {
		return false
	}
	p.skipMu.Lock()
	defer p.skipMu.Unlock()
	p.skipped = append(p.skipped, store.SkippedFile{Path: relPath, Reason: string(reason), Detail: err.Error()})
	slog.Warn("cbm.extract.skipped", "path", relPath, "reason", reason, "detail", err.Error())
	return true
}

// SkippedFiles returns the files the supervisor skipped during this run.
func (p *Pipeline) SkippedFiles() []store.SkippedFile {
	p.skipMu.Lock()
	defer p.skipMu.Unlock()
	out := make([]store.SkippedFile, len(p.skipped))
	copy(out, p.skipped)
	return out
}

// writeSkipsSidecar persists (or clears) the run's skip report next to the
// project database so index_health and doctor can show it.
func (p *Pipeline) writeSkipsSidecar() {
	if err := store.WriteSkips(p.ProjectName, p.SkippedFiles()); err != nil {
		slog.Warn("cbm.extract.skips_sidecar_failed", "project", p.ProjectName, "err", err)
	}
}

// ResetIsolationForTests closes the shared worker pool and forgets the
// command factory so the next extraction starts fresh. Tests call it so
// goroutine-leak checks see no supervisor goroutines after the run.
func ResetIsolationForTests() {
	isolationMu.Lock()
	pool := isolationPool
	isolationPool = nil
	isolationFactory = nil
	isolationOnce = sync.Once{}
	isolationMu.Unlock()
	if pool != nil {
		pool.Close()
	}
}
