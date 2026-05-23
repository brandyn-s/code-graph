package cypher

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

const defaultMaxRows = 200
const absoluteMaxRows = 10000

// defaultUnboundedPathDepth caps depth for variable-length patterns written
// as `*` with no upper bound. Without a cap, a cyclic graph would let BFS
// run unbounded; with a silent cap, the caller has no idea their `*` was
// clipped. The new behavior caps AND signals via Result.UnboundedDepthCap.
const defaultUnboundedPathDepth = 10

// Executor runs Cypher execution plans against a store.
type Executor struct {
	Store   *store.Store
	MaxRows int // 0 means defaultMaxRows

	// MaxUnboundedPathDepth overrides the depth used when a query uses a
	// bare `*` variable-length pattern with no upper bound. Zero means
	// defaultUnboundedPathDepth. The effective cap is always reported on
	// Result.UnboundedDepthCap when the cap fires so callers see when
	// their `*` was clipped.
	MaxUnboundedPathDepth int

	regexCache       map[string]*regexp.Regexp
	ctx              context.Context // set by Execute, used for DB queries
	expandLimit      int             // binding cap for current query (set per-execution)
	truncated        bool            // set to true when ANY clipping step drops results; propagated to Result
	skippedProjects  []SkippedProject
	unboundedCapHit  int // depth used when an unbounded `*` was capped; 0 means not used
}

// markTruncated records that an intermediate or final cap trimmed results.
// Called from every clipping site so the Result carries an accurate
// "some rows were dropped" signal — critical for clients like benchmark
// harnesses that would otherwise silently sample.
func (e *Executor) markTruncated() {
	e.truncated = true
}

func (e *Executor) maxRows() int {
	if e.MaxRows <= 0 {
		return defaultMaxRows
	}
	if e.MaxRows > absoluteMaxRows {
		return absoluteMaxRows
	}
	return e.MaxRows
}

// bindingCap returns the maximum number of bindings to keep during expansion.
// For aggregation queries (COUNT), we need all bindings for correct counts,
// so the cap is much higher. For non-aggregation queries, cap at maxRows*2.
func (e *Executor) bindingCap(ret *ReturnClause) int {
	if ret != nil {
		for _, item := range ret.Items {
			if item.Func == "COUNT" {
				return absoluteMaxRows * 10 // 100k cap for aggregation
			}
		}
	}
	return e.maxRows() * 2
}

// Result holds the tabular output of a query.
type Result struct {
	Columns []string         `json:"columns"`
	Rows    []map[string]any `json:"rows"`

	// Truncated is true if any clipping step (final LIMIT/max_rows cap or an
	// intermediate binding-set cap during path expansion) dropped rows. When
	// true, the returned Rows is a sample — not the full matching set. Clients
	// should either raise max_rows, narrow the query with WHERE filters, or
	// shard the query to retrieve the complete result.
	Truncated bool `json:"truncated,omitempty"`

	// EffectiveCap is the row cap that was active for this query. Reflects the
	// lower of (user-supplied max_rows, absoluteMaxRows), defaulting to
	// defaultMaxRows when max_rows is not set. Always populated so clients
	// can report "capped at N" even when Truncated is false.
	EffectiveCap int `json:"effective_cap"`

	// SkippedProjects lists projects whose execution returned an error and
	// were excluded from the merged result. Empty when no projects were
	// skipped. Populating this (instead of silently dropping the project)
	// lets callers see when a single corrupt project DB or transient query
	// failure is shaping the result set.
	SkippedProjects []SkippedProject `json:"skipped_projects,omitempty"`

	// UnboundedDepthCap is set to the depth used when a query uses a bare
	// `*` variable-length pattern and the engine had to apply its default
	// cap. Zero means no unbounded pattern was capped. Callers that issued
	// an unbounded `*` should treat any non-zero value as "this is a
	// sample to depth N — raise MaxUnboundedPathDepth or use an explicit
	// upper bound for full coverage."
	UnboundedDepthCap int `json:"unbounded_depth_cap,omitempty"`
}

// SkippedProject describes a project that errored during execution and
// was excluded from the merged result. Err is the error string at the
// time of skip.
type SkippedProject struct {
	Name string `json:"name"`
	Err  string `json:"err"`
}

// binding maps variable names to matched nodes and edges.
type binding struct {
	nodes map[string]*store.Node
	edges map[string]*store.Edge
}

func newBinding() binding {
	return binding{
		nodes: make(map[string]*store.Node),
		edges: make(map[string]*store.Edge),
	}
}

// Execute parses, plans, and executes a Cypher query across all projects.
func (e *Executor) Execute(query string) (*Result, error) {
	// Reset per-execution accumulator state. truncated / skippedProjects /
	// unboundedCapHit are populated by markTruncated() and the per-project
	// loops in executePlan + tryAggregateSQL; without this reset, a reused
	// Executor inherits flags from the previous query (e.g. Q1 truncated,
	// Q2 with LIMIT 5 still reports Truncated=true). MCP handlers
	// construct a fresh Executor per request, but the type is exported
	// and reuse is the natural pattern — pin the invariant here.
	e.truncated = false
	e.skippedProjects = nil
	e.unboundedCapHit = 0

	if e.ctx == nil {
		e.ctx = context.Background()
	}
	q, err := Parse(query)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	plan, err := BuildPlan(q)
	if err != nil {
		return nil, fmt.Errorf("plan: %w", err)
	}
	result, err := e.executePlan(plan)
	if err != nil {
		return nil, err
	}
	// Always populate EffectiveCap so clients can report the limit that was
	// active even when no truncation occurred. Truncated is already set by the
	// intermediate markTruncated() calls.
	if result != nil {
		result.EffectiveCap = e.maxRows()
		if e.truncated {
			result.Truncated = true
		}
		if len(e.skippedProjects) > 0 {
			result.SkippedProjects = e.skippedProjects
		}
		if e.unboundedCapHit > 0 {
			result.UnboundedDepthCap = e.unboundedCapHit
		}
	}
	return result, nil
}

// maxUnboundedPathDepth resolves the configured cap, falling back to the
// package default when unset.
func (e *Executor) maxUnboundedPathDepth() int {
	if e.MaxUnboundedPathDepth > 0 {
		return e.MaxUnboundedPathDepth
	}
	return defaultUnboundedPathDepth
}

func (e *Executor) executePlan(plan *Plan) (*Result, error) {
	// Fast path: push COUNT aggregation to SQL when pattern is fusible.
	if result, ok := e.tryAggregateSQL(plan); ok {
		return result, nil
	}

	projects, err := e.Store.ListProjects()
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}

	bindingCap := e.bindingCap(plan.ReturnSpec)
	e.expandLimit = bindingCap
	var allBindings []binding
	for _, proj := range projects {
		bindings, err := e.executeStepsForProject(proj.Name, plan.Steps)
		if err != nil {
			// Per-project failure (corrupt DB, transient query error, etc.)
			// is recorded on the Result so callers see when a project was
			// excluded — previously this was a silent `continue` and a
			// single bad project just shrank the result set with no signal.
			// We still surface results from healthy projects so the query
			// isn't entirely lost on one corrupt DB.
			slog.Warn("cypher.project_skipped", "project", proj.Name, "err", err)
			e.skippedProjects = append(e.skippedProjects, SkippedProject{Name: proj.Name, Err: err.Error()})
			e.markTruncated()
			continue
		}
		allBindings = append(allBindings, bindings...)
		if len(allBindings) > bindingCap {
			allBindings = allBindings[:bindingCap]
			e.markTruncated()
			break
		}
	}

	return e.projectResults(allBindings, plan.ReturnSpec)
}

// tryAggregateSQL attempts to push a COUNT aggregation query entirely to SQL.
// Returns (result, true) if the pattern is fusible, (nil, false) otherwise.
// Fusible pattern: ScanNodes + ExpandRelationship (fixed-length 1 hop) with
// RETURN containing COUNT and group-by items that map to SQL columns.
func (e *Executor) tryAggregateSQL(plan *Plan) (*Result, bool) {
	if plan.ReturnSpec == nil {
		return nil, false
	}

	countItem, groupItems := classifyCountItems(plan.ReturnSpec.Items)
	if countItem == nil {
		return nil, false
	}

	// Phase B1 (Plan 8-Phase Arc, 2026-05-09): COUNT(DISTINCT var) cannot
	// be pushed to the current SQL aggregation path because the join
	// produces one row per (caller, target) edge — naive COUNT() over the
	// joined table over-counts when the same target appears via multiple
	// edges. The Go path tracks distinct values via a set per group, which
	// is correct. Falling back to the Go path is a small perf cost on
	// queries that would otherwise SQL-aggregate but currently don't
	// dominate any common query.
	if countItem.Distinct {
		return nil, false
	}

	scan, expand, filter, ok := parseFusibleSteps(plan.Steps)
	if !ok {
		return nil, false
	}

	if !validatePushability(scan, expand, filter, groupItems) {
		return nil, false
	}

	projects, err := e.Store.ListProjects()
	if err != nil {
		return nil, false
	}

	sqlCtx := newAggregateSQLContext(scan, expand, groupItems)
	cols := buildColumnNames(plan.ReturnSpec.Items)
	countCol := cols[len(cols)-1] // COUNT column is last

	allRows := make([]map[string]any, 0)
	for _, proj := range projects {
		projRows, projErr := e.executeAggregateForProject(proj.Name, &sqlCtx, scan, expand, filter, groupItems, countCol)
		if projErr != nil {
			slog.Warn("cypher.project_skipped", "project", proj.Name, "path", "aggregate", "err", projErr)
			e.skippedProjects = append(e.skippedProjects, SkippedProject{Name: proj.Name, Err: projErr.Error()})
			e.markTruncated()
			continue
		}
		allRows = append(allRows, projRows...)
	}

	// ORDER BY
	if plan.ReturnSpec.OrderBy != "" {
		orderCol := resolveOrderColumn(plan.ReturnSpec.OrderBy, plan.ReturnSpec.Items, cols)
		sortRows(allRows, orderCol, plan.ReturnSpec.OrderDir)
	}

	// LIMIT
	var trimmed bool
	allRows, trimmed = applyLimit(allRows, plan.ReturnSpec.Limit, e.maxRows())
	if trimmed {
		e.markTruncated()
	}

	return &Result{Columns: cols, Rows: allRows}, true
}

// classifyCountItems separates return items into the COUNT item and group-by items.
func classifyCountItems(items []ReturnItem) (countItem *ReturnItem, groupItems []ReturnItem) {
	for i := range items {
		item := &items[i]
		if item.Func == "COUNT" {
			countItem = item
		} else {
			groupItems = append(groupItems, *item)
		}
	}
	return
}

// parseFusibleSteps identifies the ScanNodes + optional FilterWhere + ExpandRelationship
// pattern from plan steps. Returns (nil, nil, nil, false) if the pattern is not fusible.
func parseFusibleSteps(steps []PlanStep) (*ScanNodes, *ExpandRelationship, *FilterWhere, bool) {
	switch len(steps) {
	case 2:
		s, ok1 := steps[0].(*ScanNodes)
		ex, ok2 := steps[1].(*ExpandRelationship)
		if !ok1 || !ok2 {
			return nil, nil, nil, false
		}
		return s, ex, nil, true
	case 3:
		s, ok1 := steps[0].(*ScanNodes)
		f, ok2 := steps[1].(*FilterWhere)
		ex, ok3 := steps[2].(*ExpandRelationship)
		if !ok1 || !ok3 {
			return nil, nil, nil, false
		}
		var filter *FilterWhere
		if ok2 {
			filter = f
		}
		return s, ex, filter, true
	default:
		return nil, nil, nil, false
	}
}

// validatePushability checks if the scan/expand/filter/group combination
// can be pushed entirely to SQL.
func validatePushability(scan *ScanNodes, expand *ExpandRelationship, filter *FilterWhere, groupItems []ReturnItem) bool {
	if expand.MinHops != 1 || expand.MaxHops != 1 {
		return false
	}
	if len(expand.EdgeTypes) == 0 {
		return false
	}
	if len(scan.Props) > 0 || len(expand.ToProps) > 0 {
		return false
	}

	for _, gi := range groupItems {
		if gi.Property == "" {
			return false
		}
		if _, ok := sqlPushableColumns[gi.Property]; !ok {
			return false
		}
	}

	if filter != nil {
		for _, c := range filter.Conditions {
			if _, ok := sqlPushableColumns[c.Property]; !ok {
				return false
			}
			switch c.Operator {
			case "=", "CONTAINS", "STARTS WITH", "ENDS WITH":
				// OK
			default:
				return false
			}
		}
	}
	return true
}

// aggregateSQLContext holds pre-computed SQL fragments for aggregate queries.
type aggregateSQLContext struct {
	srcCol, tgtCol     string
	selectParts        []string
	groupByParts       []string
	srcAlias, tgtAlias string
}

// newAggregateSQLContext pre-computes direction-dependent columns and
// SELECT/GROUP BY parts for the aggregate SQL query.
func newAggregateSQLContext(scan *ScanNodes, expand *ExpandRelationship, groupItems []ReturnItem) aggregateSQLContext {
	dir := expand.Direction
	if dir == "" {
		dir = "outbound"
	}
	var srcCol, tgtCol string
	if dir == "outbound" {
		srcCol, tgtCol = "source_id", "target_id"
	} else {
		srcCol, tgtCol = "target_id", "source_id"
	}

	srcAlias, tgtAlias := "src", "tgt"
	selectParts := make([]string, 0, len(groupItems)+1)
	groupByParts := make([]string, 0, len(groupItems))
	for _, gi := range groupItems {
		col := sqlPushableColumns[gi.Property]
		alias := tgtAlias
		if gi.Variable == scan.Variable {
			alias = srcAlias
		}
		selectParts = append(selectParts, alias+"."+col)
		groupByParts = append(groupByParts, alias+"."+col)
	}
	selectParts = append(selectParts, "COUNT(*) as cnt")

	return aggregateSQLContext{
		srcCol: srcCol, tgtCol: tgtCol,
		selectParts: selectParts, groupByParts: groupByParts,
		srcAlias: srcAlias, tgtAlias: tgtAlias,
	}
}

// executeAggregateForProject runs the aggregate SQL query for a single project.
func (e *Executor) executeAggregateForProject(
	project string, ctx *aggregateSQLContext,
	scan *ScanNodes, expand *ExpandRelationship, filter *FilterWhere,
	groupItems []ReturnItem, countCol string,
) ([]map[string]any, error) {
	query, args := buildProjectAggregateSQL(project, ctx, scan, expand, filter)

	rows, qErr := e.Store.DB().QueryContext(e.ctx, query, args...)
	if qErr != nil {
		return nil, fmt.Errorf("aggregate query: %w", qErr)
	}
	defer rows.Close()

	var result []map[string]any
	for rows.Next() {
		vals := make([]any, len(groupItems)+1)
		ptrs := make([]any, len(groupItems)+1)
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if sErr := rows.Scan(ptrs...); sErr != nil {
			return nil, fmt.Errorf("aggregate scan: %w", sErr)
		}
		row := make(map[string]any)
		for i, gi := range groupItems {
			col := gi.Variable + "." + gi.Property
			if gi.Alias != "" {
				col = gi.Alias
			}
			if b, ok := vals[i].([]byte); ok {
				row[col] = string(b)
			} else {
				row[col] = vals[i]
			}
		}
		switch v := vals[len(vals)-1].(type) {
		case int64:
			row[countCol] = int(v)
		default:
			row[countCol] = v
		}
		result = append(result, row)
	}
	if rErr := rows.Err(); rErr != nil {
		return nil, fmt.Errorf("aggregate rows: %w", rErr)
	}
	return result, nil
}

// buildProjectAggregateSQL constructs the SQL query and args for one project.
func buildProjectAggregateSQL(
	project string, ctx *aggregateSQLContext,
	scan *ScanNodes, expand *ExpandRelationship, filter *FilterWhere,
) (query string, args []any) {
	var sb strings.Builder
	args = make([]any, 0, 10)

	sb.WriteString("SELECT ")
	sb.WriteString(strings.Join(ctx.selectParts, ", "))
	sb.WriteString(" FROM edges e")
	fmt.Fprintf(&sb, " JOIN nodes %s ON %s.id = e.%s", ctx.srcAlias, ctx.srcAlias, ctx.srcCol)
	fmt.Fprintf(&sb, " JOIN nodes %s ON %s.id = e.%s", ctx.tgtAlias, ctx.tgtAlias, ctx.tgtCol)
	sb.WriteString(" WHERE e.project = ?")
	args = append(args, project)

	// Edge type filter
	typePlaceholders := make([]string, len(expand.EdgeTypes))
	for i, et := range expand.EdgeTypes {
		typePlaceholders[i] = "?"
		args = append(args, et)
	}
	sb.WriteString(" AND e.type IN (" + strings.Join(typePlaceholders, ",") + ")")

	// Label filters
	if scan.Label != "" {
		fmt.Fprintf(&sb, " AND %s.label = ?", ctx.srcAlias)
		args = append(args, scan.Label)
	}
	if expand.ToLabel != "" {
		fmt.Fprintf(&sb, " AND %s.label = ?", ctx.tgtAlias)
		args = append(args, expand.ToLabel)
	}

	// Push-down WHERE conditions
	if filter != nil {
		appendFilterConditions(&sb, &args, filter, scan.Variable, ctx.srcAlias, ctx.tgtAlias)
	}

	sb.WriteString(" GROUP BY " + strings.Join(ctx.groupByParts, ", "))
	query = sb.String()
	return
}

// appendFilterConditions appends WHERE conditions to the SQL query builder.
func appendFilterConditions(sb *strings.Builder, args *[]any, filter *FilterWhere, scanVar, srcAlias, tgtAlias string) {
	for _, c := range filter.Conditions {
		col := sqlPushableColumns[c.Property]
		alias := tgtAlias
		if c.Variable == scanVar {
			alias = srcAlias
		}
		switch c.Operator {
		case "=":
			fmt.Fprintf(sb, " AND %s.%s = ?", alias, col)
			*args = append(*args, c.Value)
		case "CONTAINS":
			fmt.Fprintf(sb, " AND %s.%s LIKE ?", alias, col)
			*args = append(*args, "%"+c.Value+"%")
		case "STARTS WITH":
			fmt.Fprintf(sb, " AND %s.%s LIKE ?", alias, col)
			*args = append(*args, c.Value+"%")
		case "ENDS WITH":
			fmt.Fprintf(sb, " AND %s.%s LIKE ?", alias, col)
			*args = append(*args, "%"+c.Value)
		}
	}
}

//nolint:gocognit // step dispatch with fusion/push-down is inherently branchy
func (e *Executor) executeStepsForProject(project string, steps []PlanStep) ([]binding, error) {
	var bindings []binding

	for i, step := range steps {
		var err error
		switch s := step.(type) {
		case *ScanNodes:
			// Fast path: ScanNodes followed by ExpandRelationship can be fused
			// into a single SQL JOIN, avoiding N+1 queries.
			if i+1 < len(steps) {
				if expand, ok := steps[i+1].(*ExpandRelationship); ok && canFuseJoin(s, expand) {
					bindings, err = e.execJoinScanExpand(project, s, expand)
					if err != nil {
						return nil, err
					}
					// Mark next step as consumed by incrementing i via a skip flag
					// We'll handle this below.
					steps[i+1] = &fusedExpandMarker{}
					break
				}
			}

			// Check if the next step is a FilterWhere that can be pushed down
			var pushDown *FilterWhere
			if i+1 < len(steps) {
				if fw, ok := steps[i+1].(*FilterWhere); ok {
					pushDown = fw
				}
			}
			bindings, err = e.execScan(project, s, pushDown)
		case *ExpandRelationship:
			bindings, err = e.execExpand(s, bindings)
		case *fusedExpandMarker:
			continue // already handled by JOIN fusion
		case *FilterWhere:
			// Skip if this was already consumed by push-down
			if i > 0 {
				if _, wasScan := steps[i-1].(*ScanNodes); wasScan {
					continue // already handled in execScan
				}
			}
			bindings, err = e.execFilter(s, bindings)
		default:
			return nil, fmt.Errorf("unknown step type: %T", step)
		}
		if err != nil {
			return nil, err
		}
		isLastStep := i == len(steps)-1
		_, isExpand := step.(*ExpandRelationship)
		if isLastStep || isExpand {
			stepCap := e.expandLimit
			if stepCap <= 0 {
				stepCap = e.maxRows() * 2
			}
			if len(bindings) > stepCap {
				bindings = bindings[:stepCap]
				e.markTruncated()
			}
		}
	}

	return bindings, nil
}

// fusedExpandMarker is a placeholder step marking an ExpandRelationship
// that was already handled by JOIN fusion with the preceding ScanNodes.
type fusedExpandMarker struct{}

func (*fusedExpandMarker) stepType() string { return "fused" }

// canFuseJoin returns true if a ScanNodes + ExpandRelationship pair can be
// replaced by a single SQL JOIN. Requirements: fixed-length (1 hop), known
// edge types, standard direction, no inline property filters on source.
func canFuseJoin(scan *ScanNodes, expand *ExpandRelationship) bool {
	if expand.MinHops != 1 || expand.MaxHops != 1 {
		return false // variable-length paths need BFS
	}
	if len(expand.EdgeTypes) == 0 {
		return false // untyped edges: keep generic path
	}
	if len(scan.Props) > 0 {
		return false // inline property filters on source not supported in JOIN
	}
	if expand.Direction != "outbound" && expand.Direction != "inbound" && expand.Direction != "" {
		return false
	}
	return true
}

// execJoinScanExpand executes a fused ScanNodes→ExpandRelationship step using
// a single SQL JOIN, avoiding N+1 queries.
func (e *Executor) execJoinScanExpand(project string, scan *ScanNodes, expand *ExpandRelationship) ([]binding, error) {
	// Build type filter: type IN (?, ?, ...)
	typePlaceholders := make([]string, len(expand.EdgeTypes))
	args := make([]any, 0, len(expand.EdgeTypes)+2)
	args = append(args, project)
	for i, et := range expand.EdgeTypes {
		typePlaceholders[i] = "?"
		args = append(args, et)
	}
	typeFilter := strings.Join(typePlaceholders, ",")

	// Determine join direction
	var srcCol, tgtCol string
	dir := expand.Direction
	if dir == "" {
		dir = "outbound"
	}
	if dir == "outbound" {
		srcCol, tgtCol = "source_id", "target_id"
	} else {
		srcCol, tgtCol = "target_id", "source_id"
	}

	// Build label filters
	var srcLabelClause, tgtLabelClause string
	if scan.Label != "" {
		srcLabelClause = " AND src.label = ?"
		args = append(args, scan.Label)
	}
	if expand.ToLabel != "" {
		tgtLabelClause = " AND tgt.label = ?"
		args = append(args, expand.ToLabel)
	}

	query := fmt.Sprintf(`
		SELECT
			src.id, src.project, src.label, src.name, src.qualified_name, src.file_path, src.start_line, src.end_line, src.properties,
			tgt.id, tgt.project, tgt.label, tgt.name, tgt.qualified_name, tgt.file_path, tgt.start_line, tgt.end_line, tgt.properties,
			e.id, e.project, e.source_id, e.target_id, e.type, e.properties
		FROM edges e
		JOIN nodes src ON src.id = e.%s
		JOIN nodes tgt ON tgt.id = e.%s
		WHERE e.project = ? AND e.type IN (%s)%s%s
		LIMIT ?`,
		srcCol, tgtCol, typeFilter, srcLabelClause, tgtLabelClause)

	sqlLimit := e.expandLimit
	if sqlLimit <= 0 {
		sqlLimit = e.maxRows() * 2
	}
	// Fetch sqlLimit+1 so the cap can detect whether more rows would have
	// matched. Keep only sqlLimit of them; mark truncated if the +1 came back.
	args = append(args, sqlLimit+1)

	rows, err := e.Store.DB().QueryContext(e.ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("join scan+expand: %w", err)
	}
	defer rows.Close()

	var bindings []binding
	for rows.Next() {
		var srcN, tgtN store.Node
		var srcProps, tgtProps string
		var edge store.Edge
		var edgeProps string

		if err := rows.Scan(
			&srcN.ID, &srcN.Project, &srcN.Label, &srcN.Name, &srcN.QualifiedName, &srcN.FilePath, &srcN.StartLine, &srcN.EndLine, &srcProps,
			&tgtN.ID, &tgtN.Project, &tgtN.Label, &tgtN.Name, &tgtN.QualifiedName, &tgtN.FilePath, &tgtN.StartLine, &tgtN.EndLine, &tgtProps,
			&edge.ID, &edge.Project, &edge.SourceID, &edge.TargetID, &edge.Type, &edgeProps,
		); err != nil {
			return nil, err
		}
		srcN.Properties = store.UnmarshalProps(srcProps)
		tgtN.Properties = store.UnmarshalProps(tgtProps)
		edge.Properties = store.UnmarshalProps(edgeProps)

		// Apply target inline property filters
		if len(expand.ToProps) > 0 && !nodeMatchesProps(&tgtN, expand.ToProps) {
			continue
		}

		b := newBinding()
		if scan.Variable != "" {
			b.nodes[scan.Variable] = &srcN
		}
		if expand.ToVar != "" {
			b.nodes[expand.ToVar] = &tgtN
		}
		if expand.RelVar != "" {
			b.edges[expand.RelVar] = &edge
		}
		bindings = append(bindings, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Fetched sqlLimit+1; if the extra row came back, cap at sqlLimit and signal
	// that SQL-level truncation happened (underlying set had more matching rows).
	if len(bindings) > sqlLimit {
		bindings = bindings[:sqlLimit]
		e.markTruncated()
	}

	return bindings, nil
}

// sqlPushableColumns are node properties that map directly to SQL columns.
var sqlPushableColumns = map[string]string{
	"name":           "name",
	"qualified_name": "qualified_name",
	"label":          "label",
	"file_path":      "file_path",
}

func (e *Executor) execScan(project string, s *ScanNodes, pushDown *FilterWhere) ([]binding, error) {
	// Build dynamic SQL query with optional push-down conditions
	query := `SELECT id, project, label, name, qualified_name, file_path, start_line, end_line, properties FROM nodes WHERE project=?`
	args := []any{project}

	if s.Label != "" {
		query += " AND label=?"
		args = append(args, s.Label)
	}

	// Push down WHERE conditions into SQL where possible
	var unpushedConditions []Condition
	if pushDown != nil {
		for _, c := range pushDown.Conditions {
			col, canPush := sqlPushableColumns[c.Property]
			if !canPush {
				unpushedConditions = append(unpushedConditions, c)
				continue
			}
			switch c.Operator {
			case "=":
				query += " AND " + col + "=?"
				args = append(args, c.Value)
			case "CONTAINS":
				query += " AND " + col + " LIKE ?"
				args = append(args, "%"+c.Value+"%")
			case "STARTS WITH":
				query += " AND " + col + " LIKE ?"
				args = append(args, c.Value+"%")
			case "ENDS WITH":
				query += " AND " + col + " LIKE ?"
				args = append(args, "%"+c.Value)
			default:
				// =~, numeric comparisons, IS NULL: can't push to SQL easily
				unpushedConditions = append(unpushedConditions, c)
			}
		}
	}

	rows, err := e.Store.DB().QueryContext(e.ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("scan nodes: %w", err)
	}
	defer rows.Close()

	var nodes []*store.Node
	for rows.Next() {
		n, scanErr := scanNodeFromRows(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		nodes = append(nodes, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Apply inline property filters
	if len(s.Props) > 0 {
		nodes = filterNodesByProps(nodes, s.Props)
	}

	bindings := make([]binding, 0, len(nodes))
	for _, n := range nodes {
		// Apply unpushed conditions in Go
		if len(unpushedConditions) > 0 {
			b := newBinding()
			b.nodes[s.Variable] = n
			match, evalErr := e.evaluateConditions(b, unpushedConditions, "AND")
			if evalErr != nil {
				return nil, evalErr
			}
			if !match {
				continue
			}
		}
		b := newBinding()
		if s.Variable != "" {
			b.nodes[s.Variable] = n
		}
		bindings = append(bindings, b)
	}
	return bindings, nil
}

// scanNodeFromRows scans a single node from sql.Rows.
func scanNodeFromRows(rows *sql.Rows) (*store.Node, error) {
	var n store.Node
	var props string
	if err := rows.Scan(&n.ID, &n.Project, &n.Label, &n.Name, &n.QualifiedName, &n.FilePath, &n.StartLine, &n.EndLine, &props); err != nil {
		return nil, err
	}
	n.Properties = store.UnmarshalProps(props)
	return &n, nil
}

func (e *Executor) execExpand(s *ExpandRelationship, bindings []binding) ([]binding, error) {
	if len(bindings) == 0 {
		return nil, nil
	}

	isVariableLength := s.MinHops != 1 || s.MaxHops != 1

	if isVariableLength {
		// Variable-length: use BFS per binding (already uses recursive CTE)
		result := make([]binding, 0, len(bindings))
		for _, b := range bindings {
			fromNode, ok := b.nodes[s.FromVar]
			if !ok {
				continue
			}
			expanded, err := e.expandVariableLength(b, fromNode, s)
			if err != nil {
				return nil, err
			}
			result = append(result, expanded...)
			if len(result) > e.maxRows()*2 {
				result = result[:e.maxRows()*2]
				e.markTruncated()
				break
			}
		}
		return result, nil
	}

	// Fixed-length (1 hop): batch all source IDs, 2 queries total
	return e.expandFixedLengthBatch(s, bindings)
}

// expandFixedLengthBatch performs fixed-length (1 hop) expansion for all bindings
// in just 2 SQL queries: one for edges, one for target nodes.
func (e *Executor) expandFixedLengthBatch(s *ExpandRelationship, bindings []binding) ([]binding, error) {
	sourceIDs := collectSourceIDs(s.FromVar, bindings)
	if len(sourceIDs) == 0 {
		return nil, nil
	}

	direction := s.Direction
	if direction == "" {
		direction = "outbound"
	}

	edgesByNode, err := e.fetchEdgesForDirection(sourceIDs, s.EdgeTypes, direction)
	if err != nil {
		return nil, err
	}

	nodeMap, err := e.Store.FindNodesByIDs(collectTargetIDs(edgesByNode, direction))
	if err != nil {
		return nil, err
	}

	expandCap := e.expandLimit
	if expandCap <= 0 {
		expandCap = e.maxRows() * 2
	}
	expanded, truncated := buildExpandedBindings(bindings, s, edgesByNode, nodeMap, direction, expandCap)
	if truncated {
		e.markTruncated()
	}
	return expanded, nil
}

// collectSourceIDs extracts unique node IDs from bindings for the given variable.
func collectSourceIDs(fromVar string, bindings []binding) []int64 {
	sourceIDs := make([]int64, 0, len(bindings))
	idSeen := make(map[int64]bool, len(bindings))
	for _, b := range bindings {
		fromNode, ok := b.nodes[fromVar]
		if !ok {
			continue
		}
		if !idSeen[fromNode.ID] {
			idSeen[fromNode.ID] = true
			sourceIDs = append(sourceIDs, fromNode.ID)
		}
	}
	return sourceIDs
}

// fetchEdgesForDirection fetches edges for the given source IDs and direction.
func (e *Executor) fetchEdgesForDirection(sourceIDs []int64, edgeTypes []string, direction string) (map[int64][]*store.Edge, error) {
	switch direction {
	case "inbound":
		return e.Store.FindEdgesByTargetIDs(sourceIDs, edgeTypes)
	case "any":
		outEdges, err := e.Store.FindEdgesBySourceIDs(sourceIDs, edgeTypes)
		if err != nil {
			return nil, err
		}
		inEdges, err := e.Store.FindEdgesByTargetIDs(sourceIDs, edgeTypes)
		if err != nil {
			return nil, err
		}
		for id, edges := range inEdges {
			outEdges[id] = append(outEdges[id], edges...)
		}
		return outEdges, nil
	default: // "outbound" or unspecified
		return e.Store.FindEdgesBySourceIDs(sourceIDs, edgeTypes)
	}
}

// collectTargetIDs extracts all target node IDs from edge batches.
func collectTargetIDs(edgesByNode map[int64][]*store.Edge, direction string) []int64 {
	targetIDSet := make(map[int64]bool)
	for _, edges := range edgesByNode {
		for _, edge := range edges {
			if direction == "any" {
				targetIDSet[edge.SourceID] = true
				targetIDSet[edge.TargetID] = true
			} else {
				targetIDSet[edgeTargetID(edge, 0, direction)] = true
			}
		}
	}
	targetIDs := make([]int64, 0, len(targetIDSet))
	for id := range targetIDSet {
		targetIDs = append(targetIDs, id)
	}
	return targetIDs
}

// buildExpandedBindings creates result bindings by matching edges to target nodes.
// Returns (bindings, truncated) — truncated is true iff the maxRows*2 clip fired.
func buildExpandedBindings(bindings []binding, s *ExpandRelationship, edgesByNode map[int64][]*store.Edge, nodeMap map[int64]*store.Node, direction string, maxRows int) ([]binding, bool) {
	result := make([]binding, 0, len(bindings))
	truncated := false
	for _, b := range bindings {
		fromNode, ok := b.nodes[s.FromVar]
		if !ok {
			continue
		}
		edges := edgesByNode[fromNode.ID]
		seen := make(map[int64]bool)

		for _, edge := range edges {
			targetID := edgeTargetID(edge, fromNode.ID, direction)
			if seen[targetID] {
				continue
			}
			seen[targetID] = true

			node, exists := nodeMap[targetID]
			if !exists || (s.ToLabel != "" && node.Label != s.ToLabel) {
				continue
			}
			if len(s.ToProps) > 0 && !nodeMatchesProps(node, s.ToProps) {
				continue
			}
			newB := copyBinding(b)
			if s.ToVar != "" {
				newB.nodes[s.ToVar] = node
			}
			if s.RelVar != "" {
				newB.edges[s.RelVar] = edge
			}
			result = append(result, newB)
		}

		if len(result) > maxRows*2 {
			result = result[:maxRows*2]
			truncated = true
			break
		}
	}
	return result, truncated
}

func (e *Executor) expandVariableLength(b binding, fromNode *store.Node, s *ExpandRelationship) ([]binding, error) {
	maxDepth := s.MaxHops
	unbounded := maxDepth == 0
	if unbounded {
		maxDepth = e.maxUnboundedPathDepth()
		// Record that an unbounded `*` ran at our cap so the Result can
		// surface UnboundedDepthCap to the caller. Keep the maximum cap
		// across multiple expansions in the same query.
		if maxDepth > e.unboundedCapHit {
			e.unboundedCapHit = maxDepth
		}
		e.markTruncated()
	}

	direction := s.Direction
	if direction == "" {
		direction = "outbound"
	}

	edgeTypes := s.EdgeTypes
	if len(edgeTypes) == 0 {
		edgeTypes = []string{"CALLS"} // default
	}

	bfsResult, err := e.Store.BFS(fromNode.ID, direction, edgeTypes, maxDepth, e.maxRows())
	if err != nil {
		return nil, fmt.Errorf("bfs: %w", err)
	}

	result := make([]binding, 0, len(bfsResult.Visited))
	for _, nh := range bfsResult.Visited {
		if nh.Hop < s.MinHops {
			continue
		}
		if s.MaxHops > 0 && nh.Hop > s.MaxHops {
			continue
		}
		if s.ToLabel != "" && nh.Node.Label != s.ToLabel {
			continue
		}
		if len(s.ToProps) > 0 && !nodeMatchesProps(nh.Node, s.ToProps) {
			continue
		}
		newB := copyBinding(b)
		if s.ToVar != "" {
			newB.nodes[s.ToVar] = nh.Node
		}
		// Note: variable-length BFS doesn't bind individual edges
		result = append(result, newB)
	}
	return result, nil
}

// edgeTargetID returns the ID of the node on the "other end" of the edge relative to nodeID.
func edgeTargetID(edge *store.Edge, nodeID int64, direction string) int64 {
	switch direction {
	case "inbound":
		return edge.SourceID
	case "any":
		if edge.SourceID == nodeID {
			return edge.TargetID
		}
		return edge.SourceID
	default:
		return edge.TargetID
	}
}

func (e *Executor) execFilter(s *FilterWhere, bindings []binding) ([]binding, error) {
	var result []binding
	for _, b := range bindings {
		match, err := e.evaluateConditions(b, s.Conditions, s.Operator)
		if err != nil {
			return nil, err
		}
		if match {
			result = append(result, b)
		}
	}
	return result, nil
}

func (e *Executor) evaluateConditions(b binding, conditions []Condition, op string) (bool, error) {
	if op == "OR" {
		for _, c := range conditions {
			ok, err := e.evaluateCondition(b, c)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		return false, nil
	}
	// AND (default)
	for _, c := range conditions {
		ok, err := e.evaluateCondition(b, c)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// compiledRegex returns a compiled regex for the pattern, using a cache
// to avoid recompiling the same pattern on every binding.
func (e *Executor) compiledRegex(pattern string) (*regexp.Regexp, error) {
	if e.regexCache == nil {
		e.regexCache = make(map[string]*regexp.Regexp)
	}
	if re, ok := e.regexCache[pattern]; ok {
		return re, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	e.regexCache[pattern] = re
	return re, nil
}

func (e *Executor) evaluateCondition(b binding, c Condition) (bool, error) {
	// Try node first, then edge
	var actual any
	if node, ok := b.nodes[c.Variable]; ok {
		actual = getNodeProperty(node, c.Property)
	} else if edge, ok := b.edges[c.Variable]; ok {
		actual = getEdgeProperty(edge, c.Property)
	} else {
		return false, nil
	}

	switch c.Operator {
	case "=":
		return fmt.Sprintf("%v", actual) == c.Value, nil
	case "=~":
		s, ok := actual.(string)
		if !ok {
			return false, nil
		}
		re, err := e.compiledRegex(c.Value)
		if err != nil {
			return false, fmt.Errorf("regex %q: %w", c.Value, err)
		}
		return re.MatchString(s), nil
	case "CONTAINS":
		// String operand: substring match (standard Cypher semantics).
		if s, ok := actual.(string); ok {
			return strings.Contains(s, c.Value), nil
		}
		// Array operand: element-of match. The Cypher standard uses
		// `X IN array_prop` for this, but this engine's parser doesn't
		// accept a value on the LHS of IN (only `n.prop IN [list]` form),
		// so CONTAINS on an array property is the affordance for
		// "does this list include this element". Pre-fix behavior was
		// a silent no-match, which made array-typed properties like
		// `decorator_tags` un-queryable for tag presence. See test
		// TestEvalCondition_ContainsOnArrayProperty.
		switch arr := actual.(type) {
		case []string:
			for _, str := range arr {
				if str == c.Value {
					return true, nil
				}
			}
		case []any:
			for _, item := range arr {
				if str, ok := item.(string); ok && str == c.Value {
					return true, nil
				}
			}
		}
		return false, nil
	case "STARTS WITH":
		s, ok := actual.(string)
		if !ok {
			return false, nil
		}
		return strings.HasPrefix(s, c.Value), nil
	case "ENDS WITH":
		s, ok := actual.(string)
		if !ok {
			return false, nil
		}
		return strings.HasSuffix(s, c.Value), nil
	case "IS NULL":
		// A property is "NULL" if it doesn't exist on the node/edge
		// (getNodeProperty / getEdgeProperty return nil) or if it is
		// the empty string (the on-disk representation of an absent
		// optional string property).
		if actual == nil {
			return true, nil
		}
		if s, ok := actual.(string); ok && s == "" {
			return true, nil
		}
		return false, nil
	case "IS NOT NULL":
		if actual == nil {
			return false, nil
		}
		if s, ok := actual.(string); ok && s == "" {
			return false, nil
		}
		return true, nil
	case "IN":
		// Membership test against the parsed list literal. Values are
		// always strings at parse time (TokString and TokNumber both
		// come through as the raw text); compare via fmt.Sprintf so
		// numeric properties (e.g. start_line) match against numeric
		// list entries with the same string formatting used by '='.
		actualStr := fmt.Sprintf("%v", actual)
		for _, v := range c.Values {
			if actualStr == v {
				return true, nil
			}
		}
		return false, nil
	case ">", "<", ">=", "<=":
		return compareNumeric(actual, c.Value, c.Operator)
	default:
		return false, fmt.Errorf("unsupported operator: %s", c.Operator)
	}
}

func compareNumeric(actual any, expected, op string) (bool, error) {
	expectedNum, err := strconv.ParseFloat(expected, 64)
	if err != nil {
		return false, fmt.Errorf("invalid numeric value %q: %w", expected, err)
	}
	var actualNum float64
	switch v := actual.(type) {
	case int:
		actualNum = float64(v)
	case int64:
		actualNum = float64(v)
	case float64:
		actualNum = v
	case string:
		n, parseErr := strconv.ParseFloat(v, 64)
		if parseErr != nil {
			return false, fmt.Errorf("cannot compare %q as number: %w", v, parseErr)
		}
		actualNum = n
	default:
		return false, nil
	}

	switch op {
	case ">":
		return actualNum > expectedNum, nil
	case "<":
		return actualNum < expectedNum, nil
	case ">=":
		return actualNum >= expectedNum, nil
	case "<=":
		return actualNum <= expectedNum, nil
	default:
		return false, nil
	}
}

func getNodeProperty(n *store.Node, prop string) any {
	switch prop {
	case "name":
		return n.Name
	case "qualified_name":
		return n.QualifiedName
	case "label":
		return n.Label
	case "file_path":
		return n.FilePath
	case "start_line":
		return n.StartLine
	case "end_line":
		return n.EndLine
	case "id":
		return n.ID
	case "project":
		return n.Project
	default:
		if n.Properties != nil {
			if v, ok := n.Properties[prop]; ok {
				return v
			}
		}
		return nil
	}
}

// getEdgeProperty returns a property value from an edge.
func getEdgeProperty(edge *store.Edge, prop string) any {
	switch prop {
	case "type":
		return edge.Type
	case "id":
		return edge.ID
	case "source_id":
		return edge.SourceID
	case "target_id":
		return edge.TargetID
	default:
		if edge.Properties != nil {
			if v, ok := edge.Properties[prop]; ok {
				return v
			}
		}
		return nil
	}
}

func (e *Executor) projectResults(bindings []binding, ret *ReturnClause) (*Result, error) {
	if ret == nil {
		return e.defaultProjection(bindings)
	}

	// Check if we have a COUNT aggregation
	hasCount := false
	for _, item := range ret.Items {
		if item.Func == "COUNT" {
			hasCount = true
			break
		}
	}

	if hasCount {
		return e.aggregateResults(bindings, ret)
	}

	return e.simpleProjection(bindings, ret)
}

func (e *Executor) defaultProjection(bindings []binding) (*Result, error) {
	if len(bindings) == 0 {
		return &Result{Columns: []string{}, Rows: []map[string]any{}}, nil
	}

	// Collect all variable names from nodes and edges
	varSet := make(map[string]bool)
	edgeVarSet := make(map[string]bool)
	for _, b := range bindings {
		for k := range b.nodes {
			varSet[k] = true
		}
		for k := range b.edges {
			edgeVarSet[k] = true
		}
	}
	cols := make([]string, 0, len(varSet)*3+len(edgeVarSet))
	for k := range varSet {
		cols = append(cols, k+".name", k+".qualified_name", k+".label")
	}
	for k := range edgeVarSet {
		cols = append(cols, k+".type")
	}
	sort.Strings(cols)

	rows := make([]map[string]any, 0, len(bindings))
	for _, b := range bindings {
		row := make(map[string]any)
		for varName, node := range b.nodes {
			row[varName+".name"] = node.Name
			row[varName+".qualified_name"] = node.QualifiedName
			row[varName+".label"] = node.Label
		}
		for varName, edge := range b.edges {
			row[varName+".type"] = edge.Type
		}
		rows = append(rows, row)
	}

	if len(rows) > e.maxRows() {
		rows = rows[:e.maxRows()]
	}

	return &Result{Columns: cols, Rows: rows}, nil
}

func (e *Executor) simpleProjection(bindings []binding, ret *ReturnClause) (*Result, error) {
	cols := buildColumnNames(ret.Items)

	seen := make(map[string]bool)
	rows := make([]map[string]any, 0, len(bindings))
	for _, b := range bindings {
		row := buildProjectionRow(b, ret.Items, cols)

		// DISTINCT check
		if ret.Distinct {
			key := fmt.Sprintf("%v", row)
			if seen[key] {
				continue
			}
			seen[key] = true
		}

		rows = append(rows, row)
	}

	// ORDER BY
	if ret.OrderBy != "" {
		orderCol := resolveOrderColumn(ret.OrderBy, ret.Items, cols)
		sortRows(rows, orderCol, ret.OrderDir)
	}

	// LIMIT
	var trimmed bool
	rows, trimmed = applyLimit(rows, ret.Limit, e.maxRows())
	if trimmed {
		e.markTruncated()
	}

	return &Result{Columns: cols, Rows: rows}, nil
}

// buildColumnNames builds column names from return items.
func buildColumnNames(items []ReturnItem) []string {
	cols := make([]string, 0, len(items))
	for _, item := range items {
		col := item.Variable
		if item.Property != "" {
			col = item.Variable + "." + item.Property
		}
		// Phase B1/B2 (Plan 8-Phase Arc, 2026-05-09): function-call return
		// items render as `FUNC(args)` for callers that don't supply an alias
		// (consistent with previous COUNT(*) behavior).
		if item.Func != "" && item.Alias == "" {
			arg := item.Variable
			if item.Property != "" {
				arg = item.Variable + "." + item.Property
			}
			if item.Distinct {
				col = item.Func + "(DISTINCT " + arg + ")"
			} else {
				col = item.Func + "(" + arg + ")"
			}
		}
		if item.Alias != "" {
			col = item.Alias
		}
		cols = append(cols, col)
	}
	return cols
}

// buildProjectionRow builds a single result row from a binding.
func buildProjectionRow(b binding, items []ReturnItem, cols []string) map[string]any {
	row := make(map[string]any)
	for i, item := range items {
		row[cols[i]] = resolveItemValue(b, item)
	}
	return row
}

// resolveItemValue resolves a return item value from a binding (node or edge).
//
// Phase B2 (Plan 8-Phase Arc, 2026-05-09): function-call items (item.Func)
// short-circuit ahead of the property-access path. Currently only "LABELS"
// is supported; future built-ins extend this switch. Standard openCypher
// labels(node) returns an array of labels; code-graph nodes have a single
// label, so the result is a single-element array of strings.
func resolveItemValue(b binding, item ReturnItem) any {
	if item.Func == "LABELS" {
		if node, ok := b.nodes[item.Variable]; ok {
			if node.Label == "" {
				return []string{}
			}
			return []string{node.Label}
		}
		// labels() on an edge or unknown variable is undefined. Returning
		// an empty array (rather than nil) matches openCypher behavior on
		// missing nodes.
		return []string{}
	}
	if node, ok := b.nodes[item.Variable]; ok {
		return resolveNodeItemValue(node, item.Property)
	}
	if edge, ok := b.edges[item.Variable]; ok {
		return resolveEdgeItemValue(edge, item.Property)
	}
	return nil
}

// resolveNodeItemValue resolves a node return item: full node map or single property.
func resolveNodeItemValue(node *store.Node, property string) any {
	if property == "" {
		return map[string]any{
			"name":           node.Name,
			"qualified_name": node.QualifiedName,
			"label":          node.Label,
			"file_path":      node.FilePath,
			"start_line":     node.StartLine,
			"end_line":       node.EndLine,
		}
	}
	return getNodeProperty(node, property)
}

// resolveEdgeItemValue resolves an edge return item: full edge map or single property.
func resolveEdgeItemValue(edge *store.Edge, property string) any {
	if property == "" {
		return map[string]any{
			"type":      edge.Type,
			"source_id": edge.SourceID,
			"target_id": edge.TargetID,
		}
	}
	return getEdgeProperty(edge, property)
}

// resolveOrderColumn maps an ORDER BY field to the actual column name.
func resolveOrderColumn(orderBy string, items []ReturnItem, cols []string) string {
	// 1. Try alias match
	for i, item := range items {
		if item.Alias == orderBy {
			return cols[i]
		}
	}
	// 2. Try matching aggregate expression (e.g. "COUNT(r)")
	for i, item := range items {
		if item.Func == "COUNT" {
			expr := "COUNT(" + item.Variable + ")"
			if orderBy == expr {
				return cols[i]
			}
		}
	}
	// 3. Try matching variable.property to column name
	for i, item := range items {
		varProp := item.Variable
		if item.Property != "" {
			varProp += "." + item.Property
		}
		if orderBy == varProp {
			return cols[i]
		}
	}
	return orderBy
}

// applyLimit caps result rows. Explicit LIMIT values from the Cypher query are
// respected; when no LIMIT is specified (limit <= 0), maxRows is used as default.
// applyLimit trims rows to the effective cap (min of user LIMIT and maxRows).
// Returns (trimmed, truncated) — truncated is true iff rows were dropped, so
// the caller can surface that via Result.Truncated.
func applyLimit(rows []map[string]any, limit, maxRows int) ([]map[string]any, bool) {
	if limit <= 0 {
		limit = maxRows
	}
	if len(rows) > limit {
		return rows[:limit], true
	}
	return rows, false
}

func (e *Executor) aggregateResults(bindings []binding, ret *ReturnClause) (*Result, error) {
	groupItems, countItem := splitAggregateItems(ret.Items)
	groups, order := buildGroups(bindings, groupItems, countItem)

	// Build columns
	cols := buildColumnNames(ret.Items)

	// Build result rows
	countCol := countItem.Alias
	if countCol == "" {
		// Phase B1: COUNT(DISTINCT x.prop) renders distinctly from COUNT(x).
		arg := countItem.Variable
		if countItem.Property != "" {
			arg = countItem.Variable + "." + countItem.Property
		}
		if countItem.Distinct {
			countCol = "COUNT(DISTINCT " + arg + ")"
		} else {
			countCol = "COUNT(" + arg + ")"
		}
	}

	rows := make([]map[string]any, 0, len(order))
	for _, key := range order {
		g := groups[key]
		row := g.row
		row[countCol] = g.count
		rows = append(rows, row)
	}

	// ORDER BY
	if ret.OrderBy != "" {
		orderCol := resolveOrderColumn(ret.OrderBy, ret.Items, cols)
		sortRows(rows, orderCol, ret.OrderDir)
	}

	// LIMIT
	var trimmed bool
	rows, trimmed = applyLimit(rows, ret.Limit, e.maxRows())
	if trimmed {
		e.markTruncated()
	}

	return &Result{Columns: cols, Rows: rows}, nil
}

// splitAggregateItems separates return items into group-by items and the COUNT item.
func splitAggregateItems(items []ReturnItem) (groupItems []ReturnItem, countItem ReturnItem) {
	for _, item := range items {
		if item.Func == "COUNT" {
			countItem = item
		} else {
			groupItems = append(groupItems, item)
		}
	}
	return groupItems, countItem
}

type groupEntry struct {
	key   string
	row   map[string]any
	count int
	// Phase B1: distinctValues tracks unique values seen for COUNT(DISTINCT var)
	// or COUNT(DISTINCT var.prop). nil for non-DISTINCT aggregations.
	distinctValues map[string]struct{}
}

// buildGroups groups bindings by non-COUNT items and counts occurrences.
//
// Phase B1 (Plan 8-Phase Arc, 2026-05-09): when countItem.Distinct is true,
// the count for each group is the cardinality of the set of distinct values
// of countItem's variable+property across the group's bindings, NOT the
// total binding count. Empty/nil values are excluded from the set, matching
// COUNT(DISTINCT) semantics in standard Cypher (NULL values don't count).
func buildGroups(bindings []binding, groupItems []ReturnItem, countItem ReturnItem) (groups map[string]*groupEntry, order []string) {
	groups = make(map[string]*groupEntry)

	for _, b := range bindings {
		row := make(map[string]any)
		keyParts := make([]string, 0, len(groupItems))
		for _, item := range groupItems {
			col := item.Variable
			if item.Property != "" {
				col = item.Variable + "." + item.Property
			}
			if item.Alias != "" {
				col = item.Alias
			}
			val := resolveItemValue(b, item)
			row[col] = val
			keyParts = append(keyParts, fmt.Sprintf("%v", val))
		}
		key := strings.Join(keyParts, "\x00")
		g, ok := groups[key]
		if !ok {
			g = &groupEntry{key: key, row: row, count: 0}
			if countItem.Distinct {
				g.distinctValues = make(map[string]struct{})
			}
			groups[key] = g
			order = append(order, key)
		}
		if countItem.Distinct {
			// Resolve the COUNT(DISTINCT var) target value for this binding.
			distinctVal := resolveItemValue(b, countItem)
			if distinctVal == nil {
				continue
			}
			s := fmt.Sprintf("%v", distinctVal)
			if s == "" {
				continue
			}
			g.distinctValues[s] = struct{}{}
		} else {
			g.count++
		}
	}
	// Finalize: for DISTINCT, count = cardinality of the value set.
	for _, g := range groups {
		if g.distinctValues != nil {
			g.count = len(g.distinctValues)
		}
	}
	return groups, order
}

// sortRows sorts rows by the given column.
func sortRows(rows []map[string]any, col, dir string) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i][col], rows[j][col]
		cmp := compareValues(a, b)
		if dir == "DESC" {
			return cmp > 0
		}
		return cmp < 0
	})
}

func compareValues(a, b any) int {
	// Try numeric
	aNum, aOK := toFloat(a)
	bNum, bOK := toFloat(b)
	if aOK && bOK {
		if aNum < bNum {
			return -1
		}
		if aNum > bNum {
			return 1
		}
		return 0
	}
	// Fall back to string
	aStr := fmt.Sprintf("%v", a)
	bStr := fmt.Sprintf("%v", b)
	if aStr < bStr {
		return -1
	}
	if aStr > bStr {
		return 1
	}
	return 0
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	default:
		return 0, false
	}
}

// copyBinding makes a shallow copy of a binding.
func copyBinding(b binding) binding {
	c := newBinding()
	for k, v := range b.nodes {
		c.nodes[k] = v
	}
	for k, v := range b.edges {
		c.edges[k] = v
	}
	return c
}

// filterNodesByProps filters nodes by inline property key-value pairs.
func filterNodesByProps(nodes []*store.Node, props map[string]string) []*store.Node {
	var filtered []*store.Node
	for _, n := range nodes {
		if nodeMatchesProps(n, props) {
			filtered = append(filtered, n)
		}
	}
	return filtered
}

// nodeMatchesProps checks if a node matches all given property filters.
func nodeMatchesProps(n *store.Node, props map[string]string) bool {
	for key, val := range props {
		actual := getNodeProperty(n, key)
		if fmt.Sprintf("%v", actual) != val {
			return false
		}
	}
	return true
}
