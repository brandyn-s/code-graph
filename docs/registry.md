# Publishing to the MCP registry

The [MCP registry](https://github.com/modelcontextprotocol/registry) lists
servers by a `server.json` manifest. code-graph is distributed as release
binaries, so the manifest uses the `mcpb` package type: one entry per platform
archive with its download URL and SHA-256.

## After each release

1. Render the manifest from the published release:

   ```bash
   scripts/update-server-json.sh v0.9.0          # writes ./server.json
   ```

   The script reads `checksums.txt` from the release, fills
   `packaging/mcp-registry/server.json.tmpl`, refuses to leave a placeholder,
   and validates the JSON.

2. Publish with the registry CLI (install per the registry README):

   ```bash
   mcp-publisher login github
   mcp-publisher publish
   ```

   The `io.github.brandyn-s/code-graph` namespace is proven by the GitHub
   login, so no extra secret is needed.

3. Commit the rendered `server.json` so the repository records what was
   published for that version.

## Notes

- The schema URL pinned in the template is
  `https://static.modelcontextprotocol.io/schemas/2025-12-11/server.schema.json`;
  bump it when the registry announces a new schema and re-run the script.
- Clients that install from the registry download the archive and run
  `code-graph` over stdio; nothing else changes.
