package store

import (
	"context"
	"fmt"
	"strings"
)

// userIndexes lists all user-created indexes (excluding UNIQUE autoindexes on tables).
var userIndexes = []string{
	"idx_nodes_label",
	"idx_nodes_name",
	"idx_nodes_file",
	"idx_edges_source",
	"idx_edges_target",
	"idx_edges_type",
	"idx_edges_target_type",
	"idx_edges_source_type",
	"idx_edges_url_path",
}

// DropUserIndexes drops all user-created indexes for faster bulk writes.
// Honors ctx — callers cancelling a long-running bulk pass need the SQL
// to abort, not run to completion.
func (s *Store) DropUserIndexes(ctx context.Context) error {
	for _, idx := range userIndexes {
		if _, err := s.q.ExecContext(ctx, "DROP INDEX IF EXISTS "+idx); err != nil {
			return fmt.Errorf("drop index %s: %w", idx, err)
		}
	}
	return nil
}

// CreateUserIndexes recreates all user-created indexes (single sorted pass, O(N)).
// Honors ctx so a slow CREATE INDEX on a large table is cancellable.
func (s *Store) CreateUserIndexes(ctx context.Context) error {
	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_nodes_label ON nodes(project, label)",
		"CREATE INDEX IF NOT EXISTS idx_nodes_name ON nodes(project, name)",
		"CREATE INDEX IF NOT EXISTS idx_nodes_file ON nodes(project, file_path)",
		"CREATE INDEX IF NOT EXISTS idx_edges_source ON edges(source_id, type)",
		"CREATE INDEX IF NOT EXISTS idx_edges_target ON edges(target_id, type)",
		"CREATE INDEX IF NOT EXISTS idx_edges_type ON edges(project, type)",
		"CREATE INDEX IF NOT EXISTS idx_edges_target_type ON edges(project, target_id, type)",
		"CREATE INDEX IF NOT EXISTS idx_edges_source_type ON edges(project, source_id, type)",
		"CREATE INDEX IF NOT EXISTS idx_edges_url_path ON edges(project, url_path_gen)",
	}
	for _, ddl := range indexes {
		if _, err := s.q.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("create index: %w", err)
		}
	}
	return nil
}

// BulkInsertNodes inserts nodes in batches using plain INSERT (no ON CONFLICT).
// Assumes no duplicates exist for the project after a prior DELETE.
// Honors ctx — at the start of each chunk we check ctx.Err() so a cancel
// stops the next batch even before SQL would notice. The chunk itself
// also uses ExecContext so the in-flight statement aborts.
func (s *Store) BulkInsertNodes(ctx context.Context, nodes []*Node) error {
	for i := 0; i < len(nodes); i += nodesBatchSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := i + nodesBatchSize
		if end > len(nodes) {
			end = len(nodes)
		}
		if err := s.bulkInsertNodeChunk(ctx, nodes[i:end]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) bulkInsertNodeChunk(ctx context.Context, batch []*Node) error {
	var sb strings.Builder
	sb.WriteString("INSERT INTO nodes (project, label, name, qualified_name, file_path, start_line, end_line, properties) VALUES ")

	args := make([]any, 0, len(batch)*numNodeCols)
	for i, n := range batch {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString("(?,?,?,?,?,?,?,?)")
		args = append(args, n.Project, n.Label, n.Name, n.QualifiedName, n.FilePath, n.StartLine, n.EndLine, marshalProps(n.Properties))
	}

	if _, err := s.q.ExecContext(ctx, sb.String(), args...); err != nil {
		return fmt.Errorf("bulk insert nodes: %w", err)
	}
	return nil
}

// BulkInsertEdges inserts edges in batches using plain INSERT (no ON CONFLICT).
// Assumes no duplicates exist for the project after a prior DELETE.
// Honors ctx the same way BulkInsertNodes does.
func (s *Store) BulkInsertEdges(ctx context.Context, edges []*Edge) error {
	for i := 0; i < len(edges); i += edgesBatchSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := i + edgesBatchSize
		if end > len(edges) {
			end = len(edges)
		}
		if err := s.bulkInsertEdgeChunk(ctx, edges[i:end]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) bulkInsertEdgeChunk(ctx context.Context, batch []*Edge) error {
	var sb strings.Builder
	sb.WriteString("INSERT INTO edges (project, source_id, target_id, type, properties) VALUES ")

	args := make([]any, 0, len(batch)*numEdgeCols)
	for i, e := range batch {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString("(?,?,?,?,?)")
		args = append(args, e.Project, e.SourceID, e.TargetID, e.Type, marshalProps(e.Properties))
	}

	if _, err := s.q.ExecContext(ctx, sb.String(), args...); err != nil {
		return fmt.Errorf("bulk insert edges: %w", err)
	}
	return nil
}

// LoadNodeIDMap returns a map of qualified_name → SQLite ID for all nodes in a project.
func (s *Store) LoadNodeIDMap(ctx context.Context, project string) (map[string]int64, error) {
	rows, err := s.q.QueryContext(ctx, "SELECT id, qualified_name FROM nodes WHERE project=?", project)
	if err != nil {
		return nil, fmt.Errorf("load node id map: %w", err)
	}
	defer rows.Close()

	result := make(map[string]int64)
	for rows.Next() {
		var id int64
		var qn string
		if err := rows.Scan(&id, &qn); err != nil {
			return nil, err
		}
		result[qn] = id
	}
	return result, rows.Err()
}
