# Connecting MCP clients

`code-graph` is a stdio MCP server. Every client below just needs the path to
the binary; the installer puts it at `~/.local/bin/code-graph` (Windows:
`%LOCALAPPDATA%\code-graph\code-graph.exe`). Running `code-graph install`
auto-detects and configures all of the clients marked "auto" in one step;
`code-graph install --dry-run` shows what it would change.

Optional environment: set `VOYAGE_API_KEY` in the client config's `env` block
to enable semantic node search with Voyage, or `CODE_GRAPH_EMBED_BASE_URL` and
`CODE_GRAPH_EMBED_MODEL` to use any OpenAI-compatible embeddings endpoint
(OpenAI, Azure, Gemini, Ollama, vLLM, and others; see
[embeddings.md](embeddings.md)). Everything else works offline.

## Claude Code (auto)

```bash
claude mcp add code-graph --scope user -- ~/.local/bin/code-graph
```

Or in `~/.claude.json` / a project `.mcp.json`:

```json
{
  "mcpServers": {
    "code-graph": {
      "type": "stdio",
      "command": "/home/you/.local/bin/code-graph",
      "env": { "VOYAGE_API_KEY": "<optional>" }
    }
  }
}
```

## Claude Desktop

`~/Library/Application Support/Claude/claude_desktop_config.json` (macOS) or
`%APPDATA%\Claude\claude_desktop_config.json` (Windows):

```json
{
  "mcpServers": {
    "code-graph": { "command": "/home/you/.local/bin/code-graph" }
  }
}
```

## Codex CLI (auto)

`~/.codex/config.toml`:

```toml
[mcp_servers.code-graph]
command = "/home/you/.local/bin/code-graph"
```

## Cursor (auto)

`~/.cursor/mcp.json` (global) or `.cursor/mcp.json` (project):

```json
{
  "mcpServers": {
    "code-graph": { "command": "/home/you/.local/bin/code-graph" }
  }
}
```

## Windsurf (auto)

`~/.codeium/windsurf/mcp_config.json`:

```json
{
  "mcpServers": {
    "code-graph": { "command": "/home/you/.local/bin/code-graph" }
  }
}
```

## Gemini CLI (auto)

`~/.gemini/settings.json`:

```json
{
  "mcpServers": {
    "code-graph": { "command": "/home/you/.local/bin/code-graph" }
  }
}
```

## VS Code (auto)

`.vscode/mcp.json` in the workspace, or the user `mcp.json`:

```json
{
  "servers": {
    "code-graph": { "type": "stdio", "command": "/home/you/.local/bin/code-graph" }
  }
}
```

## Zed (auto)

`~/.config/zed/settings.json`:

```json
{
  "context_servers": {
    "code-graph": { "command": { "path": "/home/you/.local/bin/code-graph", "args": [] } }
  }
}
```

## Any other client

Point a stdio MCP server at the binary with no arguments:

```json
{ "command": "/home/you/.local/bin/code-graph", "args": [] }
```

## First run

```text
index_repository(repo_path="/absolute/repository")
index_status(project="<project-name>")
```

Indexing writes only to the cache directory (`~/.cache/code-graph`); the
checkout is never modified unless you ask for a report or visualization at a
path inside it. Large repositories can take minutes; `index_status` reports
progress and the captured index identity.
