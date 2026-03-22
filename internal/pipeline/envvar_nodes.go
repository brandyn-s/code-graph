package pipeline

import (
	"log/slog"

	"github.com/DeusData/codebase-memory-mcp/internal/fqn"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// passEnvVarNodes creates EnvVar nodes and READS_ENV edges from the envReaders
// data collected during extraction. Each unique env var key becomes a node,
// and each function that reads it gets a READS_ENV edge.
func (p *Pipeline) passEnvVarNodes() {
	if len(p.envReaders) == 0 {
		return
	}

	slog.Info("pass.envvar_nodes", "keys", len(p.envReaders))

	var nodeCount, edgeCount int

	for envKey, readerQNs := range p.envReaders {
		// Create the EnvVar node with a deterministic QN
		envQN := fqn.ModuleQN(p.ProjectName, "__env__") + "." + envKey
		envNode := &store.Node{
			Project:       p.ProjectName,
			Label:         "EnvVar",
			Name:          envKey,
			QualifiedName: envQN,
			Properties: map[string]any{
				"readers": len(readerQNs),
			},
		}

		envNodeID, err := p.Store.UpsertNode(envNode)
		if err != nil {
			slog.Debug("pass.envvar_nodes.upsert.err", "key", envKey, "err", err)
			continue
		}
		nodeCount++

		// Create READS_ENV edges from each reading function to the EnvVar node
		seen := make(map[string]bool)
		for _, funcQN := range readerQNs {
			if seen[funcQN] {
				continue
			}
			seen[funcQN] = true

			funcNode, findErr := p.Store.FindNodeByQN(p.ProjectName, funcQN)
			if findErr != nil || funcNode == nil {
				continue
			}

			_, edgeErr := p.Store.InsertEdge(&store.Edge{
				Project:  p.ProjectName,
				SourceID: funcNode.ID,
				TargetID: envNodeID,
				Type:     "READS_ENV",
			})
			if edgeErr != nil {
				slog.Debug("pass.envvar_nodes.edge.err", "func", funcQN, "key", envKey, "err", edgeErr)
				continue
			}
			edgeCount++
		}
	}

	slog.Info("pass.envvar_nodes.done", "nodes", nodeCount, "edges", edgeCount)
}
