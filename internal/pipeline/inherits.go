package pipeline

import (
	"log/slog"
	"strings"

	"github.com/brandyn-s/code-graph/internal/store"
)

// passInherits creates INHERITS edges from Class nodes to their base classes.
// Reads base_classes property (set during extractClassDef) and resolves via registry.
func (p *Pipeline) passInherits() {
	slog.Info("pass.inherits")

	count := 0
	overrideCount := 0
	for _, label := range []string{"Class", "Type", "Interface", "Enum"} {
		nodes, err := p.findNodesByLabel(p.ProjectName, label)
		if err != nil {
			continue
		}
		for _, n := range nodes {
			bases, ok := n.Properties["base_classes"]
			if strings.HasSuffix(n.FilePath, ".ts") || strings.HasSuffix(n.FilePath, ".tsx") {
				bases, ok = n.Properties["extends_types"]
			}
			if !ok {
				continue
			}
			baseList, ok := bases.([]any)
			if !ok {
				continue
			}

			moduleQN := qualifiedNamePrefix(n.QualifiedName)
			importMap := p.importMaps[moduleQN]

			for _, b := range baseList {
				baseName, ok := b.(string)
				if !ok || baseName == "" {
					continue
				}

				// Resolve base class to a registered Class/Type/Interface
				targetQN := resolveAsClass(baseName, p.registry, moduleQN, importMap)
				if targetQN == "" {
					continue
				}

				targetNode, _ := p.findNodeByQN(p.ProjectName, targetQN)
				if targetNode == nil {
					continue
				}

				_ = p.insertEdge(&store.Edge{
					Project:  p.ProjectName,
					SourceID: n.ID,
					TargetID: targetNode.ID,
					Type:     "INHERITS",
					Properties: map[string]any{
						"confidence_tier": store.ConfidenceInferred,
					},
				})
				count++
				if n.Label == "Class" && targetNode.Label == "Class" &&
					(strings.HasSuffix(n.FilePath, ".ts") || strings.HasSuffix(n.FilePath, ".tsx")) {
					overrideCount += p.createOverrideEdgesExplicit(n, targetNode)
				}
			}
		}
	}

	slog.Info("pass.inherits.done", "edges", count, "overrides", overrideCount)
}

// qualifiedNamePrefix returns the module QN portion of a fully qualified name.
// e.g., "project.path.module.ClassName" → "project.path.module"
func qualifiedNamePrefix(qn string) string {
	for i := len(qn) - 1; i >= 0; i-- {
		if qn[i] == '.' {
			return qn[:i]
		}
	}
	return qn
}
