package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// SkippedFile records a source file the extraction supervisor could not index
// because the worker crashed or hung on it. Skips live in a sidecar next to
// the project database rather than in the schema, so recording one never
// changes the index format.
type SkippedFile struct {
	Path   string `json:"path"`
	Reason string `json:"reason"` // "crash" or "timeout"
	Detail string `json:"detail,omitempty"`
}

type skipsSidecar struct {
	SchemaVersion int           `json:"schema_version"`
	WrittenAt     string        `json:"written_at"`
	Files         []SkippedFile `json:"files"`
}

// SkipsSidecarPath returns the sidecar path for a project database.
func SkipsSidecarPath(project string) (string, error) {
	dir, err := cacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, project+".skips.json"), nil
}

// WriteSkips replaces the project's skip sidecar. An empty list removes it so
// a clean reindex leaves no stale report behind.
func WriteSkips(project string, files []SkippedFile) error {
	path, err := SkipsSidecarPath(project)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove skips sidecar: %w", err)
		}
		return nil
	}
	sorted := make([]SkippedFile, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	data, err := json.MarshalIndent(skipsSidecar{
		SchemaVersion: 1,
		WrittenAt:     time.Now().UTC().Format(time.RFC3339),
		Files:         sorted,
	}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write skips sidecar: %w", err)
	}
	return os.Rename(tmp, path)
}

// ReadSkips returns the project's recorded skips, or an empty slice when the
// sidecar does not exist.
func ReadSkips(project string) ([]SkippedFile, error) {
	path, err := SkipsSidecarPath(project)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []SkippedFile{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read skips sidecar: %w", err)
	}
	var sc skipsSidecar
	if err := json.Unmarshal(data, &sc); err != nil {
		return nil, fmt.Errorf("parse skips sidecar: %w", err)
	}
	if sc.Files == nil {
		sc.Files = []SkippedFile{}
	}
	return sc.Files, nil
}
