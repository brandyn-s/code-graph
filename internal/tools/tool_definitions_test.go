package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRegisteredToolDefinitionsJSONIsDeterministicAndComplete(t *testing.T) {
	first, err := RegisteredToolDefinitionsJSON()
	if err != nil {
		t.Fatalf("first export: %v", err)
	}
	second, err := RegisteredToolDefinitionsJSON()
	if err != nil {
		t.Fatalf("second export: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("registered tool definition export is not deterministic")
	}

	var definitions []struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		InputSchema map[string]any `json:"inputSchema"`
	}
	if err := json.Unmarshal(first, &definitions); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if got, want := len(definitions), 38; got != want {
		t.Fatalf("exported tool count = %d, want %d", got, want)
	}

	seen := make(map[string]struct{}, len(definitions))
	for index, definition := range definitions {
		if definition.Name == "" {
			t.Fatalf("definition %d has an empty name", index)
		}
		if definition.Description == "" {
			t.Fatalf("tool %q has an empty description", definition.Name)
		}
		if definition.InputSchema == nil {
			t.Fatalf("tool %q has no input schema", definition.Name)
		}
		if index > 0 && definitions[index-1].Name >= definition.Name {
			t.Fatalf(
				"tool definitions are not strictly name-sorted at %q, %q",
				definitions[index-1].Name,
				definition.Name,
			)
		}
		if _, duplicate := seen[definition.Name]; duplicate {
			t.Fatalf("tool %q is exported more than once", definition.Name)
		}
		seen[definition.Name] = struct{}{}
	}
}

func TestAddToolRejectsDuplicateNameBeforeReplacement(t *testing.T) {
	const name = "duplicate_contract_tool"
	first := &mcp.Tool{
		Name:        name,
		Description: "first definition",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}
	second := &mcp.Tool{
		Name:        name,
		Description: "second definition",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}
	server := &Server{
		handlers:        make(map[string]mcp.ToolHandler),
		toolDefinitions: make(map[string]*mcp.Tool),
	}
	server.addTool(first, nil)

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Error("duplicate registration did not panic")
		} else if message := fmt.Sprint(recovered); !strings.Contains(message, name) {
			t.Errorf("duplicate registration panic %q does not name tool %q", message, name)
		}
		if got := server.toolDefinitions[name]; got != first {
			t.Errorf("duplicate registration replaced first definition with %+v", got)
		}
		if got := server.handlers[name]; got != nil {
			t.Error("duplicate registration replaced first handler")
		}
	}()

	server.addTool(second, func(
		_ context.Context,
		_ *mcp.CallToolRequest,
	) (*mcp.CallToolResult, error) {
		return nil, nil
	})
}
