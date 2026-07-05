package discover

import "testing"

// TestFullModeMaxFileSize pins the CBM_MAX_FILE_BYTES contract in the
// package that now owns it. index_health and the watcher rely on this
// exact cutoff matching the pipeline's, so all three agree on which files
// the indexer indexed. "0" disables the limit; unset/invalid → 1MB.
func TestFullModeMaxFileSize(t *testing.T) {
	t.Setenv("CBM_MAX_FILE_BYTES", "")
	if got := FullModeMaxFileSize(); got != 1<<20 {
		t.Fatalf("default = %d, want 1MB", got)
	}
	t.Setenv("CBM_MAX_FILE_BYTES", "2097152")
	if got := FullModeMaxFileSize(); got != 2<<20 {
		t.Fatalf("override = %d, want 2MB", got)
	}
	t.Setenv("CBM_MAX_FILE_BYTES", "0")
	if got := FullModeMaxFileSize(); got != 0 {
		t.Fatalf("explicit 0 = %d, want 0 (unlimited)", got)
	}
	t.Setenv("CBM_MAX_FILE_BYTES", "not-a-number")
	if got := FullModeMaxFileSize(); got != 1<<20 {
		t.Fatalf("invalid value = %d, want 1MB default", got)
	}
	t.Setenv("CBM_MAX_FILE_BYTES", "-5")
	if got := FullModeMaxFileSize(); got != 1<<20 {
		t.Fatalf("negative value = %d, want 1MB default", got)
	}
}
