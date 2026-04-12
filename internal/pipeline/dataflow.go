package pipeline

import (
	"fmt"
	"log/slog"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// passDataflow creates Parameter nodes and PARAMETER_OF edges from function parameters.
// This enables taint analysis: tracing which parameter flows into which callee argument.
//
// For each Function/Method node with param_names, a Parameter node is created per
// parameter. A PARAMETER_OF edge links each Parameter to its owning function.
func (p *Pipeline) passDataflow() {
	slog.Info("pass.dataflow")

	nodeCount := 0
	edgeCount := 0

	for _, label := range []string{"Function", "Method"} {
		nodes, err := p.findNodesByLabel(p.ProjectName, label)
		if err != nil {
			continue
		}
		for _, funcNode := range nodes {
			paramNames := extractStringSlice(funcNode.Properties, "param_names")
			if len(paramNames) == 0 {
				continue
			}
			paramTypes := extractStringSlice(funcNode.Properties, "param_types")

			for i, paramName := range paramNames {
				if paramName == "" {
					continue
				}
				paramQN := fmt.Sprintf("%s.%s", funcNode.QualifiedName, paramName)

				props := map[string]any{
					"index": i,
				}
				if i < len(paramTypes) && paramTypes[i] != "" {
					props["type"] = paramTypes[i]
				}

				if err := p.upsertNode(&store.Node{
					Project:       p.ProjectName,
					Label:         "Parameter",
					Name:          paramName,
					QualifiedName: paramQN,
					FilePath:      funcNode.FilePath,
					StartLine:     funcNode.StartLine,
					EndLine:       funcNode.StartLine, // params are on the function's declaration line
					Properties:    props,
				}); err != nil {
					slog.Warn("pass.dataflow.param_upsert_fail", "qn", paramQN, "err", err)
					continue
				}

				paramNode, _ := p.findNodeByQN(p.ProjectName, paramQN)
				if paramNode == nil {
					continue
				}

				if err := p.insertEdge(&store.Edge{
					Project:  p.ProjectName,
					SourceID: paramNode.ID,
					TargetID: funcNode.ID,
					Type:     "PARAMETER_OF",
					Properties: map[string]any{
						"index": i,
					},
				}); err != nil {
					slog.Warn("pass.dataflow.edge_fail", "param", paramQN, "func", funcNode.QualifiedName, "err", err)
				}

				nodeCount++
				edgeCount++
			}
		}
	}

	slog.Info("pass.dataflow.done", "param_nodes", nodeCount, "edges", edgeCount)
}

// extractStringSlice reads a []any property (JSON-round-tripped from []string)
// and converts it back to []string.
func extractStringSlice(props map[string]any, key string) []string {
	raw, ok := props[key]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []any:
		result := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	case []string:
		return v
	default:
		return nil
	}
}
