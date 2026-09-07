package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// installConfig holds settings for the install/uninstall commands.
type installConfig struct {
	dryRun bool
	force  bool
}

const installUsage = `Usage: code-graph install [--dry-run] [--force]

Add code-graph to PATH and register the MCP server with every detected client
(Claude Code, Codex CLI, Cursor, Windsurf, Gemini CLI, VS Code, Zed). Editors
count as detected when their config directory already exists; nothing is
created for clients that are absent.

The Claude Code skills are NOT installed by this command. They ship as a
plugin, so install only clears the loose copies written by 0.9.3 and earlier:

  /plugin marketplace add brandyn-s/code-graph
  /plugin install code-graph-skills@code-graph

  --dry-run  Print what would change without writing anything
  --force    Re-apply registrations that already exist
`

const uninstallUsage = `Usage: code-graph uninstall [--dry-run]

Remove the Claude Code skills, the orientation hook, and the MCP registration
from every client that has one.

  --dry-run  Print what would change without writing anything
`

// parseSubcommandFlags handles the boolean flags of install, uninstall, and
// update. Each recognized flag sets its target; --help prints usage. It
// returns -1 when the subcommand should proceed, otherwise the exit code.
// Unknown arguments stop the subcommand: `install --help` used to run a full
// install because the flag loop ignored anything it did not recognize.
func parseSubcommandFlags(name, usage string, args []string, flags map[string]*bool) int {
	for _, a := range args {
		switch a {
		case "--help", "-h", "help":
			fmt.Print(usage)
			return 0
		}
		target, ok := flags[a]
		if !ok {
			fmt.Fprintf(os.Stderr, "Unknown %s flag: %s\n\n%s", name, a, usage)
			return 1
		}
		*target = true
	}
	return -1
}

// clientInstalled reports whether an editor's config directory already
// exists. Install only writes into clients the user has set up; creating
// ~/.cursor, ~/.gemini, or ~/.config/zed for apps that are not there left
// stray directories behind.
func clientInstalled(configPath string) bool {
	if configPath == "" {
		return false
	}
	info, err := os.Stat(filepath.Dir(configPath))
	return err == nil && info.IsDir()
}

func runInstall(args []string) int {
	cfg := installConfig{}
	if code := parseSubcommandFlags("install", installUsage, args, map[string]*bool{
		"--dry-run": &cfg.dryRun,
		"--force":   &cfg.force,
	}); code >= 0 {
		return code
	}

	binaryPath, err := detectBinaryPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	fmt.Printf("\ncode-graph %s — install\n", version)
	fmt.Printf("Binary: %s\n\n", binaryPath)

	// PATH check
	ensurePATH(binaryPath, cfg)

	// Skills ship as a Claude Code PLUGIN, not as loose files. install only
	// cleans up the loose copies an earlier release wrote and prints how to
	// add the marketplace.
	migrateLooseSkills(cfg)

	// Claude Code MCP registration
	if claudePath := findCLI("claude"); claudePath != "" {
		fmt.Printf("[Claude Code] detected (%s)\n", claudePath)
		registerClaudeCodeMCP(binaryPath, claudePath, cfg)
	} else {
		fmt.Println("[Claude Code] not found — skipping MCP registration")
	}

	fmt.Println()

	// Codex CLI
	if codexPath := findCLI("codex"); codexPath != "" {
		fmt.Printf("[Codex CLI] detected (%s)\n", codexPath)
		installCodex(binaryPath, codexPath, cfg)
	} else {
		fmt.Println("[Codex CLI] not found — skipping")
	}

	fmt.Println()

	// Editors: Cursor, Windsurf, and Gemini CLI share the mcpServers format;
	// VS Code uses "servers" with a "type" field; Zed uses "context_servers"
	// with a "source" field. Only clients whose config directory exists are
	// touched.
	editors := []struct {
		name    string
		path    string
		install func(binaryPath, configPath string, cfg installConfig)
	}{
		{"Cursor", cursorConfigPath(), func(b, p string, c installConfig) { installEditorMCP(b, p, "Cursor", c) }},
		{"Windsurf", windsurfConfigPath(), func(b, p string, c installConfig) { installEditorMCP(b, p, "Windsurf", c) }},
		{"Gemini CLI", geminiConfigPath(), func(b, p string, c installConfig) { installEditorMCP(b, p, "Gemini CLI", c) }},
		{"VS Code", vscodeConfigPath(), installVSCodeMCP},
		{"Zed", zedConfigPath(), installZedMCP},
	}
	for _, ed := range editors {
		if !clientInstalled(ed.path) {
			fmt.Printf("[%s] not found — skipping\n", ed.name)
			continue
		}
		ed.install(binaryPath, ed.path, cfg)
	}

	fmt.Println()

	// PreToolUse orientation hook — nudges Claude Code to read the
	// ARCHITECTURE_REPORT.md (cache dir by default) before Glob/Grep on
	// indexed repos.
	installOrientationHook(cfg)

	fmt.Println("\nDone. Restart your editor/CLI to activate.")
	return 0
}

func runUninstall(args []string) int {
	cfg := installConfig{}
	if code := parseSubcommandFlags("uninstall", uninstallUsage, args, map[string]*bool{
		"--dry-run": &cfg.dryRun,
	}); code >= 0 {
		return code
	}

	fmt.Printf("\ncode-graph %s — uninstall\n\n", version)

	// Remove Claude Code skills
	removeClaudeSkills(cfg)

	// Claude Code MCP deregistration
	if claudePath := findCLI("claude"); claudePath != "" {
		fmt.Printf("[Claude Code] detected (%s)\n", claudePath)
		deregisterMCP(claudePath, "claude", cfg)
	}

	// Codex CLI MCP deregistration + instructions
	if codexPath := findCLI("codex"); codexPath != "" {
		fmt.Printf("[Codex CLI] detected (%s)\n", codexPath)
		removeCodexMCP(cfg)
		removeCodexInstructions(cfg)
	}

	// Cursor
	removeEditorMCP(cursorConfigPath(), "Cursor", cfg)

	// Windsurf
	removeEditorMCP(windsurfConfigPath(), "Windsurf", cfg)

	// Gemini CLI
	removeEditorMCP(geminiConfigPath(), "Gemini CLI", cfg)

	// VS Code Copilot
	removeVSCodeMCP(vscodeConfigPath(), cfg)

	// Zed
	removeZedMCP(zedConfigPath(), cfg)

	fmt.Println()

	// PreToolUse orientation hook
	uninstallOrientationHook(cfg)

	fmt.Println("\nDone. Binary and databases were NOT removed.")
	return 0
}

// detectBinaryPath resolves the current binary's real path.
func detectBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("detect binary: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("resolve symlink: %w", err)
	}
	return resolved, nil
}

// ensurePATH checks if the binary directory is on PATH and offers to add it.
func ensurePATH(binaryPath string, cfg installConfig) {
	binDir := filepath.Dir(binaryPath)
	pathDirs := filepath.SplitList(os.Getenv("PATH"))

	fmt.Println("[PATH]")
	for _, d := range pathDirs {
		if d == binDir {
			fmt.Printf("  ✓ %s already on PATH\n", binDir)
			return
		}
	}

	fmt.Printf("  ⚠ %s is not on PATH\n", binDir)

	if runtime.GOOS == "windows" {
		fmt.Printf("  → Add %s to your PATH environment variable manually\n", binDir)
		return
	}

	rcFile := detectShellRC()
	if rcFile == "" {
		fmt.Printf("  → Add to your shell profile: export PATH=\"%s:$PATH\"\n", binDir)
		return
	}

	line := fmt.Sprintf("export PATH=\"%s:$PATH\"", binDir)

	// Check if already present in rc file
	if content, err := os.ReadFile(rcFile); err == nil {
		if strings.Contains(string(content), line) {
			fmt.Printf("  ✓ Already in %s (restart terminal to activate)\n", rcFile)
			return
		}
	}

	if cfg.dryRun {
		fmt.Printf("  [dry-run] Would append to %s: %s\n", rcFile, line)
	} else {
		f, err := os.OpenFile(rcFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			fmt.Printf("  ⚠ Could not write to %s: %v\n", rcFile, err)
			fmt.Printf("  → Add manually: %s\n", line)
			return
		}
		defer f.Close()
		fmt.Fprintf(f, "\n# Added by code-graph install\n%s\n", line)
		fmt.Printf("  ✓ Added to %s: %s\n", rcFile, line)
		fmt.Printf("  → Run: source %s (or restart terminal)\n", rcFile)
	}
}

// detectShellRC returns the appropriate shell rc file path.
func detectShellRC() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	shell := os.Getenv("SHELL")
	switch {
	case strings.HasSuffix(shell, "/zsh"):
		return filepath.Join(home, ".zshrc")
	case strings.HasSuffix(shell, "/bash"):
		// Prefer .bashrc, fall back to .bash_profile
		bashrc := filepath.Join(home, ".bashrc")
		if _, err := os.Stat(bashrc); err == nil {
			return bashrc
		}
		return filepath.Join(home, ".bash_profile")
	case strings.HasSuffix(shell, "/fish"):
		return filepath.Join(home, ".config", "fish", "config.fish")
	default:
		// Fall back to .profile
		return filepath.Join(home, ".profile")
	}
}

// legacySkillDirNames lists skill directories written by earlier releases
// that `install` replaces and `uninstall` removes.
func legacySkillDirNames() []string {
	names := make([]string, 0, 5)
	names = append(names, legacyMCPServerKey)
	for _, suffix := range []string{"exploring", "tracing", "quality", "reference"} {
		names = append(names, "codebase-memory-"+suffix)
	}
	return names
}

// migrateLooseSkills removes skill directories that earlier releases wrote
// directly into ~/.claude/skills/ and tells the operator how to install the
// plugin instead.
//
// Up to 0.9.3 this function was installSkills(): it wrote four SKILL.md files
// straight into the user's skills directory. That had two problems no amount
// of content fixing addressed.
//
// First, it put files this repository owns into a directory another tool also
// owns, and resolved the conflict by DELETING the other tool's directories —
// legacySkillDirNames() removes the `codebase-memory-*` names on every run.
// Two installers silently fighting over one directory is not a contract.
//
// Second, loose files sit outside every quality gate. The four skills reached
// 0.9.3 with no Examples, no Success Criteria and no allowed-tools declaration
// (fixed in #5 and #6) precisely because nothing checked them; a plugin has a
// manifest, a version, and a marketplace that can be validated.
//
// The plugin root is ./cmd/code-graph/assets, which is the SAME directory that
// the embed directives in assets.go read. There is deliberately no generated
// copy of the SKILL.md files: one file serves both the embedded CLI assets and
// the published plugin, so the two cannot drift.
//
// (That preceding line avoids starting with the embed directive spelled out:
// staticcheck reads a comment opening with "// go:" as a malformed compiler
// directive, SA9009.)
func migrateLooseSkills(cfg installConfig) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("  ⚠ Cannot determine home directory: %v\n", err)
		return
	}

	fmt.Println("[Skills]")

	// Every directory an earlier release may have written: the four current
	// names, the pre-rename codebase-memory-* names, and the upstream
	// monolithic skill.
	names := make([]string, 0, len(skillFiles)+5)
	for name := range skillFiles {
		names = append(names, name)
	}
	names = append(names, legacySkillDirNames()...)
	sort.Strings(names)

	removed := 0
	for _, name := range names {
		skillDir := filepath.Join(home, ".claude", "skills", name)
		info, err := os.Stat(skillDir)
		if err != nil || !info.IsDir() {
			continue
		}
		if cfg.dryRun {
			fmt.Printf("  [dry-run] Would remove loose skill: %s\n", skillDir)
			removed++
			continue
		}
		if err := os.RemoveAll(skillDir); err != nil {
			fmt.Printf("  ⚠ remove %s: %v\n", skillDir, err)
			continue
		}
		fmt.Printf("  ✓ Removed loose skill: %s\n", skillDir)
		removed++
	}
	if removed == 0 {
		fmt.Println("  ✓ No loose skill directories to clean up")
	}

	fmt.Println("  Skills now ship as a plugin. Install them with:")
	fmt.Println("    /plugin marketplace add brandyn-s/code-graph")
	fmt.Println("    /plugin install code-graph-skills@code-graph")
}

// registerClaudeCodeMCP registers the MCP server with Claude Code CLI.
func registerClaudeCodeMCP(binaryPath, claudePath string, cfg installConfig) {
	if cfg.dryRun {
		fmt.Printf("  [dry-run] Would run: %s mcp remove -s user code-graph\n", claudePath)
		fmt.Printf("  [dry-run] Would run: %s mcp add --scope user code-graph -- %s\n", claudePath, binaryPath)
	} else {
		// Silent remove (may fail if not registered — that's fine). Also drop
		// the pre-rename registration so users don't end up with two servers.
		_ = execCLI(claudePath, "mcp", "remove", "-s", "user", legacyMCPServerKey)
		_ = execCLI(claudePath, "mcp", "remove", "-s", "user", "code-graph")
		if err := execCLI(claudePath, "mcp", "add", "--scope", "user", "code-graph", "--", binaryPath); err != nil {
			fmt.Printf("  ⚠ MCP registration failed: %v\n", err)
		} else {
			fmt.Println("  ✓ MCP server registered (scope: user)")
		}
	}
}

// installCodex installs MCP registration and instructions for Codex CLI.
func installCodex(binaryPath, _ string, cfg installConfig) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("  ⚠ Cannot determine home directory: %v\n", err)
		return
	}

	// Register MCP server via config.toml
	configFile := filepath.Join(home, ".codex", "config.toml")
	mcpSection := fmt.Sprintf("\n[mcp_servers.code-graph]\ncommand = %q\n", binaryPath)

	if cfg.dryRun {
		fmt.Printf("  [dry-run] Would add MCP server to: %s\n", configFile)
	} else {
		if err := os.MkdirAll(filepath.Dir(configFile), 0o750); err != nil {
			fmt.Printf("  ⚠ mkdir %s: %v\n", filepath.Dir(configFile), err)
		} else if err := upsertCodexMCP(configFile, mcpSection, binaryPath); err != nil {
			fmt.Printf("  ⚠ MCP registration failed: %v\n", err)
		} else {
			fmt.Printf("  ✓ MCP server registered: %s\n", configFile)
		}
	}

	// Write instructions file
	instrDir := filepath.Join(home, ".codex", "instructions")
	instrFile := filepath.Join(instrDir, "code-graph.md")

	if cfg.dryRun {
		fmt.Printf("  [dry-run] Would write: %s\n", instrFile)
	} else {
		if err := os.MkdirAll(instrDir, 0o750); err != nil {
			fmt.Printf("  ⚠ mkdir %s: %v\n", instrDir, err)
			return
		}
		if err := os.WriteFile(instrFile, []byte(codexInstructions), 0o600); err != nil {
			fmt.Printf("  ⚠ write %s: %v\n", instrFile, err)
			return
		}
		fmt.Printf("  ✓ Instructions: %s\n", instrFile)
	}
}

// upsertCodexMCP adds or updates the code-graph section in config.toml.
func upsertCodexMCP(configFile, mcpSection, binaryPath string) error {
	content, err := os.ReadFile(configFile)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	text := string(content)

	// Drop the pre-rename section so the renamed binary is registered once.
	text = removeCodexSection(text, "[mcp_servers."+legacyMCPServerKey+"]")

	// If section already exists, replace the command line
	const sectionHeader = "[mcp_servers.code-graph]"
	if idx := strings.Index(text, sectionHeader); idx >= 0 {
		// Find the end of this section (next [ or EOF)
		rest := text[idx+len(sectionHeader):]
		endIdx := strings.Index(rest, "\n[")
		if endIdx < 0 {
			endIdx = len(rest)
		}
		newSection := fmt.Sprintf("%s\ncommand = %q\n", sectionHeader, binaryPath)
		text = text[:idx] + newSection + rest[endIdx:]
	} else {
		// Append new section
		text += mcpSection
	}

	return os.WriteFile(configFile, []byte(text), 0o600)
}

// removeCodexSection deletes one TOML table (header through the next table
// header or EOF) from text. Returns text unchanged when the header is absent.
func removeCodexSection(text, header string) string {
	idx := strings.Index(text, header)
	if idx < 0 {
		return text
	}
	rest := text[idx+len(header):]
	endIdx := strings.Index(rest, "\n[")
	if endIdx < 0 {
		return strings.TrimRight(text[:idx], "\n") + "\n"
	}
	return text[:idx] + strings.TrimLeft(rest[endIdx:], "\n")
}

// removeClaudeSkills removes any loose skill directories an earlier release
// wrote. Since 0.9.4 the skills ship as a plugin, which this cannot uninstall
// — the operator removes that with `/plugin uninstall`, so the reminder below
// prevents a silent half-uninstall.
func removeClaudeSkills(cfg installConfig) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	fmt.Println("[Skills]")
	names := make([]string, 0, len(skillFiles)+5)
	for name := range skillFiles {
		names = append(names, name)
	}
	names = append(names, legacySkillDirNames()...)
	for _, name := range names {
		skillDir := filepath.Join(home, ".claude", "skills", name)
		if _, err := os.Stat(skillDir); os.IsNotExist(err) {
			continue
		}
		if cfg.dryRun {
			fmt.Printf("  [dry-run] Would remove: %s\n", skillDir)
		} else {
			if err := os.RemoveAll(skillDir); err != nil {
				fmt.Printf("  ⚠ remove %s: %v\n", skillDir, err)
			} else {
				fmt.Printf("  ✓ Removed: %s\n", skillDir)
			}
		}
	}
	fmt.Println("  Note: the code-graph-skills PLUGIN is installed separately and")
	fmt.Println("  is not removed by this command. Remove it with:")
	fmt.Println("    /plugin uninstall code-graph-skills@code-graph")
}

// deregisterMCP removes the MCP server registration from a CLI.
func deregisterMCP(cliPath, cliName string, cfg installConfig) {
	if cfg.dryRun {
		fmt.Printf("  [dry-run] Would run: %s mcp remove -s user code-graph\n", cliPath)
	} else {
		if err := execCLI(cliPath, "mcp", "remove", "-s", "user", "code-graph"); err != nil {
			fmt.Printf("  ⚠ %s MCP deregistration: %v\n", cliName, err)
		} else {
			fmt.Printf("  ✓ %s MCP server deregistered\n", cliName)
		}
	}
}

// removeCodexMCP removes the code-graph section from Codex config.toml.
func removeCodexMCP(cfg installConfig) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	configFile := filepath.Join(home, ".codex", "config.toml")
	content, err := os.ReadFile(configFile)
	if err != nil {
		return
	}

	text := string(content)
	const sectionHeader = "[mcp_servers.code-graph]"
	idx := strings.Index(text, sectionHeader)
	if idx < 0 {
		return
	}

	if cfg.dryRun {
		fmt.Printf("  [dry-run] Would remove MCP section from: %s\n", configFile)
		return
	}

	// Find end of section (next [ or EOF)
	rest := text[idx+len(sectionHeader):]
	endIdx := strings.Index(rest, "\n[")
	if endIdx < 0 {
		text = strings.TrimRight(text[:idx], "\n")
	} else {
		text = text[:idx] + rest[endIdx+1:]
	}

	if err := os.WriteFile(configFile, []byte(text), 0o600); err != nil {
		fmt.Printf("  ⚠ update %s: %v\n", configFile, err)
	} else {
		fmt.Printf("  ✓ Removed MCP section from: %s\n", configFile)
	}
}

// removeCodexInstructions removes the Codex instructions file.
func removeCodexInstructions(cfg installConfig) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	instrFile := filepath.Join(home, ".codex", "instructions", "code-graph.md")
	if _, err := os.Stat(instrFile); os.IsNotExist(err) {
		return
	}
	if cfg.dryRun {
		fmt.Printf("  [dry-run] Would remove: %s\n", instrFile)
	} else {
		if err := os.Remove(instrFile); err != nil {
			fmt.Printf("  ⚠ remove %s: %v\n", instrFile, err)
		} else {
			fmt.Printf("  ✓ Removed: %s\n", instrFile)
		}
	}
}

// findCLI locates a CLI binary by name.
func findCLI(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}

	// Check common install locations
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	candidates := []string{
		"/usr/local/bin/" + name,
		filepath.Join(home, ".npm", "bin", name),
		filepath.Join(home, ".local", "bin", name),
		filepath.Join(home, ".cargo", "bin", name),
	}
	if runtime.GOOS == "darwin" {
		candidates = append(candidates, "/opt/homebrew/bin/"+name)
	}

	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// execCLI runs a CLI command and returns any error.
func execCLI(path string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// --- Editor MCP config (Cursor, Windsurf) ---

const mcpServerKey = "code-graph"

// legacyMCPServerKey is the pre-rename server/skill name. Install removes it
// from client configs so a renamed binary does not leave a stale duplicate.
const legacyMCPServerKey = "codebase-memory-mcp"

// cursorConfigPath returns the Cursor MCP config path.
func cursorConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cursor", "mcp.json")
}

// windsurfConfigPath returns the Windsurf MCP config path.
func windsurfConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codeium", "windsurf", "mcp_config.json")
}

// installEditorMCP upserts our MCP server entry in an editor's JSON config file.
func installEditorMCP(binaryPath, configPath, editorName string, cfg installConfig) {
	if configPath == "" {
		return
	}

	fmt.Printf("[%s] MCP config: %s\n", editorName, configPath)

	if cfg.dryRun {
		fmt.Printf("  [dry-run] Would upsert %s in %s\n", mcpServerKey, configPath)
		return
	}

	// Read existing config or start fresh
	root := make(map[string]any)
	if data, err := os.ReadFile(configPath); err == nil {
		if jsonErr := json.Unmarshal(data, &root); jsonErr != nil {
			// File exists but is invalid JSON — back up before overwriting
			bakPath := configPath + ".bak"
			if bakErr := os.WriteFile(bakPath, data, 0o600); bakErr != nil {
				fmt.Printf("  ⚠ Invalid JSON in %s and backup failed: %v\n", configPath, bakErr)
				fmt.Printf("  → Fix the JSON manually or remove the file, then re-run install\n")
				return
			}
			fmt.Printf("  ⚠ Invalid JSON in %s, backed up to %s\n", configPath, bakPath)
			root = make(map[string]any)
		}
	}

	// Ensure mcpServers map exists
	servers, ok := root["mcpServers"].(map[string]any)
	if !ok {
		servers = make(map[string]any)
	}

	// Upsert our server entry (and drop the pre-rename key if present)
	delete(servers, legacyMCPServerKey)
	servers[mcpServerKey] = map[string]any{
		"command": binaryPath,
	}
	root["mcpServers"] = servers

	// Write back
	if err := os.MkdirAll(filepath.Dir(configPath), 0o750); err != nil {
		fmt.Printf("  ⚠ mkdir %s: %v\n", filepath.Dir(configPath), err)
		return
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		fmt.Printf("  ⚠ marshal JSON: %v\n", err)
		return
	}
	if err := os.WriteFile(configPath, append(out, '\n'), 0o600); err != nil {
		fmt.Printf("  ⚠ write %s: %v\n", configPath, err)
		return
	}
	fmt.Printf("  ✓ MCP server registered in %s\n", configPath)
}

// removeEditorMCP removes our MCP server entry from an editor's JSON config file.
func removeEditorMCP(configPath, editorName string, cfg installConfig) {
	if configPath == "" {
		return
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return // no config file, nothing to remove
	}

	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return
	}

	servers, ok := root["mcpServers"].(map[string]any)
	if !ok {
		return
	}
	if _, exists := servers[mcpServerKey]; !exists {
		return
	}

	fmt.Printf("[%s] MCP config: %s\n", editorName, configPath)

	if cfg.dryRun {
		fmt.Printf("  [dry-run] Would remove %s from %s\n", mcpServerKey, configPath)
		return
	}

	delete(servers, mcpServerKey)
	root["mcpServers"] = servers

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		fmt.Printf("  ⚠ marshal JSON: %v\n", err)
		return
	}
	if err := os.WriteFile(configPath, append(out, '\n'), 0o600); err != nil {
		fmt.Printf("  ⚠ write %s: %v\n", configPath, err)
		return
	}
	fmt.Printf("  ✓ Removed %s from %s\n", mcpServerKey, configPath)
}

// --- Gemini CLI ---

// geminiConfigPath returns the Gemini CLI settings path.
func geminiConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".gemini", "settings.json")
}

// --- VS Code Copilot ---

// vscodeConfigPath returns the VS Code user-level MCP config path.
func vscodeConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Code", "User", "mcp.json")
	case "linux":
		return filepath.Join(home, ".config", "Code", "User", "mcp.json")
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "Code", "User", "mcp.json")
		}
	}
	return ""
}

// installVSCodeMCP upserts our MCP server in VS Code's mcp.json (uses "servers" key).
func installVSCodeMCP(binaryPath, configPath string, cfg installConfig) {
	if configPath == "" {
		return
	}

	fmt.Printf("[VS Code] MCP config: %s\n", configPath)

	if cfg.dryRun {
		fmt.Printf("  [dry-run] Would upsert %s in %s\n", mcpServerKey, configPath)
		return
	}

	root := make(map[string]any)
	if data, err := os.ReadFile(configPath); err == nil {
		if jsonErr := json.Unmarshal(data, &root); jsonErr != nil {
			bakPath := configPath + ".bak"
			if bakErr := os.WriteFile(bakPath, data, 0o600); bakErr != nil {
				fmt.Printf("  ⚠ Invalid JSON in %s and backup failed: %v\n", configPath, bakErr)
				fmt.Printf("  → Fix the JSON manually or remove the file, then re-run install\n")
				return
			}
			fmt.Printf("  ⚠ Invalid JSON in %s, backed up to %s\n", configPath, bakPath)
			root = make(map[string]any)
		}
	}

	servers, ok := root["servers"].(map[string]any)
	if !ok {
		servers = make(map[string]any)
	}

	servers[mcpServerKey] = map[string]any{
		"type":    "stdio",
		"command": binaryPath,
	}
	root["servers"] = servers

	if err := os.MkdirAll(filepath.Dir(configPath), 0o750); err != nil {
		fmt.Printf("  ⚠ mkdir %s: %v\n", filepath.Dir(configPath), err)
		return
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		fmt.Printf("  ⚠ marshal JSON: %v\n", err)
		return
	}
	if err := os.WriteFile(configPath, append(out, '\n'), 0o600); err != nil {
		fmt.Printf("  ⚠ write %s: %v\n", configPath, err)
		return
	}
	fmt.Printf("  ✓ MCP server registered in %s\n", configPath)
}

// removeVSCodeMCP removes our MCP server from VS Code's mcp.json.
func removeVSCodeMCP(configPath string, cfg installConfig) {
	if configPath == "" {
		return
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return
	}

	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return
	}

	servers, ok := root["servers"].(map[string]any)
	if !ok {
		return
	}
	if _, exists := servers[mcpServerKey]; !exists {
		return
	}

	fmt.Printf("[VS Code] MCP config: %s\n", configPath)

	if cfg.dryRun {
		fmt.Printf("  [dry-run] Would remove %s from %s\n", mcpServerKey, configPath)
		return
	}

	delete(servers, mcpServerKey)
	root["servers"] = servers

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		fmt.Printf("  ⚠ marshal JSON: %v\n", err)
		return
	}
	if err := os.WriteFile(configPath, append(out, '\n'), 0o600); err != nil {
		fmt.Printf("  ⚠ write %s: %v\n", configPath, err)
		return
	}
	fmt.Printf("  ✓ Removed %s from %s\n", mcpServerKey, configPath)
}

// --- Zed ---

// zedConfigPath returns the Zed settings path.
func zedConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "zed", "settings.json")
}

// installZedMCP upserts our MCP server in Zed's settings.json under "context_servers".
func installZedMCP(binaryPath, configPath string, cfg installConfig) {
	if configPath == "" {
		return
	}

	fmt.Printf("[Zed] MCP config: %s\n", configPath)

	if cfg.dryRun {
		fmt.Printf("  [dry-run] Would upsert %s in %s\n", mcpServerKey, configPath)
		return
	}

	root := make(map[string]any)
	if data, err := os.ReadFile(configPath); err == nil {
		if jsonErr := json.Unmarshal(data, &root); jsonErr != nil {
			// Zed settings.json likely has other settings — don't overwrite on bad JSON
			fmt.Printf("  ⚠ Invalid JSON in %s, skipping (fix manually)\n", configPath)
			return
		}
	}

	servers, ok := root["context_servers"].(map[string]any)
	if !ok {
		servers = make(map[string]any)
	}

	servers[mcpServerKey] = map[string]any{
		"source":  "custom",
		"command": binaryPath,
	}
	root["context_servers"] = servers

	if err := os.MkdirAll(filepath.Dir(configPath), 0o750); err != nil {
		fmt.Printf("  ⚠ mkdir %s: %v\n", filepath.Dir(configPath), err)
		return
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		fmt.Printf("  ⚠ marshal JSON: %v\n", err)
		return
	}
	if err := os.WriteFile(configPath, append(out, '\n'), 0o600); err != nil {
		fmt.Printf("  ⚠ write %s: %v\n", configPath, err)
		return
	}
	fmt.Printf("  ✓ MCP server registered in %s\n", configPath)
}

// removeZedMCP removes our MCP server from Zed's settings.json.
func removeZedMCP(configPath string, cfg installConfig) {
	if configPath == "" {
		return
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return
	}

	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return
	}

	servers, ok := root["context_servers"].(map[string]any)
	if !ok {
		return
	}
	if _, exists := servers[mcpServerKey]; !exists {
		return
	}

	fmt.Printf("[Zed] MCP config: %s\n", configPath)

	if cfg.dryRun {
		fmt.Printf("  [dry-run] Would remove %s from %s\n", mcpServerKey, configPath)
		return
	}

	delete(servers, mcpServerKey)
	root["context_servers"] = servers

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		fmt.Printf("  ⚠ marshal JSON: %v\n", err)
		return
	}
	if err := os.WriteFile(configPath, append(out, '\n'), 0o600); err != nil {
		fmt.Printf("  ⚠ write %s: %v\n", configPath, err)
		return
	}
	fmt.Printf("  ✓ Removed %s from %s\n", mcpServerKey, configPath)
}
