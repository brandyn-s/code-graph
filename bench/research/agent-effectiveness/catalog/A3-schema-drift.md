# A3 — Schema drift audit

Generated 2026-05-12 by bench/research/agent-effectiveness/audit_schemas.py

## Summary

- Tools in agent_runner.py TOOL_SCHEMAS: **15**
- Tools with parseable handler InputSchema: **37**
- Tools with confirmed drift: **14**
- Tools missing from agent_runner.py: **22**
- Tools missing from handler InputSchema (parser failed or not exposed): **0**

## Drift detail

### `degree_filter`

- required only in agent: `['project']`

### `delete_project`

- properties only in agent_runner.py: `['project']`
- properties only in handler: `['project_name']`
- required only in agent: `['project']`
- required only in handler: `['project_name']`

### `detect_changes`

- properties only in agent_runner.py: `['from_rev', 'to_rev']`
- properties only in handler: `['base_branch', 'depth', 'scope']`
- required only in agent: `['project']`

### `get_architecture`

- properties only in handler: `['aspects']`
- required only in agent: `['project']`

### `get_code_snippet`

- properties only in handler: `['include_neighbors']`
- required only in agent: `['project']`

### `get_graph_schema`

- required only in agent: `['project']`

### `index_repository`

- properties only in agent_runner.py: `['path']`
- properties only in handler: `['force', 'mode', 'repo_path', 'skip_report']`
- required only in agent: `['path']`

### `index_status`

- required only in agent: `['project']`

### `ingest_traces`

- properties only in agent_runner.py: `['trace_path']`
- properties only in handler: `['file_path']`
- required only in agent: `['trace_path']`
- required only in handler: `['file_path']`

### `manage_adr`

- properties only in agent_runner.py: `['adr_id', 'operation']`
- properties only in handler: `['content', 'include', 'mode', 'sections']`
- required only in agent: `['operation', 'project']`
- required only in handler: `['mode']`

### `query_graph`

- properties only in handler: `['max_rows']`
- required only in agent: `['project']`

### `search_code`

- properties only in handler: `['offset']`
- required only in agent: `['project']`

### `search_graph`

- properties only in handler: `['case_sensitive', 'direction', 'exclude_entry_points', 'exclude_labels', 'file_pattern', 'include_connected', 'include_source', 'max_complexity', 'max_degree', 'min_complexity', 'min_degree', 'offset', 'qn_pattern', 'relationship', 'sort_by']`
- required only in agent: `['project']`

### `trace_call_path`

- properties only in handler: `['include_source', 'min_confidence', 'risk_labels']`
- required only in agent: `['project']`

## Tools missing from agent_runner.py

These tools are registered in handlers but the agent doesn't know about them. The agent can't call what it can't see.

- `code_localize`
- `code_localize_agent`
- `detect_cycles`
- `diff_graph`
- `diff_services`
- `explain_service`
- `explain_symbol`
- `find_rationale`
- `find_similar_functions`
- `generate_report`
- `get_affected_tests`
- `get_change_coupling`
- `get_relevant_context`
- `get_review_context`
- `index_health`
- `query_security_surfaces`
- `query_stig_evidence`
- `rank_by_query`
- `search_code_semantic`
- `service_map`
- `trace_data_flow`
- `visualize`

## Tools whose handler InputSchema couldn't be parsed

Most likely because the schema is built programmatically, not as a json.RawMessage literal. These are not necessarily wrong — they just can't be cross-referenced automatically.

