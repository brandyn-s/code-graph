package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/brandyn-s/code-graph/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRunTraceBFSBothPropagatesStoreErrors(t *testing.T) {
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	visited, edges, err := runTraceBFS(st, 1, "both", []string{"CALLS"}, 1, 0)
	if err == nil {
		t.Fatalf("runTraceBFS returned partial success after store failure: visited=%v edges=%v", visited, edges)
	}
	if visited != nil || edges != nil {
		t.Fatalf("runTraceBFS returned partial data after store failure: visited=%v edges=%v", visited, edges)
	}
}

func TestRunTraceBFSDoesNotReachNodesThroughFilteredEdges(t *testing.T) {
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}

	rootID, err := st.UpsertNode(&store.Node{
		Project: "test", Label: "Function", Name: "root", QualifiedName: "test.root",
	})
	if err != nil {
		t.Fatalf("insert root: %v", err)
	}
	middleID, err := st.UpsertNode(&store.Node{
		Project: "test", Label: "Function", Name: "middle", QualifiedName: "test.middle",
	})
	if err != nil {
		t.Fatalf("insert middle: %v", err)
	}
	leafID, err := st.UpsertNode(&store.Node{
		Project: "test", Label: "Function", Name: "leaf", QualifiedName: "test.leaf",
	})
	if err != nil {
		t.Fatalf("insert leaf: %v", err)
	}

	if _, err := st.InsertEdge(&store.Edge{
		Project: "test", SourceID: rootID, TargetID: middleID, Type: "CALLS",
		Properties: map[string]any{"confidence": 0.2},
	}); err != nil {
		t.Fatalf("insert filtered edge: %v", err)
	}
	if _, err := st.InsertEdge(&store.Edge{
		Project: "test", SourceID: middleID, TargetID: leafID, Type: "CALLS",
		Properties: map[string]any{"confidence": 0.9},
	}); err != nil {
		t.Fatalf("insert retained edge: %v", err)
	}

	visited, edges, err := runTraceBFS(st, rootID, "outbound", []string{"CALLS"}, 2, 0.45)
	if err != nil {
		t.Fatalf("runTraceBFS: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("trace retained %d unreachable edges, want 0: %+v", len(edges), edges)
	}
	if len(visited) != 0 {
		t.Fatalf("trace reached %d nodes only accessible through a filtered edge, want 0: %+v", len(visited), visited)
	}
}

func TestRunTraceBFSConfidenceDistinguishesAbsentFromZero(t *testing.T) {
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}

	insertNode := func(name string) int64 {
		t.Helper()
		id, err := st.UpsertNode(&store.Node{
			Project: "test", Label: "Function", Name: name, QualifiedName: "test." + name,
		})
		if err != nil {
			t.Fatalf("insert %s: %v", name, err)
		}
		return id
	}
	rootID := insertNode("root")
	missingOutID := insertNode("missing_out")
	nullOutID := insertNode("null_out")
	zeroOutID := insertNode("zero_out")
	missingInID := insertNode("missing_in")
	nullInID := insertNode("null_in")
	zeroInID := insertNode("zero_in")

	edges := []*store.Edge{
		{Project: "test", SourceID: rootID, TargetID: missingOutID, Type: "CALLS"},
		{Project: "test", SourceID: rootID, TargetID: nullOutID, Type: "CALLS", Properties: map[string]any{"confidence": nil}},
		{Project: "test", SourceID: rootID, TargetID: zeroOutID, Type: "CALLS", Properties: map[string]any{"confidence": 0.0}},
		{Project: "test", SourceID: missingInID, TargetID: rootID, Type: "CALLS"},
		{Project: "test", SourceID: nullInID, TargetID: rootID, Type: "CALLS", Properties: map[string]any{"confidence": nil}},
		{Project: "test", SourceID: zeroInID, TargetID: rootID, Type: "CALLS", Properties: map[string]any{"confidence": 0.0}},
	}
	for _, edge := range edges {
		if _, err := st.InsertEdge(edge); err != nil {
			t.Fatalf("InsertEdge %d -> %d: %v", edge.SourceID, edge.TargetID, err)
		}
	}

	tests := []struct {
		direction string
		wantNodes []string
	}{
		{direction: "outbound", wantNodes: []string{"missing_out", "null_out"}},
		{direction: "inbound", wantNodes: []string{"missing_in", "null_in"}},
		{direction: "both", wantNodes: []string{"missing_out", "null_out", "missing_in", "null_in"}},
	}
	for _, tt := range tests {
		t.Run(tt.direction, func(t *testing.T) {
			visited, retainedEdges, err := runTraceBFS(
				st, rootID, tt.direction, []string{"CALLS"}, 1, 0.45,
			)
			if err != nil {
				t.Fatalf("runTraceBFS: %v", err)
			}

			gotNodes := make(map[string]bool, len(visited))
			for _, hop := range visited {
				gotNodes[hop.Node.Name] = true
			}
			if len(gotNodes) != len(tt.wantNodes) {
				t.Fatalf("visited nodes = %v, want %v", gotNodes, tt.wantNodes)
			}
			for _, name := range tt.wantNodes {
				if !gotNodes[name] {
					t.Errorf("visited nodes omitted %q: %v", name, gotNodes)
				}
			}
			if len(retainedEdges) != len(tt.wantNodes) {
				t.Fatalf("retained edges = %d, want %d: %+v", len(retainedEdges), len(tt.wantNodes), retainedEdges)
			}
		})
	}
}

func TestTraceEdgeOutputDistinguishesExplicitZeroFromMissingConfidence(t *testing.T) {
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}

	insertNode := func(name string) int64 {
		t.Helper()
		id, err := st.UpsertNode(&store.Node{
			Project: "test", Label: "Function", Name: name, QualifiedName: "test." + name,
		})
		if err != nil {
			t.Fatalf("insert %s: %v", name, err)
		}
		return id
	}
	rootID := insertNode("root")
	missingID := insertNode("missing")
	zeroID := insertNode("zero")

	for _, edge := range []*store.Edge{
		{Project: "test", SourceID: rootID, TargetID: missingID, Type: "CALLS"},
		{
			Project: "test", SourceID: rootID, TargetID: zeroID, Type: "CALLS",
			Properties: map[string]any{"confidence": 0.0},
		},
	} {
		if _, err := st.InsertEdge(edge); err != nil {
			t.Fatalf("InsertEdge %d -> %d: %v", edge.SourceID, edge.TargetID, err)
		}
	}

	_, edges, err := runTraceBFS(st, rootID, "outbound", []string{"CALLS"}, 1, 0)
	if err != nil {
		t.Fatalf("runTraceBFS: %v", err)
	}
	output := buildEdgeList(edges)
	byTarget := make(map[string]map[string]any, len(output))
	for _, edge := range output {
		target, ok := edge["to"].(string)
		if !ok {
			t.Fatalf("edge target is not a string: %v", edge)
		}
		byTarget[target] = edge
	}

	if _, present := byTarget["missing"]["confidence"]; present {
		t.Fatalf("missing confidence was serialized: %v", byTarget["missing"])
	}
	confidence, present := byTarget["zero"]["confidence"]
	if !present {
		t.Fatalf("explicit zero confidence was omitted: %v", byTarget["zero"])
	}
	if confidence != float64(0) {
		t.Fatalf("explicit zero confidence = %v, want 0", confidence)
	}
}

func newTraceEdgeTypeTestServer(t *testing.T) *Server {
	t.Helper()
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)
	st, err := router.ForProject("test")
	if err != nil {
		t.Fatalf("ForProject: %v", err)
	}
	if err := st.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}

	rootID, err := st.UpsertNode(&store.Node{
		Project: "test", Label: "Function", Name: "root", QualifiedName: "test.root",
	})
	if err != nil {
		t.Fatalf("insert root: %v", err)
	}
	callID, err := st.UpsertNode(&store.Node{
		Project: "test", Label: "Function", Name: "call_target", QualifiedName: "test.call_target",
	})
	if err != nil {
		t.Fatalf("insert call target: %v", err)
	}
	httpCallID, err := st.UpsertNode(&store.Node{
		Project: "test", Label: "Function", Name: "http_target", QualifiedName: "test.http_target",
	})
	if err != nil {
		t.Fatalf("insert HTTP call target: %v", err)
	}
	asyncCallID, err := st.UpsertNode(&store.Node{
		Project: "test", Label: "Function", Name: "async_target", QualifiedName: "test.async_target",
	})
	if err != nil {
		t.Fatalf("insert async call target: %v", err)
	}
	usageID, err := st.UpsertNode(&store.Node{
		Project: "test", Label: "Function", Name: "usage_target", QualifiedName: "test.usage_target",
	})
	if err != nil {
		t.Fatalf("insert usage target: %v", err)
	}
	overrideID, err := st.UpsertNode(&store.Node{
		Project: "test", Label: "Method", Name: "override_target", QualifiedName: "test.override_target",
	})
	if err != nil {
		t.Fatalf("insert override target: %v", err)
	}
	for _, edge := range []*store.Edge{
		{Project: "test", SourceID: rootID, TargetID: callID, Type: "CALLS"},
		{Project: "test", SourceID: rootID, TargetID: httpCallID, Type: "HTTP_CALLS"},
		{Project: "test", SourceID: rootID, TargetID: asyncCallID, Type: "ASYNC_CALLS"},
		{Project: "test", SourceID: rootID, TargetID: usageID, Type: "USAGE"},
		{Project: "test", SourceID: rootID, TargetID: overrideID, Type: "OVERRIDE"},
	} {
		if _, err := st.InsertEdge(edge); err != nil {
			t.Fatalf("InsertEdge %s: %v", edge.Type, err)
		}
	}

	return NewServer(router)
}

func TestTraceCallPathDefaultsToCallLikeEdges(t *testing.T) {
	srv := newTraceEdgeTypeTestServer(t)
	response := metadataResponseFromHandler(
		t,
		srv.handleTraceCallPath,
		"trace_call_path",
		map[string]any{
			"function_name": "root",
			"project":       "test",
			"depth":         1,
		},
	)

	foundTypes := map[string]bool{}
	for _, raw := range toSlice(response["edges"]) {
		edge, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		edgeType, _ := edge["type"].(string)
		foundTypes[edgeType] = true
	}
	for _, want := range []string{"CALLS", "HTTP_CALLS", "ASYNC_CALLS"} {
		if !foundTypes[want] {
			t.Errorf("trace_call_path default traversal omitted call-like %s edge: %v", want, response["edges"])
		}
	}
	for _, unwanted := range []string{"USAGE", "OVERRIDE"} {
		if foundTypes[unwanted] {
			t.Errorf("trace_call_path default traversal included opt-in %s edge: %v", unwanted, response["edges"])
		}
	}
	totalResults, ok := response["total_results"].(float64)
	if !ok {
		t.Fatalf("total_results is %T, want number", response["total_results"])
	}
	if got := int(totalResults); got != 3 {
		t.Fatalf("trace_call_path reached %d nodes, want three call-like targets", got)
	}
}

func TestTraceCallPathEdgeTypesRestrictsTraversal(t *testing.T) {
	srv := newTraceEdgeTypeTestServer(t)
	response := metadataResponseFromHandler(
		t,
		srv.handleTraceCallPath,
		"trace_call_path",
		map[string]any{
			"function_name": "root",
			"project":       "test",
			"depth":         1,
			"edge_types":    []string{"USAGE"},
		},
	)

	edges := toSlice(response["edges"])
	if len(edges) != 1 {
		t.Fatalf("edge_types=[USAGE] returned %d edges, want 1: %v", len(edges), edges)
	}
	edge, ok := edges[0].(map[string]any)
	if !ok {
		t.Fatalf("edge is %T, want map", edges[0])
	}
	if got := edge["type"]; got != "USAGE" {
		t.Fatalf("edge_types=[USAGE] returned type %v, want USAGE", got)
	}
	totalResults, ok := response["total_results"].(float64)
	if !ok {
		t.Fatalf("total_results is %T, want number", response["total_results"])
	}
	if got := int(totalResults); got != 1 {
		t.Fatalf("edge_types=[USAGE] reached %d nodes, want 1", got)
	}
}

func TestTraceCallPathCallConfidenceFollowsEdgeSelection(t *testing.T) {
	tests := []struct {
		name                 string
		edgeTypes            []string
		includeMinConfidence bool
		minConfidence        float64
		wantBand             string
		wantRationale        string
	}{
		{
			name:          "default thresholded calls",
			edgeTypes:     []string{"CALLS"},
			wantBand:      "unknown",
			wantRationale: "min_confidence",
		},
		{
			name:                 "unfiltered calls have matching statistics",
			edgeTypes:            []string{"CALLS"},
			includeMinConfidence: true,
			minConfidence:        0,
			wantBand:             "high",
			wantRationale:        "calls resolved",
		},
		{
			name:          "HTTP calls lack a matching unresolved denominator",
			edgeTypes:     []string{"HTTP_CALLS"},
			wantBand:      "unknown",
			wantRationale: "only measures calls",
		},
		{
			name:          "non-call only",
			edgeTypes:     []string{"USAGE"},
			wantBand:      "unknown",
			wantRationale: "not applicable",
		},
		{
			name:          "mixed call and non-call",
			edgeTypes:     []string{"CALLS", "USAGE"},
			wantBand:      "unknown",
			wantRationale: "not applicable",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTraceEdgeTypeTestServer(t)
			args := map[string]any{
				"function_name": "root",
				"project":       "test",
				"depth":         1,
				"edge_types":    tt.edgeTypes,
			}
			if tt.includeMinConfidence {
				args["min_confidence"] = tt.minConfidence
			}
			response := metadataResponseFromHandler(
				t,
				srv.handleTraceCallPath,
				"trace_call_path",
				args,
			)

			if got := response["confidence_band"]; got != tt.wantBand {
				t.Errorf("confidence_band = %v, want %q", got, tt.wantBand)
			}
			metadata, ok := response["_metadata"].(map[string]any)
			if !ok {
				t.Fatalf("_metadata is %T, want map", response["_metadata"])
			}
			confidence, ok := metadata["confidence"].(map[string]any)
			if !ok {
				t.Fatalf("_metadata.confidence is %T, want map", metadata["confidence"])
			}
			if got := confidence["band"]; got != tt.wantBand {
				t.Errorf("_metadata.confidence.band = %v, want %q", got, tt.wantBand)
			}
			rationale, _ := confidence["rationale"].(string)
			if !strings.Contains(strings.ToLower(rationale), tt.wantRationale) {
				t.Errorf("_metadata.confidence.rationale = %q, want substring %q", rationale, tt.wantRationale)
			}
		})
	}
}

func TestTraceCallPathAllFilteredEdgesHaveUnknownConfidence(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)
	st, err := router.ForProject("test")
	if err != nil {
		t.Fatalf("ForProject: %v", err)
	}
	if err := st.UpsertProject("test", "/tmp/test"); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	rootID, err := st.UpsertNode(&store.Node{
		Project: "test", Label: "Function", Name: "root", QualifiedName: "test.root",
	})
	if err != nil {
		t.Fatalf("insert root: %v", err)
	}
	targetID, err := st.UpsertNode(&store.Node{
		Project: "test", Label: "Function", Name: "target", QualifiedName: "test.target",
	})
	if err != nil {
		t.Fatalf("insert target: %v", err)
	}
	if _, err := st.InsertEdge(&store.Edge{
		Project: "test", SourceID: rootID, TargetID: targetID, Type: "CALLS",
		Properties: map[string]any{"confidence": 0.2},
	}); err != nil {
		t.Fatalf("InsertEdge: %v", err)
	}

	response := metadataResponseFromHandler(
		t,
		NewServer(router).handleTraceCallPath,
		"trace_call_path",
		map[string]any{
			"function_name": "root",
			"project":       "test",
			"depth":         1,
			"edge_types":    []string{"CALLS"},
		},
	)

	if edges := toSlice(response["edges"]); len(edges) != 0 {
		t.Fatalf("default threshold retained %d low-confidence edges, want 0: %v", len(edges), edges)
	}
	if got := response["confidence_band"]; got != "unknown" {
		t.Fatalf("confidence_band = %v, want unknown when every selected edge was filtered", got)
	}
	if got := response["min_confidence"]; got != float64(0.45) {
		t.Fatalf("min_confidence = %v, want 0.45", got)
	}
	metadata, ok := response["_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("_metadata is %T, want map", response["_metadata"])
	}
	confidence, ok := metadata["confidence"].(map[string]any)
	if !ok {
		t.Fatalf("_metadata.confidence is %T, want map", metadata["confidence"])
	}
	rationale, _ := confidence["rationale"].(string)
	if !strings.Contains(strings.ToLower(rationale), "filtered") {
		t.Fatalf("_metadata.confidence.rationale = %q, want filtered-edge explanation", rationale)
	}
}

func TestTraceCallPathRejectsInvalidEdgeTypes(t *testing.T) {
	tests := []struct {
		name      string
		edgeTypes any
	}{
		{"empty", []string{}},
		{"unknown", []string{"READS"}},
		{"non string item", []any{"CALLS", 7}},
		{"duplicate", []string{"CALLS", "CALLS"}},
		{"not an array", "CALLS"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv := newTraceEdgeTypeTestServer(t)
			args, err := json.Marshal(map[string]any{
				"function_name": "root",
				"project":       "test",
				"edge_types":    test.edgeTypes,
			})
			if err != nil {
				t.Fatalf("marshal args: %v", err)
			}
			result, err := srv.handleTraceCallPath(context.Background(), &mcp.CallToolRequest{
				Params: &mcp.CallToolParamsRaw{
					Name:      "trace_call_path",
					Arguments: args,
				},
			})
			if err != nil {
				t.Fatalf("handler returned protocol error: %v", err)
			}
			if result == nil || !result.IsError {
				t.Fatalf("invalid edge_types must return a structured tool error, got %+v", result)
			}
			if len(result.Content) == 0 {
				t.Fatal("structured error has no content")
			}
			text, ok := result.Content[0].(*mcp.TextContent)
			if !ok {
				t.Fatalf("structured error content is %T, want TextContent", result.Content[0])
			}
			if !strings.Contains(text.Text, "edge_types") {
				t.Fatalf("error %q does not identify edge_types", text.Text)
			}
		})
	}
}

//nolint:cyclop // This boundary contract intentionally validates every related schema field.
func TestTraceCallPathSchemaAndDescriptionUseDefaultEdgeTypes(t *testing.T) {
	router, err := store.NewRouterWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}
	t.Cleanup(router.CloseAll)

	srv := NewServer(router)
	srv.sessionOnce.Do(func() {})
	srv.updateOnce.Do(func() {})

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.MCPServer().Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(
		&mcp.Implementation{Name: "trace-schema-test", Version: "dev"},
		&mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}},
	)
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	list, err := clientSession.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	var traceTool *mcp.Tool
	for _, tool := range list.Tools {
		if tool.Name == "trace_call_path" {
			traceTool = tool
			break
		}
	}
	if traceTool == nil {
		t.Fatal("trace_call_path not advertised")
	}

	expectedDefault := []string{"CALLS", "HTTP_CALLS", "ASYNC_CALLS"}
	expectedSupported := []string{"CALLS", "HTTP_CALLS", "ASYNC_CALLS", "USAGE", "OVERRIDE"}
	for _, edgeType := range expectedDefault {
		if !strings.Contains(traceTool.Description, edgeType) {
			t.Errorf("description omits default edge type %s: %q", edgeType, traceTool.Description)
		}
	}
	for _, edgeType := range []string{"USAGE", "OVERRIDE"} {
		if !strings.Contains(traceTool.Description, edgeType) {
			t.Errorf("description omits opt-in edge type %s: %q", edgeType, traceTool.Description)
		}
	}
	if !strings.Contains(strings.ToLower(traceTool.Description), "opt-in") {
		t.Errorf("description does not distinguish opt-in non-call relationships: %q", traceTool.Description)
	}
	if !strings.Contains(strings.ToLower(traceTool.Description), "confidence is calibrated only for unfiltered calls-only traces") {
		t.Errorf("description does not explain confidence calibration scope: %q", traceTool.Description)
	}

	schema, ok := traceTool.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("input schema is %T, want map", traceTool.InputSchema)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties are %T, want map", schema["properties"])
	}
	edgeTypes, ok := properties["edge_types"].(map[string]any)
	if !ok {
		t.Fatalf("edge_types schema is %T, want map", properties["edge_types"])
	}
	items, ok := edgeTypes["items"].(map[string]any)
	if !ok {
		t.Fatalf("edge_types items schema is %T, want map", edgeTypes["items"])
	}
	enumValues := toSlice(items["enum"])
	if len(enumValues) != len(expectedSupported) {
		t.Fatalf("edge_types enum has %d values, want %d: %v", len(enumValues), len(expectedSupported), enumValues)
	}
	for i, edgeType := range expectedSupported {
		if enumValues[i] != edgeType {
			t.Errorf("edge_types enum[%d] = %v, want %s", i, enumValues[i], edgeType)
		}
	}
	if edgeTypes["minItems"] != float64(1) {
		t.Errorf("edge_types minItems = %v, want 1", edgeTypes["minItems"])
	}
	if edgeTypes["maxItems"] != float64(len(expectedSupported)) {
		t.Errorf("edge_types maxItems = %v, want %d", edgeTypes["maxItems"], len(expectedSupported))
	}
	if edgeTypes["uniqueItems"] != true {
		t.Errorf("edge_types uniqueItems = %v, want true", edgeTypes["uniqueItems"])
	}
	defaultValues := toSlice(edgeTypes["default"])
	if len(defaultValues) != len(expectedDefault) {
		t.Fatalf("edge_types default has %d values, want %d: %v", len(defaultValues), len(expectedDefault), defaultValues)
	}
	for i, edgeType := range expectedDefault {
		if defaultValues[i] != edgeType {
			t.Errorf("edge_types default[%d] = %v, want %s", i, defaultValues[i], edgeType)
		}
	}

	minConfidence, ok := properties["min_confidence"].(map[string]any)
	if !ok {
		t.Fatalf("min_confidence schema is %T, want map", properties["min_confidence"])
	}
	if minConfidence["minimum"] != float64(0) {
		t.Errorf("min_confidence minimum = %v, want 0", minConfidence["minimum"])
	}
	if minConfidence["maximum"] != float64(1) {
		t.Errorf("min_confidence maximum = %v, want 1", minConfidence["maximum"])
	}
	description, _ := minConfidence["description"].(string)
	if !strings.Contains(description, "selected edge types") {
		t.Errorf("min_confidence description does not match traversal semantics: %q", description)
	}
	if !strings.Contains(description, "missing or null") {
		t.Errorf("min_confidence description does not explain absent confidence semantics: %q", description)
	}
	if !strings.Contains(description, "explicit numeric zero") {
		t.Errorf("min_confidence description does not explain zero confidence semantics: %q", description)
	}
	if !strings.Contains(description, "confidence_band is unknown whenever min_confidence is positive") {
		t.Errorf("min_confidence description does not explain thresholded confidence semantics: %q", description)
	}
}

func TestTraceCallPathRejectsInvalidMinConfidence(t *testing.T) {
	tests := []struct {
		name          string
		minConfidence any
	}{
		{"below zero", -0.1},
		{"above one", 1.1},
		{"not numeric", "high"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv := newTraceEdgeTypeTestServer(t)
			args, err := json.Marshal(map[string]any{
				"function_name":  "root",
				"project":        "test",
				"min_confidence": test.minConfidence,
			})
			if err != nil {
				t.Fatalf("marshal args: %v", err)
			}
			result, err := srv.handleTraceCallPath(context.Background(), &mcp.CallToolRequest{
				Params: &mcp.CallToolParamsRaw{
					Name:      "trace_call_path",
					Arguments: args,
				},
			})
			if err != nil {
				t.Fatalf("handler returned protocol error: %v", err)
			}
			if result == nil || !result.IsError {
				t.Fatalf("invalid min_confidence must return a structured tool error, got %+v", result)
			}
			if len(result.Content) == 0 {
				t.Fatal("structured error has no content")
			}
			text, ok := result.Content[0].(*mcp.TextContent)
			if !ok {
				t.Fatalf("structured error content is %T, want TextContent", result.Content[0])
			}
			if !strings.Contains(text.Text, "min_confidence") {
				t.Fatalf("error %q does not identify min_confidence", text.Text)
			}
		})
	}
}
