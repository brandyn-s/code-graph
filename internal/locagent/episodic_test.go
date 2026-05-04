package locagent

import (
	"strings"
	"testing"
)

func TestFormatEpisodicSection_Empty(t *testing.T) {
	got := formatEpisodicSection(nil)
	if got != "" {
		t.Fatalf("expected empty string for nil hits, got %q", got)
	}
	got = formatEpisodicSection([]EpisodicHit{})
	if got != "" {
		t.Fatalf("expected empty string for empty slice, got %q", got)
	}
}

func TestFormatEpisodicSection_Renders(t *testing.T) {
	hits := []EpisodicHit{
		{
			QName:        "django/django#21203",
			Title:        "Refs #35303 -- Improved use of async methods in RemoteUserMiddleware.",
			ChangedFiles: []string{"django/contrib/auth/middleware.py", "tests/auth_tests/test_remote_user.py"},
			Score:        0.6742,
		},
		{
			QName:        "vllm-project/vllm#41526",
			Title:        "[DSv4] Tune default value of VLLM_MULTI_STREAM_GEMM_TOKEN_THRESHOLD",
			ChangedFiles: []string{"vllm/envs.py"},
			Score:        0.6104,
		},
	}
	got := formatEpisodicSection(hits)

	for _, want := range []string{
		"Similar past issues",
		"django/django#21203",
		"RemoteUserMiddleware",
		"django/contrib/auth/middleware.py",
		"0.674", // score formatted to 3 decimals
		"vllm-project/vllm#41526",
		"vllm/envs.py",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("formatted section missing %q\n--- output ---\n%s", want, got)
		}
	}
}

func TestFormatEpisodicSection_TruncatesFileList(t *testing.T) {
	files := make([]string, maxFilesPerHit+3)
	for i := range files {
		files[i] = "file" + string(rune('A'+i)) + ".py"
	}
	hits := []EpisodicHit{
		{QName: "x/y#1", Title: "t", ChangedFiles: files, Score: 0.5},
	}
	got := formatEpisodicSection(hits)

	if !strings.Contains(got, "...") {
		t.Errorf("expected truncation marker '...', not found in:\n%s", got)
	}
	// Ensure the (maxFilesPerHit+1)th file is NOT mentioned by name
	overLimit := files[maxFilesPerHit]
	if strings.Contains(got, overLimit) {
		t.Errorf("over-limit file %q should not appear in output:\n%s", overLimit, got)
	}
}

func TestPropString(t *testing.T) {
	props := map[string]any{
		"pr_title":   "fix: things",
		"pr_number":  float64(123), // JSON numbers come back as float64
		"empty_str":  "",
		"null_value": nil,
	}
	if got := propString(props, "pr_title"); got != "fix: things" {
		t.Errorf("pr_title: got %q", got)
	}
	if got := propString(props, "pr_number"); got != "" {
		t.Errorf("pr_number (non-string): got %q, want empty", got)
	}
	if got := propString(props, "missing"); got != "" {
		t.Errorf("missing key: got %q, want empty", got)
	}
}

func TestPropStringSlice(t *testing.T) {
	tests := []struct {
		name  string
		props map[string]any
		key   string
		want  []string
	}{
		{
			name:  "[]any from json.Unmarshal",
			props: map[string]any{"files": []any{"a.py", "b.py"}},
			key:   "files",
			want:  []string{"a.py", "b.py"},
		},
		{
			name:  "[]string direct",
			props: map[string]any{"files": []string{"a.py", "b.py"}},
			key:   "files",
			want:  []string{"a.py", "b.py"},
		},
		{
			name:  "drops non-string elements",
			props: map[string]any{"files": []any{"a.py", 42, "b.py", nil}},
			key:   "files",
			want:  []string{"a.py", "b.py"},
		},
		{
			name:  "missing key returns nil",
			props: map[string]any{},
			key:   "files",
			want:  nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := propStringSlice(tc.props, tc.key)
			if len(got) != len(tc.want) {
				t.Fatalf("len: got %d want %d (%v vs %v)", len(got), len(tc.want), got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d]: got %q want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
