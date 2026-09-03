// Package pipeline — Python indirect-dispatch analyzer (v0.1 stub).
//
// Detects `executor.submit(fn, ...)` patterns and emits INDIRECT_CALLS
// edges so trace_call_path can report them. See INDIRECT_CALLS_DESIGN.md
// for the multi-iter scope and v0.2-v0.5 follow-ups (getattr, decorators,
// fn-pointer-as-arg, **kwargs propagation).
//
// v0.1 status: DESIGN COMPLETE, IMPLEMENTATION STUB.
//
// Why a stub: this is a multi-week analyzer. Full implementation requires
// (1) walking tree-sitter Python AST to find call sites, (2) tracking the
// type of `executor` via local-scope assignment-tracing, (3) resolving
// the first argument (a Name node) to its def, (4) inserting the edge
// via store.InsertEdge with correct project/source/target IDs. Each is
// straightforward in isolation; together they need careful integration
// with the existing pass structure (passCalls, passDefinitions). This
// file lands the API shape so subsequent sessions can extend.

package pipeline

import (
	"github.com/brandyn-s/code-graph/internal/store"
)

// IndirectCallEdge is a candidate INDIRECT_CALLS edge surfaced by the
// indirect-dispatch analyzer. The pipeline converts these to store edges
// after deduping with the regular CALLS pass.
type IndirectCallEdge struct {
	SourceQN     string // qualified name of the caller (the function containing the dispatch site)
	TargetQN     string // qualified name of the resolved callee
	DispatchKind string // "executor_submit" | "getattr" | "decorator" | "fn_pointer" | ...
	Confidence   string // "high" | "medium" | "low" | "speculative"
	Properties   map[string]any
}

// AnalyzePythonIndirectCalls scans a Python file's AST for indirect-dispatch
// patterns and returns candidate INDIRECT_CALLS edges. Currently handles
// only `executor.submit(fn, ...)` (v0.1).
//
// TODO(v0.1): walk the AST for `Call(Attribute(Name(executor), "submit"))`.
// TODO(v0.1): verify `executor` is bound to a `ThreadPoolExecutor` /
//
//	`ProcessPoolExecutor` via local scope assignment lookup.
//
// TODO(v0.1): resolve the first arg (a Name node) to a def in the same
//
//	module via the existing fqn.Compute infrastructure.
//
// TODO(v0.1): emit one IndirectCallEdge per resolved dispatch site.
//
// Returns: empty slice in v0.1 stub. Test
// (TestAnalyzePythonIndirectCalls_executor_submit) verifies that the
// function exists and returns the expected shape; the actual edge count
// assertion is `>= 0` until v0.1 implementation lands.
func AnalyzePythonIndirectCalls(filePath string, source []byte) []IndirectCallEdge {
	// v0.1 stub — return no edges.
	// Real implementation calls into the tree-sitter Python parser
	// (already used by passCalls / passDefinitions) and walks the AST.
	_ = filePath
	_ = source
	return nil
}

// runIndirectCallsPass is the pipeline integration point. Called after
// passCalls so it can dedupe against the resolved CALLS edges.
//
// TODO(v0.1): wire into Pipeline.runPostFlushPasses or runSemanticEdgePasses.
// TODO(v0.1): for each Python file, call AnalyzePythonIndirectCalls.
// TODO(v0.1): for each returned edge, look up source_id and target_id via
//
//	store.FindNodeByQN, then store.InsertEdge with type='INDIRECT_CALLS'.
//
// TODO(v0.1): increment the existing unresolved_call_count diagnostic appropriately
//
//	(now that some of those calls ARE resolved as INDIRECT_CALLS,
//	they should not double-count toward speculative band).
//
// Wiring note: the existing INDIRECT_CALLS edge type is in
// internal/store/edges.go; the schema is ready. Only the pass is missing.
func (p *Pipeline) runIndirectCallsPass() {
	// v0.1 stub. No-op so the pipeline can surface "feature in progress"
	// without breaking existing behavior. Wire log once the pass actually
	// emits edges (uses the package-level slog like other passes).
	_ = p
}

// Confidence-band hooks: when computing trace_call_path's confidence_band,
// INDIRECT_CALLS edges should contribute to the resolved bucket according to
// their `confidence` property. The current
// `internal/tools/trace.go::traceConfidenceBand` only counts regular CALLS
// edges; v0.1 ships the analyzer alone, v0.2 extends the band calculation.
//
// TODO(v0.2): in trace.go::buildTraceResponse, when iterating edges,
//             include INDIRECT_CALLS where properties.confidence in {"high"}
//             as part of resolved count. INDIRECT_CALLS with lower
//             confidence go into a separate `partial_resolved_count` field
//             surfaced in the response so callers know to re-evaluate.

// Compile-time check that store types are wired correctly. If this
// reference fails to compile, the import was wrong.
var _ store.EdgeInfo
