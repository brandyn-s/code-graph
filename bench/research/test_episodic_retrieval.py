"""Quick smoke test: cosine-retrieve top-K IssueMemory nodes for a query.

Mirrors what the C3 agent-loop step will do at retrieval time:
  1. Embed query via Voyage with input_type='query'
  2. Cosine-search over stored IssueMemory embeddings
  3. Return top-K (qualified_name, score, changed_files, pr_title, body_preview)

This is intentionally a toy implementation in Python to validate the
ingestion artifact end-to-end. The production retrieval is a Go path
through internal/store/embeddings.go (already exists for code semantic
search) which the agent will reuse.

Usage:
  VOYAGE_API_KEY=... python test_episodic_retrieval.py "django authentication middleware"
  VOYAGE_API_KEY=... python test_episodic_retrieval.py "pandas dataframe groupby aggregation bug" --k 5
"""

from __future__ import annotations

import argparse
import json
import os
import sqlite3
import struct
import sys
import urllib.request
from pathlib import Path

VOYAGE_URL = "https://api.voyageai.com/v1/embeddings"
VOYAGE_MODEL = "voyage-code-3"
PROJECT_NAME = "episodic-memory-locbench"


def voyage_embed_query(text: str, api_key: str) -> list[float]:
    body = json.dumps({
        "input": [text],
        "model": VOYAGE_MODEL,
        "input_type": "query",
    }).encode("utf-8")
    req = urllib.request.Request(
        VOYAGE_URL,
        data=body,
        headers={"Authorization": f"Bearer {api_key}", "Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=60) as resp:
        data = json.loads(resp.read().decode("utf-8"))
    return data["data"][0]["embedding"]


def blob_to_float32s(blob: bytes) -> list[float]:
    n = len(blob) // 4
    return list(struct.unpack(f"<{n}f", blob))


def cosine(a: list[float], b: list[float]) -> float:
    dot = sum(x * y for x, y in zip(a, b))
    na = sum(x * x for x in a) ** 0.5
    nb = sum(x * x for x in b) ** 0.5
    if na == 0 or nb == 0:
        return 0.0
    return dot / (na * nb)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("query", type=str)
    parser.add_argument("--k", type=int, default=3)
    parser.add_argument(
        "--project",
        type=str,
        default=PROJECT_NAME,
        help="Project name to query against. Default 'episodic-memory-locbench'. "
             "Use 'episodic-memory-redacted' for the redacted-internal corpus.",
    )
    parser.add_argument(
        "--db",
        type=Path,
        default=None,
        help="DB path. Defaults to ~/.cache/codebase-memory-mcp/{project}.db",
    )
    args = parser.parse_args()
    if args.db is None:
        args.db = Path.home() / ".cache" / "codebase-memory-mcp" / f"{args.project}.db"

    api_key = os.environ.get("VOYAGE_API_KEY")
    if not api_key:
        print("ERROR: VOYAGE_API_KEY not set", file=sys.stderr)
        sys.exit(1)

    if not args.db.exists():
        print(f"ERROR: {args.db} not found. Run ingest_locbench_issues.py first.", file=sys.stderr)
        sys.exit(1)

    print(f"Embedding query: {args.query!r}", file=sys.stderr)
    qvec = voyage_embed_query(args.query, api_key)

    conn = sqlite3.connect(str(args.db))
    rows = conn.execute(
        """SELECT n.id, n.qualified_name, n.properties, e.embedding
           FROM nodes n JOIN node_embeddings e ON n.id = e.node_id
           WHERE n.project=? AND n.label='IssueMemory'""",
        (args.project,),
    ).fetchall()
    conn.close()

    print(f"Scoring against {len(rows)} IssueMemory nodes...", file=sys.stderr)
    scored = []
    for nid, qn, props_json, blob in rows:
        vec = blob_to_float32s(blob)
        score = cosine(qvec, vec)
        scored.append((score, nid, qn, json.loads(props_json)))
    scored.sort(reverse=True, key=lambda x: x[0])

    print(f"\nTop-{args.k} for query: {args.query!r}\n")
    for rank, (score, nid, qn, props) in enumerate(scored[: args.k]):
        title = props.get("pr_title", "")
        files = props.get("changed_files", [])
        print(f"#{rank+1}  score={score:.4f}  {qn}")
        print(f"    title: {title}")
        print(f"    files: {files[:4]}{'...' if len(files) > 4 else ''}")
        print()


if __name__ == "__main__":
    main()
