package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// degree_filter answers questions of the shape "find functions with N callers"
// or "find functions calling nothing" — i.e., in/out-degree predicates on the
// CALLS edge type for a given node label.
//
// Motivated by PSM head-to-head Q9 (2026-05-12): both our binary AND upstream
// codebase-memory-mcp v0.6.1 exhaust max_turns trying to express "functions
// with no callers" via Cypher on an 80K-node graph. Neither engine plans the
// degree-filter query efficiently enough for an agent to converge within its
// turn budget. A dedicated tool with a direct SQL aggregate sidesteps the
// Cypher planner and answers in one call.
//
// Schema:
//
//	{
//	  project: string,
//	  label:   string,   // Function | Method | Class | etc.
//	  direction: "inbound" | "outbound",  // inbound = callers; outbound = callees
//	  edge_type: string, // CALLS by default; any project edge type
//	  op:        "eq" | "lt" | "le" | "gt" | "ge",
//	  value:     integer,
//	  limit:     integer  // max examples to return (default 20, cap 200)
//	}
//
// Returns:
//
//	{
//	  count:     <total nodes matching>,
//	  examples:  [{name, qualified_name, file, degree}, ...]
//	}

func (s *Server) registerDegreeFilterTool() {
	s.addTool(&mcp.Tool{
		Name: "degree_filter",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(false),
			DestructiveHint: boolPtr(false),
		},
		Description: "Find nodes whose in-degree or out-degree on a given edge type matches a predicate. Use this for: dead-code detection (functions with zero callers — direction=inbound, op=eq, value=0); entry-point discovery; high-fan-in hubs (op=ge, value=20); leaf functions (direction=outbound, op=eq, value=0). Returns total count plus up to `limit` examples with degree. Far faster than Cypher for degree-aggregate queries on large graphs (both codebase-memory engines hit max_turns trying to plan this via query_graph).",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"project":   {"type": "string", "description": "Project name (optional — uses session project if omitted)"},
				"label":     {"type": "string", "description": "Node label to filter (Function, Method, Class, Interface, etc.). Required."},
				"direction": {"type": "string", "enum": ["inbound", "outbound"], "description": "inbound = count incoming edges (callers); outbound = count outgoing edges (callees). Required."},
				"edge_type": {"type": "string", "description": "Edge type to count. Default CALLS. Other useful types: IMPORTS, DEFINES, IMPLEMENTS, OVERRIDE."},
				"op":        {"type": "string", "enum": ["eq", "lt", "le", "gt", "ge"], "description": "Comparison: eq (=), lt (<), le (<=), gt (>), ge (>=). Required."},
				"value":     {"type": "integer", "description": "Threshold to compare degree against. 0 for no-edge cases. Required."},
				"limit":     {"type": "integer", "description": "Max example nodes to return (1-200, default 20)"}
			},
			"required": ["label", "direction", "op", "value"]
		}`),
	}, s.handleDegreeFilter)
}

func (s *Server) handleDegreeFilter(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	project := getStringArg(args, "project")
	label := getStringArg(args, "label")
	direction := getStringArg(args, "direction")
	edgeType := getStringArg(args, "edge_type")
	op := getStringArg(args, "op")
	value := getIntArg(args, "value", -1)
	limit := getIntArg(args, "limit", 20)

	if label == "" {
		return errResult("label is required"), nil
	}
	if direction != "inbound" && direction != "outbound" {
		return errResult("direction must be 'inbound' or 'outbound'"), nil
	}
	if edgeType == "" {
		edgeType = "CALLS"
	}
	validOps := map[string]string{"eq": "=", "lt": "<", "le": "<=", "gt": ">", "ge": ">="}
	sqlOp, ok := validOps[op]
	if !ok {
		return errResult("op must be one of eq, lt, le, gt, ge"), nil
	}
	if value < 0 {
		return errResult("value is required and must be >= 0"), nil
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}

	st, err := s.resolveStore(project)
	if err != nil {
		return errResult(fmt.Sprintf("resolve store: %v", err)), nil
	}
	projName := s.resolveProjectName(project)
	projects, _ := st.ListProjects()
	if len(projects) > 0 {
		projName = projects[0].Name
	}

	// SQL: count edges per node, filter by op + value, return matching nodes.
	// inbound = COUNT incoming edges where target_id = nodes.id
	// outbound = COUNT outgoing edges where source_id = nodes.id
	var sqlQuery string
	switch direction {
	case "inbound":
		sqlQuery = `
			WITH degree AS (
				SELECT n.id AS node_id, n.name, n.qualified_name, n.file_path,
				       COALESCE(d.cnt, 0) AS deg
				FROM nodes n
				LEFT JOIN (
					SELECT target_id, COUNT(*) AS cnt
					FROM edges
					WHERE project = ? AND type = ?
					GROUP BY target_id
				) d ON d.target_id = n.id
				WHERE n.project = ? AND n.label = ?
			)
			SELECT node_id, name, qualified_name, file_path, deg
			FROM degree
			WHERE deg ` + sqlOp + ` ?
		`
	case "outbound":
		sqlQuery = `
			WITH degree AS (
				SELECT n.id AS node_id, n.name, n.qualified_name, n.file_path,
				       COALESCE(d.cnt, 0) AS deg
				FROM nodes n
				LEFT JOIN (
					SELECT source_id, COUNT(*) AS cnt
					FROM edges
					WHERE project = ? AND type = ?
					GROUP BY source_id
				) d ON d.source_id = n.id
				WHERE n.project = ? AND n.label = ?
			)
			SELECT node_id, name, qualified_name, file_path, deg
			FROM degree
			WHERE deg ` + sqlOp + ` ?
		`
	}

	db := st.DB()
	if db == nil {
		return errResult("store: no DB handle"), nil
	}

	rows, err := db.Query(sqlQuery, projName, edgeType, projName, label, value)
	if err != nil {
		return errResult(fmt.Sprintf("query: %v", err)), nil
	}
	defer rows.Close()

	type example struct {
		Name          string `json:"name"`
		QualifiedName string `json:"qualified_name,omitempty"`
		File          string `json:"file,omitempty"`
		Degree        int    `json:"degree"`
	}

	count := 0
	var examples []example
	for rows.Next() {
		var nodeID int64
		var name, qn, file string
		var deg int
		if err := rows.Scan(&nodeID, &name, &qn, &file, &deg); err != nil {
			return errResult(fmt.Sprintf("scan: %v", err)), nil
		}
		count++
		if len(examples) < limit {
			examples = append(examples, example{
				Name:          name,
				QualifiedName: qn,
				File:          file,
				Degree:        deg,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return errResult(fmt.Sprintf("rows: %v", err)), nil
	}

	response := map[string]any{
		"project":   projName,
		"label":     label,
		"direction": direction,
		"edge_type": edgeType,
		"op":        op,
		"value":     value,
		"count":     count,
		"examples":  examples,
	}
	result := jsonResult(response)
	s.addUpdateNotice(result)
	return result, nil
}
