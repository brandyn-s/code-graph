# A4 — Questions-by-category matrix

Built from A1 (CLI extraction path), A2 (tool audit), A3 (schema drift). Each
category lists 5-8 question shapes that would exercise the failure mode if it
recurred.

## Category 1: Banner / sidecar interference with CLI extraction

**Failure shape**: `addUpdateNotice` prepends a banner to `result.Content[0]`. CLI's content-extraction must skip it to find the real response. Fix shipped 2026-05-12 covers all 10+ callers; the regression test must trip when fix is reverted.

**At-risk tools** (24+ call `addUpdateNotice`): get_architecture, search_graph, search_code, get_graph_schema, query_graph, get_code_snippet, trace_call_path, find_rationale, list_projects, manage_adr, ...

**Question shapes**:
1. Ask for graph schema only — first call is `get_graph_schema`; if banner leaks, response is "⚡ Update available..." not JSON.
2. Ask for project list — first call is `list_projects`; same surface.
3. Ask for top-level architecture summary — first call is `get_architecture`.
4. Ask the agent to find an entity by name — first call is `search_graph`.
5. Ask the agent to retrieve source for a function — first call is `get_code_snippet`.

**Scoring signal**: did the first tool response contain `⚡ Update available` AND nothing else? Binary fail.

## Category 2: Default-too-verbose responses bloating agent context

**Failure shape**: tool emits >10KB of JSON when the caller wanted a summary. Agent's context fills; later questions hit `max_turns_exhausted`.

**At-risk tools** (measured default-on-PSM):
- `list_projects`: 76 KB
- `service_map`: 46 KB
- `find_rationale`: 16 KB
- `detect_cycles`: 10 KB
- `get_graph_schema`: 8 KB (vs upstream 1.4 KB)

**Question shapes**:
1. Ask "list the projects I have indexed" — `list_projects` is the natural call.
2. Ask "show me service-to-service dependencies in PSM" — `service_map` is natural.
3. Ask "find SAFETY / WHY comments" — `find_rationale` is natural.
4. Ask "are there any circular dependencies" — `detect_cycles` is natural.
5. Ask "what edge types are in the graph" — `get_graph_schema` is natural.
6. Then ask a follow-up question to verify context isn't blown.

**Scoring signal**: did the tool's first response exceed 10KB? AND did follow-up questions hit `max_turns_exhausted`? Combined binary fail.

## Category 3: Tool-schema drift (agent ≠ handler)

**Failure shape**: agent's TOOL_SCHEMAS describes params that the handler doesn't accept (or vice versa). Agent makes calls that fail validation. From A3 audit: 14 tools have confirmed drift, 22 tools the agent doesn't know about at all.

**Confirmed drift cases worth testing**:
1. `delete_project`: agent says `project`, handler wants `project_name`
2. `ingest_traces`: agent says `trace_path`, handler wants `file_path`
3. `manage_adr`: agent says `{operation, adr_id}`, handler wants `{mode, content, include, sections}` — completely different API
4. `index_repository`: agent says `path`, handler wants `repo_path`
5. `detect_changes`: agent says `{from_rev, to_rev}`, handler wants `{base_branch, depth, scope}`
6. `search_graph`: agent missing 15 properties the handler accepts (case_sensitive, direction, exclude_entry_points, file_pattern, include_source, max_complexity, min_complexity, min_degree, qn_pattern, relationship, sort_by, etc.)

**Question shapes**:
1. Ask agent to manage an ADR — exposes `manage_adr` mismatch
2. Ask agent to find Functions in a specific file matching a name pattern — exposes `search_graph.file_pattern` gap
3. Ask agent to find high-complexity functions — exposes `search_graph.min_complexity` gap
4. Ask agent to find functions in a specific qualified-name namespace — exposes `search_graph.qn_pattern` gap
5. Ask agent to find recent git changes — exposes `detect_changes` mismatch
6. Ask agent to perform a trace at high confidence only — exposes `trace_call_path.min_confidence` gap

**Scoring signal**: did the agent's `tool_calls` show ANY `ok=False` because of arg-validation? Binary fail. ALSO: did the agent fail to call a tool because the schema misled it?

## Category 4: Missing tool surface for a question class

**Failure shape**: agent doesn't have the right tool, thrashes through alternatives, eventually `max_turns_exhausted`. Q9 (find no-caller functions) was the canonical case before `degree_filter` shipped.

**Likely-missing surfaces** (questions where current toolset MAY thrash):
1. "Find all functions with cyclomatic complexity > 20" — `search_graph.min_complexity` exists in handler but not in agent schema (Category 3 overlap)
2. "Find functions called by exactly one caller" — `degree_filter` now covers it
3. "List all crates in the workspace by node count" — would need `get_architecture(aspects=['packages'])` with packages aspect; default summary doesn't include it
4. "Which functions read environment variables?" — graph has `READS_ENV` edge but no dedicated tool to enumerate
5. "Find all unsafe Rust blocks" — `find_rationale(kind='SAFETY')` covers this; verify agent picks it
6. "Show the architecture of a specific sub-crate" — `explain_service` exists in handler but is missing from agent_runner (Category 3 overlap)

**Scoring signal**: agent stop_reason == `max_turns_exhausted` on a question where a competent answer is structurally possible.

## Category 5: CLI vs MCP transport divergence

**Failure shape**: tool returns one thing in MCP stdio mode, different thing in CLI mode. Banner-bug was the canonical case. This category has limited surface because CLI mode is the same code path as the MCP server's tool dispatch — only the OUTPUT MARSHALING differs.

**Question shapes** (limited — most likely a non-issue once Category 1 is covered):
1. Verify each tool returns same JSON shape in CLI as it would over MCP.
2. Verify stop_reason / IsError flagging in CLI matches MCP.
3. Verify Unicode handling (UTF-8) is identical.

**Scoring signal**: CLI response shape == programmatic MCP response shape for each tool. Probably an off-line test, not a question-shaped test. **Defer to Phase B / Phase C as a separate unit-test layer, not a question.**

## Category 6: Output-shape regressions from upstream framework changes

**Failure shape**: upstream go-sdk MCP package changes Content[] semantics, SDK upgrade changes how results are wrapped, SQLite schema migration changes column names (`type` vs `rel_type` was the canonical case).

**Question shapes**:
1. Run `get_graph_schema` and validate response is `{edge_types: [{type, count}, ...], node_labels: [{label, count}, ...]}` shape.
2. Run `query_graph` with a simple count query, validate response is a list-shaped result.
3. Run `search_graph(label='Function', limit=5)`, validate response is an array of 5 items.
4. Run `trace_call_path(function_name='main', direction='outbound')`, validate response shape.
5. Run `degree_filter(label='Function', direction='inbound', op='eq', value=0, limit=5)`, validate response is `{count, examples}`.
6. Run `get_architecture` default, validate response is the new compact `{project, total_nodes, total_edges, node_labels, edge_types}` shape (not the legacy 500KB).

**Scoring signal**: response JSON schema validation. Hard PASS/FAIL on each. Schema test, not LLM judge.

## Aggregate counts

- Category 1 (banner): 5 questions
- Category 2 (verbosity): 6 questions
- Category 3 (schema drift): 6 questions
- Category 4 (missing surface): 6 questions
- Category 5 (CLI/MCP divergence): SKIPPED (offline unit test instead)
- Category 6 (output-shape): 6 questions (schema-validation, not LLM-scored)

**Total: 23 LLM-graded questions + 6 schema-validated questions = 29.**

Battery cost estimate (LLM-graded only): ~29 questions × ~$0.50/Q on Opus 4.7 ≈ **$15** per run. ≤$20 budget met.

Optimization: questions in category 1 (banner) can use Haiku 4.5 ($0.05/Q) because they only need the agent to make ONE tool call and the binary outcome is mechanical (banner-in-response or not). Saves ~$2.

Final budget estimate: **~$13 per run** at 25 min wall on Opus 4.7 with parallelism.
