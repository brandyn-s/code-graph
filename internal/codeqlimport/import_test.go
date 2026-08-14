package codeqlimport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/indexidentity"
)

func TestImportBindsAttestedCodeQLPathToRepository(t *testing.T) {
	repo := t.TempDir()
	writeImportFixture(t, filepath.Join(repo, "src", "input.go"), "package source\n\nfunc Input() string { return \"request\" }\n")
	writeImportFixture(t, filepath.Join(repo, "src", "sink.go"), "package sink\n\nfunc Execute(value string) {}\n")
	gitImportFixture(t, repo, "init")
	gitImportFixture(t, repo, "config", "user.email", "fixture@example.invalid")
	gitImportFixture(t, repo, "config", "user.name", "Fixture")
	gitImportFixture(t, repo, "add", ".")
	gitImportFixture(t, repo, "commit", "-m", "fixture")

	identity, err := indexidentity.Capture(repo)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}

	receiptPath := filepath.Join(t.TempDir(), "codeql-receipt.json")
	writeImportJSON(t, receiptPath, map[string]any{
		"schema_version":           1,
		"repository_id":            identity.RepositoryID,
		"source_revision":          identity.SourceRevision,
		"index_generation":         identity.IndexGeneration,
		"analyzer_version":         "2.23.1",
		"extractor_version":        "codeql/go-all@4.0.0",
		"language":                 "go",
		"database_manifest_sha256": strings.Repeat("a", 64),
		"database_content_sha256":  strings.Repeat("b", 64),
		"database_quality": map[string]any{
			"status":           "pass",
			"source_files":     2,
			"baseline_lines":   6,
			"extractor_errors": 0,
		},
		"query_pack_manifest_sha256": strings.Repeat("c", 64),
		"queries": []map[string]any{{
			"query_id":      "go/sql-injection",
			"analysis_kind": "variable_level_taint",
		}},
	})

	sarifPath := filepath.Join(t.TempDir(), "results.sarif")
	writeImportJSON(t, sarifPath, map[string]any{
		"version": "2.1.0",
		"runs": []map[string]any{{
			"tool": map[string]any{"driver": map[string]any{
				"name":    "CodeQL command-line toolchain",
				"version": "2.23.1",
				"rules": []map[string]any{{
					"id":         "go/sql-injection",
					"properties": map[string]any{"kind": "path-problem"},
				}},
			}},
			"artifacts": []map[string]any{
				{"location": map[string]any{"uri": "src/input.go", "uriBaseId": "%SRCROOT%"}},
				{"location": map[string]any{"uri": "src/sink.go", "uriBaseId": "%SRCROOT%"}},
			},
			"results": []map[string]any{{
				"ruleId": "go/sql-injection",
				"codeFlows": []map[string]any{{
					"threadFlows": []map[string]any{{
						"locations": []map[string]any{
							{"location": importIndexedLocation(0, 3, 0, 0, 38)},
							{"location": importIndexedLocation(1, 3, 0, 0, 26)},
						},
					}},
				}},
			}},
		}},
	})

	result, err := Import(Request{
		RepositoryRoot: repo,
		SARIFPath:      sarifPath,
		ReceiptPath:    receiptPath,
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(result.EvidenceRefs) != 1 {
		t.Fatalf("evidence refs = %d, want 1", len(result.EvidenceRefs))
	}
	ref := result.EvidenceRefs[0]
	if ref.RepositoryID != identity.RepositoryID || ref.SourceRevision != identity.SourceRevision || ref.IndexGeneration != identity.IndexGeneration {
		t.Fatalf("evidence identity = (%q, %q, %q), want captured identity", ref.RepositoryID, ref.SourceRevision, ref.IndexGeneration)
	}
	if ref.AnalysisRef == nil {
		t.Fatal("analysis ref is nil")
	}
	if ref.AnalysisRef.QueryAttestationSHA256 != fileSHA256(t, receiptPath) {
		t.Fatalf("query attestation digest = %q, want receipt digest", ref.AnalysisRef.QueryAttestationSHA256)
	}
	if ref.AnalysisRef.SARIFSHA256 != fileSHA256(t, sarifPath) {
		t.Fatalf("SARIF digest = %q, want source artifact digest", ref.AnalysisRef.SARIFSHA256)
	}
	if got := ref.AnalysisRef.PathSteps; len(got) != 2 || got[0].Role != "source" || got[1].Role != "sink" || got[0].RelativePath != "src/input.go" || got[1].RelativePath != "src/sink.go" {
		t.Fatalf("path steps = %#v", got)
	}
}

func TestImportRejectsCoordinatesOutsideArtifact(t *testing.T) {
	repo := t.TempDir()
	writeImportFixture(t, filepath.Join(repo, "source.go"), "package fixture\n")
	locations := []sarifThreadFlowLocation{
		{Location: sarifLocation{PhysicalLocation: sarifPhysicalLocation{
			ArtifactLocation: sarifArtifactLocation{URI: "source.go"},
			Region:           sarifRegion{StartLine: 2, StartColumn: 1, EndLine: 2, EndColumn: 2},
		}}},
		{Location: sarifLocation{PhysicalLocation: sarifPhysicalLocation{
			ArtifactLocation: sarifArtifactLocation{URI: "source.go"},
			Region:           sarifRegion{StartLine: 1, StartColumn: 1, EndLine: 1, EndColumn: 2},
		}}},
	}

	_, err := importPathSteps(repo, nil, locations)
	if err == nil || !strings.Contains(err.Error(), "outside artifact") {
		t.Fatalf("expected out-of-bounds coordinate error, got %v", err)
	}
}

func TestValidateRegionRejectsSymlinkOutsideRepository(t *testing.T) {
	repository := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.go")
	writeImportFixture(t, outside, "package outside\n")
	if err := os.Symlink(outside, filepath.Join(repository, "linked.go")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	err := validateRegion(repository, "linked.go", sarifRegion{
		StartLine: 1, StartColumn: 1, EndLine: 1, EndColumn: 2,
	})
	if err == nil {
		t.Fatal("expected root-scoped coordinate read to reject an external symlink")
	}
}

func TestDecodeJSONRejectsTruncatedTrailingValue(t *testing.T) {
	var destination map[string]any
	err := decodeJSON([]byte(`{"version":"2.1.0"} {`), &destination)
	if err == nil {
		t.Fatal("expected truncated trailing JSON value to be rejected")
	}
}

func importIndexedLocation(index, startLine, startColumn, endLine, endColumn int) map[string]any {
	return map[string]any{
		"physicalLocation": map[string]any{
			"artifactLocation": map[string]any{"index": index, "uriBaseId": "%SRCROOT%"},
			"region": map[string]any{
				"startLine": startLine, "startColumn": startColumn,
				"endLine": endLine, "endColumn": endColumn,
			},
		},
	}
}

func writeImportFixture(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func writeImportJSON(t *testing.T, path string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	writeImportFixture(t, path, string(encoded))
}

func gitImportFixture(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}
