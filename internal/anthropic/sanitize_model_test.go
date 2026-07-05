package anthropic

import "testing"

// TestSanitizeModelID pins the Claude Code bracket-suffix stripping.
// Hosts pin ANTHROPIC_MODEL with session notation like
// "claude-sonnet-5[1m]"; the raw string 404s against the Messages API.
func TestSanitizeModelID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"claude-sonnet-5[1m]", "claude-sonnet-5"},
		{"claude-fable-5[1m]", "claude-fable-5"},
		{"claude-haiku-4-5-20251001", "claude-haiku-4-5-20251001"},
		{"  claude-sonnet-5 [1m] ", "claude-sonnet-5"},
		{"", ""},
		{"   ", ""},
		// A leading bracket is not session notation — leave it alone so a
		// genuinely malformed value still fails loudly at the API.
		{"[weird]", "[weird]"},
	}
	for _, c := range cases {
		if got := SanitizeModelID(c.in); got != c.want {
			t.Errorf("SanitizeModelID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestNewClientSanitizesInheritedModel pins the end-to-end path: a client
// built under a Claude Code-style ANTHROPIC_MODEL pin must carry the
// sanitized base id.
func TestNewClientSanitizesInheritedModel(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("ANTHROPIC_MODEL", "claude-sonnet-5[1m]")
	c := NewClient()
	if c == nil {
		t.Fatal("NewClient returned nil with key set")
	}
	if got := c.Model(); got != "claude-sonnet-5" {
		t.Errorf("Model() = %q, want claude-sonnet-5", got)
	}
}
