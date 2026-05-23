package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// UpsertNode inserts or replaces a node (dedup by qualified_name) and
// returns the node's row id.
//
// Uses SQLite's RETURNING clause (available since 3.35, March 2021) so
// the id is read directly from the row written or updated by this
// statement. The pre-RETURNING path used LastInsertId() with a fallback
// SELECT when it returned 0, and accepted "occasional FK failures in
// downstream edge inserts" when LastInsertId returned a stale (non-zero)
// id on a conflict. RETURNING closes that race: SQLite emits one row
// per inserted or updated row, so the id is always the id of the row
// this statement just touched, regardless of conflict behavior.
func (s *Store) UpsertNode(n *Node) (int64, error) {
	var id int64
	err := s.q.QueryRow(`
		INSERT INTO nodes (project, label, name, qualified_name, file_path, start_line, end_line, properties)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project, qualified_name) DO UPDATE SET
			label=excluded.label, name=excluded.name, file_path=excluded.file_path,
			start_line=excluded.start_line, end_line=excluded.end_line, properties=excluded.properties
		RETURNING id`,
		n.Project, n.Label, n.Name, n.QualifiedName, n.FilePath, n.StartLine, n.EndLine, marshalProps(n.Properties)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert node: %w", err)
	}
	return id, nil
}

// FindNodeByID finds a node by its primary key ID.
func (s *Store) FindNodeByID(id int64) (*Node, error) {
	row := s.q.QueryRow(`SELECT id, project, label, name, qualified_name, file_path, start_line, end_line, properties
		FROM nodes WHERE id=?`, id)
	return scanNode(row)
}

// FindNodeByQN finds a node by project and qualified name.
func (s *Store) FindNodeByQN(project, qualifiedName string) (*Node, error) {
	row := s.q.QueryRow(`SELECT id, project, label, name, qualified_name, file_path, start_line, end_line, properties
		FROM nodes WHERE project=? AND qualified_name=?`, project, qualifiedName)
	return scanNode(row)
}

// FindNodesByName finds nodes by project and name.
func (s *Store) FindNodesByName(project, name string) ([]*Node, error) {
	rows, err := s.q.Query(`SELECT id, project, label, name, qualified_name, file_path, start_line, end_line, properties
		FROM nodes WHERE project=? AND name=?`, project, name)
	if err != nil {
		return nil, fmt.Errorf("find by name: %w", err)
	}
	defer rows.Close()
	return scanNodes(rows)
}

// FindNodesByQNSuffix finds nodes whose qualified_name ends with "."+suffix.
// Matches at QN segment boundaries to prevent partial word matches.
func (s *Store) FindNodesByQNSuffix(project, suffix string) ([]*Node, error) {
	rows, err := s.q.Query(`SELECT id, project, label, name, qualified_name, file_path, start_line, end_line, properties
		FROM nodes WHERE project=? AND qualified_name LIKE ?`,
		project, "%."+suffix)
	if err != nil {
		return nil, fmt.Errorf("find by qn suffix: %w", err)
	}
	defer rows.Close()
	return scanNodes(rows)
}

// FindNodesByLabel finds all nodes with a given label in a project.
func (s *Store) FindNodesByLabel(project, label string) ([]*Node, error) {
	rows, err := s.q.Query(`SELECT id, project, label, name, qualified_name, file_path, start_line, end_line, properties
		FROM nodes WHERE project=? AND label=?`, project, label)
	if err != nil {
		return nil, fmt.Errorf("find by label: %w", err)
	}
	defer rows.Close()
	return scanNodes(rows)
}

// FindNodesByFile finds all nodes in a given file.
func (s *Store) FindNodesByFile(project, filePath string) ([]*Node, error) {
	rows, err := s.q.Query(`SELECT id, project, label, name, qualified_name, file_path, start_line, end_line, properties
		FROM nodes WHERE project=? AND file_path=?`, project, filePath)
	if err != nil {
		return nil, fmt.Errorf("find by file: %w", err)
	}
	defer rows.Close()
	return scanNodes(rows)
}

// CountNodes returns the number of nodes in a project.
func (s *Store) CountNodes(project string) (int, error) {
	var count int
	err := s.q.QueryRow("SELECT COUNT(*) FROM nodes WHERE project=?", project).Scan(&count)
	return count, err
}

// DeleteNodesByProject deletes all nodes for a project.
func (s *Store) DeleteNodesByProject(project string) error {
	_, err := s.q.Exec("DELETE FROM nodes WHERE project=?", project)
	return err
}

// DeleteNodesByFile deletes all nodes for a specific file in a project.
func (s *Store) DeleteNodesByFile(project, filePath string) error {
	_, err := s.q.Exec("DELETE FROM nodes WHERE project=? AND file_path=?", project, filePath)
	return err
}

// DeleteNodesByLabel deletes all nodes with a given label in a project.
func (s *Store) DeleteNodesByLabel(project, label string) error {
	_, err := s.q.Exec("DELETE FROM nodes WHERE project=? AND label=?", project, label)
	return err
}

// FindNodesByIDs returns a map of nodeID → *Node for the given IDs.
func (s *Store) FindNodesByIDs(ids []int64) (map[int64]*Node, error) {
	if len(ids) == 0 {
		return map[int64]*Node{}, nil
	}
	result := make(map[int64]*Node, len(ids))
	const batchSize = 998 // leave room under 999 limit

	for i := 0; i < len(ids); i += batchSize {
		end := i + batchSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[i:end]

		placeholders := make([]string, len(chunk))
		args := make([]any, len(chunk))
		for j, id := range chunk {
			placeholders[j] = "?"
			args[j] = id
		}

		query := fmt.Sprintf(
			"SELECT id, project, label, name, qualified_name, file_path, start_line, end_line, properties FROM nodes WHERE id IN (%s)",
			strings.Join(placeholders, ","))

		if err := func() error {
			rows, err := s.q.Query(query, args...)
			if err != nil {
				return fmt.Errorf("find nodes by ids: %w", err)
			}
			defer rows.Close()
			nodes, err := scanNodes(rows)
			if err != nil {
				return err
			}
			for _, n := range nodes {
				result[n.ID] = n
			}
			return nil
		}(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// SetNodeIntProperty atomically sets a single integer property on a node by
// project + qualified_name. Uses SQLite's json_set so the rest of the
// properties map is preserved. Returns the number of rows updated (0 if
// the node doesn't exist).
//
// Added 2026-05-02 for the unresolved_call_count diagnostic. Generalized so
// future int counters (e.g. cyclomatic_call_count, dispatch_count) can use
// the same path without re-implementing read-modify-write.
func (s *Store) SetNodeIntProperty(project, qualifiedName, key string, value int) (int64, error) {
	path := "$." + key
	res, err := s.q.Exec(`UPDATE nodes SET properties = json_set(COALESCE(properties, '{}'), ?, ?)
		WHERE project=? AND qualified_name=?`, path, value, project, qualifiedName)
	if err != nil {
		return 0, fmt.Errorf("set node int property %s: %w", key, err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// FindNodesByFileOverlap returns nodes whose line range overlaps [startLine, endLine].
// The fileSuffix is matched with LIKE '%' || ? against the file_path column to handle
// relative/absolute path differences.
func (s *Store) FindNodesByFileOverlap(project, fileSuffix string, startLine, endLine int) ([]*Node, error) {
	rows, err := s.q.Query(`SELECT id, project, label, name, qualified_name, file_path, start_line, end_line, properties
		FROM nodes WHERE project=? AND file_path LIKE '%' || ? AND start_line <= ? AND end_line >= ?
		AND label NOT IN ('Project', 'Package', 'Folder', 'File', 'Module')`,
		project, fileSuffix, endLine, startLine)
	if err != nil {
		return nil, fmt.Errorf("find by file overlap: %w", err)
	}
	defer rows.Close()
	return scanNodes(rows)
}

// AllNodes returns all nodes for a project.
func (s *Store) AllNodes(project string) ([]*Node, error) {
	rows, err := s.q.Query(`SELECT id, project, label, name, qualified_name, file_path, start_line, end_line, properties
		FROM nodes WHERE project=?`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNodes(rows)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanNode(row scanner) (*Node, error) {
	var n Node
	var props string
	err := row.Scan(&n.ID, &n.Project, &n.Label, &n.Name, &n.QualifiedName, &n.FilePath, &n.StartLine, &n.EndLine, &props)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	n.Properties = unmarshalProps(props)
	return &n, nil
}

func scanNodes(rows *sql.Rows) ([]*Node, error) {
	var result []*Node
	for rows.Next() {
		var n Node
		var props string
		if err := rows.Scan(&n.ID, &n.Project, &n.Label, &n.Name, &n.QualifiedName, &n.FilePath, &n.StartLine, &n.EndLine, &props); err != nil {
			return nil, err
		}
		n.Properties = unmarshalProps(props)
		result = append(result, &n)
	}
	return result, rows.Err()
}

// FindNodesByProperty finds nodes with a specific JSON property value.
// If label is non-empty, also filters by label.
func (s *Store) FindNodesByProperty(project, label, propKey, propValue string) ([]*Node, error) {
	var query string
	var args []any
	if label != "" {
		query = `SELECT id, project, label, name, qualified_name, file_path, start_line, end_line, properties
			FROM nodes WHERE project=? AND label=? AND json_extract(properties, '$.' || ?) = ?`
		args = []any{project, label, propKey, propValue}
	} else {
		query = `SELECT id, project, label, name, qualified_name, file_path, start_line, end_line, properties
			FROM nodes WHERE project=? AND json_extract(properties, '$.' || ?) = ?`
		args = []any{project, propKey, propValue}
	}
	rows, err := s.q.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("find nodes by property: %w", err)
	}
	defer rows.Close()
	return scanNodes(rows)
}

// Formula-derived batch size: SQLite has a 999 bind variable limit.
const numNodeCols = 8
const nodesBatchSize = 999 / numNodeCols // = 124

// UpsertNodeBatch inserts or updates multiple nodes in batched multi-row INSERTs.
// Returns a map of qualifiedName → ID for all upserted nodes.
func (s *Store) UpsertNodeBatch(nodes []*Node) (map[string]int64, error) {
	if len(nodes) == 0 {
		return map[string]int64{}, nil
	}

	result := make(map[string]int64, len(nodes))

	for i := 0; i < len(nodes); i += nodesBatchSize {
		end := i + nodesBatchSize
		if end > len(nodes) {
			end = len(nodes)
		}
		batch := nodes[i:end]

		if err := s.upsertNodeChunk(batch, result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *Store) upsertNodeChunk(batch []*Node, idMap map[string]int64) error {
	// Build multi-row INSERT
	var sb strings.Builder
	sb.WriteString(`INSERT INTO nodes (project, label, name, qualified_name, file_path, start_line, end_line, properties) VALUES `)

	args := make([]any, 0, len(batch)*8)
	for i, n := range batch {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString("(?,?,?,?,?,?,?,?)")
		args = append(args, n.Project, n.Label, n.Name, n.QualifiedName, n.FilePath, n.StartLine, n.EndLine, marshalProps(n.Properties))
	}
	sb.WriteString(` ON CONFLICT(project, qualified_name) DO UPDATE SET
		label=excluded.label, name=excluded.name, file_path=excluded.file_path,
		start_line=excluded.start_line, end_line=excluded.end_line, properties=excluded.properties`)

	if _, err := s.q.Exec(sb.String(), args...); err != nil {
		return fmt.Errorf("upsert node batch: %w", err)
	}

	// Recover IDs via SELECT ... IN (...)
	// Group by project since the UNIQUE constraint is (project, qualified_name)
	byProject := make(map[string][]string)
	for _, n := range batch {
		byProject[n.Project] = append(byProject[n.Project], n.QualifiedName)
	}

	for project, qns := range byProject {
		if err := s.resolveNodeIDs(project, qns, idMap); err != nil {
			return err
		}
	}
	return nil
}

// resolveNodeIDs fetches IDs for a set of qualified names in a single project.
// Respects the 999-var limit by batching the IN clause.
func (s *Store) resolveNodeIDs(project string, qns []string, idMap map[string]int64) error {
	// 1 var for project + N vars for QNs; batch to stay under 999
	const maxQNsPerQuery = 998

	for i := 0; i < len(qns); i += maxQNsPerQuery {
		end := i + maxQNsPerQuery
		if end > len(qns) {
			end = len(qns)
		}
		chunk := qns[i:end]

		placeholders := make([]string, len(chunk))
		args := make([]any, 0, len(chunk)+1)
		args = append(args, project)
		for j, qn := range chunk {
			placeholders[j] = "?"
			args = append(args, qn)
		}

		query := fmt.Sprintf("SELECT id, qualified_name FROM nodes WHERE project = ? AND qualified_name IN (%s)",
			strings.Join(placeholders, ","))

		if err := func() error {
			rows, err := s.q.Query(query, args...)
			if err != nil {
				return fmt.Errorf("resolve node IDs: %w", err)
			}
			defer rows.Close()
			for rows.Next() {
				var id int64
				var qn string
				if err := rows.Scan(&id, &qn); err != nil {
					return err
				}
				idMap[qn] = id
			}
			return rows.Err()
		}(); err != nil {
			return err
		}
	}
	return nil
}

// FindNodeIDsByQNs returns a map of qualifiedName → ID for the given QNs in a project.
func (s *Store) FindNodeIDsByQNs(project string, qns []string) (map[string]int64, error) {
	if len(qns) == 0 {
		return map[string]int64{}, nil
	}
	idMap := make(map[string]int64, len(qns))
	if err := s.resolveNodeIDs(project, qns, idMap); err != nil {
		return nil, err
	}
	return idMap, nil
}

// FindNodeLabelsByQNs returns a map of qualifiedName → label for the given QNs.
// Used by the CALLS pass to filter out edges targeting Variable/Class/File nodes
// (a CALLS edge should only target Function or Method). Before this existed,
// 38% of CALLS edges on some Rust projects pointed at Variable nodes generated
// from config files (diesel.toml entries, Cargo.toml), inflating false-positive
// rate with zero semantic value (see 2026-04-24 accuracy harness incident).
func (s *Store) FindNodeLabelsByQNs(project string, qns []string) (map[string]string, error) {
	if len(qns) == 0 {
		return map[string]string{}, nil
	}
	const maxQNsPerQuery = 998
	labelMap := make(map[string]string, len(qns))
	for i := 0; i < len(qns); i += maxQNsPerQuery {
		end := i + maxQNsPerQuery
		if end > len(qns) {
			end = len(qns)
		}
		chunk := qns[i:end]
		placeholders := make([]string, len(chunk))
		args := make([]any, 0, len(chunk)+1)
		args = append(args, project)
		for j, qn := range chunk {
			placeholders[j] = "?"
			args = append(args, qn)
		}
		query := fmt.Sprintf(
			"SELECT qualified_name, label FROM nodes WHERE project = ? AND qualified_name IN (%s)",
			strings.Join(placeholders, ","),
		)
		rows, err := s.q.Query(query, args...)
		if err != nil {
			return nil, fmt.Errorf("resolve node labels: %w", err)
		}
		err = func() error {
			defer rows.Close()
			for rows.Next() {
				var qn, label string
				if err := rows.Scan(&qn, &label); err != nil {
					return err
				}
				labelMap[qn] = label
			}
			return rows.Err()
		}()
		if err != nil {
			return nil, err
		}
	}
	return labelMap, nil
}
