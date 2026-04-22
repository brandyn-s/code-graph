package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The install path touches ~/.claude/settings.json which is live user state;
// these tests use tempdirs so they never modify the caller's real config.

func TestMergeOrientationHookIntoSettings_FreshFile(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	script := filepath.Join(dir, "orientation.sh")

	changed, err := mergeOrientationHookIntoSettings(settings, script, false)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true on fresh install")
	}

	raw, _ := os.ReadFile(settings)
	if !strings.Contains(string(raw), orientationHookMarker) {
		t.Errorf("expected marker %q in settings.json, got:\n%s", orientationHookMarker, raw)
	}
	if !strings.Contains(string(raw), orientationHookMatcher) {
		t.Errorf("expected matcher %q in settings.json", orientationHookMatcher)
	}
}

func TestMergeOrientationHookIntoSettings_Idempotent(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	script := filepath.Join(dir, "orientation.sh")

	// First install
	if _, err := mergeOrientationHookIntoSettings(settings, script, false); err != nil {
		t.Fatalf("first install: %v", err)
	}
	before, _ := os.ReadFile(settings)

	// Second install without --force should be a no-op
	changed, err := mergeOrientationHookIntoSettings(settings, script, false)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if changed {
		t.Error("second install without --force should be idempotent no-op")
	}

	after, _ := os.ReadFile(settings)
	if string(before) != string(after) {
		t.Errorf("idempotent install changed file; before=\n%s\nafter=\n%s", before, after)
	}
}

func TestMergeOrientationHookIntoSettings_PreservesOtherHooks(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")

	// Seed settings.json with a pre-existing PreToolUse entry unrelated to us.
	seed := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": "Bash",
					"hooks": []any{map[string]any{
						"type":    "command",
						"command": "/usr/bin/true",
					}},
				},
			},
		},
		"unrelated_key": "value-that-should-survive",
	}
	seedBytes, _ := json.Marshal(seed)
	if err := os.WriteFile(settings, seedBytes, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := mergeOrientationHookIntoSettings(settings, "/tmp/orientation.sh", false); err != nil {
		t.Fatalf("merge: %v", err)
	}

	raw, _ := os.ReadFile(settings)
	if !strings.Contains(string(raw), "/usr/bin/true") {
		t.Error("pre-existing Bash hook should survive our merge")
	}
	if !strings.Contains(string(raw), "unrelated_key") {
		t.Error("unrelated top-level keys should survive our merge")
	}
	if !strings.Contains(string(raw), orientationHookMarker) {
		t.Error("our marker missing after merge into pre-populated settings.json")
	}
}

func TestRemoveOrientationHookFromSettings_LeavesOthersAlone(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	script := filepath.Join(dir, "orientation.sh")

	// Install, then add an unrelated hook, then uninstall.
	if _, err := mergeOrientationHookIntoSettings(settings, script, false); err != nil {
		t.Fatalf("install: %v", err)
	}

	// Inject an unrelated hook into the live file.
	raw, _ := os.ReadFile(settings)
	var root map[string]any
	_ = json.Unmarshal(raw, &root)
	hooks := root["hooks"].(map[string]any)
	pre := hooks["PreToolUse"].([]any)
	pre = append(pre, map[string]any{
		"matcher": "Bash",
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": "/usr/bin/true",
		}},
	})
	hooks["PreToolUse"] = pre
	out, _ := json.MarshalIndent(root, "", "  ")
	_ = os.WriteFile(settings, out, 0o600)

	// Uninstall
	removed, err := removeOrientationHookFromSettings(settings)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !removed {
		t.Error("expected removed=true")
	}

	raw, _ = os.ReadFile(settings)
	s := string(raw)
	if strings.Contains(s, orientationHookMarker) {
		t.Error("our marker should be gone after uninstall")
	}
	if !strings.Contains(s, "/usr/bin/true") {
		t.Error("unrelated Bash hook should survive uninstall")
	}
}

func TestRemoveOrientationHookFromSettings_NoopWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")

	removed, err := removeOrientationHookFromSettings(settings)
	if err != nil {
		t.Fatalf("uninstall on nonexistent file: %v", err)
	}
	if removed {
		t.Error("should not report removed when file does not exist")
	}
}
