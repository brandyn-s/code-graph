package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brandyn-s/code-graph/internal/embed"
	"github.com/brandyn-s/code-graph/internal/store"
)

func doctorEnv(t *testing.T, cacheDir string, extra map[string]string) func(string) string {
	t.Helper()
	t.Setenv("CODE_GRAPH_CACHE_DIR", cacheDir)
	for _, k := range []string{"VOYAGE_API_KEY", "CODE_GRAPH_TOOLSET", "CODE_GRAPH_EMBED_PROVIDER", "CODE_GRAPH_EMBED_BASE_URL", "CODE_GRAPH_EMBED_MODEL", "CODE_GRAPH_EMBED_API_KEY", "OPENAI_API_KEY", "CODE_GRAPH_SKIP_EMBEDDINGS"} {
		t.Setenv(k, "")
	}
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
	report := collectDoctorReport(context.Background(), getenv, func(context.Context, embed.Resolution) string {
		probeCalled = true
		return "reachable"
	})
	if probeCalled {
		t.Fatal("endpoint probe ran without a configured provider")
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
	emb := decoded["embeddings"].(map[string]any)
	for _, key := range []string{"enabled", "status", "provider", "reachability", "voyage_reachability"} {
		if _, ok := emb[key]; !ok {
			t.Errorf("embeddings missing %q", key)
		}
	}
	if report.Toolset != "core" {
		t.Errorf("toolset = %q, want core", report.Toolset)
	}
	if report.Embeddings.Enabled || report.Embeddings.Provider != "off" || !strings.Contains(report.Embeddings.Reachability, "not_checked") {
		t.Errorf("embeddings = %+v, want off and unchecked", report.Embeddings)
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
	var probed embed.Resolution
	report := collectDoctorReport(context.Background(), getenv, func(_ context.Context, res embed.Resolution) string {
		probed = res
		return "reachable (HTTP 405)"
	})
	if probed.Provider != embed.ProviderVoyage {
		t.Errorf("probe received %+v, want voyage", probed)
	}
	if report.Embeddings.Provider != "voyage" || report.Embeddings.Model == "" {
		t.Errorf("embeddings = %+v", report.Embeddings)
	}
	if report.Embeddings.Reachability != "reachable (HTTP 405)" || report.Embeddings.VoyageReachability != "reachable (HTTP 405)" {
		t.Errorf("reachability = %q / voyage %q", report.Embeddings.Reachability, report.Embeddings.VoyageReachability)
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
	if !strings.Contains(out.String(), "toolset: full") || !strings.Contains(out.String(), "provider: voyage") {
		t.Errorf("text report missing toolset/provider lines:\n%s", out.String())
	}
}

func TestDoctorReport_OpenAICompatibleProvider(t *testing.T) {
	getenv := doctorEnv(t, t.TempDir(), map[string]string{
		"CODE_GRAPH_EMBED_BASE_URL": "http://localhost:11434/v1",
		"CODE_GRAPH_EMBED_MODEL":    "nomic-embed-text",
		"CODE_GRAPH_EMBED_API_KEY":  "local-secret",
	})
	var probed embed.Resolution
	report := collectDoctorReport(context.Background(), getenv, func(_ context.Context, res embed.Resolution) string {
		probed = res
		return "reachable (HTTP 200)"
	})
	if probed.Provider != embed.ProviderOpenAI || probed.BaseURL != "http://localhost:11434/v1" {
		t.Errorf("probe received %+v", probed)
	}
	e := report.Embeddings
	if !e.Enabled || e.Provider != "openai" || e.Model != "nomic-embed-text" || e.Endpoint != "localhost:11434" {
		t.Errorf("embeddings = %+v", e)
	}
	if e.Reachability != "reachable (HTTP 200)" || e.VoyageReachability != "not_applicable" {
		t.Errorf("reachability = %q / voyage %q", e.Reachability, e.VoyageReachability)
	}
	raw, _ := json.Marshal(report)
	if strings.Contains(string(raw), "local-secret") {
		t.Error("JSON report leaked CODE_GRAPH_EMBED_API_KEY")
	}
}

func TestDoctorReport_MisconfiguredProviderIsAWarning(t *testing.T) {
	getenv := doctorEnv(t, t.TempDir(), map[string]string{"CODE_GRAPH_EMBED_PROVIDER": "openai"})
	report := collectDoctorReport(context.Background(), getenv, nil)
	if report.Embeddings.Provider != "off" {
		t.Errorf("provider = %q, want off", report.Embeddings.Provider)
	}
	found := false
	for _, w := range report.Warnings {
		if strings.Contains(w, "CODE_GRAPH_EMBED_MODEL is unset") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want the misconfiguration explained", report.Warnings)
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
