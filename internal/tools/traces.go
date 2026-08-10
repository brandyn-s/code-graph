package tools

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/DeusData/codebase-memory-mcp/internal/traces"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerTraceTools() {
	s.addTool(&mcp.Tool{
		Name: "ingest_traces",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			IdempotentHint:  false,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},

		Description: "Ingest OpenTelemetry JSON traces (OTLP format) to validate and enrich HTTP_CALLS edges. Matches HTTP spans to existing edges by URL path, boosts confidence by +0.15 (capped at 1.0), and sets validated_by_trace=true, trace_call_count, and p99_latency_ns on matched edges. Use after index_repository to confirm static analysis predictions with runtime data. Export traces via: otel-cli or collector with OTLP JSON exporter. Returns error if file_path does not exist or is not valid OTLP JSON.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"project": {
					"type": "string",
					"description": "Name of the indexed project"
				},
				"file_path": {
					"type": "string",
					"description": "Path to the OTLP JSON export file"
				}
			},
			"required": ["project", "file_path"]
		}`),
	}, s.handleIngestTraces)

	// Relationship evidence is registered with trace tooling because it is the
	// read path that exposes both static resolver provenance and any runtime
	// confirmation written by ingest_traces. It remains independently usable
	// when no traces have been ingested.
	s.registerRelationshipEvidenceTool()
}

func (s *Server) handleIngestTraces(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	project := getStringArg(args, "project")
	filePath := getStringArg(args, "file_path")

	if project == "" || filePath == "" {
		return errResult("project and file_path are required"), nil
	}

	// Resolve to absolute path and validate it doesn't escape via traversal.
	absPath, pathErr := filepath.Abs(filePath)
	if pathErr != nil {
		return errResult("invalid file_path: " + pathErr.Error()), nil //nolint:nilerr // WHY: MCP handler contract returns tool errors in result, Go error is always nil
	}
	filePath = absPath

	st, err := s.resolveStore(project)
	if err != nil {
		return errResult("store: " + err.Error()), nil //nolint:nilerr // WHY: MCP handler contract returns tool errors in result, Go error is always nil
	}

	result, err := traces.Ingest(st, project, filePath)
	if err != nil {
		return errResult(err.Error()), nil
	}

	// Wrap the IngestResult in a map so we can add the standard
	// _metadata block alongside the trace counts. The IngestResult
	// struct is owned by internal/traces, so wrapping at the tool
	// boundary keeps cross-package coupling minimal.
	return jsonResult(map[string]any{
		"spans_processed": result.SpansProcessed,
		"edges_validated": result.EdgesValidated,
		"edges_enriched":  result.EdgesEnriched,
		"_metadata":       s.stdWriteToolMetadata(ActionOutcomeUpdated),
	}), nil
}
