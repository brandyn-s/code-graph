// Command export-tool-schemas prints the canonical registered MCP tool
// definitions without starting a server transport or invoking tool handlers.
package main

import (
	"fmt"
	"os"

	"github.com/DeusData/codebase-memory-mcp/internal/tools"
)

func main() {
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
