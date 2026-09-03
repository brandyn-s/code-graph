"""
Compare the `pre_graph_grep` rate between two transcript corpora produced
by bench/transcripts.py — the A/B measurement for PR 3 (PreToolUse
orientation hook).

Pre-install baseline (from PR #51, captured 2026-04-22, 14-day window):
    76.7% of qualifying sessions ran Glob/Grep/Read before their first
    code-search/code-graph tool call.

Stop-ship (PR 3):
    Post-install rate must drop to <=37% (>=40 pp reduction) on a
    sample of >=30 sessions collected over a fresh 14-day window after
    the hook is in place.

Protocol
--------
1. Install the hook:  code-graph install
   (writes ~/.claude/hooks/codebase-memory-orientation.sh + registers
   a PreToolUse matcher `Glob|Grep` in ~/.claude/settings.json)
2. Restart Claude Code so the hook takes effect.
3. Use Claude Code normally for N days (N >= 14 recommended for
   comparable sample size to the baseline).
4. Capture the post-install corpus:
       python bench/transcripts.py --output bench/transcripts_post.jsonl
5. Compare:
       python bench/compare_pregrep.py --baseline bench/transcripts_pre.jsonl \
                                       --post     bench/transcripts_post.jsonl
   Or for one-shot against the 76.7% baseline without a pre-corpus file:
       python bench/compare_pregrep.py --post bench/transcripts_post.jsonl \
                                       --baseline-rate 0.767

Output
------
- `pre_graph_grep` rate in each corpus.
- Absolute delta and percentage-point delta.
- Sample-size check (warn if post corpus <30 qualifying sessions).
- Stop-ship verdict: PASS if post <= 37% and delta >= 40 pp; FAIL otherwise.
- Optional per-session-length breakdown to check whether the hook helps
  short vs long sessions differently.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path


def load_corpus(path: Path) -> list[dict]:
    records = []
    with path.open(encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                records.append(json.loads(line))
            except json.JSONDecodeError:
                continue
    return records


def pregrep_rate(records: list[dict]) -> tuple[int, int, float]:
    if not records:
        return 0, 0, 0.0
    n = len(records)
    hits = sum(1 for r in records if r.get("pre_graph_grep"))
    return hits, n, hits / n


def session_length_bucket(tool_count: int) -> str:
    if tool_count < 30:
        return "short (<30 tool calls)"
    if tool_count < 150:
        return "medium (30-149)"
    return "long (150+)"


def breakdown_by_length(records: list[dict]) -> dict[str, tuple[int, int, float]]:
    buckets: dict[str, list[dict]] = {
        "short (<30 tool calls)": [],
        "medium (30-149)": [],
        "long (150+)": [],
    }
    for r in records:
        buckets[session_length_bucket(int(r.get("tool_call_count", 0)))].append(r)
    return {name: pregrep_rate(recs) for name, recs in buckets.items()}


def main() -> int:
    ap = argparse.ArgumentParser(description="PR 3 hook A/B measurement")
    ap.add_argument("--baseline", type=Path,
                    help="baseline transcripts JSONL (from pre-install)")
    ap.add_argument("--baseline-rate", type=float,
                    help="if no --baseline file, compare against this rate "
                         "(e.g. 0.767 for the pre-install number)")
    ap.add_argument("--post", type=Path, required=True,
                    help="post-install transcripts JSONL (required)")
    ap.add_argument("--stop-ship-target", type=float, default=0.37,
                    help="post_rate <= this value to pass (default 0.37)")
    ap.add_argument("--stop-ship-delta-pp", type=float, default=40.0,
                    help="minimum percentage-point reduction required (default 40)")
    ap.add_argument("--min-sample", type=int, default=30,
                    help="minimum qualifying sessions in the post corpus for "
                         "a valid comparison (default 30)")
    args = ap.parse_args()

    if not args.baseline and args.baseline_rate is None:
        ap.error("must pass either --baseline <file> or --baseline-rate <float>")

    # Load post corpus
    post_records = load_corpus(args.post)
    p_hits, p_n, p_rate = pregrep_rate(post_records)

    # Load baseline
    if args.baseline:
        b_records = load_corpus(args.baseline)
        b_hits, b_n, b_rate = pregrep_rate(b_records)
        baseline_source = f"corpus {args.baseline} ({b_hits}/{b_n})"
    else:
        b_records = []
        b_n = 0
        b_hits = None
        b_rate = float(args.baseline_rate)
        baseline_source = f"flat rate {b_rate:.1%} (no pre-corpus)"

    delta_pp = (b_rate - p_rate) * 100.0

    # Emit a readable report
    print("PR 3 PreToolUse orientation hook — A/B measurement")
    print("=" * 58)
    print(f"baseline: {baseline_source}")
    if b_hits is not None:
        print(f"  pre_graph_grep rate: {b_rate:.1%}  ({b_hits}/{b_n})")
    else:
        print(f"  pre_graph_grep rate: {b_rate:.1%}")
    print()
    print(f"post-install: {args.post} ({p_hits}/{p_n})")
    print(f"  pre_graph_grep rate: {p_rate:.1%}  ({p_hits}/{p_n})")
    print()
    print(f"delta:            {delta_pp:+.1f} percentage points")
    print(f"target:           post rate <= {args.stop_ship_target:.1%} "
          f"AND delta >= {args.stop_ship_delta_pp:.1f} pp")
    print()

    # Sample-size guard: a post rate of 0% on 3 sessions is not evidence.
    if p_n < args.min_sample:
        print(f"WARNING: post corpus has only {p_n} qualifying sessions "
              f"(<{args.min_sample}). Collect more before relying on this "
              "measurement.")
        print()

    # Breakdown by session length — sometimes hooks help long sessions
    # more than short ones (more Glob/Grep opportunities to redirect).
    if post_records:
        print("per-session-length breakdown (post corpus):")
        for bucket, (h, n, r) in breakdown_by_length(post_records).items():
            if n == 0:
                continue
            print(f"  {bucket:<28} {r:>6.1%}  ({h}/{n})")
        print()

    # Verdict
    target_met = p_rate <= args.stop_ship_target
    delta_met = delta_pp >= args.stop_ship_delta_pp
    sample_ok = p_n >= args.min_sample

    if target_met and delta_met and sample_ok:
        print("VERDICT: PASS — hook meets the stop-ship bar.")
        return 0
    else:
        print("VERDICT: FAIL — hook does not meet the stop-ship bar.")
        if not target_met:
            print(f"  - post rate {p_rate:.1%} > target {args.stop_ship_target:.1%}")
        if not delta_met:
            print(f"  - delta {delta_pp:+.1f} pp < required {args.stop_ship_delta_pp:.1f} pp")
        if not sample_ok:
            print(f"  - post sample size {p_n} < {args.min_sample} (insufficient data)")
        return 1


if __name__ == "__main__":
    sys.exit(main())
