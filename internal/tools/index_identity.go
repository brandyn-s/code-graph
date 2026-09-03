package tools

import (
	"fmt"
	"strings"

	"github.com/brandyn-s/code-graph/internal/indexidentity"
	"github.com/brandyn-s/code-graph/internal/store"
)

const missingIdentityReason = "index predates identity tracking; re-run index_repository to capture a coherent checkout identity"

func (s *Server) indexIdentityCapture() func(string) (*indexidentity.Envelope, error) {
	if s.captureIndexIdentity != nil {
		return s.captureIndexIdentity
	}
	return indexidentity.Capture
}

func persistTerminalIndexIdentityError(
	st *store.Store,
	project string,
	code string,
	cause error,
) error {
	reason := fmt.Sprintf("%s: %v; re-run index_repository", code, cause)
	if err := st.SetIndexIdentityState(project, indexidentity.StatusError, reason); err != nil {
		return fmt.Errorf("persist terminal index identity state: %w", err)
	}
	return nil
}

func persistCoherentIndexIdentity(
	st *store.Store,
	project string,
	start *indexidentity.Envelope,
	startErr error,
	end *indexidentity.Envelope,
	endErr error,
) error {
	if startErr == nil && start == nil {
		startErr = fmt.Errorf("identity capture returned a nil start envelope")
	}
	if endErr == nil && end == nil {
		endErr = fmt.Errorf("identity capture returned a nil end envelope")
	}

	var identityErr error
	var reason string
	switch {
	case startErr != nil:
		identityErr = startErr
		reason = fmt.Sprintf(
			"identity_capture_start_failed: %v; correct the Git checkout and re-run index_repository",
			startErr,
		)
	case endErr != nil:
		identityErr = endErr
		reason = fmt.Sprintf(
			"identity_capture_end_failed: %v; correct the Git checkout and re-run index_repository",
			endErr,
		)
	case !sameSourceIdentity(start, end):
		identityErr = fmt.Errorf(
			"source_changed_during_index: source identity fields differ: %s",
			describeSourceIdentityDifferences("start", start, "end", end),
		)
		reason = identityErr.Error() + "; allow source changes to settle and re-run index_repository"
	default:
		identityErr = st.SetIndexIdentity(project, end)
		if identityErr != nil {
			reason = fmt.Sprintf("identity_persist_failed: %v; re-run index_repository", identityErr)
		}
	}
	if reason == "" {
		return nil
	}
	if stateErr := st.SetIndexIdentityState(project, indexidentity.StatusError, reason); stateErr != nil {
		return fmt.Errorf("%v; persisting identity failure state: %w", identityErr, stateErr)
	}
	return identityErr
}

func sameSourceIdentity(left, right *indexidentity.Envelope) bool {
	return left != nil &&
		right != nil &&
		left.RepositoryID == right.RepositoryID &&
		left.CheckoutID == right.CheckoutID &&
		left.SourceRevision == right.SourceRevision &&
		left.DirtyFingerprint == right.DirtyFingerprint &&
		left.IndexGeneration == right.IndexGeneration
}

func describeSourceIdentityDifferences(
	leftLabel string,
	left *indexidentity.Envelope,
	rightLabel string,
	right *indexidentity.Envelope,
) string {
	type field struct {
		name        string
		left, right string
	}
	fields := []field{
		{name: "repository_id", left: left.RepositoryID, right: right.RepositoryID},
		{name: "checkout_id", left: left.CheckoutID, right: right.CheckoutID},
		{name: "source_revision", left: left.SourceRevision, right: right.SourceRevision},
		{name: "dirty_fingerprint", left: left.DirtyFingerprint, right: right.DirtyFingerprint},
		{name: "index_generation", left: left.IndexGeneration, right: right.IndexGeneration},
	}
	var differences []string
	for _, candidate := range fields {
		if candidate.left != candidate.right {
			differences = append(
				differences,
				fmt.Sprintf(
					"%s(%s=%q, %s=%q)",
					candidate.name,
					leftLabel,
					candidate.left,
					rightLabel,
					candidate.right,
				),
			)
		}
	}
	return strings.Join(differences, ", ")
}

func addIndexIdentity(result map[string]any, st *store.Store, project string) bool {
	record, err := st.GetIndexIdentity(project)
	if err != nil {
		result["identity_status"] = indexidentity.StatusError
		result["identity_reason"] = fmt.Sprintf("index identity could not be read: %v; re-run index_repository", err)
		return false
	}

	result["identity_status"] = record.Status
	switch record.Status {
	case indexidentity.StatusCaptured:
		result["index_identity"] = record.Identity
		result["identity_reason"] = ""
		return record.Identity != nil
	case indexidentity.StatusMissing:
		result["identity_reason"] = missingIdentityReason
	default:
		result["identity_reason"] = record.Reason
	}
	return false
}

func (s *Server) addLiveIndexIdentity(
	result map[string]any,
	st *store.Store,
	project string,
	rootPath string,
) bool {
	if coherent := addIndexIdentity(result, st, project); !coherent {
		return false
	}
	persisted, ok := result["index_identity"].(*indexidentity.Envelope)
	if !ok || persisted == nil {
		result["identity_status"] = indexidentity.StatusError
		result["identity_reason"] = "persisted index identity is unavailable; re-run index_repository"
		return false
	}
	if rootPath == "" {
		result["identity_status"] = indexidentity.StatusError
		result["identity_reason"] = "source_identity_capture_failed: project root_path is empty; re-run index_repository"
		return false
	}

	current, err := s.indexIdentityCapture()(rootPath)
	if err != nil {
		result["identity_status"] = indexidentity.StatusError
		result["identity_reason"] = fmt.Sprintf(
			"source_identity_capture_failed: %v; restore the checkout or re-run index_repository",
			err,
		)
		return false
	}
	if !sameSourceIdentity(current, persisted) {
		result["identity_status"] = indexidentity.StatusStaleSource
		result["identity_reason"] = fmt.Sprintf(
			"source_changed_since_index: source identity fields differ: %s; re-run index_repository",
			describeSourceIdentityDifferences("indexed", persisted, "current", current),
		)
		return false
	}
	return true
}
