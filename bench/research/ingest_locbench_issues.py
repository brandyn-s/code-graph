"""Ingest Loc-Bench mined issues as IssueMemory nodes in the code-graph store.

Reads ~/.cache/code-graph/episodic-memory/locbench-issues.jsonl (mined by
mine_locbench_issues.py), embeds each via Voyage AI, and writes IssueMemory
nodes + embeddings to a dedicated code-graph project at
~/.cache/code-graph/episodic-memory-locbench.db.

This is a STANDALONE ingestion path (not the regular tree-sitter pipeline).
The corpus is one-shot mined from external repos and ingested into its own
project namespace, queryable independently of any indexed source code.

Schema follows code-graph's existing table definitions in internal/store/store.go:
  projects(name, indexed_at, root_path)
  nodes(id, project, label, name, qualified_name, file_path, start_line, end_line, properties)
  node_embeddings(node_id, model, embedding BLOB)

Embedding text format: "# IssueMemory {org}/{repo}#{pr}\n{title}\n{body_truncated}"
mirrors the format used by pass_embeddings.go (label-prefix + identity + content).

Voyage model: voyage-code-3 (matches code-graph default; same dimension
allows future cross-project comparisons if desired).

Usage:
  VOYAGE_API_KEY=... python ingest_locbench_issues.py
  VOYAGE_API_KEY=... python ingest_locbench_issues.py --limit 10  # smoke test
"""

from __future__ import annotations

import argparse
import json
import os
import sqlite3
import struct
import sys
import time
import urllib.error
import urllib.request
from datetime import datetime, timezone
from pathlib import Path

VOYAGE_URL = "https://api.voyageai.com/v1/embeddings"
VOYAGE_MODEL = "voyage-code-3"
VOYAGE_BATCH_SIZE = 64
VOYAGE_BATCH_DELAY_S = 1.0

DEFAULT_PROJECT_NAME = "episodic-memory-locbench"
DEFAULT_ROOT_PATH = "<synthetic: locbench mined issues>"
LABEL = "IssueMemory"

BODY_TRUNCATE_CHARS = 8000  # Per C2 plan


SCHEMA_SQL = """
CREATE TABLE IF NOT EXISTS projects (
    name TEXT PRIMARY KEY,
    indexed_at TEXT NOT NULL,
    root_path TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS nodes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project TEXT NOT NULL REFERENCES projects(name) ON DELETE CASCADE,
    label TEXT NOT NULL,
    name TEXT NOT NULL,
    qualified_name TEXT NOT NULL,
    file_path TEXT DEFAULT '',
    start_line INTEGER DEFAULT 0,
    end_line INTEGER DEFAULT 0,
    properties TEXT DEFAULT '{}',
    UNIQUE(project, qualified_name)
);

CREATE INDEX IF NOT EXISTS idx_nodes_label ON nodes(project, label);
CREATE INDEX IF NOT EXISTS idx_nodes_name ON nodes(project, name);

CREATE TABLE IF NOT EXISTS node_embeddings (
    node_id INTEGER PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
    model TEXT NOT NULL,
    embedding BLOB NOT NULL
);
"""


def voyage_embed_batch(texts: list[str], api_key: str) -> list[list[float]]:
    """Embed a batch via Voyage REST. Retries on 429/5xx."""
    body = json.dumps({
        "input": texts,
        "model": VOYAGE_MODEL,
        "input_type": "document",
    }).encode("utf-8")
    for attempt in range(4):
        req = urllib.request.Request(
            VOYAGE_URL,
            data=body,
            headers={
                "Authorization": f"Bearer {api_key}",
                "Content-Type": "application/json",
            },
        )
        try:
            with urllib.request.urlopen(req, timeout=120) as resp:
                data = json.loads(resp.read().decode("utf-8"))
            # Sort by index to maintain input order
            entries = sorted(data["data"], key=lambda e: e["index"])
            return [e["embedding"] for e in entries]
        except urllib.error.HTTPError as e:
            if e.code in (429, 500, 502, 503, 504) and attempt < 3:
                wait = 2 ** attempt
                print(f"  voyage retry: status={e.code} attempt={attempt+1} wait={wait}s", file=sys.stderr)
                time.sleep(wait)
                continue
            raise
        except urllib.error.URLError as e:
            if attempt < 3:
                wait = 2 ** attempt
                print(f"  voyage net retry: err={e} attempt={attempt+1} wait={wait}s", file=sys.stderr)
                time.sleep(wait)
                continue
            raise
    raise RuntimeError("voyage embed: exhausted retries")


def float32s_to_blob(vec: list[float]) -> bytes:
    """Serialize float32 vector as little-endian bytes (matches Go's float32sToBlob)."""
    return struct.pack(f"<{len(vec)}f", *vec)


def build_embedding_text(rec: dict) -> str:
    """Build the text input for embedding.

    Format mirrors pass_embeddings.go's buildEmbeddingText: label-prefix +
    identity line + content. Body truncated to BODY_TRUNCATE_CHARS so the
    Voyage payload stays well below their token cap (~32K per request).
    """
    title = rec.get("pr_title", "") or ""
    body = rec.get("pr_body", "") or ""
    body_trunc = body[:BODY_TRUNCATE_CHARS]
    qn = f"{rec['org']}/{rec['repo']}#{rec['pr_number']}"
    return f"# IssueMemory {qn}\n{title}\n{body_trunc}"


def load_records(path: Path, limit: int | None) -> list[dict]:
    records = []
    with path.open(encoding="utf-8") as f:
        for line in f:
            records.append(json.loads(line))
            if limit and len(records) >= limit:
                break
    return records


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--input",
        type=Path,
        default=Path.home() / ".cache" / "code-graph" / "episodic-memory" / "locbench-issues.jsonl",
    )
    parser.add_argument(
        "--db",
        type=Path,
        default=None,
        help="SQLite output path. Defaults to ~/.cache/code-graph/{project}.db",
    )
    parser.add_argument(
        "--project",
        type=str,
        default=DEFAULT_PROJECT_NAME,
        help="code-graph project name (also used in nodes.project column). "
             "Default 'episodic-memory-locbench'. Use 'episodic-memory-redacted' "
             "for the redacted-internal corpus, or any other namespaced corpus.",
    )
    parser.add_argument(
        "--root-path",
        type=str,
        default=None,
        help="Root path string for the project row (cosmetic). "
             "Default depends on --project.",
    )
    parser.add_argument("--limit", type=int, default=None, help="Smoke-test record cap")
    parser.add_argument("--dry-run", action="store_true", help="Embed but don't write to DB")
    args = parser.parse_args()
    if args.db is None:
        args.db = Path.home() / ".cache" / "code-graph" / f"{args.project}.db"
    if args.root_path is None:
        args.root_path = (
            DEFAULT_ROOT_PATH if args.project == DEFAULT_PROJECT_NAME
            else f"<synthetic: {args.project} mined corpus>"
        )

    api_key = os.environ.get("VOYAGE_API_KEY")
    if not api_key:
        print("ERROR: VOYAGE_API_KEY not set", file=sys.stderr)
        sys.exit(1)

    if not args.input.exists():
        print(f"ERROR: {args.input} not found. Run mine_locbench_issues.py first.", file=sys.stderr)
        sys.exit(1)

    records = load_records(args.input, args.limit)
    print(f"Loaded {len(records)} records from {args.input}", file=sys.stderr)

    # Embed
    texts = [build_embedding_text(r) for r in records]
    print(f"Embedding {len(texts)} records via Voyage ({VOYAGE_MODEL})...", file=sys.stderr)
    all_vecs: list[list[float]] = []
    for i in range(0, len(texts), VOYAGE_BATCH_SIZE):
        batch = texts[i : i + VOYAGE_BATCH_SIZE]
        if i > 0:
            time.sleep(VOYAGE_BATCH_DELAY_S)
        vecs = voyage_embed_batch(batch, api_key)
        all_vecs.extend(vecs)
        print(f"  embedded {len(all_vecs)}/{len(texts)}", file=sys.stderr)

    if not all_vecs:
        print("ERROR: no vectors returned", file=sys.stderr)
        sys.exit(1)

    dim = len(all_vecs[0])
    print(f"Embedding dimension: {dim}", file=sys.stderr)

    if args.dry_run:
        print(f"DRY RUN: would write {len(records)} nodes + embeddings to {args.db}", file=sys.stderr)
        return

    # Write to SQLite
    args.db.parent.mkdir(parents=True, exist_ok=True)
    conn = sqlite3.connect(str(args.db))
    try:
        conn.executescript("PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;")
        conn.executescript(SCHEMA_SQL)

        now_iso = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
        conn.execute(
            "INSERT INTO projects(name, indexed_at, root_path) VALUES(?, ?, ?) "
            "ON CONFLICT(name) DO UPDATE SET indexed_at=excluded.indexed_at, root_path=excluded.root_path",
            (args.project, now_iso, args.root_path),
        )

        for rec, vec in zip(records, all_vecs):
            qn = f"{rec['org']}/{rec['repo']}#{rec['pr_number']}"
            name = f"{rec['repo']}#{rec['pr_number']}"
            props = {
                "org": rec["org"],
                "repo": rec["repo"],
                "pr_number": rec["pr_number"],
                "pr_title": rec.get("pr_title", ""),
                "merged_at": rec.get("merged_at", ""),
                "changed_files": rec.get("changed_files", []),
                "linked_issues": rec.get("linked_issues", []),
            }
            cur = conn.execute(
                "INSERT INTO nodes(project, label, name, qualified_name, file_path, start_line, end_line, properties) "
                "VALUES(?, ?, ?, ?, '', 0, 0, ?) "
                "ON CONFLICT(project, qualified_name) DO UPDATE SET "
                "label=excluded.label, name=excluded.name, properties=excluded.properties",
                (args.project, LABEL, name, qn, json.dumps(props)),
            )
            node_id = cur.lastrowid
            if not node_id:
                # On conflict, fetch the existing id
                row = conn.execute(
                    "SELECT id FROM nodes WHERE project=? AND qualified_name=?",
                    (args.project, qn),
                ).fetchone()
                if not row:
                    raise RuntimeError(f"failed to resolve node id for {qn}")
                node_id = row[0]

            blob = float32s_to_blob(vec)
            conn.execute(
                "INSERT INTO node_embeddings(node_id, model, embedding) VALUES(?, ?, ?) "
                "ON CONFLICT(node_id) DO UPDATE SET model=excluded.model, embedding=excluded.embedding",
                (node_id, VOYAGE_MODEL, blob),
            )

        conn.commit()
    finally:
        conn.close()

    print(f"Wrote {len(records)} IssueMemory nodes + embeddings to {args.db}", file=sys.stderr)
    print(f"Voyage cost estimate: ~${len(records) * 0.00018 * 0.5:.2f} (Batch API discount)", file=sys.stderr)


if __name__ == "__main__":
    main()
