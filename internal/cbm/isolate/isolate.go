// Package isolate runs tree-sitter extraction in supervised worker processes
// so a crashing or hanging file is skipped and reported instead of taking the
// whole indexer (and the MCP server around it) down.
//
// Modelled on upstream codebase-memory-mcp 9b9638e1 / fb334f78 / e242ce1e,
// adapted to this Go pipeline: instead of supervising the whole index run and
// re-running it with the culprit quarantined, a small pool of long-lived
// worker processes each extract one file at a time over a gob stream. When a
// worker dies mid-request the in-flight file is recorded as a crash skip and
// the worker is replaced; when a request exceeds the per-file quiet timeout the
// worker is killed, the file is recorded as a timeout skip, and the worker is
// replaced. Everything else continues.
//
// Recursion is prevented structurally: workers run RunWorker and never the
// pipeline, and the parent selects worker mode by argv, not by an ambient
// environment variable.
package isolate

import (
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/brandyn-s/code-graph/internal/cbm"
	"github.com/brandyn-s/code-graph/internal/lang"
)

// Request is one extraction job sent to a worker.
type Request struct {
	Source   []byte
	Language lang.Language
	Project  string
	RelPath  string
}

// Response is the worker's answer. Err is the extraction error text, if any.
type Response struct {
	Result *cbm.FileResult
	Err    string
}

// SkipReason classifies why a file was skipped by the supervisor.
type SkipReason string

const (
	SkipCrash   SkipReason = "crash"
	SkipTimeout SkipReason = "timeout"
)

// SkipError is returned by Pool.Extract when the file could not be extracted
// because the worker crashed or hung on it.
type SkipError struct {
	Reason SkipReason
	Detail string
}

func (e *SkipError) Error() string {
	return fmt.Sprintf("extraction skipped (%s): %s", e.Reason, e.Detail)
}

// IsSkip reports whether err is a supervisor skip and returns its reason.
func IsSkip(err error) (SkipReason, bool) {
	var se *SkipError
	if errors.As(err, &se) {
		return se.Reason, true
	}
	return "", false
}

// Test-only fault injection, honoured only inside a worker process. A relPath
// containing CBM_TEST_CRASH_ON aborts the worker; one containing
// CBM_TEST_HANG_ON never answers. Both are checked by the supervisor tests so
// the guard is exercised against a real fault, never a fixture that may stop
// faulting (upstream's reasoning in 9b9638e1).
const (
	EnvTestCrashOn = "CBM_TEST_CRASH_ON"
	EnvTestHangOn  = "CBM_TEST_HANG_ON"
)

// RunWorker serves extraction requests from r to w until EOF. It is the body of
// the hidden `code-graph cbm-extract-worker` subcommand.
func RunWorker(r io.Reader, w io.Writer) error {
	dec := gob.NewDecoder(r)
	enc := gob.NewEncoder(w)
	crashOn := os.Getenv(EnvTestCrashOn)
	hangOn := os.Getenv(EnvTestHangOn)
	for {
		var req Request
		if err := dec.Decode(&req); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("decode request: %w", err)
		}
		if crashOn != "" && strings.Contains(req.RelPath, crashOn) {
			// Simulate a native crash: die without answering.
			os.Exit(139)
		}
		if hangOn != "" && strings.Contains(req.RelPath, hangOn) {
			select {} // never answers; the supervisor's quiet timeout must fire
		}
		var resp Response
		res, err := cbm.ExtractFile(req.Source, req.Language, req.Project, req.RelPath)
		if err != nil {
			resp.Err = err.Error()
		} else {
			resp.Result = res
		}
		if err := enc.Encode(&resp); err != nil {
			return fmt.Errorf("encode response: %w", err)
		}
	}
}

// CommandFactory builds the worker command. Production uses the running binary
// with the `cbm-extract-worker` argument; tests substitute the test binary.
type CommandFactory func() *exec.Cmd

// DefaultCommandFactory launches the current executable in worker mode.
func DefaultCommandFactory() (CommandFactory, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate executable: %w", err)
	}
	return func() *exec.Cmd {
		cmd := exec.Command(exe, "cbm-extract-worker")
		cmd.Stderr = os.Stderr
		return cmd
	}, nil
}

type worker struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	enc   *gob.Encoder
	dec   *gob.Decoder
	done  chan error // closed when the process exits; carries Wait()'s error
}

// Pool supervises N worker processes.
type Pool struct {
	factory CommandFactory
	timeout time.Duration
	slots   chan *worker // idle workers (nil entries mean "spawn on demand")
	mu      sync.Mutex
	closed  bool
	stats   Stats
}

// Stats counts supervisor outcomes since the pool was created.
type Stats struct {
	Extracted int
	Crashes   int
	Timeouts  int
	Respawns  int
}

// NewPool creates a pool of size workers. Workers are spawned lazily.
func NewPool(size int, timeout time.Duration, factory CommandFactory) *Pool {
	if size < 1 {
		size = 1
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	p := &Pool{factory: factory, timeout: timeout, slots: make(chan *worker, size)}
	for i := 0; i < size; i++ {
		p.slots <- nil
	}
	return p
}

// Stats returns a snapshot of the supervisor counters.
func (p *Pool) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}

func (p *Pool) spawn() (*worker, error) {
	cmd := p.factory()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	w := &worker{
		cmd:   cmd,
		stdin: stdin,
		enc:   gob.NewEncoder(stdin),
		dec:   gob.NewDecoder(stdout),
		done:  make(chan error, 1),
	}
	go func() { w.done <- cmd.Wait(); close(w.done) }()
	return w, nil
}

// kill terminates the worker (a no-op if it already exited) and returns the
// process exit error, which describes the signal for a native crash.
func (w *worker) kill() error {
	_ = w.stdin.Close()
	if w.cmd.Process != nil {
		_ = w.cmd.Process.Kill()
	}
	err, ok := <-w.done
	if !ok {
		return errors.New("worker already reaped")
	}
	return err
}

// Extract runs one file through a worker. It returns *SkipError when the
// worker crashed or exceeded the quiet timeout on this file; other errors are
// ordinary extraction errors reported by the worker.
func (p *Pool) Extract(ctx context.Context, req Request) (*cbm.FileResult, error) {
	var w *worker
	select {
	case w = <-p.slots:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		p.slots <- w
		return nil, errors.New("isolate: pool closed")
	}
	if w == nil {
		nw, err := p.spawn()
		if err != nil {
			p.slots <- nil
			return nil, fmt.Errorf("isolate: spawn worker: %w", err)
		}
		w = nw
	}

	type reply struct {
		resp Response
		err  error
	}
	replyCh := make(chan reply, 1)
	go func() {
		if err := w.enc.Encode(&req); err != nil {
			replyCh <- reply{err: err}
			return
		}
		var resp Response
		err := w.dec.Decode(&resp)
		replyCh <- reply{resp: resp, err: err}
	}()

	timer := time.NewTimer(p.timeout)
	defer timer.Stop()
	select {
	case r := <-replyCh:
		if r.err != nil {
			// Broken pipe or EOF: the worker died on this file.
			exitErr := w.kill()
			p.mu.Lock()
			p.stats.Crashes++
			p.stats.Respawns++
			p.mu.Unlock()
			p.slots <- nil
			return nil, &SkipError{Reason: SkipCrash, Detail: fmt.Sprintf("worker exited during extraction of %s: %v", req.RelPath, exitErr)}
		}
		p.mu.Lock()
		p.stats.Extracted++
		p.mu.Unlock()
		p.slots <- w
		if r.resp.Err != "" {
			return nil, errors.New(r.resp.Err)
		}
		return r.resp.Result, nil
	case <-timer.C:
		_ = w.kill()
		p.mu.Lock()
		p.stats.Timeouts++
		p.stats.Respawns++
		p.mu.Unlock()
		p.slots <- nil
		return nil, &SkipError{Reason: SkipTimeout, Detail: fmt.Sprintf("no response within %s for %s", p.timeout, req.RelPath)}
	case <-ctx.Done():
		_ = w.kill()
		p.slots <- nil
		return nil, ctx.Err()
	}
}

// Close terminates all workers. Extract calls after Close fail.
func (p *Pool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()
	for i := 0; i < cap(p.slots); i++ {
		w := <-p.slots
		if w != nil {
			_ = w.stdin.Close() // EOF lets the worker exit cleanly
			select {
			case <-w.done:
			case <-time.After(2 * time.Second):
				_ = w.kill()
			}
		}
	}
}
