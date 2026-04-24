"""Verify code-graph's Nix pub/sub output against hand-curated ground truth.

Reads `nix_pubsub_hand_oracle.json`, queries code-graph's indexed DB for
the listed services, and reports per-service pass/fail along with
missed topics and unexpected (extra) topics. This is the honest F1
check — the oracle and code-graph share regex logic, so `compare_nix_pubsub.py`
produces ~1.0 F1 on any consistent extractor (instrument-validation).
This script holds code-graph to manually-read module contents.

Usage:
    python check_nix_hand_oracle.py <db-path> --project <project-name>
"""

from __future__ import annotations

import argparse
import json
import sqlite3
import sys
from pathlib import Path


def load_hand_oracle() -> list[dict]:
    path = Path(__file__).parent / "nix_pubsub_hand_oracle.json"
    return json.loads(path.read_text(encoding="utf-8"))["services"]


def query_service_topics(
    db_path: Path, project: str, service: str, strict: bool = True
) -> tuple[set[str], set[str]]:
    """Returns (pub_topics, sub_topics) that code-graph has for the given Service.
    When strict=True, excludes AMBIGUOUS-tier edges (conditional appends, resolved
    const vars) so the comparison matches the hand oracle's "always subscribes"
    semantics. Set strict=False to include conditional edges."""
    tier_clause = "AND json_extract(e.properties, '$.confidence_tier') != 'AMBIGUOUS'" if strict else ""
    con = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)
    con.row_factory = sqlite3.Row
    try:
        rows = con.execute(
            f"""
            SELECT e.type AS edge_type, tgt.name AS topic
            FROM edges e
            JOIN nodes src ON e.source_id = src.id
            JOIN nodes tgt ON e.target_id = tgt.id
            WHERE e.project = ?
              AND src.label = 'Service'
              AND src.name  = ?
              AND tgt.label = 'Topic'
              AND e.type IN ('PUBLISHES_TO', 'SUBSCRIBES_TO')
              {tier_clause}
            """,
            (project, service),
        ).fetchall()
    finally:
        con.close()
    pubs, subs = set(), set()
    for r in rows:
        (pubs if r["edge_type"] == "PUBLISHES_TO" else subs).add(r["topic"])
    return pubs, subs


def check(db_path: Path, project: str) -> tuple[int, int]:
    cases = load_hand_oracle()
    ok_count = 0
    fail_count = 0
    total_tp = total_fp = total_fn = 0

    for case in cases:
        svc = case["service"]
        expected_pub = set(case.get("pub_topics", []))
        expected_sub = set(case.get("sub_topics", []))
        must_include_subs = set(case.get("must_include_in_sub_topics", []))
        got_pub, got_sub = query_service_topics(db_path, project, svc)

        problems: list[str] = []

        # Publisher comparison
        if expected_pub:
            pub_missed = expected_pub - got_pub
            pub_extra = got_pub - expected_pub
            if pub_missed:
                problems.append(f"pub missing {sorted(pub_missed)}")
            # Extras are OK for pub (service may legitimately publish to more)
            # but log them for visibility.
            if pub_extra:
                problems.append(f"pub extras (informational): {sorted(pub_extra)}")
            total_tp += len(expected_pub & got_pub)
            total_fp += len(pub_extra)
            total_fn += len(pub_missed)

        # Subscriber comparison
        if expected_sub:
            sub_missed = expected_sub - got_sub
            sub_extra = got_sub - expected_sub
            if sub_missed:
                problems.append(f"sub missing {sorted(sub_missed)}")
            if sub_extra:
                problems.append(f"sub extras (informational): {sorted(sub_extra)}")
            total_tp += len(expected_sub & got_sub)
            total_fp += len(sub_extra)
            total_fn += len(sub_missed)

        if must_include_subs:
            missing_must = must_include_subs - got_sub
            if missing_must:
                problems.append(f"must-include subs missing {sorted(missing_must)}")

        # "Missing" (fn) problems are fails; "extras" alone are warnings.
        has_fn = any("missing" in p for p in problems)
        status = "FAIL" if has_fn else "OK"
        if has_fn:
            fail_count += 1
        else:
            ok_count += 1

        print(f"  [{status}] {svc}")
        for p in problems:
            prefix = "    !  " if "missing" in p else "    ~  "
            print(f"{prefix}{p}")

    precision = total_tp / (total_tp + total_fp) if (total_tp + total_fp) else 0.0
    recall    = total_tp / (total_tp + total_fn) if (total_tp + total_fn) else 0.0
    f1        = 2 * precision * recall / (precision + recall) if (precision + recall) else 0.0

    print()
    print("Hand-oracle aggregate:")
    print(f"  tp={total_tp}  fp={total_fp}  fn={total_fn}")
    print(f"  precision={precision:.3f}  recall={recall:.3f}  F1={f1:.3f}")
    print(f"  {ok_count}/{ok_count + fail_count} services passed")

    return ok_count, fail_count


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("db_path", type=Path)
    ap.add_argument("--project", required=True)
    args = ap.parse_args(argv[1:])
    print(f"Hand-oracle check: project={args.project}")
    _, fail = check(args.db_path, args.project)
    return 1 if fail else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
