package pipeline

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/brandyn-s/code-graph/internal/discover"
	"github.com/brandyn-s/code-graph/internal/store"
)

// Regex extractors for OPA Rego policy files.
var (
	// opaToolNameRe matches `input.tool_name == "tool_name"` patterns in Rego rules.
	opaToolNameRe = regexp.MustCompile(`input\.tool_name\s*==\s*"([^"]+)"`)

	// opaPackageRe matches `package foo.bar` declarations at the start of a line.
	opaPackageRe = regexp.MustCompile(`(?m)^package\s+(\S+)`)
)

// extractOPAToolRefs returns all tool names referenced via input.tool_name == "xxx" in a Rego source.
func extractOPAToolRefs(source string) []string {
	matches := opaToolNameRe.FindAllStringSubmatch(source, -1)
	if len(matches) == 0 {
		return nil
	}
	// Deduplicate while preserving order.
	seen := make(map[string]struct{}, len(matches))
	var result []string
	for _, m := range matches {
		name := m[1]
		if _, ok := seen[name]; !ok {
			seen[name] = struct{}{}
			result = append(result, name)
		}
	}
	return result
}

// extractOPAPackage returns the package name from a Rego source, or empty string if none found.
func extractOPAPackage(source string) string {
	m := opaPackageRe.FindStringSubmatch(source)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// passOPALinker walks the repo looking for .rego files, extracts OPA policy tool
// references, creates policy nodes, and links them to target tool/function nodes
// with POLICY_GATES edges.
func (p *Pipeline) passOPALinker() {
	var regoFiles []string

	err := filepath.WalkDir(p.RepoPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // WHY: WalkDir callback - skip inaccessible entries
		}
		// Skip ignored directories (same patterns as discover/infrascan).
		if d.IsDir() {
			if discover.IGNORE_PATTERNS[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".rego") {
			regoFiles = append(regoFiles, path)
		}
		return nil
	})
	if err != nil {
		slog.Warn("pass.opa_linker.walk", "err", err)
		return
	}

	if len(regoFiles) == 0 {
		return // no-op: no .rego files found
	}

	totalEdges := 0

	for _, regoPath := range regoFiles {
		data, err := os.ReadFile(regoPath)
		if err != nil {
			slog.Warn("pass.opa_linker.read", "file", regoPath, "err", err)
			continue
		}
		source := string(data)

		toolRefs := extractOPAToolRefs(source)
		if len(toolRefs) == 0 {
			continue
		}

		pkg := extractOPAPackage(source)
		if pkg == "" {
			pkg = "unknown"
		}

		// Compute a relative path for the file_path field.
		relPath, err := filepath.Rel(p.RepoPath, regoPath)
		if err != nil {
			relPath = regoPath
		}
		relPath = filepath.ToSlash(relPath)

		// Create (or upsert) a node representing this OPA policy.
		policyQN := "opa_policy:" + pkg
		policyNode := &store.Node{
			Project:       p.ProjectName,
			Label:         "Function",
			Name:          pkg,
			QualifiedName: policyQN,
			FilePath:      relPath,
			StartLine:     1,
			EndLine:       1,
			Properties: map[string]any{
				"security_role": RoleAuthBoundary,
				"kind":          "opa_policy",
			},
		}
		policyID, err := p.Store.UpsertNode(policyNode)
		if err != nil {
			slog.Warn("pass.opa_linker.upsert_policy", "pkg", pkg, "err", err)
			continue
		}

		// For each referenced tool, find matching nodes and create POLICY_GATES edges.
		for _, toolName := range toolRefs {
			targets, err := p.Store.FindNodesByName(p.ProjectName, toolName)
			if err != nil {
				continue
			}
			for _, target := range targets {
				edge := &store.Edge{
					Project:  p.ProjectName,
					SourceID: policyID,
					TargetID: target.ID,
					Type:     "POLICY_GATES",
					Properties: map[string]any{
						"policy_package": pkg,
						"tool_name":      toolName,
					},
				}
				if _, err := p.Store.InsertEdge(edge); err != nil {
					slog.Warn("pass.opa_linker.edge", "policy", pkg, "tool", toolName, "err", err)
					continue
				}
				totalEdges++
			}
		}
	}

	if totalEdges > 0 {
		slog.Info("pass.opa_linker", "rego_files", len(regoFiles), "edges", totalEdges)
	}
}
