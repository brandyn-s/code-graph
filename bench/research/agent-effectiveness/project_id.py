"""Python mirror of internal/pipeline/pipeline.go::ProjectNameFromPath.

Code-graph derives the project_id (the SQLite-store key for a project)
from the indexing root path. The agent-effectiveness battery needs to
ask the server about specific projects by id, so its target list must
carry project_ids that match what the Go pipeline would produce. The
corpus.json file hard-codes developer-machine paths and IDs; CI clones
fixtures to different paths, so without this helper every CI run gets
a project_id mismatch and every schema-validation question against
the ripgrep fixture fails.

Lives in its own module so the agent-effectiveness harness can import
it without dragging in agent_runner's anthropic dependency.

MUST stay in sync with the Go implementation. If pipeline.go changes
the derivation rules, update both sites.
"""
from __future__ import annotations


def project_name_from_path(abs_path: str) -> str:
    """Mirror of ProjectNameFromPath (internal/pipeline/pipeline.go).

    Steps:
      1. Normalize separators (backslash -> forward slash).
      2. Collapse runs of `/` (matches Go's filepath.Clean for POSIX).
      3. Lowercase a Windows drive letter (`C:/foo` -> `c:/foo`).
      4. Replace `/` and `:` with `-`.
      5. Collapse runs of `--` to single `-`.
      6. Strip leading dashes.
      7. Empty string -> "root".
    """
    if not abs_path:
        return "root"
    cleaned = abs_path.replace("\\", "/")
    while "//" in cleaned:
        cleaned = cleaned.replace("//", "/")
    if len(cleaned) >= 2 and cleaned[1] == ":":
        cleaned = cleaned[0].lower() + cleaned[1:]
    name = cleaned.replace("/", "-").replace(":", "-")
    while "--" in name:
        name = name.replace("--", "-")
    name = name.lstrip("-")
    return name or "root"
