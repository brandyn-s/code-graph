package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunCodeQLImportRequiresRepositorySARIFAndReceipt(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := runCodeQLImport([]string{"--repository", "/tmp/repo"}, &stdout, &stderr)

	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, "--repository, --sarif, and --receipt are required") {
		t.Fatalf("stderr = %q", got)
	}
}
