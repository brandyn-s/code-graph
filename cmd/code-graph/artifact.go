package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/brandyn-s/code-graph/internal/artifact"
	"github.com/brandyn-s/code-graph/internal/pipeline"
	"github.com/brandyn-s/code-graph/internal/store"
)

// runExportArtifact implements `code-graph export-artifact`.
func runExportArtifact(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("export-artifact", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", "", "repository path whose index to export (default: current directory)")
	project := fs.String("project", "", "project name to export (overrides --repo)")
	out := fs.String("out", "", "output file (default: <project>"+artifact.Extension+" in the current directory)")
	asJSON := fs.Bool("json", false, "print the artifact header as JSON")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: code-graph export-artifact [--repo PATH | --project NAME] [--out FILE] [--json]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	name := *project
	if name == "" {
		path := *repo
		if path == "" {
			path = "."
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			fmt.Fprintln(stderr, "export-artifact:", err)
			return 1
		}
		name = pipeline.ProjectNameFromPath(abs)
	}
	st, err := store.Open(name)
	if err != nil {
		fmt.Fprintf(stderr, "export-artifact: open index for %s: %v\n", name, err)
		return 1
	}
	defer st.Close()
	outPath := *out
	if outPath == "" {
		outPath = name + artifact.Extension
	}
	h, err := artifact.Export(context.Background(), st, name, outPath, version)
	if err != nil {
		fmt.Fprintln(stderr, "export-artifact:", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"path": outPath, "header": h})
		return 0
	}
	fmt.Fprintf(stdout, "exported %s\n  project: %s\n  nodes: %d  edges: %d  files: %d\n  indexed_at: %s\n  identity: %s\n",
		outPath, h.Project, h.NodeCount, h.EdgeCount, h.FileCount, h.IndexedAt, describeIdentity(h))
	return 0
}

// runImportArtifact implements `code-graph import-artifact`.
func runImportArtifact(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("import-artifact", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", "", "local checkout the artifact should serve (default: the artifact's recorded root path)")
	allowStale := fs.Bool("allow-stale", false, "import even if the artifact was built from another revision or a dirty tree")
	force := fs.Bool("force", false, "replace an existing local index for the project")
	asJSON := fs.Bool("json", false, "print the import report as JSON")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: code-graph import-artifact FILE [--repo PATH] [--allow-stale] [--force] [--json]")
		fs.PrintDefaults()
	}
	// Accept the FILE operand before or after the flags: Go's flag package
	// stops at the first positional argument, so lift it out first.
	file, flags := splitOperand(args, map[string]bool{"-repo": true, "--repo": true})
	if err := fs.Parse(flags); err != nil {
		return 2
	}
	if file == "" || fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	report, err := artifact.Import(context.Background(), file, artifact.ImportOptions{
		RepoPath:    *repo,
		AllowStale:  *allowStale,
		Force:       *force,
		ProjectName: pipeline.ProjectNameFromPath,
	})
	if err != nil {
		fmt.Fprintln(stderr, "import-artifact:", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
		return 0
	}
	fmt.Fprintf(stdout, "imported %s\n  project: %s (from %s)\n  repo: %s\n  db: %s\n  nodes: %d  edges: %d  files: %d\n",
		file, report.Project, report.Header.Project, report.RepoPath, report.DBPath,
		report.Header.NodeCount, report.Header.EdgeCount, report.Header.FileCount)
	if report.Stale {
		fmt.Fprintf(stdout, "  WARNING stale: %s\n  index_health will report identity_status=stale_source until index_repository runs\n", report.StaleReason)
	}
	return 0
}

func describeIdentity(h *artifact.Header) string {
	if h.Identity == nil {
		return h.IdentityStatus + " (" + h.IdentityReason + ")"
	}
	rev := h.Identity.SourceRevision
	if len(rev) > 12 {
		rev = rev[:12]
	}
	return fmt.Sprintf("revision %s, tree %s", rev, h.Identity.DirtyFingerprint)
}

// splitOperand removes the first positional argument from args and returns
// it with the remaining flag arguments. valueFlags names flags that consume
// the following argument, so their values are not mistaken for the operand.
func splitOperand(args []string, valueFlags map[string]bool) (operand string, rest []string) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			rest = append(rest, a)
			if valueFlags[a] && i+1 < len(args) {
				rest = append(rest, args[i+1])
				i++
			}
			continue
		}
		if operand == "" {
			operand = a
			continue
		}
		rest = append(rest, a)
	}
	return operand, rest
}
