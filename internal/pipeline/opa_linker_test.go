package pipeline

import (
	"testing"
)

func TestExtractOPAToolRefs(t *testing.T) {
	source := `
package acme.slack

default allow = false

allow {
	input.tool_name == "conversations_add_message"
	input.user_email == "admin@example.com"
}

allow {
	input.tool_name == "set_channel_topic"
}

allow {
	input.tool_name == "conversations_add_message"
}
`
	got := extractOPAToolRefs(source)
	want := []string{"conversations_add_message", "set_channel_topic"}

	if len(got) != len(want) {
		t.Fatalf("extractOPAToolRefs() returned %d refs, want %d: %v", len(got), len(want), got)
	}
	for i, g := range got {
		if g != want[i] {
			t.Errorf("extractOPAToolRefs()[%d] = %q, want %q", i, g, want[i])
		}
	}
}

func TestExtractOPAPackage(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{
			name:   "simple package",
			source: "package authz\n\ndefault allow = false\n",
			want:   "authz",
		},
		{
			name:   "dotted package",
			source: "package acme.mcp.slack\n\nimport input\n",
			want:   "acme.mcp.slack",
		},
		{
			name:   "package with comment above",
			source: "# OPA policy for Slack\npackage slack_write_policy\n",
			want:   "slack_write_policy",
		},
		{
			name:   "no package declaration",
			source: "default allow = false\nallow { true }\n",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractOPAPackage(tt.source)
			if got != tt.want {
				t.Errorf("extractOPAPackage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractOPAToolRefsEmpty(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{
			name:   "empty source",
			source: "",
		},
		{
			name:   "no tool refs",
			source: "package authz\n\ndefault allow = false\nallow { input.user_role == \"admin\" }\n",
		},
		{
			name:   "no tool_name pattern at all",
			source: "package authz\nallow { input.method == \"GET\" }\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractOPAToolRefs(tt.source)
			if len(got) != 0 {
				t.Errorf("extractOPAToolRefs() = %v, want empty", got)
			}
		})
	}
}

func TestExtractOPAToolRefsVariousSpacing(t *testing.T) {
	source := `input.tool_name=="compact_tool"
input.tool_name ==  "spaced_tool"
input.tool_name	== "tab_tool"`

	got := extractOPAToolRefs(source)
	want := []string{"compact_tool", "spaced_tool", "tab_tool"}

	if len(got) != len(want) {
		t.Fatalf("extractOPAToolRefs() returned %d refs, want %d: %v", len(got), len(want), got)
	}
	for i, g := range got {
		if g != want[i] {
			t.Errorf("extractOPAToolRefs()[%d] = %q, want %q", i, g, want[i])
		}
	}
}
