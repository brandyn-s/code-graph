"""F1 comparison for Nix pub/sub extraction on psm.

Queries code-graph's SQLite DB for (Service, Topic, PUBLISHES_TO/SUBSCRIBES_TO)
edges, compares against oracle_nix_pubsub.py's extraction, and reports
per-service + aggregate precision/recall/F1.

Usage:
    python compare_nix_pubsub.py <repo-root> <db-path>

The comparison excludes AMBIGUOUS-tier edges from the precision/recall
numerator (they're informational only) unless --include-ambiguous is set.

Why the ambiguous exclusion: conditional appends and resolved const vars
are inherently lower-confidence; holding code-graph to the same strict
F1 standard on them as on declarative literals would penalize it for
working-as-designed behavior. The ambiguous edges surface in a separate
diagnostic block.
"""

from __future__ import annotations

import argparse
import json
import sqlite3
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
from oracle_nix_pubsub import scan_repo  # noqa: E402


def query_graph(db_path: Path, project: str, include_ambiguous: bool = False):
    """Pull (service, topic, edge_type, confidence_tier, conditional) rows
    from the indexed DB for the given project."""
    con = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)
    con.row_factory = sqlite3.Row
    try:
        rows = con.execute(
            """
            SELECT
              src.name AS service,
              tgt.name AS topic,
              e.type   AS edge_type,
              json_extract(e.properties, '$.confidence_tier') AS tier,
              json_extract(e.properties, '$.conditional')     AS conditional,
              json_extract(e.properties, '$.source')          AS src_source
            FROM edges e
            JOIN nodes src ON e.source_id = src.id
            JOIN nodes tgt ON e.target_id = tgt.id
            WHERE e.project = ?
              AND src.label = 'Service'
              AND tgt.label = 'Topic'
              AND e.type IN ('PUBLISHES_TO','SUBSCRIBES_TO')
            """,
            (project,),
        ).fetchall()
    finally:
        con.close()

    pub_edges = {}  # service -> set of topics
    sub_edges = {}
    pub_ambig = {}  # ambiguous (conditional / AMBIGUOUS tier) bucket
    sub_ambig = {}

    for r in rows:
        # Only treat edges emitted by the Nix pass (source == "nix").
        # Other passes can write Topic edges too — keep them out of
        # the Nix-specific F1.
        if r["src_source"] != "nix":
            continue
        is_ambig = r["tier"] == "AMBIGUOUS" or r["conditional"] == 1
        tgt_map = pub_edges if r["edge_type"] == "PUBLISHES_TO" else sub_edges
        amb_map = pub_ambig if r["edge_type"] == "PUBLISHES_TO" else sub_ambig
        if not is_ambig or include_ambiguous:
            tgt_map.setdefault(r["service"], set()).add(r["topic"])
        else:
            amb_map.setdefault(r["service"], set()).add(r["topic"])
    return pub_edges, sub_edges, pub_ambig, sub_ambig


def _f1(tp: int, fp: int, fn: int) -> tuple[float, float, float]:
    p = tp / (tp + fp) if (tp + fp) else 0.0
    r = tp / (tp + fn) if (tp + fn) else 0.0
    f = 2 * p * r / (p + r) if (p + r) else 0.0
    return p, r, f


def compare(repo_root: Path, db_path: Path, project: str, include_ambiguous: bool):
    oracle_map = {}
    for o in scan_repo(repo_root):
        if o.service_name:
            svc_pub = set()
            if o.pub_topic:
                svc_pub.add(o.pub_topic)
            svc_pub |= o.imp_pub_topics  # imperative pubs count
            svc_sub = set(o.sub_topics) | set(o.imp_sub_topics)
            oracle_map.setdefault(o.service_name, {"pub": set(), "sub": set()})
            oracle_map[o.service_name]["pub"] |= svc_pub
            oracle_map[o.service_name]["sub"] |= svc_sub
        # cross-file additional subs
        for svc, topics in o.additional_subs.items():
            oracle_map.setdefault(svc, {"pub": set(), "sub": set()})
            oracle_map[svc]["sub"] |= topics

    pub_eg, sub_eg, pub_ambig, sub_ambig = query_graph(db_path, project, include_ambiguous)

    total_pub_tp = total_pub_fp = total_pub_fn = 0
    total_sub_tp = total_sub_fp = total_sub_fn = 0

    per_service: list[dict] = []

    all_services = set(oracle_map) | set(pub_eg) | set(sub_eg)
    for svc in sorted(all_services):
        oracle = oracle_map.get(svc, {"pub": set(), "sub": set()})
        got_pub = pub_eg.get(svc, set())
        got_sub = sub_eg.get(svc, set())
        pub_tp = len(oracle["pub"] & got_pub)
        pub_fp = len(got_pub - oracle["pub"])
        pub_fn = len(oracle["pub"] - got_pub)
        sub_tp = len(oracle["sub"] & got_sub)
        sub_fp = len(got_sub - oracle["sub"])
        sub_fn = len(oracle["sub"] - got_sub)

        total_pub_tp += pub_tp; total_pub_fp += pub_fp; total_pub_fn += pub_fn
        total_sub_tp += sub_tp; total_sub_fp += sub_fp; total_sub_fn += sub_fn

        per_service.append({
            "service": svc,
            "pub": {
                "oracle": sorted(oracle["pub"]),
                "got": sorted(got_pub),
                "tp": pub_tp, "fp": pub_fp, "fn": pub_fn,
                "missed": sorted(oracle["pub"] - got_pub),
                "extra": sorted(got_pub - oracle["pub"]),
            },
            "sub": {
                "oracle": sorted(oracle["sub"]),
                "got": sorted(got_sub),
                "tp": sub_tp, "fp": sub_fp, "fn": sub_fn,
                "missed": sorted(oracle["sub"] - got_sub),
                "extra": sorted(got_sub - oracle["sub"]),
            },
        })

    pub_p, pub_r, pub_f = _f1(total_pub_tp, total_pub_fp, total_pub_fn)
    sub_p, sub_r, sub_f = _f1(total_sub_tp, total_sub_fp, total_sub_fn)

    return {
        "include_ambiguous": include_ambiguous,
        "aggregate": {
            "publish": {
                "tp": total_pub_tp, "fp": total_pub_fp, "fn": total_pub_fn,
                "precision": round(pub_p, 4),
                "recall":    round(pub_r, 4),
                "f1":        round(pub_f, 4),
            },
            "subscribe": {
                "tp": total_sub_tp, "fp": total_sub_fp, "fn": total_sub_fn,
                "precision": round(sub_p, 4),
                "recall":    round(sub_r, 4),
                "f1":        round(sub_f, 4),
            },
        },
        "ambiguous_buckets": {
            "pub_ambiguous_edges": sum(len(v) for v in pub_ambig.values()),
            "sub_ambiguous_edges": sum(len(v) for v in sub_ambig.values()),
            "pub_ambiguous_by_service": {k: sorted(v) for k, v in pub_ambig.items()},
            "sub_ambiguous_by_service": {k: sorted(v) for k, v in sub_ambig.items()},
        },
        "per_service": per_service,
    }


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("repo_root", type=Path)
    ap.add_argument("db_path", type=Path)
    ap.add_argument(
        "--project",
        default=None,
        help="project name in the DB (defaults to sanitized repo path)",
    )
    ap.add_argument("--include-ambiguous", action="store_true",
                    help="include AMBIGUOUS-tier edges in the F1 numerator")
    ap.add_argument("--json", action="store_true", help="emit raw JSON")
    args = ap.parse_args(argv[1:])

    if args.project is None:
        # Same sanitization code-graph uses: replace [\\/:] with '-' and strip leading ':-'.
        sanitized = "c" + str(args.repo_root).replace("\\", "-").replace("/", "-").replace(":", "")
        args.project = sanitized

    result = compare(args.repo_root, args.db_path, args.project, args.include_ambiguous)

    if args.json:
        json.dump(result, sys.stdout, indent=2)
        print()
        return 0

    agg = result["aggregate"]
    print(f"Nix pub/sub F1 (AMBIGUOUS {'INCLUDED' if args.include_ambiguous else 'EXCLUDED'}):")
    print(f"  PUBLISH    tp={agg['publish']['tp']:4d}  fp={agg['publish']['fp']:4d}  fn={agg['publish']['fn']:4d}  "
          f"P={agg['publish']['precision']:.3f}  R={agg['publish']['recall']:.3f}  F1={agg['publish']['f1']:.3f}")
    print(f"  SUBSCRIBE  tp={agg['subscribe']['tp']:4d}  fp={agg['subscribe']['fp']:4d}  fn={agg['subscribe']['fn']:4d}  "
          f"P={agg['subscribe']['precision']:.3f}  R={agg['subscribe']['recall']:.3f}  F1={agg['subscribe']['f1']:.3f}")
    ambig = result["ambiguous_buckets"]
    print(f"  AMBIGUOUS (informational): pub={ambig['pub_ambiguous_edges']}  sub={ambig['sub_ambiguous_edges']}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
