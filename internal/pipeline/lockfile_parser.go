package pipeline

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/brandyn-s/code-graph/internal/store"
)

// Dependency represents a third-party package parsed from a lockfile.
type Dependency struct {
	Name    string
	Version string
}

// parseGoSum parses a go.sum file and returns deduplicated dependencies.
// go.sum has two entries per module (h1: hash and /go.mod hash); we deduplicate.
func parseGoSum(content string) []Dependency {
	seen := make(map[string]string) // name -> version
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		name := parts[0]
		version := strings.TrimSuffix(parts[1], "/go.mod")
		if _, ok := seen[name]; !ok {
			seen[name] = version
		}
	}
	deps := make([]Dependency, 0, len(seen))
	for name, version := range seen {
		deps = append(deps, Dependency{Name: name, Version: version})
	}
	return deps
}

// parseRequirementsLock parses a pip-compile requirements.lock/.txt file.
// Lines like "boto3==1.34.0" are dependencies; comments and indented lines are skipped.
func parseRequirementsLock(content string) []Dependency {
	var deps []Dependency
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		// Handle == separator (most common in lock files)
		if idx := strings.Index(line, "=="); idx > 0 {
			name := strings.TrimSpace(line[:idx])
			version := strings.TrimSpace(line[idx+2:])
			// Strip any trailing comments or markers (e.g., " ; python_version >= '3.8'")
			if semi := strings.Index(version, ";"); semi >= 0 {
				version = strings.TrimSpace(version[:semi])
			}
			if backslash := strings.Index(version, "\\"); backslash >= 0 {
				version = strings.TrimSpace(version[:backslash])
			}
			deps = append(deps, Dependency{Name: name, Version: version})
		}
	}
	return deps
}

// packageLockPackages is a minimal struct for parsing package-lock.json v3 packages map.
type packageLockPackages struct {
	Packages map[string]struct {
		Version string `json:"version"`
	} `json:"packages"`
}

// parsePackageLockJSON parses an npm package-lock.json (lockfileVersion 3) packages map.
// The root entry (key "") is excluded.
func parsePackageLockJSON(content string) []Dependency {
	var lockfile packageLockPackages
	if err := json.Unmarshal([]byte(content), &lockfile); err != nil {
		slog.Warn("lockfile.parse.package-lock", "err", err)
		return nil
	}
	var deps []Dependency
	for key, pkg := range lockfile.Packages {
		if key == "" || pkg.Version == "" {
			continue
		}
		// key is like "node_modules/express" or "node_modules/@scope/pkg"
		name := strings.TrimPrefix(key, "node_modules/")
		deps = append(deps, Dependency{Name: name, Version: pkg.Version})
	}
	return deps
}

// passLockfileDeps is a post-flush pass that parses lockfiles at the repo root
// and creates Package nodes with DEPENDS_ON edges from the root Module node.
func (p *Pipeline) passLockfileDeps() {
	type lockfileEntry struct {
		filename string
		parser   func(string) []Dependency
	}
	lockfiles := []lockfileEntry{
		{"go.sum", parseGoSum},
		{"requirements.lock", parseRequirementsLock},
		{"requirements.txt", parseRequirementsLock},
		{"package-lock.json", parsePackageLockJSON},
	}

	var allDeps []Dependency
	var sources []string // parallel to allDeps, tracks which lockfile each dep came from

	for _, lf := range lockfiles {
		path := filepath.Join(p.RepoPath, lf.filename)
		data, err := os.ReadFile(path)
		if err != nil {
			continue // file doesn't exist, skip
		}
		deps := lf.parser(string(data))
		for _, d := range deps {
			allDeps = append(allDeps, d)
			sources = append(sources, lf.filename)
		}
	}

	if len(allDeps) == 0 {
		return
	}

	slog.Info("pass.lockfile_deps", "count", len(allDeps))

	// Find a root Module node to use as the DEPENDS_ON source.
	modules, err := p.Store.FindNodesByLabel(p.ProjectName, "Module")
	if err != nil {
		slog.Warn("pass.lockfile_deps.find_modules", "err", err)
	}
	var rootModuleID int64
	if len(modules) > 0 {
		rootModuleID = modules[0].ID
	}

	for i, dep := range allDeps {
		qn := p.ProjectName + ".__pkg__." + dep.Name
		pkgID, err := p.Store.UpsertNode(&store.Node{
			Project:       p.ProjectName,
			Label:         "Package",
			Name:          dep.Name,
			QualifiedName: qn,
			Properties: map[string]any{
				"version": dep.Version,
				"source":  sources[i],
			},
		})
		if err != nil {
			slog.Warn("pass.lockfile_deps.upsert_node", "dep", dep.Name, "err", err)
			continue
		}

		if rootModuleID != 0 {
			_, err = p.Store.InsertEdge(&store.Edge{
				Project:  p.ProjectName,
				SourceID: rootModuleID,
				TargetID: pkgID,
				Type:     "DEPENDS_ON",
				Properties: map[string]any{
					"version": dep.Version,
					"source":  sources[i],
				},
			})
			if err != nil {
				slog.Warn("pass.lockfile_deps.insert_edge", "dep", dep.Name, "err", err)
			}
		}
	}
}
