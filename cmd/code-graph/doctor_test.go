package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brandyn-s/code-graph/internal/store"
)

func doctorEnv(t *testing.T, cacheDir string, extra map[string]string) func(string) string {
	t.Helper()
	t.Setenv("CODE_GRAPH_CACHE_DIR", cacheDir)
	t.Setenv("VOYAGE_API_KEY", "")
	t.Setenv("CODE_GRAPH_TOOLSET", "")
	for k, v := range extra {
		t.Setenv(k, v)
	}
	return os.Getenv
}

func TestDoctorReport_JSONShapeAndReadOnly(t *testing.T) {
	cache := t.TempDir()
	fixture, err := os.ReadFile(filepath.Join("..", "..", "internal", "store", "testdata", "format-v1", "go-minimal.db"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	dbPath := filepath.Join(cache, "demo.db")
	if err := os.WriteFile(dbPath, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(dbPath)
	getenv := doctorEnv(t, cache, nil)

	probeCalled := false
	report := collectDoctorReport(context.Background(), getenv, func(context.Context) string {
		probeCalled = true
		return "reachable"
	})
	if probeCalled {
		t.Fatal("voyage probe ran without VOYAGE_API_KEY")
	}

	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"version", "platform", "toolset", "embeddings", "cache", "grammars", "config", "index_format", "warnings"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("report missing %q", key)
		}
	}
	if report.Toolset != "core" {
		t.Errorf("toolset = %q, want core", report.Toolset)
	}
	if report.Embeddings.Enabled || !strings.Contains(report.Embeddings.Reachability, "not_checked") {
		t.Errorf("embeddings = %+v, want disabled and unchecked", report.Embeddings)
	}
	if report.IndexFormat.Current != store.FormatVersion {
		t.Errorf("index format = %d", report.IndexFormat.Current)
	}
	if len(report.Grammars.Compiled) == 0 {
		t.Error("no compiled grammars reported")
	}
	if len(report.Cache.Projects) != 1 {
		t.Fatalf("projects = %+v, want the one fixture", report.Cache.Projects)
	}
	p := report.Cache.Projects[0]
	if p.Name != "demo" || p.FormatVersion != store.FormatVersion || p.SizeMB <= 0 || p.Error != "" {
		t.Errorf("project = %+v", p)
	}
	for _, c := range report.Config {
		if c.Name == "VOYAGE_API_KEY" && c.Value != "" {
			t.Errorf("secret leaked: %+v", c)
		}
	}
	after, _ := os.Stat(dbPath)
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Error("doctor modified the project database; it must be read-only")
	}
}

func TestDoctorReport_RedactsSecretsAndProbesWhenKeyPresent(t *testing.T) {
	getenv := doctorEnv(t, t.TempDir(), map[string]string{"VOYAGE_API_KEY": "sk-test-not-real", "CODE_GRAPH_TOOLSET": "full"})
	report := collectDoctorReport(context.Background(), getenv, func(context.Context) string { return "reachable (HTTP 405)" })
	if report.Embeddings.Reachability != "reachable (HTTP 405)" {
		t.Errorf("reachability = %q", report.Embeddings.Reachability)
	}
	if report.Toolset != "full" {
		t.Errorf("toolset = %q", report.Toolset)
	}
	for _, c := range report.Config {
		if c.Name == "VOYAGE_API_KEY" && (c.Value != "<set>" || !c.Set) {
			t.Errorf("secret not redacted: %+v", c)
		}
	}
	var out bytes.Buffer
	printDoctorReport(&out, report)
	if strings.Contains(out.String(), "sk-test-not-real") {
		t.Error("text report leaked the API key")
	}
	if !strings.Contains(out.String(), "toolset: full") {
		t.Errorf("text report missing toolset line:\n%s", out.String())
	}
}

func TestRunDoctor_JSONFlagEmitsValidJSON(t *testing.T) {
	doctorEnv(t, t.TempDir(), nil)
	var stdout, stderr bytes.Buffer
	if code := runDoctor([]string{"--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	var decoded doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout.String())
	}
	if code := runDoctor([]string{"--bogus"}, &stdout, &stderr); code != 1 {
		t.Errorf("unknown flag exit = %d", code)
	}
}
