package store

import (
	"context"
	"fmt"
	"strings"
)

// SnapshotTo writes a consistent copy of the database to path using
// `VACUUM INTO`, which produces a compact single-file image regardless of the
// WAL state of the live database. path must not exist.
func (s *Store) SnapshotTo(path string) error {
	if strings.ContainsAny(path, "'") {
		return fmt.Errorf("snapshot path must not contain quotes: %q", path)
	}
	if _, err := s.db.ExecContext(context.Background(), fmt.Sprintf("VACUUM INTO '%s'", path)); err != nil {
		return fmt.Errorf("snapshot database: %w", err)
	}
	return nil
}

// CountFileHashes returns the number of tracked files for a project.
func (s *Store) CountFileHashes(project string) (int, error) {
	var n int
	if err := s.q.QueryRow(`SELECT COUNT(*) FROM file_hashes WHERE project = ?`, project).Scan(&n); err != nil {
		return 0, fmt.Errorf("count file hashes: %w", err)
	}
	return n, nil
}

// tablesWithProjectColumn lists every table that carries a `project` column,
// so a project rename reaches tables added after this code was written.
func (s *Store) tablesWithProjectColumn() ([]string, error) {
	tables, err := s.listUserTables()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, table := range tables {
		has, err := s.tableHasProjectColumn(table)
		if err != nil {
			return nil, err
		}
		if has {
			out = append(out, table)
		}
	}
	return out, nil
}

func (s *Store) listUserTables() ([]string, error) {
	rows, err := s.q.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	return tables, nil
}

func (s *Store) tableHasProjectColumn(table string) (bool, error) {
	cols, err := s.q.Query(fmt.Sprintf(`PRAGMA table_info(%q)`, table))
	if err != nil {
		return false, fmt.Errorf("table info %s: %w", table, err)
	}
	defer cols.Close()
	hasProject := false
	for cols.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := cols.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == "project" {
			hasProject = true
		}
	}
	if err := cols.Err(); err != nil {
		return false, fmt.Errorf("table info %s: %w", table, err)
	}
	return hasProject, nil
}

// RewriteProject renames a project in place: the projects row, every table
// with a project column, the project prefix of node qualified names, and
// qualified names embedded in node/edge properties JSON. Used when an
// exported graph artifact is imported for a checkout at a different path
// (project names are derived from the absolute repository path).
func (s *Store) RewriteProject(ctx context.Context, oldName, newName, newRoot string) error {
	if oldName == "" || newName == "" {
		return fmt.Errorf("rewrite project: names must be non-empty")
	}
	if oldName == newName {
		_, err := s.q.Exec(`UPDATE projects SET root_path = ? WHERE name = ?`, newRoot, oldName)
		return err
	}
	return s.WithTransaction(ctx, func(tx *Store) error {
		var exists int
		if err := tx.q.QueryRow(`SELECT COUNT(*) FROM projects WHERE name = ?`, newName).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			return fmt.Errorf("rewrite project: %q already exists in this database", newName)
		}
		// Insert the new row first so foreign keys from child tables stay
		// satisfied while they are moved over; the old row goes last.
		if _, err := tx.q.Exec(`INSERT INTO projects (name, indexed_at, root_path)
			SELECT ?, indexed_at, ? FROM projects WHERE name = ?`, newName, newRoot, oldName); err != nil {
			return fmt.Errorf("rewrite project: insert new row: %w", err)
		}
		tables, err := tx.tablesWithProjectColumn()
		if err != nil {
			return err
		}
		oldPrefix := oldName + "."
		newPrefix := newName + "."
		for _, table := range tables {
			if table == "projects" {
				continue
			}
			if _, err := tx.q.Exec(fmt.Sprintf(`UPDATE %q SET project = ? WHERE project = ?`, table), newName, oldName); err != nil {
				return fmt.Errorf("rewrite project: %s: %w", table, err)
			}
		}
		if _, err := tx.q.Exec(`UPDATE nodes SET qualified_name = ? || substr(qualified_name, length(?) + 1)
			WHERE project = ? AND substr(qualified_name, 1, length(?)) = ?`,
			newPrefix, oldPrefix, newName, oldPrefix, oldPrefix); err != nil {
			return fmt.Errorf("rewrite project: node qualified names: %w", err)
		}
		if _, err := tx.q.Exec(`UPDATE nodes SET qualified_name = ? WHERE project = ? AND qualified_name = ?`, newName, newName, oldName); err != nil {
			return fmt.Errorf("rewrite project: root qualified name: %w", err)
		}
		quotedOld := `"` + oldPrefix
		quotedNew := `"` + newPrefix
		for _, table := range []string{"nodes", "edges"} {
			if _, err := tx.q.Exec(fmt.Sprintf(`UPDATE %q SET properties = replace(properties, ?, ?) WHERE project = ? AND instr(properties, ?) > 0`, table),
				quotedOld, quotedNew, newName, quotedOld); err != nil {
				return fmt.Errorf("rewrite project: %s properties: %w", table, err)
			}
		}
		if _, err := tx.q.Exec(`DELETE FROM projects WHERE name = ?`, oldName); err != nil {
			return fmt.Errorf("rewrite project: delete old row: %w", err)
		}
		return nil
	})
}
