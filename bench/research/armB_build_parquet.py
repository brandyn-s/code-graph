#!/usr/bin/env python3
"""Build the arm-B parquet for the SweRank pre-filter pilot.

Takes the original Loc-Bench parquet and arm C's per-case JSON, and prepends
a retrieval-candidate block to problem_statement for every pinned instance:

    Candidate files (retrieval):
    - path/a.py
    - path/b.py
    ...

    <original problem_statement>

This is the plan's minimal-change injection: eval_locbench_batch consumes the
modified parquet via --parquet; every other knob (iter=2, MRR, prompts) stays
identical to arm A, so the only delta is the candidate block.

NOTE: the batch harness queries with the FIRST PARAGRAPH only (split on
"\\n\\n"), so the candidate block is built as a single newline-separated
paragraph and the original first paragraph is appended INSIDE the same
paragraph (separated by a single newline) — otherwise injection would
silently evict the issue text from the agent's query.
"""
from __future__ import annotations

import argparse
import json
from pathlib import Path

import pandas as pd


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--parquet", required=True, type=Path)
    ap.add_argument("--armc", required=True, type=Path, help="arm C per-case JSON")
    ap.add_argument("--out", required=True, type=Path)
    args = ap.parse_args()

    armc = json.loads(args.armc.read_text(encoding="utf-8"))
    candidates = {
        c["instance_id"]: c.get("top_files", [])
        for c in armc.get("cases", [])
        if c.get("indexed")
    }

    df = pd.read_parquet(args.parquet)
    injected = skipped = 0

    def inject(row: pd.Series) -> str:
        nonlocal injected, skipped
        stmt = row["problem_statement"]
        files = candidates.get(row["instance_id"])
        if not files:
            skipped += 1
            return stmt
        first, _, rest = stmt.partition("\n\n")
        block = "Candidate files (retrieval):\n" + "\n".join(f"- {f}" for f in files)
        injected += 1
        merged_first = block + "\n" + first.strip()
        return merged_first + ("\n\n" + rest if rest else "")

    df = df.copy()
    df["problem_statement"] = df.apply(inject, axis=1)
    df.to_parquet(args.out)
    print(f"wrote {args.out}: injected={injected} untouched={skipped + (len(df) - injected - skipped)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
