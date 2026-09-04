// Command export-tool-schemas prints the canonical registered MCP tool
// definitions without starting a server transport or invoking tool handlers.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/brandyn-s/code-graph/internal/tools"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--core" {
		core, err := json.Marshal(tools.CoreToolNames())
		if err != nil {
			fmt.Fprintf(os.Stderr, "export core toolset: %v\n", err)
			os.Exit(1)
		}
		if _, err := os.Stdout.Write(append(core, '\n')); err != nil {
			fmt.Fprintf(os.Stderr, "write core toolset: %v\n", err)
			os.Exit(1)
		}
		return
	}
	definitions, err := tools.RegisteredToolDefinitionsJSON()
	if err != nil {
		fmt.Fprintf(os.Stderr, "export registered tool definitions: %v\n", err)
		os.Exit(1)
	}
	if _, err := os.Stdout.Write(definitions); err != nil {
		fmt.Fprintf(os.Stderr, "write registered tool definitions: %v\n", err)
		os.Exit(1)
	}
}
