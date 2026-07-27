package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestCaptureStderrDrainsBeyondWindowsPipeCapacity(t *testing.T) {
	payload := strings.Repeat("x", 256<<10)

	output := captureStderr(t, func() {
		if _, err := os.Stderr.WriteString(payload); err != nil {
			t.Fatalf("write oversized stderr payload: %v", err)
		}
	})

	if output != payload {
		t.Fatalf("captured %d bytes, want %d", len(output), len(payload))
	}
}

func captureStderr(t *testing.T, write func()) string {
	t.Helper()

	capture, err := os.CreateTemp(t.TempDir(), "stderr-*.log")
	if err != nil {
		t.Fatalf("create stderr capture file: %v", err)
	}
	defer capture.Close()

	originalStderr := os.Stderr
	os.Stderr = capture
	defer func() {
		os.Stderr = originalStderr
	}()

	write()
	os.Stderr = originalStderr

	if _, err := capture.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("rewind stderr capture file: %v", err)
	}
	output, err := io.ReadAll(capture)
	if err != nil {
		t.Fatalf("read stderr capture file: %v", err)
	}
	return string(output)
}

func TestTraceCLIHelpDistinguishesDefaultAndOptInEdges(t *testing.T) {
	help := captureStderr(t, printHelpTools)
	for _, want := range []string{
		"edge_types: defaults to CALLS, HTTP_CALLS, ASYNC_CALLS",
		"USAGE and OVERRIDE are opt-in",
		"missing/null confidence is retained; explicit numeric zero is filtered when threshold > 0",
		"call confidence: calibrated only for unfiltered CALLS-only traces",
		"positive thresholds and other edge selections report confidence as unknown",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("trace CLI help must contain %q", want)
		}
	}
}
