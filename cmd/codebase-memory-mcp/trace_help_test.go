package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestTraceCLIHelpDistinguishesDefaultAndOptInEdges(t *testing.T) {
	originalStderr := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	os.Stderr = writer
	t.Cleanup(func() {
		os.Stderr = originalStderr
		_ = reader.Close()
		_ = writer.Close()
	})

	printHelpTools()
	if err := writer.Close(); err != nil {
		t.Fatalf("close stderr writer: %v", err)
	}
	os.Stderr = originalStderr

	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read help output: %v", err)
	}
	help := string(output)
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
