package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/selfupdate"
)

func TestExtractBinaryFromTarGz(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	content := []byte("fake binary content")
	hdr := &tar.Header{
		Name:     "codebase-memory-mcp-linux-amd64",
		Mode:     0o700,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gw.Close()

	extracted, err := extractBinaryFromTarGz(buf.Bytes())
	if err != nil {
		t.Fatalf("extractBinaryFromTarGz error: %v", err)
	}
	if !bytes.Equal(extracted, content) {
		t.Fatalf("extracted content mismatch: %q vs %q", extracted, content)
	}
}

func TestExtractBinaryFromTarGz_NotFound(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	hdr := &tar.Header{
		Name:     "some-other-file",
		Mode:     0o600,
		Size:     5,
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gw.Close()

	_, err := extractBinaryFromTarGz(buf.Bytes())
	if err == nil {
		t.Fatal("expected error when binary not found in archive")
	}
}

func TestExtractBinaryFromTarGz_InvalidData(t *testing.T) {
	_, err := extractBinaryFromTarGz([]byte("not a valid tar.gz"))
	if err == nil {
		t.Fatal("expected error for invalid tar.gz data")
	}
}

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source")
	dst := filepath.Join(dir, "dest")

	content := []byte("test content for copy")
	if err := os.WriteFile(src, content, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile error: %v", err)
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if !bytes.Equal(data, content) {
		t.Fatalf("copy mismatch: %q vs %q", data, content)
	}
}

func TestCopyFile_SourceNotFound(t *testing.T) {
	dir := t.TempDir()
	err := copyFile(filepath.Join(dir, "nonexistent"), filepath.Join(dir, "dest"))
	if err == nil {
		t.Fatal("expected error for nonexistent source")
	}
}

func TestDownloadAndVerify_FailsWithoutChecksums(t *testing.T) {
	// Set up a server that serves the release asset but NOT checksums.txt
	assetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gw)
		content := []byte("#!/bin/sh\necho codebase-memory-mcp test")
		hdr := &tar.Header{
			Name:     "codebase-memory-mcp",
			Mode:     0o755,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}
		_ = tw.WriteHeader(hdr)
		_, _ = tw.Write(content)
		_ = tw.Close()
		_ = gw.Close()
		_, _ = w.Write(buf.Bytes())
	}))
	defer assetServer.Close()

	// Allow test server URLs for download
	origPrefixes := selfupdate.AllowedDownloadPrefixes
	selfupdate.AllowedDownloadPrefixes = append(selfupdate.AllowedDownloadPrefixes, assetServer.URL)
	t.Cleanup(func() { selfupdate.AllowedDownloadPrefixes = origPrefixes })

	// Release with an asset but NO checksums.txt
	release := &selfupdate.Release{
		TagName: "v9.9.9",
		Assets: []selfupdate.Asset{
			{
				Name:               "codebase-memory-mcp-linux-amd64.tar.gz",
				BrowserDownloadURL: assetServer.URL + "/binary.tar.gz",
				Size:               1024,
			},
		},
	}

	asset := &release.Assets[0]
	_, err := downloadAndVerify(context.Background(), release, "codebase-memory-mcp-linux-amd64.tar.gz", asset)
	if err == nil {
		t.Fatal("expected error when checksums are unavailable, got nil")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("expected checksum-related error, got: %v", err)
	}
}

func TestDownloadAndVerify_ProvenanceFailureAbortsBeforeExtraction(t *testing.T) {
	archive := []byte("not a valid tar.gz")
	hash := sha256.Sum256(archive)
	checksum := hex.EncodeToString(hash[:])
	const assetName = "codebase-memory-mcp-linux-amd64.tar.gz"

	assetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/checksums.txt":
			_, _ = fmt.Fprintf(w, "%s  %s\n", checksum, assetName)
		case "/archive":
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer assetServer.Close()

	origPrefixes := selfupdate.AllowedDownloadPrefixes
	selfupdate.AllowedDownloadPrefixes = append(
		selfupdate.AllowedDownloadPrefixes,
		assetServer.URL,
	)
	t.Cleanup(func() { selfupdate.AllowedDownloadPrefixes = origPrefixes })

	originalVerifier := verifyReleaseArchive
	verificationCalled := false
	verifyReleaseArchive = func(
		_ context.Context,
		tag string,
		gotAssetName string,
		gotArchive []byte,
	) error {
		verificationCalled = true
		if tag != "v9.9.9" {
			t.Fatalf("verification tag = %q, want v9.9.9", tag)
		}
		if gotAssetName != assetName {
			t.Fatalf("verification asset = %q, want %q", gotAssetName, assetName)
		}
		if !bytes.Equal(gotArchive, archive) {
			t.Fatalf("verification archive = %q, want %q", gotArchive, archive)
		}
		return errors.New("simulated provenance failure")
	}
	t.Cleanup(func() { verifyReleaseArchive = originalVerifier })

	release := &selfupdate.Release{
		TagName: "v9.9.9",
		Assets: []selfupdate.Asset{
			{
				Name:               "checksums.txt",
				BrowserDownloadURL: assetServer.URL + "/checksums.txt",
			},
			{
				Name:               assetName,
				BrowserDownloadURL: assetServer.URL + "/archive",
			},
		},
	}

	_, err := downloadAndVerify(
		context.Background(),
		release,
		assetName,
		&release.Assets[1],
	)
	if !verificationCalled {
		t.Fatal("release verifier was not called")
	}
	if err == nil || !strings.Contains(err.Error(), "release verification failed") {
		t.Fatalf("downloadAndVerify() error = %v, want release verification failure", err)
	}
	if !strings.Contains(err.Error(), "simulated provenance failure") {
		t.Fatalf("downloadAndVerify() error = %v, want verifier cause", err)
	}
	if strings.Contains(err.Error(), "gzip") || strings.Contains(err.Error(), "extract") {
		t.Fatalf("archive was extracted after provenance failure: %v", err)
	}
}

func TestHistoricalReleaseSuccessMessageDoesNotClaimProvenance(t *testing.T) {
	message := strings.ToLower(releaseVerificationSuccessMessage)
	if strings.Contains(message, "provenance") {
		t.Fatalf(
			"historical release uses success message %q, which overclaims provenance",
			releaseVerificationSuccessMessage,
		)
	}
}

func TestExtractBinaryFromTarGz_RejectsOversizedEntry(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	hdr := &tar.Header{
		Name:     "codebase-memory-mcp",
		Mode:     0o755,
		Size:     300 * 1024 * 1024, // 300 MB - over the limit
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte("fake"))
	_ = tw.Close()
	_ = gw.Close()

	_, err := extractBinaryFromTarGz(buf.Bytes())
	if err == nil {
		t.Fatal("expected error for oversized tar entry")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected size-related error, got: %v", err)
	}
}
