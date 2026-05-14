// cbm-call-audit — Phase A''' diagnostic for the C extractor's CALLS
// under-emission on Rust function bodies (2026-05-14 ABC roadmap).
//
// Reads one or more Rust source files, runs cbm.ExtractFile, and reports
// CBMCall records grouped by enclosing_func_qn. Surfaces functions whose
// CBMCall count is suspiciously low relative to source size — the bug
// shape established earlier: assetman/src/cmd/runners.rs::run_services
// has ~30 visible call sites but emits 1 CBMCall.
//
// Usage:
//
//	cbm-call-audit --project <project> --file file1.rs [--file file2.rs ...]
//	cbm-call-audit --project <project> --dir <directory>
//
// Output (one JSON object per file on stdout):
//
//	{
//	  "file": "assetman/src/cmd/runners.rs",
//	  "module_qn": "...",
//	  "functions": [
//	    {"qn":"...run_services","cbm_calls":1,"loc_lines":50,
//	     "calls_per_line":0.02},
//	    ...
//	  ]
//	}
//
// loc_lines is the function's source line count from the Definition record.
// calls_per_line is a rough density metric; low density on a non-trivial
// function (>10 lines) is the bug-class signature.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/cbm"
	"github.com/DeusData/codebase-memory-mcp/internal/lang"
)

type funcRow struct {
	QN           string  `json:"qn"`
	CBMCalls     int     `json:"cbm_calls"`
	LocLines     int     `json:"loc_lines"`
	CallsPerLine float64 `json:"calls_per_line"`
}

type fileReport struct {
	File      string    `json:"file"`
	ModuleQN  string    `json:"module_qn"`
	Functions []funcRow `json:"functions"`
	Skipped   string    `json:"skipped,omitempty"`
}

func auditFile(project, relPath, absPath string) fileReport {
	report := fileReport{File: relPath}
	source, err := os.ReadFile(absPath)
	if err != nil {
		report.Skipped = fmt.Sprintf("read error: %v", err)
		return report
	}
	result, err := cbm.ExtractFile(source, lang.Rust, project, relPath)
	if err != nil {
		report.Skipped = fmt.Sprintf("extract error: %v", err)
		return report
	}
	report.ModuleQN = result.ModuleQN

	// Map function QN -> Definition for line counts.
	defByQN := make(map[string]cbm.Definition, len(result.Definitions))
	for _, d := range result.Definitions {
		if d.Label == "Function" || d.Label == "Method" {
			defByQN[d.QualifiedName] = d
		}
	}

	// Count CBMCalls per enclosing_func_qn.
	callsByQN := make(map[string]int)
	for _, c := range result.Calls {
		callsByQN[c.EnclosingFuncQN]++
	}

	// Emit one row per Function/Method definition; include zero-call
	// functions explicitly (that's the bug surface).
	for qn, def := range defByQN {
		count := callsByQN[qn]
		loc := def.Lines
		var density float64
		if loc > 0 {
			density = float64(count) / float64(loc)
		}
		report.Functions = append(report.Functions, funcRow{
			QN:           qn,
			CBMCalls:     count,
			LocLines:     loc,
			CallsPerLine: density,
		})
	}
	return report
}

func main() {
	project := flag.String("project", "", "Project name (used for QN computation)")
	dir := flag.String("dir", "", "Directory of Rust files to audit")
	files := flag.String("files", "", "Comma-separated list of files to audit")
	flag.Parse()

	if *project == "" {
		log.Fatal("--project required")
	}
	if *dir == "" && *files == "" {
		log.Fatal("one of --dir or --files required")
	}

	var inputs []string
	if *files != "" {
		for _, f := range strings.Split(*files, ",") {
			f = strings.TrimSpace(f)
			if f != "" {
				inputs = append(inputs, f)
			}
		}
	}
	if *dir != "" {
		err := filepath.WalkDir(*dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if strings.HasSuffix(p, ".rs") {
				inputs = append(inputs, p)
			}
			return nil
		})
		if err != nil {
			log.Fatalf("walk error: %v", err)
		}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	for _, p := range inputs {
		rel := p
		if *dir != "" {
			r, err := filepath.Rel(*dir, p)
			if err == nil {
				rel = filepath.ToSlash(r)
			}
		}
		report := auditFile(*project, rel, p)
		if err := enc.Encode(report); err != nil {
			log.Fatalf("encode error: %v", err)
		}
	}
}
