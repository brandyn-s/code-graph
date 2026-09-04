package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/brandyn-s/code-graph/internal/config"
	"github.com/brandyn-s/code-graph/internal/embed"
	"github.com/brandyn-s/code-graph/internal/lang"
	"github.com/brandyn-s/code-graph/internal/store"
	"github.com/brandyn-s/code-graph/internal/tools"
)

// doctorReport is the machine-readable shape of `code-graph doctor --json`.
// Field names are part of the support contract; add fields, do not rename.
type doctorReport struct {
	Version  string `json:"version"`
	Platform string `json:"platform"`
	Toolset  string `json:"toolset"`

	Embeddings struct {
		Enabled      bool   `json:"enabled"`
		Status       string `json:"status"`
		Reachability string `json:"voyage_reachability"`
	} `json:"embeddings"`

	Cache struct {
		Dir      string          `json:"dir"`
		Projects []doctorProject `json:"projects"`
		TotalMB  float64         `json:"total_mb"`
	} `json:"cache"`

	Grammars struct {
		Compiled []string `json:"compiled"`
		Excluded []string `json:"excluded_from_build"`
	} `json:"grammars"`

	Config []doctorConfig `json:"config"`

	IndexFormat struct {
		Current      int `json:"current"`
		MinSupported int `json:"min_supported"`
	} `json:"index_format"`

	Warnings []string `json:"warnings"`
}

type doctorProject struct {
	Name          string  `json:"name"`
	RootPath      string  `json:"root_path"`
	DBPath        string  `json:"db_path"`
	SizeMB        float64 `json:"size_mb"`
	FormatVersion int     `json:"format_version"`
	Error         string  `json:"error,omitempty"`
}

type doctorConfig struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Set     bool   `json:"set"`
	Default string `json:"default"`
}

// runDoctor implements `code-graph doctor [--json]`.
func runDoctor(args []string, stdout, stderr io.Writer) int {
	asJSON := false
	for _, a := range args {
		switch a {
		case "--json":
			asJSON = true
		case "--help", "-h", "help":
			fmt.Fprintln(stdout, "Usage: code-graph doctor [--json]\n\nPrint resolved configuration, cache and index state, embeddings mode,\ncompiled grammars, and platform details for support requests.")
			return 0
		default:
			fmt.Fprintf(stderr, "Unknown doctor flag: %s\n", a)
			return 1
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	report := collectDoctorReport(ctx, os.Getenv, doctorProbeVoyage)
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintf(stderr, "encode report: %v\n", err)
			return 1
		}
		return 0
	}
	printDoctorReport(stdout, report)
	return 0
}

// voyageProbe reports reachability of the embeddings endpoint. It is injected
// so tests never touch the network.
type voyageProbe func(ctx context.Context) string

// collectDoctorReport gathers every diagnostic without mutating state: project
// databases are opened read-only and never stamped.
func collectDoctorReport(ctx context.Context, getenv func(string) string, probe voyageProbe) doctorReport {
	var r doctorReport
	r.Version = version
	r.Platform = runtime.GOOS + "/" + runtime.GOARCH
	r.Toolset = tools.ActiveToolset()
	r.IndexFormat.Current = store.FormatVersion
	r.IndexFormat.MinSupported = store.MinSupportedFormatVersion

	enabled, status := embeddingMode(getenv)
	r.Embeddings.Enabled = enabled
	r.Embeddings.Status = strings.TrimPrefix(status, "code-graph: ")
	switch {
	case strings.TrimSpace(getenv(config.VoyageAPIKey.Name)) == "":
		r.Embeddings.Reachability = "not_checked (no VOYAGE_API_KEY)"
	case probe == nil:
		r.Embeddings.Reachability = "not_checked"
	default:
		r.Embeddings.Reachability = probe(ctx)
	}

	for _, k := range config.All() {
		v, ok := os.LookupEnv(k.Name)
		if ok && k.Secret && v != "" {
			v = "<set>"
		}
		r.Config = append(r.Config, doctorConfig{Name: k.Name, Value: v, Set: ok && v != "", Default: k.Default})
	}

	for _, l := range lang.AllLanguages() {
		if lang.BuildIncludes(l) {
			r.Grammars.Compiled = append(r.Grammars.Compiled, string(l))
		}
	}
	for _, l := range lang.ExcludedFromBuild() {
		r.Grammars.Excluded = append(r.Grammars.Excluded, string(l))
	}
	sort.Strings(r.Grammars.Compiled)
	sort.Strings(r.Grammars.Excluded)
	if r.Grammars.Excluded == nil {
		r.Grammars.Excluded = []string{}
	}

	// Scan the cache directory directly instead of through the store router:
	// opening a store switches journal modes and stamps legacy databases,
	// and a diagnostic must not modify anything.
	dir, err := store.CacheDir()
	if err != nil {
		r.Warnings = append(r.Warnings, fmt.Sprintf("cache directory unavailable: %v", err))
		return r
	}
	r.Cache.Dir = dir
	entries, err := os.ReadDir(dir)
	if err != nil {
		r.Warnings = append(r.Warnings, fmt.Sprintf("list projects: %v", err))
		return r
	}
	r.Cache.Projects = []doctorProject{}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".db")
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".db") || strings.HasPrefix(name, "_") || name == "codebase-memory" {
			continue
		}
		dbPath := filepath.Join(dir, e.Name())
		dp := doctorProject{Name: name, DBPath: dbPath}
		if info, statErr := os.Stat(dbPath); statErr == nil {
			dp.SizeMB = float64(info.Size()) / (1024 * 1024)
			r.Cache.TotalMB += dp.SizeMB
		}
		version, root, readErr := readProjectMetaReadOnly(ctx, dbPath)
		dp.FormatVersion, dp.RootPath = version, root
		if readErr != nil {
			dp.Error = readErr.Error()
		} else if version > store.FormatVersion {
			r.Warnings = append(r.Warnings, fmt.Sprintf("%s: index format %d is newer than this build (%d); upgrade code-graph", name, version, store.FormatVersion))
		}
		if root != "" {
			if _, statErr := os.Stat(root); statErr != nil {
				r.Warnings = append(r.Warnings, fmt.Sprintf("%s: indexed root %s is missing; delete_project or re-index", name, root))
			}
		}
		r.Cache.Projects = append(r.Cache.Projects, dp)
	}
	sort.Slice(r.Cache.Projects, func(i, j int) bool { return r.Cache.Projects[i].Name < r.Cache.Projects[j].Name })
	if r.Warnings == nil {
		r.Warnings = []string{}
	}
	return r
}

// readProjectMetaReadOnly reads user_version and the indexed root path over a
// read-only connection so a diagnostic never stamps or checkpoints a database.
// It falls back to immutable mode when the WAL sidecars cannot be created.
func readProjectMetaReadOnly(ctx context.Context, dbPath string) (version int, root string, err error) {
	for _, dsn := range []string{
		"file:" + dbPath + "?mode=ro&_busy_timeout=2000",
		"file:" + dbPath + "?immutable=1",
	} {
		version, root, err = queryProjectMeta(ctx, dsn)
		if err == nil {
			return version, root, nil
		}
	}
	return 0, "", err
}

func queryProjectMeta(ctx context.Context, dsn string) (int, string, error) {
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return 0, "", err
	}
	defer db.Close()
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, "", err
	}
	var root sql.NullString
	// Older databases may lack the projects table; treat that as "unknown root".
	_ = db.QueryRowContext(ctx, "SELECT root_path FROM projects ORDER BY name LIMIT 1").Scan(&root)
	return version, root.String, nil
}

// doctorProbeVoyage issues one HEAD request with a short timeout. Any HTTP
// response counts as reachable; only transport failures are reported.
func doctorProbeVoyage(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, embed.VoyageEmbedURL, nil)
	if err != nil {
		return "error: " + err.Error()
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "unreachable: " + err.Error()
	}
	_ = resp.Body.Close()
	return fmt.Sprintf("reachable (HTTP %d)", resp.StatusCode)
}

func printDoctorReport(w io.Writer, r doctorReport) {
	fmt.Fprintf(w, "code-graph %s (%s)\n", r.Version, r.Platform)
	fmt.Fprintf(w, "toolset: %s\n", r.Toolset)
	fmt.Fprintf(w, "embeddings: %s; voyage: %s\n", r.Embeddings.Status, r.Embeddings.Reachability)
	fmt.Fprintf(w, "index format: %d (supports %d..%d)\n", r.IndexFormat.Current, r.IndexFormat.MinSupported, r.IndexFormat.Current)
	fmt.Fprintf(w, "\ncache: %s (%d projects, %.1f MB)\n", r.Cache.Dir, len(r.Cache.Projects), r.Cache.TotalMB)
	for _, p := range r.Cache.Projects {
		line := fmt.Sprintf("  %-40s %7.1f MB  format %d  %s", p.Name, p.SizeMB, p.FormatVersion, p.RootPath)
		if p.Error != "" {
			line += "  ERROR: " + p.Error
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprintf(w, "\ngrammars compiled (%d): %s\n", len(r.Grammars.Compiled), strings.Join(r.Grammars.Compiled, ", "))
	if len(r.Grammars.Excluded) > 0 {
		fmt.Fprintf(w, "grammars excluded from this build: %s (rebuild with -tags cbm_all)\n", strings.Join(r.Grammars.Excluded, ", "))
	}
	fmt.Fprintln(w, "\nconfig (set values only; secrets redacted):")
	setAny := false
	for _, c := range r.Config {
		if c.Set {
			fmt.Fprintf(w, "  %s=%s\n", c.Name, c.Value)
			setAny = true
		}
	}
	if !setAny {
		fmt.Fprintln(w, "  (all defaults)")
	}
	if len(r.Warnings) > 0 {
		fmt.Fprintln(w, "\nwarnings:")
		for _, wmsg := range r.Warnings {
			fmt.Fprintf(w, "  - %s\n", wmsg)
		}
	}
}
