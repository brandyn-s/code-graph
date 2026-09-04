package isolate

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/brandyn-s/code-graph/internal/lang"
)

// TestMain doubles as the worker process: when GO_ISOLATE_WORKER=1 the test
// binary serves extraction requests over stdio exactly like the
// `code-graph cbm-extract-worker` subcommand does.
func TestMain(m *testing.M) {
	if os.Getenv("GO_ISOLATE_WORKER") == "1" {
		if err := RunWorker(os.Stdin, os.Stdout); err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func testFactory(t *testing.T, extraEnv ...string) CommandFactory {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return func() *exec.Cmd {
		cmd := exec.Command(exe)
		cmd.Env = append(os.Environ(), "GO_ISOLATE_WORKER=1")
		cmd.Env = append(cmd.Env, extraEnv...)
		return cmd
	}
}

const goSrc = "package p\n\nfunc Greet(name string) string { return \"hi \" + name }\n\nfunc main() { Greet(\"x\") }\n"

func TestWorkerExtractsNormally(t *testing.T) {
	p := NewPool(2, 10*time.Second, testFactory(t))
	defer p.Close()
	res, err := p.Extract(context.Background(), Request{Source: []byte(goSrc), Language: lang.Go, Project: "iso", RelPath: "main.go"})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	var names []string
	for _, d := range res.Definitions {
		names = append(names, d.Name)
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "Greet") || !strings.Contains(joined, "main") {
		t.Fatalf("definitions missing: %s", joined)
	}
	if st := p.Stats(); st.Extracted != 1 || st.Crashes != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestCrashingFileIsSkippedAndTheRestContinues(t *testing.T) {
	p := NewPool(1, 10*time.Second, testFactory(t, EnvTestCrashOn+"=crashme"))
	defer p.Close()
	ctx := context.Background()

	if _, err := p.Extract(ctx, Request{Source: []byte(goSrc), Language: lang.Go, Project: "iso", RelPath: "ok1.go"}); err != nil {
		t.Fatalf("first extract: %v", err)
	}
	_, err := p.Extract(ctx, Request{Source: []byte(goSrc), Language: lang.Go, Project: "iso", RelPath: "crashme.go"})
	reason, ok := IsSkip(err)
	if !ok || reason != SkipCrash {
		t.Fatalf("expected crash skip, got %v", err)
	}
	// The pool must have replaced the dead worker transparently.
	res, err := p.Extract(ctx, Request{Source: []byte(goSrc), Language: lang.Go, Project: "iso", RelPath: "ok2.go"})
	if err != nil {
		t.Fatalf("extract after crash: %v", err)
	}
	if len(res.Definitions) == 0 {
		t.Fatal("no definitions after respawn")
	}
	st := p.Stats()
	if st.Crashes != 1 || st.Respawns != 1 || st.Extracted != 2 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestHangingFileTimesOutAndTheRestContinues(t *testing.T) {
	p := NewPool(1, 500*time.Millisecond, testFactory(t, EnvTestHangOn+"=hangme"))
	defer p.Close()
	ctx := context.Background()

	start := time.Now()
	_, err := p.Extract(ctx, Request{Source: []byte(goSrc), Language: lang.Go, Project: "iso", RelPath: "hangme.go"})
	reason, ok := IsSkip(err)
	if !ok || reason != SkipTimeout {
		t.Fatalf("expected timeout skip, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout took too long: %s", elapsed)
	}
	if _, err := p.Extract(ctx, Request{Source: []byte(goSrc), Language: lang.Go, Project: "iso", RelPath: "ok.go"}); err != nil {
		t.Fatalf("extract after hang: %v", err)
	}
	if st := p.Stats(); st.Timeouts != 1 || st.Respawns != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestExtractionErrorsPassThroughWithoutSkip(t *testing.T) {
	p := NewPool(1, 10*time.Second, testFactory(t))
	defer p.Close()
	// An unsupported language is an ordinary extraction error, not a skip.
	_, err := p.Extract(context.Background(), Request{Source: []byte("x"), Language: lang.Language("nope"), Project: "iso", RelPath: "a.nope"})
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := IsSkip(err); ok {
		t.Fatalf("ordinary error classified as skip: %v", err)
	}
}
