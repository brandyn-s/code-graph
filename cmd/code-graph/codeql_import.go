package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/brandyn-s/code-graph/internal/codeqlimport"
)

func runCodeQLImport(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("import-codeql", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repository := flags.String("repository", "", "clean Git repository analyzed by CodeQL")
	sarif := flags.String("sarif", "", "CodeQL SARIF 2.1.0 artifact")
	receipt := flags.String("receipt", "", "operator-owned query-attestation receipt")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *repository == "" || *sarif == "" || *receipt == "" {
		fmt.Fprintln(stderr, "--repository, --sarif, and --receipt are required")
		return 2
	}
	result, err := codeqlimport.Import(codeqlimport.Request{
		RepositoryRoot: *repository,
		SARIFPath:      *sarif,
		ReceiptPath:    *receipt,
	})
	if err != nil {
		fmt.Fprintf(stderr, "import CodeQL evidence: %v\n", err)
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintf(stderr, "encode CodeQL evidence: %v\n", err)
		return 1
	}
	return 0
}
