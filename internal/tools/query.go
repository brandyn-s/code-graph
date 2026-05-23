package tools

import (
	"context"
	"fmt"

	"github.com/DeusData/codebase-memory-mcp/internal/cypher"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) handleQueryGraph(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	query := getStringArg(args, "query")
	if query == "" {
		return errResult("missing required 'query' parameter"), nil
	}

	maxRows := getIntArg(args, "max_rows", 0)
	cacheKey := fmt.Sprintf("cypher:%s:%s:%d", getStringArg(args, "project"), query, maxRows)

	if cached, ok := s.queryCache.Get(cacheKey); ok {
		res := jsonResult(cached)
		s.addUpdateNotice(res)
		return res, nil
	}

	st, err := s.resolveStore(getStringArg(args, "project"))
	if err != nil {
		return errResult(fmt.Sprintf("resolve store: %v", err)), nil
	}

	exec := &cypher.Executor{Store: st, MaxRows: maxRows}
	result, err := exec.Execute(query)
	if err != nil {
		return errResult(fmt.Sprintf("query error: %v", err)), nil
	}

	responseData := map[string]any{
		"columns":       result.Columns,
		"rows":          result.Rows,
		"total":         len(result.Rows),
		"effective_cap": result.EffectiveCap,
		"_metadata":     s.stdReadGraphMetadata(getStringArg(args, "project")),
	}
	// Surface truncation explicitly so clients can detect when the returned rows
	// are a sample rather than the full matching set. The bench-accuracy harness
	// incident (2026-04-23) showed that silent capping at 200 rows produced a
	// 14.5% "precision" reading that was actually 97.9% once the cap was
	// bypassed via WHERE-shard queries. Explicit signaling prevents re-runs of
	// that failure mode.
	if result.Truncated {
		responseData["capped"] = true
		responseData["capped_hint"] = fmt.Sprintf(
			"Results truncated at %d rows. Raise max_rows (up to 10000), narrow with WHERE, or shard by caller prefix.",
			result.EffectiveCap,
		)
	}
	// Surface projects whose per-project execution failed. Previously a
	// single corrupt-DB project would silently drop out of the merged
	// result; callers can now see which projects were excluded.
	if len(result.SkippedProjects) > 0 {
		skipped := make([]map[string]any, 0, len(result.SkippedProjects))
		for _, sp := range result.SkippedProjects {
			skipped = append(skipped, map[string]any{"name": sp.Name, "err": sp.Err})
		}
		responseData["skipped_projects"] = skipped
	}
	// Surface unbounded-`*` cap so callers know when they wrote `*` but
	// the engine applied a depth limit. The signal is suppressed when the
	// query used an explicit `*N..M` upper bound.
	if result.UnboundedDepthCap > 0 {
		responseData["unbounded_depth_cap"] = result.UnboundedDepthCap
		responseData["unbounded_depth_hint"] = fmt.Sprintf(
			"Unbounded `*` path was capped at depth %d. Use `*N..M` for an explicit bound, or raise via Executor.MaxUnboundedPathDepth.",
			result.UnboundedDepthCap,
		)
	}
	s.addIndexStatus(responseData)

	s.queryCache.Set(cacheKey, responseData)

	res := jsonResult(responseData)
	s.addUpdateNotice(res)
	return res, nil
}
