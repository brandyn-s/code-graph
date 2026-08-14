// Package codeqlimport converts an already-produced CodeQL SARIF artifact
// into immutable code-graph evidence. It never launches CodeQL or mutates an
// index, and it accepts only paths bound to a clean repository checkout and an
// operator-owned query-attestation receipt.
package codeqlimport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/DeusData/codebase-memory-mcp/internal/evidence"
	"github.com/DeusData/codebase-memory-mcp/internal/indexidentity"
)

const SchemaVersion = 1

type Request struct {
	RepositoryRoot string
	SARIFPath      string
	ReceiptPath    string
}

type Result struct {
	SchemaVersion   int                    `json:"schema_version"`
	RepositoryID    string                 `json:"repository_id"`
	SourceRevision  string                 `json:"source_revision"`
	IndexGeneration string                 `json:"index_generation"`
	ReceiptSHA256   string                 `json:"receipt_sha256"`
	SARIFSHA256     string                 `json:"sarif_sha256"`
	EvidenceRefs    []evidence.EvidenceRef `json:"evidence_refs"`
}

type receipt struct {
	SchemaVersion           int                      `json:"schema_version"`
	RepositoryID            string                   `json:"repository_id"`
	SourceRevision          string                   `json:"source_revision"`
	IndexGeneration         string                   `json:"index_generation"`
	AnalyzerVersion         string                   `json:"analyzer_version"`
	ExtractorVersion        string                   `json:"extractor_version"`
	Language                string                   `json:"language"`
	DatabaseManifestSHA256  string                   `json:"database_manifest_sha256"`
	DatabaseContentSHA256   string                   `json:"database_content_sha256"`
	DatabaseQuality         evidence.DatabaseQuality `json:"database_quality"`
	QueryPackManifestSHA256 string                   `json:"query_pack_manifest_sha256"`
	Queries                 []queryAttestation       `json:"queries"`
}

type queryAttestation struct {
	QueryID      string `json:"query_id"`
	AnalysisKind string `json:"analysis_kind"`
}

type sarifLog struct {
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool      sarifTool       `json:"tool"`
	Artifacts []sarifArtifact `json:"artifacts"`
	Results   []sarifResult   `json:"results"`
}

type sarifArtifact struct {
	Location sarifArtifactLocation `json:"location"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name    string      `json:"name"`
	Version string      `json:"version"`
	Rules   []sarifRule `json:"rules"`
}

type sarifRule struct {
	ID         string `json:"id"`
	Properties struct {
		Kind string `json:"kind"`
	} `json:"properties"`
}

type sarifResult struct {
	RuleID    string          `json:"ruleId"`
	CodeFlows []sarifCodeFlow `json:"codeFlows"`
}

type sarifCodeFlow struct {
	ThreadFlows []sarifThreadFlow `json:"threadFlows"`
}

type sarifThreadFlow struct {
	Locations []sarifThreadFlowLocation `json:"locations"`
}

type sarifThreadFlowLocation struct {
	Location sarifLocation `json:"location"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysicalLocation `json:"physicalLocation"`
}

type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
	Region           sarifRegion           `json:"region"`
}

type sarifArtifactLocation struct {
	URI       string `json:"uri"`
	URIBaseID string `json:"uriBaseId"`
	Index     *int   `json:"index"`
}

type sarifRegion struct {
	StartLine   int `json:"startLine"`
	StartColumn int `json:"startColumn"`
	EndLine     int `json:"endLine"`
	EndColumn   int `json:"endColumn"`
}

func Import(request Request) (Result, error) {
	identity, err := indexidentity.Capture(request.RepositoryRoot)
	if err != nil {
		return Result{}, fmt.Errorf("capture repository identity: %w", err)
	}
	if identity.DirtyFingerprint != "clean" {
		return Result{}, fmt.Errorf("repository checkout must be clean")
	}

	receiptBytes, err := os.ReadFile(request.ReceiptPath)
	if err != nil {
		return Result{}, fmt.Errorf("read query-attestation receipt: %w", err)
	}
	var attestation receipt
	if err := decodeStrict(receiptBytes, &attestation); err != nil {
		return Result{}, fmt.Errorf("decode query-attestation receipt: %w", err)
	}
	if err := validateReceipt(&attestation, identity); err != nil {
		return Result{}, err
	}
	receiptDigest := sha256Hex(receiptBytes)

	sarifBytes, err := os.ReadFile(request.SARIFPath)
	if err != nil {
		return Result{}, fmt.Errorf("read SARIF: %w", err)
	}
	var log sarifLog
	if err := decodeJSON(sarifBytes, &log); err != nil {
		return Result{}, fmt.Errorf("decode SARIF: %w", err)
	}
	if log.Version != "2.1.0" {
		return Result{}, fmt.Errorf("SARIF version must be 2.1.0")
	}
	if len(log.Runs) != 1 {
		return Result{}, fmt.Errorf("SARIF must contain exactly one run")
	}
	run := log.Runs[0]
	driverName := strings.ToLower(strings.TrimSpace(run.Tool.Driver.Name))
	if driverName != "codeql" && driverName != "codeql command-line toolchain" {
		return Result{}, fmt.Errorf("SARIF tool driver must be CodeQL")
	}
	if strings.TrimSpace(run.Tool.Driver.Version) != strings.TrimSpace(attestation.AnalyzerVersion) {
		return Result{}, fmt.Errorf("SARIF analyzer version does not match receipt")
	}

	attestedQueries := make(map[string]bool, len(attestation.Queries))
	for _, query := range attestation.Queries {
		attestedQueries[strings.TrimSpace(query.QueryID)] = true
	}
	pathRules := make(map[string]bool, len(run.Tool.Driver.Rules))
	for _, rule := range run.Tool.Driver.Rules {
		if strings.TrimSpace(rule.Properties.Kind) == "path-problem" {
			pathRules[strings.TrimSpace(rule.ID)] = true
		}
	}

	sarifDigest := sha256Hex(sarifBytes)
	refs := make([]evidence.EvidenceRef, 0)
	for resultIndex, sarifResult := range run.Results {
		queryID := strings.TrimSpace(sarifResult.RuleID)
		if !attestedQueries[queryID] {
			continue
		}
		if !pathRules[queryID] {
			return Result{}, fmt.Errorf("attested query %q is not a CodeQL path-problem rule", queryID)
		}
		for codeFlowIndex, codeFlow := range sarifResult.CodeFlows {
			for threadFlowIndex, threadFlow := range codeFlow.ThreadFlows {
				steps, stepErr := importPathSteps(request.RepositoryRoot, run.Artifacts, threadFlow.Locations)
				if stepErr != nil {
					return Result{}, fmt.Errorf("result %d code flow %d thread flow %d: %w", resultIndex, codeFlowIndex, threadFlowIndex, stepErr)
				}
				analysis := evidence.NewAttestedCodeQLAnalysisRef(
					identity.RepositoryID,
					identity.SourceRevision,
					identity.IndexGeneration,
					attestation.AnalyzerVersion,
					attestation.ExtractorVersion,
					attestation.Language,
					attestation.DatabaseManifestSHA256,
					attestation.DatabaseContentSHA256,
					attestation.DatabaseQuality,
					attestation.QueryPackManifestSHA256,
					sarifDigest,
					receiptDigest,
					queryID,
					resultIndex,
					codeFlowIndex,
					threadFlowIndex,
					steps,
				)
				refs = append(refs, evidence.NewAnalysisEvidenceRef(analysis))
			}
		}
	}
	if len(refs) == 0 {
		return Result{}, fmt.Errorf("SARIF contains no attested CodeQL paths")
	}
	return Result{
		SchemaVersion:   SchemaVersion,
		RepositoryID:    identity.RepositoryID,
		SourceRevision:  identity.SourceRevision,
		IndexGeneration: identity.IndexGeneration,
		ReceiptSHA256:   receiptDigest,
		SARIFSHA256:     sarifDigest,
		EvidenceRefs:    refs,
	}, nil
}

func validateReceipt(value *receipt, identity *indexidentity.Envelope) error {
	if value.SchemaVersion != SchemaVersion {
		return fmt.Errorf("query-attestation receipt schema_version must equal %d", SchemaVersion)
	}
	if value.RepositoryID != identity.RepositoryID || value.SourceRevision != identity.SourceRevision || value.IndexGeneration != identity.IndexGeneration {
		return fmt.Errorf("query-attestation receipt does not match the live repository identity")
	}
	if strings.TrimSpace(value.AnalyzerVersion) == "" || strings.TrimSpace(value.ExtractorVersion) == "" || strings.TrimSpace(value.Language) == "" {
		return fmt.Errorf("query-attestation receipt analyzer, extractor, and language are required")
	}
	for name, digest := range map[string]string{
		"database_manifest_sha256":   value.DatabaseManifestSHA256,
		"database_content_sha256":    value.DatabaseContentSHA256,
		"query_pack_manifest_sha256": value.QueryPackManifestSHA256,
	} {
		if !isSHA256(digest) {
			return fmt.Errorf("query-attestation receipt %s must be 64 lowercase hex characters", name)
		}
	}
	if value.DatabaseQuality.Status != "pass" || value.DatabaseQuality.SourceFiles <= 0 || value.DatabaseQuality.BaselineLines <= 0 || value.DatabaseQuality.ExtractorErrors < 0 {
		return fmt.Errorf("query-attestation receipt database quality is not passing")
	}
	if len(value.Queries) == 0 {
		return fmt.Errorf("query-attestation receipt must contain at least one query")
	}
	seen := make(map[string]bool, len(value.Queries))
	for _, query := range value.Queries {
		queryID := strings.TrimSpace(query.QueryID)
		if queryID == "" || strings.TrimSpace(query.AnalysisKind) != "variable_level_taint" {
			return fmt.Errorf("query-attestation receipt queries must name variable_level_taint query IDs")
		}
		if seen[queryID] {
			return fmt.Errorf("query-attestation receipt query IDs must be unique")
		}
		seen[queryID] = true
	}
	return nil
}

func importPathSteps(repositoryRoot string, artifacts []sarifArtifact, locations []sarifThreadFlowLocation) ([]evidence.AnalysisPathStep, error) {
	if len(locations) < 2 {
		return nil, fmt.Errorf("CodeQL path must contain at least two locations")
	}
	steps := make([]evidence.AnalysisPathStep, 0, len(locations))
	for position, item := range locations {
		physical := item.Location.PhysicalLocation
		artifactLocation, err := resolveArtifactLocation(artifacts, physical.ArtifactLocation)
		if err != nil {
			return nil, fmt.Errorf("location %d: %w", position, err)
		}
		relativePath, err := safeRelativePath(repositoryRoot, artifactLocation)
		if err != nil {
			return nil, fmt.Errorf("location %d: %w", position, err)
		}
		region := physical.Region
		if region.StartColumn == 0 {
			region.StartColumn = 1
		}
		if region.EndLine == 0 {
			region.EndLine = region.StartLine
		}
		if region.StartLine <= 0 || region.StartColumn <= 0 || region.EndLine <= 0 || region.EndColumn <= 0 || region.EndLine < region.StartLine || (region.EndLine == region.StartLine && region.EndColumn < region.StartColumn) {
			return nil, fmt.Errorf("location %d has incomplete or invalid exact coordinates", position)
		}
		if err := validateRegion(repositoryRoot, relativePath, region); err != nil {
			return nil, fmt.Errorf("location %d: %w", position, err)
		}
		role := "intermediate"
		if position == 0 {
			role = "source"
		} else if position == len(locations)-1 {
			role = "sink"
		}
		steps = append(steps, evidence.AnalysisPathStep{
			Position: position, Role: role, RelativePath: relativePath,
			StartLine: region.StartLine, StartColumn: region.StartColumn,
			EndLine: region.EndLine, EndColumn: region.EndColumn,
		})
	}
	return steps, nil
}

func resolveArtifactLocation(artifacts []sarifArtifact, location sarifArtifactLocation) (sarifArtifactLocation, error) {
	resolved := location
	if location.Index != nil {
		if *location.Index < 0 || *location.Index >= len(artifacts) {
			return sarifArtifactLocation{}, fmt.Errorf("artifact index is outside the run artifact table")
		}
		registered := artifacts[*location.Index].Location
		if registered.Index != nil {
			return sarifArtifactLocation{}, fmt.Errorf("run artifact location cannot contain another artifact index")
		}
		if resolved.URI == "" {
			resolved.URI = registered.URI
		} else if registered.URI != "" && resolved.URI != registered.URI {
			return sarifArtifactLocation{}, fmt.Errorf("artifact index and inline URI disagree")
		}
		if resolved.URIBaseID == "" {
			resolved.URIBaseID = registered.URIBaseID
		} else if registered.URIBaseID != "" && resolved.URIBaseID != registered.URIBaseID {
			return sarifArtifactLocation{}, fmt.Errorf("artifact index and inline URI base disagree")
		}
		resolved.Index = nil
	}
	if resolved.URIBaseID != "" && resolved.URIBaseID != "%SRCROOT%" {
		return sarifArtifactLocation{}, fmt.Errorf("artifact URI base must be %%SRCROOT%%")
	}
	return resolved, nil
}

func validateRegion(repositoryRoot, relativePath string, region sarifRegion) error {
	repository, err := os.OpenRoot(repositoryRoot)
	if err != nil {
		return fmt.Errorf("open repository root: %w", err)
	}
	defer func() { _ = repository.Close() }()

	contents, err := repository.ReadFile(filepath.FromSlash(relativePath))
	if err != nil {
		return fmt.Errorf("read artifact coordinates: %w", err)
	}
	if !utf8.Valid(contents) {
		return fmt.Errorf("coordinates refer to a non-UTF-8 artifact")
	}
	lines := strings.Split(string(contents), "\n")
	if region.StartLine > len(lines) || region.EndLine > len(lines) {
		return fmt.Errorf("coordinates are outside artifact")
	}
	lineColumns := func(line int) int {
		return utf8.RuneCountInString(strings.TrimSuffix(lines[line-1], "\r")) + 1
	}
	if region.StartColumn > lineColumns(region.StartLine) || region.EndColumn > lineColumns(region.EndLine) {
		return fmt.Errorf("coordinates are outside artifact")
	}
	return nil
}

func safeRelativePath(repositoryRoot string, location sarifArtifactLocation) (string, error) {
	if location.Index != nil {
		return "", fmt.Errorf("artifact index must be resolved before path validation")
	}
	if strings.TrimSpace(location.URI) == "" || strings.Contains(location.URI, `\\`) {
		return "", fmt.Errorf("artifact URI must be a non-empty slash-separated relative path")
	}
	parsed, err := url.Parse(location.URI)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", fmt.Errorf("artifact URI must be repository-relative without query or fragment")
	}
	decoded, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil || decoded == "" || strings.HasPrefix(decoded, "/") {
		return "", fmt.Errorf("artifact URI cannot be decoded as a repository-relative path")
	}
	for _, part := range strings.Split(decoded, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("artifact URI contains an unsafe path segment")
		}
	}
	if path.Clean(decoded) != decoded {
		return "", fmt.Errorf("artifact URI is not canonical")
	}
	root, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve repository root symlinks: %w", err)
	}
	repository, err := os.OpenRoot(root)
	if err != nil {
		return "", fmt.Errorf("open repository root: %w", err)
	}
	defer func() { _ = repository.Close() }()

	candidate, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(decoded)))
	if err != nil {
		return "", fmt.Errorf("resolve artifact: %w", err)
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("artifact escapes repository root")
	}
	info, err := repository.Stat(relative)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("artifact is not a regular repository file")
	}
	return filepath.ToSlash(relative), nil
}

func decodeStrict(contents []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func decodeJSON(contents []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return fmt.Errorf("multiple JSON values are not allowed")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func isSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(value) == value
}

func sha256Hex(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}
