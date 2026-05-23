package store

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

// Edge confidence tier. Stored in Edge.Properties["confidence_tier"]; the
// generated column `confidence_tier_gen` on the edges table (see store.go
// initSchema) surfaces this value as a first-class, indexable field so
// Cypher queries can filter with `WHERE r.confidence_tier = 'X'`. Absent
// properties default to EXTRACTED at the column level via COALESCE.
//
// Note on naming: the property key is `confidence_tier` (not `confidence`)
// to avoid collision with the pre-existing numeric `confidence` property
// used by configlink_strategies.go for per-strategy heuristic scores.
// The tier is categorical; the legacy score is continuous.
//
// EXTRACTED
//
//	Direct, source-proven relationship. The AST literally says the
//	edge exists (a function call expression, an import statement, a
//	class definition). This is the default for any edge whose creator
//	does not set a confidence_tier property.
//
// INFERRED
//
//	Relationship deduced via static reasoning beyond the raw AST:
//	interface satisfaction by method-set matching, inherited methods
//	across class hierarchies, an HTTP caller matched to a route
//	handler, a test function matched to its production target via a
//	naming heuristic. Still high-signal, but a grammar change or a
//	refactor could invalidate it.
//
// AMBIGUOUS
//
//	Relationship asserted via a fuzzy match that may be wrong. A
//	config file whose values happen to match a variable name, a git
//	file-coupling metric below a high-confidence threshold, a
//	parameterized URL path that could match multiple routes. Useful
//	for suggestion-surface tools; filter these out for automated
//	blast-radius calculations.
const (
	ConfidenceExtracted = "EXTRACTED"
	ConfidenceInferred  = "INFERRED"
	ConfidenceAmbiguous = "AMBIGUOUS"
)

// ConfidenceTier returns the stored confidence tier for an edge, defaulting
// to EXTRACTED when the property is absent (e.g. edges created before this
// column was introduced, or by passes that do not set it).
func (e *Edge) ConfidenceTier() string {
	if e == nil || e.Properties == nil {
		return ConfidenceExtracted
	}
	if v, ok := e.Properties["confidence_tier"].(string); ok && v != "" {
		return v
	}
	return ConfidenceExtracted
}

// InsertEdge inserts an edge (dedup by source_id, target_id, type).
func (s *Store) InsertEdge(e *Edge) (int64, error) {
	res, err := s.q.Exec(`
		INSERT INTO edges (project, source_id, target_id, type, properties)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(source_id, target_id, type) DO UPDATE SET properties=json_patch(properties, excluded.properties)`,
		e.Project, e.SourceID, e.TargetID, e.Type, marshalProps(e.Properties))
	if err != nil {
		return 0, fmt.Errorf("insert edge: %w", err)
	}
	return res.LastInsertId()
}

// FindEdgesBySource finds all edges from a given source node.
func (s *Store) FindEdgesBySource(sourceID int64) ([]*Edge, error) {
	rows, err := s.q.Query(`SELECT id, project, source_id, target_id, type, properties
		FROM edges WHERE source_id=?`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("find edges by source: %w", err)
	}
	defer rows.Close()
	return scanEdges(rows)
}

// FindEdgesByTarget finds all edges to a given target node.
func (s *Store) FindEdgesByTarget(targetID int64) ([]*Edge, error) {
	rows, err := s.q.Query(`SELECT id, project, source_id, target_id, type, properties
		FROM edges WHERE target_id=?`, targetID)
	if err != nil {
		return nil, fmt.Errorf("find edges by target: %w", err)
	}
	defer rows.Close()
	return scanEdges(rows)
}

// FindEdgesBySourceAndType finds edges from a source with a specific type.
func (s *Store) FindEdgesBySourceAndType(sourceID int64, edgeType string) ([]*Edge, error) {
	rows, err := s.q.Query(`SELECT id, project, source_id, target_id, type, properties
		FROM edges WHERE source_id=? AND type=?`, sourceID, edgeType)
	if err != nil {
		return nil, fmt.Errorf("find edges by source+type: %w", err)
	}
	defer rows.Close()
	return scanEdges(rows)
}

// FindEdgesByTargetAndType finds edges to a target with a specific type.
func (s *Store) FindEdgesByTargetAndType(targetID int64, edgeType string) ([]*Edge, error) {
	rows, err := s.q.Query(`SELECT id, project, source_id, target_id, type, properties
		FROM edges WHERE target_id=? AND type=?`, targetID, edgeType)
	if err != nil {
		return nil, fmt.Errorf("find edges by target+type: %w", err)
	}
	defer rows.Close()
	return scanEdges(rows)
}

// FindEdgesByType returns all edges of a given type for a project.
func (s *Store) FindEdgesByType(project, edgeType string) ([]*Edge, error) {
	rows, err := s.q.Query(`SELECT id, project, source_id, target_id, type, properties
		FROM edges WHERE project=? AND type=?`, project, edgeType)
	if err != nil {
		return nil, fmt.Errorf("find edges by type: %w", err)
	}
	defer rows.Close()
	return scanEdges(rows)
}

// CountEdges returns the number of edges in a project.
func (s *Store) CountEdges(project string) (int, error) {
	var count int
	err := s.q.QueryRow("SELECT COUNT(*) FROM edges WHERE project=?", project).Scan(&count)
	return count, err
}

// AllEdges returns every edge in a project. Used by whole-graph algorithms
// (PageRank, centrality) that need to load the full edge set into memory.
// For large projects consumers should prefer streaming or type-scoped reads.
func (s *Store) AllEdges(project string) ([]*Edge, error) {
	rows, err := s.q.Query(`SELECT id, project, source_id, target_id, type, properties
		FROM edges WHERE project=?`, project)
	if err != nil {
		return nil, fmt.Errorf("all edges: %w", err)
	}
	defer rows.Close()
	return scanEdges(rows)
}

// DeleteEdgesByProject deletes all edges for a project.
func (s *Store) DeleteEdgesByProject(project string) error {
	_, err := s.q.Exec("DELETE FROM edges WHERE project=?", project)
	return err
}

// CountEdgesByType returns the number of edges of a given type for a project.
func (s *Store) CountEdgesByType(project, edgeType string) (int, error) {
	var count int
	err := s.q.QueryRow("SELECT COUNT(*) FROM edges WHERE project=? AND type=?", project, edgeType).Scan(&count)
	return count, err
}

// EdgeCountsByType returns edge counts grouped by edge type.
func (s *Store) EdgeCountsByType(project string) (map[string]int, error) {
	rows, err := s.q.Query(`SELECT type, COUNT(*) FROM edges WHERE project=? GROUP BY type ORDER BY COUNT(*) DESC`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]int)
	for rows.Next() {
		var edgeType string
		var count int
		if err := rows.Scan(&edgeType, &count); err != nil {
			return nil, err
		}
		result[edgeType] = count
	}
	return result, rows.Err()
}

// CallsResolverRuleStats returns CALLS edge counts grouped by resolver_rule.
// Reads the indexed resolver_rule_gen generated column, so the aggregation
// stays cheap (O(distinct rules) seeks) even on graphs with millions of
// edges. Rows whose resolver_rule property is missing land in the "unset"
// bucket — these are typically pre-migration edges from older indexes or
// edges emitted by passes that don't categorize (HTTP_CALLS, INHERITS,
// etc., which CALLS rows shouldn't contain). The output is suitable for
// surfacing in index_health so operators can see precision-distribution
// at a glance ("how many edges came from cross-package-suffix vs
// cross-package-import-map?") without scanning JSON properties.
func (s *Store) CallsResolverRuleStats(project string) (map[string]int, error) {
	rows, err := s.q.Query(`
		SELECT COALESCE(resolver_rule_gen, 'unset') AS rule, COUNT(*) AS cnt
		FROM edges WHERE project=? AND type='CALLS'
		GROUP BY rule ORDER BY cnt DESC`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]int)
	for rows.Next() {
		var rule string
		var count int
		if err := rows.Scan(&rule, &count); err != nil {
			return nil, err
		}
		result[rule] = count
	}
	return result, rows.Err()
}

// CallsConfidenceTierStats returns CALLS edge counts grouped by
// confidence_tier (EXTRACTED, HIGH, MEDIUM, LOW, SPECULATIVE, …). Same
// shape as CallsResolverRuleStats; uses the indexed confidence_tier_gen
// generated column. Gives operators a quick "how risky is this graph?"
// summary — a graph dominated by SPECULATIVE has different reliability
// characteristics than one dominated by HIGH, even if total edge count
// is the same.
func (s *Store) CallsConfidenceTierStats(project string) (map[string]int, error) {
	rows, err := s.q.Query(`
		SELECT COALESCE(confidence_tier_gen, 'unset') AS tier, COUNT(*) AS cnt
		FROM edges WHERE project=? AND type='CALLS'
		GROUP BY tier ORDER BY cnt DESC`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]int)
	for rows.Next() {
		var tier string
		var count int
		if err := rows.Scan(&tier, &count); err != nil {
			return nil, err
		}
		result[tier] = count
	}
	return result, rows.Err()
}

// CallsResolutionStats returns CALLS edge counts grouped by resolution_strategy.
func (s *Store) CallsResolutionStats(project string) (map[string]int, error) {
	rows, err := s.q.Query(`
		SELECT json_extract(properties, '$.resolution_strategy') AS strategy, COUNT(*) AS cnt
		FROM edges WHERE project=? AND type='CALLS'
		GROUP BY strategy ORDER BY cnt DESC`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]int)
	for rows.Next() {
		var strategy sql.NullString
		var count int
		if err := rows.Scan(&strategy, &count); err != nil {
			return nil, err
		}
		key := strategy.String
		if key == "" {
			key = "unset"
		}
		result[key] = count
	}
	return result, rows.Err()
}

// FindCallerFilesOfTargetsInFiles returns the set of file paths
// containing source nodes for any edge of one of the given types whose
// target node's file_path is in targetFilePaths.
//
// This is the building block for Plan 1 Phase 3's invalidation
// rewrite. Today findDependentFiles only walks one hop of the import
// graph (callers of changed modules); it misses cases where a call
// site's resolution depended on a changed function in a different
// module that the caller's module DIDN'T import directly (transitive
// callers, type-based dispatch, stranded handlers). This helper lets
// the pipeline ask the question directly: "Which files contain
// edges pointing AT functions in the changed files?" The result is
// the union of those caller-files; if their target's resolution may
// have shifted, we re-resolve them.
//
// Uses idx_nodes_file (project, file_path) to scope the target side
// and idx_edges_target_type (project, target_id, type) on the edge
// join — both present from initial schema, no migration needed. Edge
// types are filtered explicitly rather than IN() so the planner can
// use the composite index; the typical caller passes
// ["CALLS", "USES", "HTTP_CALLS"].
//
// Returns a deduped, sorted slice of relative file paths. Empty
// targetFilePaths or edgeTypes returns nil without querying.
func (s *Store) FindCallerFilesOfTargetsInFiles(project string, targetFilePaths, edgeTypes []string) ([]string, error) {
	if len(targetFilePaths) == 0 || len(edgeTypes) == 0 {
		return nil, nil
	}

	pathPH := strings.Repeat("?,", len(targetFilePaths))
	pathPH = pathPH[:len(pathPH)-1]
	typePH := strings.Repeat("?,", len(edgeTypes))
	typePH = typePH[:len(typePH)-1]

	args := make([]any, 0, 1+len(targetFilePaths)+1+len(edgeTypes))
	args = append(args, project)
	for _, p := range targetFilePaths {
		args = append(args, p)
	}
	args = append(args, project)
	for _, t := range edgeTypes {
		args = append(args, t)
	}

	q := fmt.Sprintf(`
		SELECT DISTINCT src.file_path
		FROM nodes tgt
		JOIN edges e ON e.target_id = tgt.id AND e.project = tgt.project
		JOIN nodes src ON src.id = e.source_id AND src.project = e.project
		WHERE tgt.project = ?
		  AND tgt.file_path IN (%s)
		  AND e.project = ?
		  AND e.type IN (%s)
		  AND src.file_path != ''
		ORDER BY src.file_path`, pathPH, typePH)

	rows, err := s.q.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("find caller files: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var fp string
		if err := rows.Scan(&fp); err != nil {
			return nil, err
		}
		out = append(out, fp)
	}
	return out, rows.Err()
}

// DeleteEdgesByType deletes all edges of a given type for a project.
func (s *Store) DeleteEdgesByType(project, edgeType string) error {
	_, err := s.q.Exec("DELETE FROM edges WHERE project=? AND type=?", project, edgeType)
	return err
}

// DeleteEdgesBySourceFile deletes edges of a given type where the source node
// belongs to a specific file. Used for incremental re-indexing of CALLS edges.
func (s *Store) DeleteEdgesBySourceFile(project, filePath, edgeType string) error {
	_, err := s.q.Exec(`
		DELETE FROM edges WHERE id IN (
			SELECT e.id FROM edges e
			JOIN nodes n ON e.source_id = n.id
			WHERE e.project=? AND n.file_path=? AND e.type=?
		)`, project, filePath, edgeType)
	return err
}

// FindEdgesByURLPath returns edges where url_path contains the given substring.
// Uses the generated column index for prefix matches, falls back to json_extract for substring.
func (s *Store) FindEdgesByURLPath(project, pathSubstring string) ([]*Edge, error) {
	rows, err := s.q.Query(`
		SELECT id, project, source_id, target_id, type, properties
		FROM edges
		WHERE project = ? AND url_path_gen LIKE ?`,
		project, "%"+pathSubstring+"%")
	if err != nil {
		return nil, fmt.Errorf("find edges by url_path: %w", err)
	}
	defer rows.Close()
	return scanEdges(rows)
}

// Formula-derived batch size: SQLite has a 999 bind variable limit.
const numEdgeCols = 5
const edgesBatchSize = 999 / numEdgeCols // = 199

// InsertEdgeBatch inserts multiple edges in batched multi-row INSERTs.
func (s *Store) InsertEdgeBatch(edges []*Edge) error {
	if len(edges) == 0 {
		return nil
	}

	for i := 0; i < len(edges); i += edgesBatchSize {
		end := i + edgesBatchSize
		if end > len(edges) {
			end = len(edges)
		}
		if err := s.insertEdgeChunk(edges[i:end]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) insertEdgeChunk(batch []*Edge) error {
	var sb strings.Builder
	sb.WriteString(`INSERT INTO edges (project, source_id, target_id, type, properties) VALUES `)

	args := make([]any, 0, len(batch)*5)
	for i, e := range batch {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString("(?,?,?,?,?)")
		args = append(args, e.Project, e.SourceID, e.TargetID, e.Type, marshalProps(e.Properties))
	}
	sb.WriteString(` ON CONFLICT(source_id, target_id, type) DO UPDATE SET properties=json_patch(properties, excluded.properties)`)

	_, err := s.q.Exec(sb.String(), args...)
	if err == nil {
		return nil
	}

	// Batch failed (likely FK constraint from stale LastInsertId) — fall back
	// to one-at-a-time inserts so one bad edge doesn't break the entire batch.
	skipped := 0
	for _, e := range batch {
		if _, err2 := s.q.Exec(`
			INSERT INTO edges (project, source_id, target_id, type, properties)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(source_id, target_id, type) DO UPDATE SET properties=json_patch(properties, excluded.properties)`,
			e.Project, e.SourceID, e.TargetID, e.Type, marshalProps(e.Properties)); err2 != nil {
			skipped++
		}
	}
	if skipped > 0 {
		slog.Info("edges.batch.fk_skip", "skipped", skipped, "total", len(batch))
	}
	return nil
}

// FindEdgesBySourceIDs returns all edges where source_id is in the given set,
// optionally filtered by edge types. Groups results by source_id for efficient lookup.
func (s *Store) FindEdgesBySourceIDs(sourceIDs []int64, edgeTypes []string) (map[int64][]*Edge, error) {
	if len(sourceIDs) == 0 {
		return map[int64][]*Edge{}, nil
	}

	result := make(map[int64][]*Edge, len(sourceIDs))
	const batchSize = 500 // leave room for type args

	for i := 0; i < len(sourceIDs); i += batchSize {
		end := i + batchSize
		if end > len(sourceIDs) {
			end = len(sourceIDs)
		}
		chunk := sourceIDs[i:end]

		placeholders := make([]string, len(chunk))
		args := make([]any, 0, len(chunk)+len(edgeTypes))
		for j, id := range chunk {
			placeholders[j] = "?"
			args = append(args, id)
		}

		query := fmt.Sprintf(
			"SELECT id, project, source_id, target_id, type, properties FROM edges WHERE source_id IN (%s)",
			strings.Join(placeholders, ","))

		if len(edgeTypes) > 0 {
			typePH := make([]string, len(edgeTypes))
			for j, et := range edgeTypes {
				typePH[j] = "?"
				args = append(args, et)
			}
			query += " AND type IN (" + strings.Join(typePH, ",") + ")"
		}

		if err := func() error {
			rows, err := s.q.Query(query, args...)
			if err != nil {
				return fmt.Errorf("find edges by source ids: %w", err)
			}
			defer rows.Close()
			edges, err := scanEdges(rows)
			if err != nil {
				return err
			}
			for _, e := range edges {
				result[e.SourceID] = append(result[e.SourceID], e)
			}
			return nil
		}(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// FindEdgesByTargetIDs returns all edges where target_id is in the given set,
// optionally filtered by edge types. Groups results by target_id.
func (s *Store) FindEdgesByTargetIDs(targetIDs []int64, edgeTypes []string) (map[int64][]*Edge, error) {
	if len(targetIDs) == 0 {
		return map[int64][]*Edge{}, nil
	}

	result := make(map[int64][]*Edge, len(targetIDs))
	const batchSize = 500

	for i := 0; i < len(targetIDs); i += batchSize {
		end := i + batchSize
		if end > len(targetIDs) {
			end = len(targetIDs)
		}
		chunk := targetIDs[i:end]

		placeholders := make([]string, len(chunk))
		args := make([]any, 0, len(chunk)+len(edgeTypes))
		for j, id := range chunk {
			placeholders[j] = "?"
			args = append(args, id)
		}

		query := fmt.Sprintf(
			"SELECT id, project, source_id, target_id, type, properties FROM edges WHERE target_id IN (%s)",
			strings.Join(placeholders, ","))

		if len(edgeTypes) > 0 {
			typePH := make([]string, len(edgeTypes))
			for j, et := range edgeTypes {
				typePH[j] = "?"
				args = append(args, et)
			}
			query += " AND type IN (" + strings.Join(typePH, ",") + ")"
		}

		if err := func() error {
			rows, err := s.q.Query(query, args...)
			if err != nil {
				return fmt.Errorf("find edges by target ids: %w", err)
			}
			defer rows.Close()
			edges, err := scanEdges(rows)
			if err != nil {
				return err
			}
			for _, e := range edges {
				result[e.TargetID] = append(result[e.TargetID], e)
			}
			return nil
		}(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// NodeDegree returns inbound and outbound CALLS-family edge counts for a node.
// Includes CALLS, CALLS_EXTERNAL (real-to-stub), and CALLS_PSEUDO (module-default
// caller) so degree reflects the same surface users saw before the type split.
func (s *Store) NodeDegree(nodeID int64) (inbound, outbound int) {
	const inSQL = "SELECT COUNT(*) FROM edges WHERE target_id=? AND type IN ('CALLS','CALLS_EXTERNAL','CALLS_PSEUDO')"
	const outSQL = "SELECT COUNT(*) FROM edges WHERE source_id=? AND type IN ('CALLS','CALLS_EXTERNAL','CALLS_PSEUDO')"
	_ = s.q.QueryRow(inSQL, nodeID).Scan(&inbound)
	_ = s.q.QueryRow(outSQL, nodeID).Scan(&outbound)
	return
}

// NodeNeighborNames returns the names of callers and callees for a node,
// considering CALLS-family (CALLS, CALLS_EXTERNAL, CALLS_PSEUDO), HTTP_CALLS,
// and ASYNC_CALLS edge types.
func (s *Store) NodeNeighborNames(nodeID int64, limit int) (callerNames, calleeNames []string) {
	callerNames = queryNeighborNames(s.q,
		`SELECT DISTINCT n.name FROM edges e JOIN nodes n ON e.source_id = n.id
		 WHERE e.target_id = ? AND e.type IN ('CALLS','CALLS_EXTERNAL','CALLS_PSEUDO','HTTP_CALLS','ASYNC_CALLS')
		 ORDER BY n.name LIMIT ?`, nodeID, limit)
	calleeNames = queryNeighborNames(s.q,
		`SELECT DISTINCT n.name FROM edges e JOIN nodes n ON e.target_id = n.id
		 WHERE e.source_id = ? AND e.type IN ('CALLS','CALLS_EXTERNAL','CALLS_PSEUDO','HTTP_CALLS','ASYNC_CALLS')
		 ORDER BY n.name LIMIT ?`, nodeID, limit)
	return
}

// queryNeighborNames runs a query returning a single name column.
func queryNeighborNames(q Querier, query string, args ...any) []string {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil
	}
	return names
}

func scanEdges(rows *sql.Rows) ([]*Edge, error) {
	var result []*Edge
	for rows.Next() {
		var e Edge
		var props string
		if err := rows.Scan(&e.ID, &e.Project, &e.SourceID, &e.TargetID, &e.Type, &props); err != nil {
			return nil, err
		}
		e.Properties = unmarshalProps(props)
		result = append(result, &e)
	}
	return result, rows.Err()
}
