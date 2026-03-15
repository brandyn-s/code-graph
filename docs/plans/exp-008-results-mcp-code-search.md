# code-search-mcp Results (EXP-008)

## Installation

- **Package:** code-search-mcp 0.6.1 (PyPI name differs from GitHub: `mcp-code-search`)
- **Method:** pip install code-search-mcp
- **Dependencies:** mcp (minimal)

## Outcome: FAILED — Not Functional

### Issues

1. **Encoding crash on Windows.** `index_repository` returns `'charmap' codec can't encode characters` because the tool outputs Chinese text that Windows cp1252 can't handle. The entire codebase and tool descriptions are in Chinese (simplified).

2. **Missing method.** `smart_search` returns `'Database' object has no attribute 'search_content'` — the Database class is missing an expected method. This is a code-level bug in the package.

3. **Index never built.** `get_stats` returns 0 files, 0 symbols, 0 content blocks.

### Root Causes

- Tool was designed for Chinese-language environments; no encoding handling for Windows non-UTF-8 locales
- Package appears to be an early-stage project with unfinished features
- No semantic search capability — this is a FTS5 + AST indexer, not an embedding-based search tool

### Verdict

**Disqualified.** Cannot index, cannot search. Uninstalled.
