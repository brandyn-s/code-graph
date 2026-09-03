"""Correlate cbm-call-audit output with indexed code-graph DB CALLS edges.

For each function:
  - extractor count = sum of CBMCalls in the audit JSON
  - DB count = COUNT(edges) WHERE source = function AND type = 'CALLS'

Difference = resolver drop count for that function. High drop counts on
functions with high extractor counts are the next-investigation target.

Usage:
  cbm-call-audit --project PROJECT --dir REPO_DIR > audit.jsonl
  python correlate_cbm_audit_to_db.py \\
      --audit audit.jsonl \\
      --project-prefix PROJECT \\
      --db ~/.cache/code-graph/PROJECT.db

Originally written for Phase A''' of the ABC future-arcs roadmap
(2026-05-14). Finding on assetman: 4780 CBMCalls extracted, 548 edges
in DB — 88.5% resolver drop rate. The C extractor isn't the
under-emission bug surface; the resolver is.
"""
from __future__ import annotations

import argparse
import json
import sqlite3
import sys
from pathlib import Path


def load_extractor_output(path: Path, project_prefix: str) -> tuple[
        dict[str, int], dict[str, int]]:
    extractor_calls: dict[str, int] = {}
    extractor_loc: dict[str, int] = {}
    text = path.read_text(encoding="utf-8")
    decoder = json.JSONDecoder()
    i = 0
    while i < len(text):
        while i < len(text) and text[i].isspace():
            i += 1
        if i >= len(text):
            break
        try:
            obj, end = decoder.raw_decode(text, i)
        except json.JSONDecodeError as e:
            print(f"  decode error at {i}: {e}", file=sys.stderr)
            break
        i = end
        for fr in (obj.get("functions") or []):
            qn = fr["qn"]
            if qn.startswith(project_prefix + "."):
                short = qn[len(project_prefix) + 1:]
                extractor_calls[short] = (
                    extractor_calls.get(short, 0) + fr["cbm_calls"]
                )
                extractor_loc[short] = fr["loc_lines"]
    return extractor_calls, extractor_loc


def load_db_call_counts(db: Path, project_prefix: str) -> dict[str, int]:
    conn = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
    cur = conn.cursor()
    cur.execute("""
        SELECT n.qualified_name, COUNT(e.id)
        FROM nodes n
        LEFT JOIN edges e ON e.source_id = n.id AND e.type = 'CALLS'
        WHERE n.label IN ('Function','Method')
        GROUP BY n.id
    """)
    db_calls: dict[str, int] = {}
    for qn, count in cur.fetchall():
        if qn.startswith(project_prefix + "."):
            short = qn[len(project_prefix) + 1:]
            db_calls[short] = count
    return db_calls


def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("--audit", required=True,
                   help="JSONL output from cbm-call-audit")
    p.add_argument("--project-prefix", required=True,
                   help="QN prefix matching audit --project and DB project")
    p.add_argument("--db", required=True,
                   help="Path to indexed code-graph DB")
    p.add_argument("--top", type=int, default=25,
                   help="How many top-drop functions to print")
    args = p.parse_args()

    extractor_calls, extractor_loc = load_extractor_output(
        Path(args.audit), args.project_prefix)
    db_calls = load_db_call_counts(Path(args.db), args.project_prefix)
    print(f"Loaded {len(extractor_calls)} functions from audit")
    print(f"Loaded {len(db_calls)} functions from DB\n")

    all_funcs = set(extractor_calls.keys()) | set(db_calls.keys())
    rows = []
    for fn in all_funcs:
        ext_n = extractor_calls.get(fn, 0)
        db_n = db_calls.get(fn, 0)
        loc = extractor_loc.get(fn, 0)
        rows.append((fn, ext_n, db_n, ext_n - db_n, loc))

    rows.sort(key=lambda r: -r[3])

    print(f"{'Function (last 60 chars)':<60}  ext  db  dropped  loc")
    print(f"{'-' * 60}  ---  --  -------  ---")
    for fn, ext_n, db_n, dropped, loc in rows[:args.top]:
        print(f"{fn[-60:]:<60}  {ext_n:>3}  {db_n:>2}  {dropped:>7}  {loc:>3}")

    total_extracted = sum(extractor_calls.values())
    total_db = sum(db_calls.values())
    drop_rate = (1 - total_db / total_extracted) if total_extracted else 0
    print("\n=== Summary ===")
    print(f"  total CBMCalls extracted: {total_extracted}")
    print(f"  total CALLS edges in DB:  {total_db}")
    print(f"  resolver drop rate:       {drop_rate:.2%}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
