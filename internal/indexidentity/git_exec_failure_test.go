package indexidentity

// Capture used to collapse two unrelated failures into one message:
//
//	insideWorktree, err := gitOutput(...)
//	if err != nil || strings.TrimSpace(...) != "true" {
//	    return nil, fmt.Errorf("not a Git repository: %s", ...)
//	}
//
// So when git could not be EXECUTED at all, the operator was told the checkout
// was not a repository. Observed 2026-07-27: 19 projects re-indexed inside a
// sandbox that blocks subprocesses each reported
// "not a Git repository: <path>; correct the Git checkout and re-run
// index_repository" for checkouts with a .git/ directory and a resolvable HEAD.
// The index built at 100% coverage — only the identity record was lost — so
// that message was the entire diagnostic surface, and it pointed at the wrong
// thing.
//
// These tests pin the discriminator: an *exec.ExitError means git RAN and
// answered (a real not-a-repository signal), anything else means we never got
// an answer.

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassifyGitExecFailure_ExitErrorIsARealAnswer(t *testing.T) {
	// `false` runs and exits non-zero — the shape of git answering "no".
	err := exec.CommandContext(t.Context(), "false").Run()
	if err == nil {
		t.Skip("`false` unexpectedly succeeded")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Skipf("expected an ExitError from `false`, got %T", err)
	}
	if got := classifyGitExecFailure(err); got != nil {
		t.Errorf("classifyGitExecFailure(ExitError) = %v, want nil (git ran and answered)", got)
	}
}

func TestClassifyGitExecFailure_BinaryNotFoundIsAnExecFailure(t *testing.T) {
	err := exec.CommandContext(t.Context(), "definitely-not-a-real-binary-9f3a2b").Run()
	if err == nil {
		t.Skip("the bogus binary unexpectedly ran")
	}
	got := classifyGitExecFailure(err)
	if got == nil {
		t.Fatal("classifyGitExecFailure(exec.Error) = nil, want a descriptive exec failure")
	}
	if !strings.Contains(got.Error(), "could not be executed") {
		t.Errorf("error should say git could not be executed, got: %v", got)
	}
}

func TestClassifyGitExecFailure_PermissionDeniedIsAnExecFailure(t *testing.T) {
	// A non-executable file: exec fails with a permission error, not an exit code.
	path := filepath.Join(t.TempDir(), "not-executable")
	if err := os.WriteFile(path, []byte("#!/bin/sh\ntrue\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	err := exec.CommandContext(t.Context(), path).Run()
	if err == nil {
		t.Skip("non-executable file unexpectedly ran")
	}
	if !errors.Is(err, fs.ErrPermission) {
		var pathErr *exec.Error
		if !errors.As(err, &pathErr) {
			t.Skipf("unexpected error class for a non-executable: %T %v", err, err)
		}
	}
	if got := classifyGitExecFailure(err); got == nil {
		t.Error("classifyGitExecFailure(permission error) = nil, want an exec failure")
	}
}

func TestClassifyGitExecFailure_NilIsNil(t *testing.T) {
	if got := classifyGitExecFailure(nil); got != nil {
		t.Errorf("classifyGitExecFailure(nil) = %v, want nil", got)
	}
}

func TestClassifyGitExecFailure_ContextCancellationIsAnExecFailure(t *testing.T) {
	if got := classifyGitExecFailure(context.Canceled); got == nil {
		t.Error("a cancelled invocation must not be reported as a real answer")
	}
}

// TestCapture_NonRepoStillSaysNotAGitRepository guards the other direction:
// the new branch must not swallow the genuine signal. A real directory that is
// simply not a repository must still report "not a Git repository".
func TestCapture_NonRepoStillSaysNotAGitRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available; cannot exercise the real-answer path")
	}
	dir := t.TempDir()

	_, err := Capture(dir)
	if err == nil {
		t.Fatal("Capture on a non-repository succeeded; expected an error")
	}
	if !strings.Contains(err.Error(), "not a Git repository") {
		t.Errorf("expected the genuine not-a-repository message, got: %v", err)
	}
	if strings.Contains(err.Error(), "cannot run git") {
		t.Errorf("a real git answer was misreported as an exec failure: %v", err)
	}
}

// TestCapture_ExecFailureDoesNotClaimNotARepository is the regression test for
// the reported symptom. PATH is emptied so `git` cannot be found — standing in
// for the sandbox denial — against a directory that IS a valid repository.
// Pre-fix this returned "not a Git repository: <path>".
func TestCapture_ExecFailureDoesNotClaimNotARepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	if out, err := exec.CommandContext(t.Context(), "git", "-C", dir, "init").CombinedOutput(); err != nil {
		t.Skipf("git init failed: %v: %s", err, out)
	}

	// Make git unfindable. exec.Command resolves via PATH at Run time, so this
	// reproduces "git cannot be executed" without needing a real sandbox.
	t.Setenv("PATH", "")

	_, err := Capture(dir)
	if err == nil {
		t.Fatal("Capture succeeded with git unavailable; expected an error")
	}
	if strings.Contains(err.Error(), "not a Git repository") {
		t.Errorf("exec failure still misreported as a bad checkout: %v", err)
	}
	if !strings.Contains(err.Error(), "cannot run git") {
		t.Errorf("expected a cannot-run-git error, got: %v", err)
	}
	// The remedy must be in the message — it is the only diagnostic the
	// operator sees, and the index itself is fine.
	if !strings.Contains(err.Error(), "sandbox") {
		t.Errorf("error should mention the sandbox possibility, got: %v", err)
	}
}
