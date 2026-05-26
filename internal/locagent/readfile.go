// readfile.go — the read_file tool for the locagent loop.
//
// Why this tool exists: the agent's primitives (rank_by_query, code_localize)
// only see graph metadata. They can't disambiguate sibling methods, can't
// confirm a class actually has the method named in the issue, and can't
// route around partial-extraction bugs (e.g. PR #91's pandas case where
// StringMethods._validate exists in the source but not in the graph).
//
// Adding read_file lets the agent verify candidates against actual code,
// matching the LocAgent paper's tool surface and (per session 2026-04-25
// recall-ceiling A1) closing one of two known bottleneck classes.
//
// Safety: the tool refuses paths outside the indexed project root, refuses
// paths containing `..`, caps file size to 1 MB, caps line range to 200
// lines. The agent is on a leash even though the LLM controls the
// arguments.

package locagent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// readFileMaxLines caps how many lines the agent can request in one call.
// 200 is enough to read the body of most class definitions; larger ranges
// inflate context cost without meaningfully helping localization.
const readFileMaxLines = 200

// readFileMaxBytes caps total bytes returned even if the line range is
// short — guards against pathological lines or binary-ish files.
const readFileMaxBytes = 1024 * 1024 // 1 MB

// readFileArgs is the JSON shape the LLM sends.
type readFileArgs struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

// readFileResult is what we feed back to the model. We include the
// observed line numbers so the agent can chain reads if needed.
type readFileResult struct {
	Path       string `json:"path"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	TotalLines int    `json:"total_lines"`
	Truncated  bool   `json:"truncated,omitempty"`
	Content    string `json:"content"`
}

// readFile validates args and reads the requested slice of the file.
// Returns a string error message (instead of a Go error) when the LLM
// passed bad arguments, so the agent gets a tool_result it can act on
// rather than a hard tool failure.
func readFile(rootPath string, args readFileArgs) (any, error) {
	if rootPath == "" {
		return nil, fmt.Errorf("project root path is empty; reindex required")
	}
	if args.Path == "" {
		return map[string]string{"error": "path is required"}, nil
	}
	// Reject obvious traversal attempts before resolving.
	if strings.Contains(args.Path, "..") {
		return map[string]string{"error": "path may not contain '..'"}, nil
	}
	// Reject absolute paths — the agent should pass paths relative to the
	// project root, matching how file_path appears in graph entities.
	if filepath.IsAbs(args.Path) {
		return map[string]string{"error": "path must be relative to project root, not absolute"}, nil
	}
	cleanRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	full := filepath.Join(cleanRoot, filepath.FromSlash(args.Path))
	cleanFull, err := filepath.Abs(full)
	if err != nil {
		return map[string]string{"error": "could not resolve path"}, nil
	}
	// Lexical containment check (fast-reject before any I/O).
	rel, err := filepath.Rel(cleanRoot, cleanFull)
	if err != nil || strings.HasPrefix(rel, "..") {
		return map[string]string{"error": fmt.Sprintf("path %q is outside project root", args.Path)}, nil
	}

	// Resolve symlinks on both root and target, then re-verify
	// containment. This defeats symlink-escape attacks where a repo
	// contains e.g. 'leak -> /' so 'leak/etc/shadow' passes the
	// lexical check but dereferences outside the repo.
	resolvedRoot, err := filepath.EvalSymlinks(cleanRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve root symlinks: %w", err)
	}
	resolvedFull, err := filepath.EvalSymlinks(cleanFull)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{"error": fmt.Sprintf("file not found: %s", args.Path)}, nil
		}
		return map[string]string{"error": fmt.Sprintf("cannot resolve path: %v", err)}, nil
	}
	rel, err = filepath.Rel(resolvedRoot, resolvedFull)
	if err != nil || strings.HasPrefix(rel, "..") {
		return map[string]string{"error": fmt.Sprintf("path %q escapes project root via symlink", args.Path)}, nil
	}

	info, err := os.Stat(resolvedFull)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{"error": fmt.Sprintf("file not found: %s", args.Path)}, nil
		}
		return map[string]string{"error": fmt.Sprintf("stat: %v", err)}, nil
	}
	if info.IsDir() {
		return map[string]string{"error": fmt.Sprintf("%s is a directory", args.Path)}, nil
	}

	data, err := os.ReadFile(resolvedFull)
	if err != nil {
		return map[string]string{"error": fmt.Sprintf("read: %v", err)}, nil
	}
	// Defensive: refuse files that are binary or comically large. We don't
	// detect binary precisely; size cap is the proxy.
	truncatedByBytes := false
	if len(data) > readFileMaxBytes {
		data = data[:readFileMaxBytes]
		truncatedByBytes = true
	}

	lines := strings.Split(string(data), "\n")
	totalLines := len(lines)

	// Default range: first 200 lines. Both 0 means agent didn't supply.
	start := args.StartLine
	end := args.EndLine
	if start <= 0 {
		start = 1
	}
	if end <= 0 {
		end = start + readFileMaxLines - 1
	}
	if start > totalLines {
		// Asked past EOF — return the last 50 lines as a hint.
		start = max(1, totalLines-49)
		end = totalLines
	}
	if end > totalLines {
		end = totalLines
	}
	if end-start+1 > readFileMaxLines {
		end = start + readFileMaxLines - 1
	}

	// Slice (1-indexed inputs to 0-indexed slice).
	clip := lines[start-1 : end]
	// Prepend line numbers so the agent can quote line-by-line.
	var b strings.Builder
	for i, line := range clip {
		fmt.Fprintf(&b, "%5d  %s\n", start+i, line)
	}

	return readFileResult{
		Path:       args.Path,
		StartLine:  start,
		EndLine:    end,
		TotalLines: totalLines,
		Truncated:  truncatedByBytes || (end-start+1) >= readFileMaxLines,
		Content:    b.String(),
	}, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
