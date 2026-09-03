package store

import (
	"database/sql"
	"fmt"

	"github.com/brandyn-s/code-graph/internal/indexidentity"
)

// SetIndexIdentity persists a successfully captured checkout identity.
func (s *Store) SetIndexIdentity(project string, identity *indexidentity.Envelope) error {
	if identity == nil {
		return fmt.Errorf("set index identity: identity is nil")
	}
	_, err := s.q.Exec(`
		INSERT INTO index_identity (
			project, schema_version, repository_id, checkout_id, source_revision,
			dirty_fingerprint, index_generation, captured_at, identity_status, identity_reason
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '')
		ON CONFLICT(project) DO UPDATE SET
			schema_version=excluded.schema_version,
			repository_id=excluded.repository_id,
			checkout_id=excluded.checkout_id,
			source_revision=excluded.source_revision,
			dirty_fingerprint=excluded.dirty_fingerprint,
			index_generation=excluded.index_generation,
			captured_at=excluded.captured_at,
			identity_status=excluded.identity_status,
			identity_reason=''`,
		project,
		identity.SchemaVersion,
		identity.RepositoryID,
		identity.CheckoutID,
		identity.SourceRevision,
		identity.DirtyFingerprint,
		identity.IndexGeneration,
		identity.CapturedAt,
		indexidentity.StatusCaptured,
	)
	if err != nil {
		return fmt.Errorf("set index identity: %w", err)
	}
	return nil
}

// SetIndexIdentityState invalidates any previous envelope and records why a
// coherent identity is not currently available.
func (s *Store) SetIndexIdentityState(project, status, reason string) error {
	_, err := s.q.Exec(`
		INSERT INTO index_identity (
			project, schema_version, repository_id, checkout_id, source_revision,
			dirty_fingerprint, index_generation, captured_at, identity_status, identity_reason
		) VALUES (?, 0, '', '', '', '', '', '', ?, ?)
		ON CONFLICT(project) DO UPDATE SET
			schema_version=0,
			repository_id='',
			checkout_id='',
			source_revision='',
			dirty_fingerprint='',
			index_generation='',
			captured_at='',
			identity_status=excluded.identity_status,
			identity_reason=excluded.identity_reason`,
		project, status, reason)
	if err != nil {
		return fmt.Errorf("set index identity state: %w", err)
	}
	return nil
}

// GetIndexIdentity returns the persisted envelope state. A legacy project with
// no identity row is represented explicitly as missing.
func (s *Store) GetIndexIdentity(project string) (*indexidentity.Record, error) {
	var identity indexidentity.Envelope
	var status, reason string
	err := s.q.QueryRow(`
		SELECT schema_version, repository_id, checkout_id, source_revision,
			dirty_fingerprint, index_generation, captured_at, identity_status, identity_reason
		FROM index_identity WHERE project=?`, project).
		Scan(
			&identity.SchemaVersion,
			&identity.RepositoryID,
			&identity.CheckoutID,
			&identity.SourceRevision,
			&identity.DirtyFingerprint,
			&identity.IndexGeneration,
			&identity.CapturedAt,
			&status,
			&reason,
		)
	if err == sql.ErrNoRows {
		return &indexidentity.Record{Status: indexidentity.StatusMissing}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get index identity: %w", err)
	}
	record := &indexidentity.Record{Status: status, Reason: reason}
	if status == indexidentity.StatusCaptured {
		if err := identity.Validate(); err != nil {
			record.Status = indexidentity.StatusError
			record.Reason = fmt.Sprintf("persisted index identity is incomplete or invalid: %v; re-run index_repository", err)
			return record, nil
		}
		record.Identity = &identity
	}
	return record, nil
}
