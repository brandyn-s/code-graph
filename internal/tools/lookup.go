// Cross-project node lookup used by trace and snippet handlers.
//
// Split from tools.go without behaviour changes.
package tools

import (
	"fmt"
	"strings"

	"github.com/brandyn-s/code-graph/internal/store"
)

// findNodeAcrossProjects searches for a node in the specified project, accepting
// EITHER a simple name or a fully-qualified name. Falls back to the session
// project if no filter is given.
//
// The QN branch is load-bearing, not a convenience: the ambiguity guard in
// handleTraceCallPath returns candidate `qualified_name` values and instructs the
// caller to "re-call with a fully-qualified name to disambiguate". Before this
// lookup existed, that retry always failed — FindNodesByName matches the `name`
// column, which never contains a dotted QN — so every ambiguous symbol
// (`validate`, `run`, `main`, `make_token`) was permanently untraceable. The
// tool advertised an escape hatch wired to the wrong column.
func (s *Server) findNodeAcrossProjects(name string, projectFilter ...string) (*store.Node, string, error) {
	filter := s.sessionProject
	if len(projectFilter) > 0 && projectFilter[0] != "" {
		if projectFilter[0] == "*" || projectFilter[0] == "all" {
			return nil, "", fmt.Errorf("cross-project queries are not supported; use list_projects to find a specific project name, or omit the project parameter to use the current session project")
		}
		filter = projectFilter[0]
	}
	if filter == "" {
		return nil, "", fmt.Errorf("no project specified and no session project detected")
	}
	if !s.router.HasProject(filter) {
		return nil, "", fmt.Errorf("project %q not found; use list_projects to see available projects", filter)
	}
	// Touch watcher so cross-project queries keep that project fresh.
	if filter != s.sessionProject {
		s.watcher.TouchProject(filter)
	}

	st, err := s.router.ForProject(filter)
	if err != nil {
		return nil, "", err
	}
	projects, _ := st.ListProjects()

	// Exact-QN pass first. A dotted string is QN-shaped, and a QN is unique per
	// project, so this resolves the disambiguation retry deterministically —
	// no nodes[0] guess. Runs as its own pass over all projects so an exact QN
	// match always beats a short-name match in a different project.
	if strings.Contains(name, ".") {
		for _, p := range projects {
			node, findErr := st.FindNodeByQN(p.Name, name)
			if findErr != nil || node == nil {
				continue
			}
			return node, p.Name, nil
		}
	}

	for _, p := range projects {
		nodes, findErr := st.FindNodesByName(p.Name, name)
		if findErr != nil {
			continue
		}
		if len(nodes) > 0 {
			return nodes[0], p.Name, nil
		}
	}
	return nil, "", fmt.Errorf("node not found: %s", name)
}
