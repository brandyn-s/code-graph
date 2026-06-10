package pipeline

import "testing"

// Full-mode discovery applies a 1MB default file-size cutoff (generated
// files — parser tables, bundled assets — dominate bytes above that line:
// measured 96% of indexable bytes on this repo for 18x less definitions-
// pass time). CBM_MAX_FILE_BYTES overrides; "0" disables entirely.
func TestFullModeMaxFileSize(t *testing.T) {
	t.Setenv("CBM_MAX_FILE_BYTES", "")
	if got := fullModeMaxFileSize(); got != 1<<20 {
		t.Fatalf("default = %d, want 1MB", got)
	}
	t.Setenv("CBM_MAX_FILE_BYTES", "2097152")
	if got := fullModeMaxFileSize(); got != 2<<20 {
		t.Fatalf("override = %d, want 2MB", got)
	}
	t.Setenv("CBM_MAX_FILE_BYTES", "0")
	if got := fullModeMaxFileSize(); got != 0 {
		t.Fatalf("explicit 0 = %d, want 0 (unlimited)", got)
	}
	t.Setenv("CBM_MAX_FILE_BYTES", "not-a-number")
	if got := fullModeMaxFileSize(); got != 1<<20 {
		t.Fatalf("invalid value = %d, want 1MB default", got)
	}
}
