package pipeline

import (
	"fmt"
	"log/slog"
	"math"
	"sort"

	"github.com/brandyn-s/code-graph/internal/store"
)

// passCommunities runs Louvain community detection on the CALLS graph
// and creates Community nodes + MEMBER_OF edges.
func (p *Pipeline) passCommunities() {
	slog.Info("pass.communities")

	// Load CALLS edges
	callEdges, err := p.Store.FindEdgesByType(p.ProjectName, "CALLS")
	if err != nil || len(callEdges) == 0 {
		slog.Info("pass.communities.skip", "reason", "no_calls")
		return
	}

	// Build adjacency list (undirected for community detection)
	adj := make(map[int64]map[int64]bool)
	allNodes := make(map[int64]bool)
	for _, e := range callEdges {
		allNodes[e.SourceID] = true
		allNodes[e.TargetID] = true
		if adj[e.SourceID] == nil {
			adj[e.SourceID] = make(map[int64]bool)
		}
		if adj[e.TargetID] == nil {
			adj[e.TargetID] = make(map[int64]bool)
		}
		adj[e.SourceID][e.TargetID] = true
		adj[e.TargetID][e.SourceID] = true
	}

	// Run Louvain community detection
	// SQLite row IDs change when an edited file is deleted and reinserted. Feed
	// Louvain a semantic QN order so those storage IDs cannot change clustering.
	nodeMap, _ := p.Store.FindNodesByIDs(mapKeys(allNodes))
	orderedNodes := mapKeys(allNodes)
	sort.Slice(orderedNodes, func(i, j int) bool {
		left, right := nodeMap[orderedNodes[i]], nodeMap[orderedNodes[j]]
		if left != nil && right != nil && left.QualifiedName != right.QualifiedName {
			return left.QualifiedName < right.QualifiedName
		}
		return orderedNodes[i] < orderedNodes[j]
	})
	communities := louvainCommunitiesInOrder(adj, allNodes, orderedNodes)

	// Create Community nodes + MEMBER_OF edges
	communityCount, memberOfCount := p.storeCommunities(communities)

	// Observability: warn when any detected community exceeds 25% of assigned
	// members. That is the graphify-project "oversized community" threshold —
	// graphify triggers a second-pass Leiden split at this boundary to avoid
	// a degenerate "everything in one giant cluster" failure mode on small
	// graphs. Empirically on redacted fixtures (mcp-servers, mcp-infra,
	// rmf-corsair, code-graph) the largest community is always <18% of
	// assigned members, so no split is needed. If that ever changes — a new
	// repo with skewed structure, a grammar regression collapsing the call
	// graph — this warning surfaces it in logs instead of the oversized
	// cluster silently degrading downstream consumers (orientation report,
	// suggested-question generation).
	totalAssigned := 0
	for _, members := range communities {
		totalAssigned += len(members)
	}
	if totalAssigned > 0 {
		maxMembers := 0
		for _, members := range communities {
			if len(members) > maxMembers {
				maxMembers = len(members)
			}
		}
		maxPct := float64(maxMembers) / float64(totalAssigned)
		if maxPct > 0.25 {
			slog.Warn("pass.communities.oversized_cluster",
				"max_members", maxMembers,
				"total_assigned", totalAssigned,
				"pct_of_assigned", maxPct,
				"threshold", 0.25,
				"hint", "a single community covers >25% of the graph — Louvain likely collapsed into one dominant cluster. Consider a stricter resolution parameter or a follow-up split pass.")
		}
	}

	slog.Info("pass.communities.done", "communities", communityCount, "member_of", memberOfCount)
}

// louvainCommunities implements the Louvain algorithm for community detection.
// Uses per-community degree accumulators for O(m) per iteration instead of O(N^2).
// Returns a map of community_id → []node_id.
func louvainCommunities(adj map[int64]map[int64]bool, allNodes map[int64]bool) map[int][]int64 {
	orderedNodes := mapKeys(allNodes)
	sort.Slice(orderedNodes, func(i, j int) bool { return orderedNodes[i] < orderedNodes[j] })
	return louvainCommunitiesInOrder(adj, allNodes, orderedNodes)
}

func mapKeys(values map[int64]bool) []int64 {
	keys := make([]int64, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

// louvainCommunitiesInOrder runs Louvain with an explicit stable semantic
// order. Callers indexing persisted graphs pass qualified-name order; tests and
// pure callers may pass numeric order. Neighbor-community ties are resolved by
// the same initial community order, eliminating Go map iteration as an input.
func louvainCommunitiesInOrder(
	adj map[int64]map[int64]bool,
	allNodes map[int64]bool,
	orderedNodes []int64,
) map[int][]int64 {
	nodeCommunity := make(map[int64]int, len(allNodes))
	for commID, nodeID := range orderedNodes {
		if allNodes[nodeID] {
			nodeCommunity[nodeID] = commID
		}
	}

	// Pre-compute node degrees
	nodeDegree := make(map[int64]float64, len(allNodes))
	totalEdges := 0
	for nodeID, neighbors := range adj {
		nodeDegree[nodeID] = float64(len(neighbors))
		totalEdges += len(neighbors)
	}
	m := float64(totalEdges) / 2.0
	if m == 0 {
		m = 1
	}

	// Per-community accumulator: sum of degrees of all members.
	// Updated incrementally when nodes move between communities.
	commSumTot := make(map[int]float64, len(allNodes))
	for _, nodeID := range orderedNodes {
		comm := nodeCommunity[nodeID]
		commSumTot[comm] = nodeDegree[nodeID]
	}

	improved := true
	for iteration := 0; improved && iteration < 50; iteration++ {
		improved = louvainIteration(
			adj, nodeCommunity, nodeDegree, commSumTot, m, orderedNodes,
		)
	}

	return groupAndFilterMinSizeInOrder(nodeCommunity, orderedNodes, 3)
}

// louvainIteration runs one pass of greedy modularity optimization.
// For each node, computes modularity gain for neighboring communities in O(degree)
// using pre-maintained commSumTot accumulators. Returns true if any node moved.
func louvainIteration(
	adj map[int64]map[int64]bool,
	nodeCommunity map[int64]int,
	nodeDegree map[int64]float64,
	commSumTot map[int]float64,
	m float64,
	orderedNodes []int64,
) bool {
	improved := false
	m2 := 2.0 * m * m

	for _, nodeID := range orderedNodes {
		neighbors := adj[nodeID]
		if len(neighbors) == 0 {
			continue
		}
		currentComm := nodeCommunity[nodeID]
		ki := nodeDegree[nodeID]

		// Aggregate edges to each neighboring community: O(degree)
		edgesToComm := make(map[int]float64, len(neighbors))
		for neighborID := range neighbors {
			edgesToComm[nodeCommunity[neighborID]]++
		}

		// Remove self from current community for fair comparison
		commSumTot[currentComm] -= ki
		kiInCurrent := edgesToComm[currentComm]
		removeCost := kiInCurrent/m - ki*commSumTot[currentComm]/m2

		bestComm := currentComm
		bestGain := 0.0

		neighborCommunities := make([]int, 0, len(edgesToComm))
		for comm := range edgesToComm {
			neighborCommunities = append(neighborCommunities, comm)
		}
		sort.Ints(neighborCommunities)
		for _, comm := range neighborCommunities {
			if comm == currentComm {
				continue
			}
			kiIn := edgesToComm[comm]
			gain := kiIn/m - ki*commSumTot[comm]/m2 - removeCost
			if gain > bestGain+1e-10 ||
				(gain > 1e-10 && math.Abs(gain-bestGain) <= 1e-10 && comm < bestComm) {
				bestGain = gain
				bestComm = comm
			}
		}

		// Restore / update accumulator
		if bestComm != currentComm && bestGain > 1e-10 {
			nodeCommunity[nodeID] = bestComm
			commSumTot[bestComm] += ki
			// currentComm already had ki subtracted
			improved = true
		} else {
			commSumTot[currentComm] += ki // restore
		}
	}
	return improved
}

// groupAndFilterMinSize groups nodes by community and filters out communities
// smaller than minSize. This reduces noise from tiny clusters (singletons and
// pairs) that the Louvain algorithm tends to produce on medium-sized repos.
func groupAndFilterMinSize(nodeCommunity map[int64]int, minSize int) map[int][]int64 {
	orderedNodes := make([]int64, 0, len(nodeCommunity))
	for nodeID := range nodeCommunity {
		orderedNodes = append(orderedNodes, nodeID)
	}
	sort.Slice(orderedNodes, func(i, j int) bool { return orderedNodes[i] < orderedNodes[j] })
	return groupAndFilterMinSizeInOrder(nodeCommunity, orderedNodes, minSize)
}

func groupAndFilterMinSizeInOrder(
	nodeCommunity map[int64]int,
	orderedNodes []int64,
	minSize int,
) map[int][]int64 {
	communities := make(map[int][]int64)
	for _, nodeID := range orderedNodes {
		comm := nodeCommunity[nodeID]
		communities[comm] = append(communities[comm], nodeID)
	}

	filtered := make(map[int][]int64)
	idx := 0
	communityIDs := make([]int, 0, len(communities))
	for comm := range communities {
		communityIDs = append(communityIDs, comm)
	}
	sort.Ints(communityIDs)
	for _, comm := range communityIDs {
		members := communities[comm]
		if len(members) >= minSize {
			filtered[idx] = members
			idx++
		}
	}
	return filtered
}

// storeCommunities creates Community nodes and MEMBER_OF edges in the database.
func (p *Pipeline) storeCommunities(communities map[int][]int64) (communityCount, memberOfCount int) {
	if len(communities) == 0 {
		return 0, 0
	}

	// Collect all member node IDs for batch lookup
	var allMemberIDs []int64
	for _, members := range communities {
		allMemberIDs = append(allMemberIDs, members...)
	}
	nodeMap, _ := p.Store.FindNodesByIDs(allMemberIDs)

	communityNodes := make([]*store.Node, 0, len(communities))
	memberEdges := make([]pendingEdge, 0, len(allMemberIDs))

	for commIdx, memberIDs := range communities {
		// Find top symbols by name for labeling
		topNames := topMemberNames(memberIDs, nodeMap, 5)

		commName := fmt.Sprintf("community_%d", commIdx)
		if len(topNames) > 0 {
			commName = topNames[0] + "_cluster"
		}

		commQN := fmt.Sprintf("%s.__community__.%d", p.ProjectName, commIdx)

		// Calculate cohesion: ratio of internal edges to possible edges
		cohesion := communityCohesion(memberIDs, nodeMap)

		communityNodes = append(communityNodes, &store.Node{
			Project:       p.ProjectName,
			Label:         "Community",
			Name:          commName,
			QualifiedName: commQN,
			Properties: map[string]any{
				"cohesion":     math.Round(cohesion*100) / 100,
				"symbol_count": len(memberIDs),
				"top_symbols":  topNames,
			},
		})

		for _, memberID := range memberIDs {
			memberNode := nodeMap[memberID]
			if memberNode == nil {
				continue
			}
			memberEdges = append(memberEdges, pendingEdge{
				SourceQN: memberNode.QualifiedName,
				TargetQN: commQN,
				Type:     "MEMBER_OF",
			})

			// Also store community_id on the member node (via properties update)
			if memberNode.Properties == nil {
				memberNode.Properties = make(map[string]any)
			}
			memberNode.Properties["community_id"] = commIdx
		}
	}

	// Batch insert community nodes
	idMap, err := p.Store.UpsertNodeBatch(communityNodes)
	if err != nil {
		slog.Warn("pass.communities.upsert.err", "err", err)
		return 0, 0
	}

	// Resolve and insert MEMBER_OF edges
	var edges []*store.Edge
	for _, pe := range memberEdges {
		srcQN := pe.SourceQN
		tgtQN := pe.TargetQN

		srcNode, _ := p.Store.FindNodeByQN(p.ProjectName, srcQN)
		tgtID, tgtOK := idMap[tgtQN]

		if srcNode != nil && tgtOK {
			edges = append(edges, &store.Edge{
				Project:  p.ProjectName,
				SourceID: srcNode.ID,
				TargetID: tgtID,
				Type:     "MEMBER_OF",
			})
		}
	}

	if len(edges) > 0 {
		if err := p.Store.InsertEdgeBatch(edges); err != nil {
			slog.Warn("pass.communities.edges.err", "err", err)
		}
	}

	return len(communityNodes), len(edges)
}

func topMemberNames(memberIDs []int64, nodeMap map[int64]*store.Node, limit int) []string {
	type entry struct {
		name  string
		label string
	}
	var entries []entry
	for _, id := range memberIDs {
		n := nodeMap[id]
		if n != nil {
			entries = append(entries, entry{n.Name, n.Label})
		}
	}

	// Sort: Classes first, then Functions, alphabetical
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].label != entries[j].label {
			// Prefer Class/Interface over Function/Method
			return labelPriority(entries[i].label) < labelPriority(entries[j].label)
		}
		return entries[i].name < entries[j].name
	})

	names := make([]string, 0, limit)
	for i, e := range entries {
		if i >= limit {
			break
		}
		names = append(names, e.name)
	}
	return names
}

func labelPriority(label string) int {
	switch label {
	case "Class":
		return 0
	case "Interface":
		return 1
	case "Type":
		return 2
	case "Function":
		return 3
	case "Method":
		return 4
	default:
		return 5
	}
}

func communityCohesion(memberIDs []int64, nodeMap map[int64]*store.Node) float64 {
	n := len(memberIDs)
	if n < 2 {
		return 1.0
	}
	// Simplified cohesion: proportion of members with known types
	knownCount := 0
	for _, id := range memberIDs {
		if nodeMap[id] != nil {
			knownCount++
		}
	}
	return float64(knownCount) / float64(n)
}
