package store

import (
	"fmt"
	"strings"
)

// TraverseResult holds BFS traversal results.
type TraverseResult struct {
	Root    *Node
	Visited []*NodeHop
	Edges   []EdgeInfo
}

// NodeHop is a node with its BFS hop distance.
type NodeHop struct {
	Node *Node
	Hop  int
}

// EdgeInfo is a simplified edge for output.
type EdgeInfo struct {
	FromName   string
	ToName     string
	Type       string
	Confidence float64
}

// BFS performs breadth-first traversal following edges of given types using a
// recursive CTE, replacing the previous per-node Go-side loop with a single
// SQL round-trip.
// direction: "outbound" follows source->target, "inbound" follows target->source.
// maxDepth caps the BFS depth, maxResults caps total visited nodes.
func (s *Store) BFS(startNodeID int64, direction string, edgeTypes []string, maxDepth, maxResults int) (*TraverseResult, error) {
	if maxDepth <= 0 {
		maxDepth = 3
	}
	if maxResults <= 0 {
		maxResults = 200
	}
	if len(edgeTypes) == 0 {
		edgeTypes = []string{"CALLS"}
	}

	// Build type filter placeholders
	typePlaceholders := make([]string, len(edgeTypes))
	typeArgs := make([]any, len(edgeTypes))
	for i, et := range edgeTypes {
		typePlaceholders[i] = "?"
		typeArgs[i] = et
	}
	typeFilter := strings.Join(typePlaceholders, ",")

	// Determine join columns based on direction
	var joinCol, nextCol string
	if direction == "inbound" {
		joinCol, nextCol = "target_id", "source_id"
	} else {
		joinCol, nextCol = "source_id", "target_id"
	}

	// Recursive CTE: traverse edges up to maxDepth hops, collect node IDs + edges
	query := fmt.Sprintf(`
		WITH RECURSIVE bfs(node_id, hop) AS (
			SELECT ?, 0
			UNION ALL
			SELECT e.%s, b.hop + 1
			FROM bfs b
			JOIN edges e ON e.%s = b.node_id AND e.type IN (%s)
			WHERE b.hop < ?
		)
		SELECT DISTINCT n.id, n.project, n.label, n.name, n.qualified_name,
			n.file_path, n.start_line, n.end_line, n.properties, bfs.hop
		FROM bfs
		JOIN nodes n ON n.id = bfs.node_id
		WHERE bfs.hop > 0
		ORDER BY bfs.hop, n.name
		LIMIT ?`,
		nextCol, joinCol, typeFilter)

	// Build args: startNodeID, ...typeArgs, maxDepth, maxResults
	args := make([]any, 0, 3+len(typeArgs))
	args = append(args, startNodeID)
	args = append(args, typeArgs...)
	args = append(args, maxDepth, maxResults)

	rows, err := s.q.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("bfs cte: %w", err)
	}
	defer rows.Close()

	result := &TraverseResult{}
	for rows.Next() {
		var n Node
		var props string
		var hop int
		if err := rows.Scan(&n.ID, &n.Project, &n.Label, &n.Name, &n.QualifiedName,
			&n.FilePath, &n.StartLine, &n.EndLine, &props, &hop); err != nil {
			return nil, err
		}
		n.Properties = unmarshalProps(props)
		result.Visited = append(result.Visited, &NodeHop{Node: &n, Hop: hop})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Collect edge info with a second CTE query that returns actual edges
	edgeQuery := fmt.Sprintf(`
		WITH RECURSIVE bfs(node_id, hop) AS (
			SELECT ?, 0
			UNION ALL
			SELECT e.%s, b.hop + 1
			FROM bfs b
			JOIN edges e ON e.%s = b.node_id AND e.type IN (%s)
			WHERE b.hop < ?
		)
		SELECT DISTINCT src.name, tgt.name, e.type,
			COALESCE(json_extract(e.properties, '$.confidence'), 0) as confidence
		FROM bfs b
		JOIN edges e ON e.%s = b.node_id AND e.type IN (%s)
		JOIN nodes src ON src.id = e.source_id
		JOIN nodes tgt ON tgt.id = e.target_id
		WHERE b.hop < ?`,
		nextCol, joinCol, typeFilter,
		joinCol, typeFilter)

	// Build edge args: startNodeID, ...typeArgs, maxDepth, ...typeArgs, maxDepth
	edgeArgs := make([]any, 0, 4+2*len(typeArgs))
	edgeArgs = append(edgeArgs, startNodeID)
	edgeArgs = append(edgeArgs, typeArgs...)
	edgeArgs = append(edgeArgs, maxDepth)
	edgeArgs = append(edgeArgs, typeArgs...)
	edgeArgs = append(edgeArgs, maxDepth)

	edgeRows, err := s.q.Query(edgeQuery, edgeArgs...)
	if err != nil {
		return nil, fmt.Errorf("bfs edges: %w", err)
	}
	defer edgeRows.Close()

	for edgeRows.Next() {
		var ei EdgeInfo
		if err := edgeRows.Scan(&ei.FromName, &ei.ToName, &ei.Type, &ei.Confidence); err != nil {
			return nil, err
		}
		result.Edges = append(result.Edges, ei)
	}

	return result, edgeRows.Err()
}

// neighborIDs returns the IDs of the nodes adjacent to nodeID along edges of the
// given types, in the given direction ("outbound" follows source->target,
// "inbound" follows target->source).
func (s *Store) neighborIDs(nodeID int64, direction string, edgeTypes []string) ([]int64, error) {
	var ids []int64
	for _, et := range edgeTypes {
		var edges []*Edge
		var err error
		if direction == "inbound" {
			edges, err = s.FindEdgesByTargetAndType(nodeID, et)
		} else {
			edges, err = s.FindEdgesBySourceAndType(nodeID, et)
		}
		if err != nil {
			return nil, err
		}
		for _, e := range edges {
			if direction == "inbound" {
				ids = append(ids, e.SourceID)
			} else {
				ids = append(ids, e.TargetID)
			}
		}
	}
	return ids, nil
}

// ReachableExcluding reports whether targetID is reachable from startID within
// maxDepth hops, following edges of the given types in the given direction,
// WITHOUT routing through any node in the exclude set. A node in exclude is a
// cut point: it is never entered, so no path may pass through it (the target
// itself is still detectable even if excluded).
//
// Taint analysis uses this to decide sanitization soundly: a (source, sink)
// pair is "sanitized" iff the sink is NOT reachable from the source once every
// sanitizer/auth_boundary node is excluded — i.e. EVERY path from source to
// sink crosses a sanitizer. If any sanitizer-free path exists, the pair is
// unsanitized. This replaces the prior heuristic, which flagged a pair
// sanitized whenever a sanitizer merely appeared anywhere in the BFS-reachable
// set and so produced false "sanitized" verdicts when the sanitizer sat on an
// unrelated branch.
func (s *Store) ReachableExcluding(startID, targetID int64, direction string, edgeTypes []string, maxDepth int, exclude map[int64]bool) (bool, error) {
	if startID == targetID {
		return true, nil
	}
	if maxDepth <= 0 {
		maxDepth = 3
	}
	if len(edgeTypes) == 0 {
		edgeTypes = []string{"CALLS"}
	}

	visited := map[int64]bool{startID: true}
	type item struct {
		id    int64
		depth int
	}
	queue := []item{{startID, 0}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth >= maxDepth {
			continue
		}
		neighbors, err := s.neighborIDs(cur.id, direction, edgeTypes)
		if err != nil {
			return false, err
		}
		for _, nid := range neighbors {
			if nid == targetID {
				return true, nil
			}
			if visited[nid] || exclude[nid] {
				continue
			}
			visited[nid] = true
			queue = append(queue, item{nid, cur.depth + 1})
		}
	}
	return false, nil
}

// ShortestPath returns the node-ID sequence of a shortest path (fewest hops)
// from startID to targetID following edges of the given types in the given
// direction, bounded by maxDepth, or nil if no such path exists within the
// bound. It is a BFS that records one parent pointer per discovered node and
// reconstructs the path by walking parents back from the target. It is used to
// produce a concrete witness path (e.g. to name the sanitizer on a sanitized
// taint path).
func (s *Store) ShortestPath(startID, targetID int64, direction string, edgeTypes []string, maxDepth int) ([]int64, error) {
	if startID == targetID {
		return []int64{startID}, nil
	}
	if maxDepth <= 0 {
		maxDepth = 3
	}
	if len(edgeTypes) == 0 {
		edgeTypes = []string{"CALLS"}
	}

	parent := map[int64]int64{startID: startID}
	type item struct {
		id    int64
		depth int
	}
	queue := []item{{startID, 0}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth >= maxDepth {
			continue
		}
		neighbors, err := s.neighborIDs(cur.id, direction, edgeTypes)
		if err != nil {
			return nil, err
		}
		for _, nid := range neighbors {
			if _, seen := parent[nid]; seen {
				continue
			}
			parent[nid] = cur.id
			if nid == targetID {
				path := []int64{nid}
				for path[len(path)-1] != startID {
					path = append(path, parent[path[len(path)-1]])
				}
				// Reverse into source->target order.
				for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
					path[i], path[j] = path[j], path[i]
				}
				return path, nil
			}
			queue = append(queue, item{nid, cur.depth + 1})
		}
	}
	return nil, nil
}
