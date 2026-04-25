"""Recall ceiling probe: for each Loc-Bench instance where the agent
missed at file level, query the indexed graph directly to determine
whether the ground-truth entity exists in the graph at all.

Outcomes per miss:
  - PRESENT: graph has the entity → agent is the bottleneck (the agent
    either didn't surface it or surfaced it but our scorer missed)
  - ABSENT: graph doesn't have the entity → the indexer/extractor is the
    bottleneck (no amount of agent-loop tweaking will help; we need to
    fix the language-specific extractor)

This is the gating check before investing in read_file / prompt tweaks.
If 2+ misses are extractor issues, the LocAgent gap lives in extraction,
not reasoning.
"""
from __future__ import annotations

import json
import sqlite3
import sys
from pathlib import Path

CACHE_DIR = Path.home() / ".cache" / "codebase-memory-mcp"

# The 3 instances where hybrid-agent scored file=N in the n=16 run
# (locbench-scored-cosine0.md). vllm had a separate SHM-corruption issue
# but is included for completeness — its DB needs re-index before any
# meaningful recall claim.
MISSES = [
    {
        "instance_id": "internetarchive__openlibrary-3196",
        "db": "c-tmp-locbench-batch-internetarchive__openlibrary-3196.db",
        "ground_truth": [
            "openlibrary/plugins/upstream/utils.py:setup",
        ],
    },
    {
        "instance_id": "pandas-dev__pandas-59900",
        "db": "c-tmp-locbench-batch-pandas-dev__pandas-59900.db",
        "ground_truth": [
            "pandas/core/strings/accessor.py:StringMethods._validate",
        ],
    },
    {
        "instance_id": "vllm-project__vllm-11138",
        "db": "c-tmp-locbench-batch-vllm-project__vllm-11138.db",
        "ground_truth": [
            "vllm/executor/ray_utils.py:initialize_ray_cluster",
        ],
    },
]


def probe_db(db_path: Path, ground_truth: list[str]) -> list[dict]:
    """For each ground-truth item, search the nodes table for matches."""
    if not db_path.exists():
        return [{"gt": gt, "status": "DB_MISSING", "details": str(db_path)} for gt in ground_truth]

    results: list[dict] = []
    try:
        conn = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)
        conn.row_factory = sqlite3.Row
    except sqlite3.OperationalError as e:
        return [{"gt": gt, "status": "DB_OPEN_ERROR", "details": str(e)} for gt in ground_truth]

    cur = conn.cursor()

    # Total node count for context
    try:
        total = cur.execute("SELECT COUNT(*) FROM nodes").fetchone()[0]
    except sqlite3.OperationalError as e:
        conn.close()
        return [{"gt": gt, "status": "QUERY_ERROR", "details": str(e)} for gt in ground_truth]

    for gt in ground_truth:
        if ":" not in gt:
            results.append({"gt": gt, "status": "MALFORMED_GT"})
            continue
        file_part, func_part = gt.split(":", 1)
        comps = func_part.split(".")
        if len(comps) >= 2:
            cls = comps[0]
            fn = comps[-1]
        else:
            cls = None
            fn = func_part

        # Search 1: any node whose file_path == ground_truth file
        file_rows = cur.execute(
            "SELECT label, name, qualified_name FROM nodes "
            "WHERE file_path = ? LIMIT 50",
            (file_part,),
        ).fetchall()

        # Search 2: any node whose name == fn (case-insensitive)
        name_rows = cur.execute(
            "SELECT label, name, qualified_name, file_path FROM nodes "
            "WHERE LOWER(name) = LOWER(?) LIMIT 50",
            (fn,),
        ).fetchall()

        # Search 3: any node whose qualified_name ends with .Class.fn (case-insensitive)
        qn_pattern: str
        if cls:
            qn_pattern = f"%.{cls.lower()}.{fn.lower()}"
        else:
            qn_pattern = f"%.{fn.lower()}"
        qn_rows = cur.execute(
            "SELECT label, name, qualified_name, file_path FROM nodes "
            "WHERE LOWER(qualified_name) LIKE ? LIMIT 50",
            (qn_pattern,),
        ).fetchall()

        # The strict match: same file_path AND qn ends with the right tail
        strict_match = [
            dict(r) for r in qn_rows
            if r["file_path"] == file_part
        ]

        results.append({
            "gt": gt,
            "file_part": file_part,
            "func_part": func_part,
            "class": cls,
            "fn": fn,
            "file_nodes_count": len(file_rows),
            "file_nodes_sample": [dict(r) for r in file_rows[:5]],
            "name_match_count": len(name_rows),
            "name_match_sample": [dict(r) for r in name_rows[:5]],
            "qn_pattern": qn_pattern,
            "qn_match_count": len(qn_rows),
            "qn_match_sample": [dict(r) for r in qn_rows[:5]],
            "strict_match_count": len(strict_match),
            "strict_match_sample": strict_match[:3],
            "status": "PRESENT" if strict_match else (
                "FILE_PRESENT_BUT_NO_FN" if file_rows else "FILE_ABSENT"
            ),
            "graph_size": total,
        })

    conn.close()
    return results


def classify(probe_results: list[dict]) -> str:
    """Reduce per-gt results to a single verdict for the instance."""
    statuses = {r.get("status") for r in probe_results}
    if "DB_MISSING" in statuses or "DB_OPEN_ERROR" in statuses or "QUERY_ERROR" in statuses:
        return "INFRASTRUCTURE"
    if any(r["status"] == "PRESENT" for r in probe_results):
        return "AGENT_BOTTLENECK"
    if all(r["status"] == "FILE_ABSENT" for r in probe_results):
        return "EXTRACTOR_BOTTLENECK"
    return "PARTIAL_EXTRACTION"


def main() -> int:
    summary: list[dict] = []
    for miss in MISSES:
        db = CACHE_DIR / miss["db"]
        print(f"\n=== {miss['instance_id']} ===")
        print(f"  db: {db.name} (exists={db.exists()})")
        results = probe_db(db, miss["ground_truth"])
        verdict = classify(results)
        print(f"  verdict: {verdict}")
        for r in results:
            if r.get("status") == "DB_MISSING":
                print(f"    DB missing: {r['details']}")
                continue
            if r.get("status") == "DB_OPEN_ERROR":
                print(f"    DB open error: {r['details']}")
                continue
            if r.get("status") == "QUERY_ERROR":
                print(f"    Query error: {r['details']}")
                continue
            print(f"    GT: {r['gt']}")
            print(f"      strict_match (file+qn): {r['strict_match_count']}")
            for sm in r.get("strict_match_sample", []):
                print(f"        - {sm['label']} | {sm['qualified_name']}")
            print(f"      file '{r['file_part']}' has {r['file_nodes_count']} nodes")
            for fn_node in r.get("file_nodes_sample", [])[:3]:
                print(f"        - {fn_node['label']}: {fn_node['name']}")
            print(f"      name '{r['fn']}' appears in {r['name_match_count']} nodes (any file)")
            print(f"      qn pattern '{r['qn_pattern']}' matches {r['qn_match_count']} nodes")
            print(f"      total graph size: {r['graph_size']} nodes")
        summary.append({
            "instance": miss["instance_id"],
            "verdict": verdict,
            "results": results,
        })

    print("\n=== SUMMARY ===")
    for s in summary:
        print(f"  {s['instance']}: {s['verdict']}")

    out = Path(__file__).parent / "recall-ceiling-2026-04-25.json"
    out.write_text(json.dumps(summary, indent=2, default=str), encoding="utf-8")
    print(f"\nFull data written to: {out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
