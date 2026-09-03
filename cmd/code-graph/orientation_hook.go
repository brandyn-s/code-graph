package main

// This file adds a Claude Code PreToolUse hook that nudges the agent to
// consult ARCHITECTURE_REPORT.md (written by the generate_report MCP tool,
// refreshed on every index_repository) before running Glob/Grep on an
// unfamiliar codebase.
//
// The hook is installed user-wide at ~/.claude/settings.json and writes a
// tiny shell script to ~/.claude/hooks/codebase-memory-orientation.sh. The
// script exits 0 on every invocation (never blocks), emitting a one-line
// reminder to stderr when the indexed project has a report. Claude Code
// surfaces hook stderr as additional context to the agent.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// orientationHookMarker is the stable string we use to find our hook entry
// inside settings.json on re-install or uninstall. Unique enough to never
// collide with user-authored hooks.
const orientationHookMarker = "codebase-memory-orientation"

// orientationHookMatcher is the Claude Code PreToolUse matcher — fires
// before Glob/Grep (the two most common file-exploration tools). Read is
// intentionally excluded: agents routinely Read specific files they've
// already identified, and pinging them about ARCHITECTURE_REPORT.md in
// that flow is noise.
const orientationHookMatcher = "Glob|Grep"

// orientationHookScript is the shell script that executes on each PreToolUse.
// Exits 0 so the tool call proceeds; emits the reminder to stderr only when
// the current project has a report. `CLAUDE_PROJECT_DIR` is set by Claude
// Code for every hook invocation.
const orientationHookScript = `#!/bin/sh
# Installed by: code-graph install
# Purpose: surface ARCHITECTURE_REPORT.md when the agent is about to grep/glob
# on a repo the code-graph MCP has already indexed.
# codebase-memory-orientation hook

project_dir="${CLAUDE_PROJECT_DIR:-$PWD}"
report="$project_dir/ARCHITECTURE_REPORT.md"

if [ -f "$report" ]; then
  echo "[code-graph] This repo has an indexed ARCHITECTURE_REPORT.md at $report." >&2
  echo "[code-graph] It lists god nodes, cohesive communities, cross-package boundaries, and 5 suggested graph queries." >&2
  echo "[code-graph] Prefer query_graph / trace_call_path / get_relevant_context over raw file search for structural questions." >&2
fi

exit 0
`

// installOrientationHook writes the hook script to ~/.claude/hooks/ and
// merges a PreToolUse entry into ~/.claude/settings.json. Idempotent: a
// second install with no --force is a no-op (detects the marker).
func installOrientationHook(cfg installConfig) {
	fmt.Println("[PreToolUse orientation hook]")

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("  ⚠ could not resolve home dir: %v\n", err)
		return
	}

	hooksDir := filepath.Join(home, ".claude", "hooks")
	scriptPath := filepath.Join(hooksDir, "codebase-memory-orientation.sh")
	settingsPath := filepath.Join(home, ".claude", "settings.json")

	if cfg.dryRun {
		fmt.Printf("  [dry-run] would write %s\n", scriptPath)
		fmt.Printf("  [dry-run] would add PreToolUse matcher %q to %s\n",
			orientationHookMatcher, settingsPath)
		return
	}

	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		fmt.Printf("  ⚠ mkdir %s: %v\n", hooksDir, err)
		return
	}

	// Always rewrite the script — if the marker string changes between
	// binary versions, force=true from the user is handled by this overwrite.
	if err := os.WriteFile(scriptPath, []byte(orientationHookScript), 0o755); err != nil {
		fmt.Printf("  ⚠ write %s: %v\n", scriptPath, err)
		return
	}
	fmt.Printf("  ✓ wrote %s\n", scriptPath)

	// Merge settings.json. Read -> modify -> write. Preserve unknown keys.
	changed, err := mergeOrientationHookIntoSettings(settingsPath, scriptPath, cfg.force)
	if err != nil {
		fmt.Printf("  ⚠ update %s: %v\n", settingsPath, err)
		return
	}
	if changed {
		fmt.Printf("  ✓ added PreToolUse matcher %q to %s\n",
			orientationHookMatcher, settingsPath)
	} else {
		fmt.Printf("  ✓ PreToolUse hook already present in %s (no change)\n", settingsPath)
	}
}

// uninstallOrientationHook removes our script and our settings.json entry.
func uninstallOrientationHook(cfg installConfig) {
	fmt.Println("[PreToolUse orientation hook]")

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("  ⚠ could not resolve home dir: %v\n", err)
		return
	}

	scriptPath := filepath.Join(home, ".claude", "hooks", "codebase-memory-orientation.sh")
	settingsPath := filepath.Join(home, ".claude", "settings.json")

	if cfg.dryRun {
		fmt.Printf("  [dry-run] would remove %s\n", scriptPath)
		fmt.Printf("  [dry-run] would strip PreToolUse matcher %q from %s\n",
			orientationHookMatcher, settingsPath)
		return
	}

	if _, statErr := os.Stat(scriptPath); statErr == nil {
		if err := os.Remove(scriptPath); err != nil {
			fmt.Printf("  ⚠ remove %s: %v\n", scriptPath, err)
		} else {
			fmt.Printf("  ✓ removed %s\n", scriptPath)
		}
	}

	removed, err := removeOrientationHookFromSettings(settingsPath)
	if err != nil {
		fmt.Printf("  ⚠ update %s: %v\n", settingsPath, err)
		return
	}
	if removed {
		fmt.Printf("  ✓ stripped PreToolUse matcher %q from %s\n",
			orientationHookMatcher, settingsPath)
	} else {
		fmt.Printf("  - no matching entry found in %s\n", settingsPath)
	}
}

// hookEntry models one PreToolUse entry in Claude Code's settings.json:
//
//	{
//	  "matcher": "Glob|Grep",
//	  "hooks": [ { "type": "command", "command": "..." } ]
//	}
//
// Unknown JSON keys on the PreToolUse array items are preserved by using
// map[string]any throughout — we never re-serialize a typed struct.
func mergeOrientationHookIntoSettings(settingsPath, scriptPath string, force bool) (bool, error) {
	raw, err := os.ReadFile(settingsPath)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read: %w", err)
	}

	root := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &root); err != nil {
			return false, fmt.Errorf("parse json: %w", err)
		}
	}

	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		root["hooks"] = hooks
	}

	pre, _ := hooks["PreToolUse"].([]any)
	// Detect existing entry by its command string containing our marker.
	entryCommand := fmt.Sprintf("sh %q # %s", scriptPath, orientationHookMarker)
	for i, entry := range pre {
		em, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		matcher, _ := em["matcher"].(string)
		subHooks, _ := em["hooks"].([]any)
		for _, sh := range subHooks {
			shm, ok := sh.(map[string]any)
			if !ok {
				continue
			}
			cmd, _ := shm["command"].(string)
			if strings.Contains(cmd, orientationHookMarker) {
				if !force && matcher == orientationHookMatcher {
					return false, nil // already installed, same matcher
				}
				// Replace in-place so --force / matcher drift updates the entry.
				em["matcher"] = orientationHookMatcher
				em["hooks"] = []any{map[string]any{
					"type":    "command",
					"command": entryCommand,
				}}
				pre[i] = em
				hooks["PreToolUse"] = pre
				return writeJSON(settingsPath, root)
			}
		}
	}

	// Not present — append a new entry.
	newEntry := map[string]any{
		"matcher": orientationHookMatcher,
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": entryCommand,
		}},
	}
	pre = append(pre, newEntry)
	hooks["PreToolUse"] = pre

	return writeJSON(settingsPath, root)
}

// removeOrientationHookFromSettings strips our PreToolUse entry from
// settings.json if present. Returns true iff the file was modified.
func removeOrientationHookFromSettings(settingsPath string) (bool, error) {
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read: %w", err)
	}

	root := map[string]any{}
	if err := json.Unmarshal(raw, &root); err != nil {
		return false, fmt.Errorf("parse json: %w", err)
	}

	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		return false, nil
	}
	pre, _ := hooks["PreToolUse"].([]any)
	if len(pre) == 0 {
		return false, nil
	}

	kept := pre[:0]
	stripped := false
	for _, entry := range pre {
		em, ok := entry.(map[string]any)
		if !ok {
			kept = append(kept, entry)
			continue
		}
		subHooks, _ := em["hooks"].([]any)
		ourHook := false
		for _, sh := range subHooks {
			shm, ok := sh.(map[string]any)
			if !ok {
				continue
			}
			if cmd, _ := shm["command"].(string); strings.Contains(cmd, orientationHookMarker) {
				ourHook = true
				break
			}
		}
		if ourHook {
			stripped = true
			continue
		}
		kept = append(kept, entry)
	}
	if !stripped {
		return false, nil
	}

	if len(kept) == 0 {
		delete(hooks, "PreToolUse")
		if len(hooks) == 0 {
			delete(root, "hooks")
		}
	} else {
		hooks["PreToolUse"] = kept
	}

	written, err := writeJSON(settingsPath, root)
	return written, err
}

// writeJSON atomically writes the root object as pretty-printed JSON.
func writeJSON(path string, root map[string]any) (bool, error) {
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return false, fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return false, fmt.Errorf("rename: %w", err)
	}
	return true, nil
}
