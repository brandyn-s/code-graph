"""Phase C1b: Loc-Bench failure-audit harness.

Loads the latest Loc-Bench eval results, identifies misses (cases where
predicted didn't match expected_paths), and produces a per-case
classification scaffold for human review.

The harness handles the mechanical part — loading + diffing + producing
a YAML scaffold. The classification step (assigning each case to bucket
a/b/c) is human work. The harness's job is to make the human work
maximally efficient: present each miss with enough context to classify
in <30 seconds.

Bucket definitions (from Plan 1 Phase C1):
  (a) wrong-function-despite-correct-graph — predicted file is right,
      function within is wrong. Indicator that the agent picked the
      wrong sibling, not that the graph is missing data.
  (b) missing-dynamic-dispatch-edge — predicted is wrong AND the
      relevant correct entity is reachable only via an unresolved
      dispatch edge in the graph. The graph IS the bottleneck.
  (c) scorer-artifact — case where the iter=1 vs iter=2 difference
      explains the miss (canonical-class-vs-semantically-near, or
      class-name-format mismatch).
  (d) other — flag for analysis but doesn't fit a/b/c.

Decision rule:
  >60% in (a) → Func Acc@10 work is #1 priority
  >60% in (b) → cross-language indirect-call coverage is #1
  >60% in (c) → scorer/protocol ablation is #1

Usage:
    python bench/research/locbench_failure_audit.py
        Loads latest results, produces locbench_failure_audit_TODO.yaml
        with cases ready for classification.

    python bench/research/locbench_failure_audit.py --analyze
        Reads locbench_failure_audit_TODO.yaml back, computes the
        bucket distribution, prints the decision-rule outcome.

    python bench/research/locbench_failure_audit.py --baseline 2026-05-04-loc-bench-n200-iter2
        Use a specific Loc-Bench run as the source.
"""
from __future__ import annotations

import argparse
import json
import pathlib
import sys
from collections import Counter
from typing import Any

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
BASELINES_DIR = REPO_ROOT / "bench" / "accuracy" / "baselines"
OUTPUT_DIR = REPO_ROOT / "bench" / "research"

DEFAULT_BASELINE = "2026-05-04-loc-bench-n200-iter2"


def latest_baseline() -> pathlib.Path | None:
    """Find the most recent loc-bench JSON results file."""
    candidates = sorted(BASELINES_DIR.glob("*loc-bench*.json"))
    if not candidates:
        return None
    return candidates[-1]


def load_results(path: pathlib.Path) -> list[dict[str, Any]]:
    """Load Loc-Bench results. The shape varies by run; try common
    structures."""
    raw = json.loads(path.read_text(encoding="utf-8"))
    if isinstance(raw, list):
        return raw
    if isinstance(raw, dict):
        # Common shapes: {"cases": [...]} or {"results": [...]}
        for key in ("cases", "results", "data", "items"):
            if key in raw and isinstance(raw[key], list):
                return raw[key]
    return []


def is_miss(case: dict[str, Any]) -> bool:
    """Heuristic: a case is a miss if its file-level / class-level /
    func-level scores are all not-yet-1.0. Different runs use different
    score keys; this is a best-effort heuristic."""
    for key in ("file_match", "file_acc", "file_correct", "predicted_correct"):
        if key in case:
            v = case[key]
            if isinstance(v, bool):
                return not v
            if isinstance(v, (int, float)):
                return v < 1.0
    # If no score key present, can't tell — exclude from miss set.
    return False


def case_summary(case: dict[str, Any]) -> dict[str, Any]:
    """Extract the human-relevant fields for classification."""
    issue = case.get("issue", case.get("query", case.get("problem_statement", "")))
    expected = case.get("expected_paths", case.get("expected", case.get("ground_truth", [])))
    predicted = case.get("predicted_paths", case.get("predicted", case.get("output", [])))
    return {
        "id": case.get("id", case.get("case_id", "?")),
        "issue_excerpt": str(issue)[:200] + ("..." if len(str(issue)) > 200 else ""),
        "expected": expected if isinstance(expected, list) else [expected],
        "predicted": predicted if isinstance(predicted, list) else [predicted],
        "bucket": "TODO",  # human fills in: a, b, c, d
        "rationale": "TODO",
    }


def emit_todo_yaml(misses: list[dict[str, Any]], output_path: pathlib.Path, sample_size: int) -> None:
    """Write a human-classifiable YAML scaffold."""
    sampled = misses[:sample_size]
    lines = [
        "# Loc-Bench failure-audit classification scaffold.",
        "# Phase C1b of Plan 1 (post-roundtable recommendations).",
        "#",
        "# For each case, set `bucket` to one of: a, b, c, d.",
        "#   a — wrong-function-despite-correct-graph",
        "#   b — missing-dynamic-dispatch-edge",
        "#   c — scorer-artifact (iter=1 vs iter=2, canonical-class issue)",
        "#   d — other / unclear",
        "#",
        f"# Sample size: {len(sampled)} of {len(misses)} total misses",
        "",
        "cases:",
    ]
    for c in sampled:
        lines.append(f"  - id: {json.dumps(c['id'])}")
        lines.append(f"    issue_excerpt: {json.dumps(c['issue_excerpt'])}")
        lines.append(f"    expected: {json.dumps(c['expected'])}")
        lines.append(f"    predicted: {json.dumps(c['predicted'])}")
        lines.append(f"    bucket: {c['bucket']}")
        lines.append(f"    rationale: {json.dumps(c['rationale'])}")
        lines.append("")
    output_path.write_text("\n".join(lines), encoding="utf-8")


def analyze_classified(yaml_path: pathlib.Path) -> int:
    """Read back the classified YAML and produce the decision-rule outcome."""
    if not yaml_path.exists():
        print(f"No classification file at {yaml_path}", file=sys.stderr)
        print("Run the harness first to generate the TODO scaffold.", file=sys.stderr)
        return 1

    text = yaml_path.read_text(encoding="utf-8")
    # Lightweight YAML parse — we only care about the bucket field per case.
    buckets: Counter = Counter()
    classified = 0
    for line in text.splitlines():
        line = line.strip()
        if line.startswith("bucket:"):
            val = line.split(":", 1)[1].strip()
            if val in ("a", "b", "c", "d"):
                buckets[val] += 1
                classified += 1
            elif val != "TODO":
                buckets["unknown"] += 1

    if classified == 0:
        print(f"No cases classified yet in {yaml_path}", file=sys.stderr)
        print("Edit the file and set `bucket: a/b/c/d` per case, then re-run.", file=sys.stderr)
        return 1

    print(f"=== Phase C1b: Loc-Bench failure-audit results ===\n")
    print(f"Classified cases: {classified}\n")

    bucket_descriptions = {
        "a": "wrong-function-despite-correct-graph",
        "b": "missing-dynamic-dispatch-edge",
        "c": "scorer-artifact",
        "d": "other / unclear",
    }
    for bucket in ["a", "b", "c", "d"]:
        count = buckets.get(bucket, 0)
        pct = 100.0 * count / classified
        bar = "#" * int(40 * count / max(1, max(buckets.values())))
        desc = bucket_descriptions[bucket]
        print(f"  ({bucket}) {desc:50s} {count:>4} ({pct:>5.1f}%) {bar}")

    # Decision rule
    print("\n=== Decision rule outcome ===")
    threshold = 0.60
    for bucket, desc in bucket_descriptions.items():
        if buckets.get(bucket, 0) / classified >= threshold:
            actions = {
                "a": "Func Acc@10 work is #1 priority — investigate why the agent picks wrong functions despite correct file-level localization.",
                "b": "Cross-language indirect-call coverage is #1 — INDIRECT_CALLS v0.4/v0.5 + Go interface dispatch + Rust trait-object work.",
                "c": "Scorer/protocol ablation is #1 — investigate iter=1 vs iter=2 + canonical-class-name handling.",
                "d": "Mixed signal — extend audit to a larger sample before committing to direction.",
            }
            print(f"  Bucket ({bucket}) at {100*buckets.get(bucket,0)/classified:.1f}% (>= {100*threshold:.0f}% threshold)")
            print(f"  → {actions[bucket]}")
            return 0

    print("  No bucket dominates (none >=60%). Findings are mixed.")
    print("  Recommendation: extend audit sample to 100+ cases AND surface")
    print("  per-language breakdown to see if a single language drives the")
    print("  miss profile.")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=(__doc__ or "").split("\n\n")[0])
    ap.add_argument("--analyze", action="store_true",
                    help="Read back the classified YAML and produce decision-rule outcome")
    ap.add_argument("--baseline", default=None,
                    help="Specific baseline filename (without .json suffix)")
    ap.add_argument("--sample-size", type=int, default=50,
                    help="Number of misses to include in the TODO scaffold")
    args = ap.parse_args()

    yaml_path = OUTPUT_DIR / "locbench_failure_audit_TODO.yaml"

    if args.analyze:
        return analyze_classified(yaml_path)

    if args.baseline:
        results_path = BASELINES_DIR / f"{args.baseline}.json"
    else:
        results_path = latest_baseline()
        if results_path is None:
            print(f"No Loc-Bench JSON found in {BASELINES_DIR}", file=sys.stderr)
            return 1

    if not results_path.exists():
        print(f"Results file not found: {results_path}", file=sys.stderr)
        return 1

    print(f"Loading results from {results_path.name}...", file=sys.stderr)
    cases = load_results(results_path)
    print(f"  {len(cases)} total cases", file=sys.stderr)

    misses = [case_summary(c) for c in cases if is_miss(c)]
    print(f"  {len(misses)} misses identified", file=sys.stderr)

    if not misses:
        print("No misses found in the results — either the run was perfect OR", file=sys.stderr)
        print("the heuristic miss-detection didn't find the right score keys", file=sys.stderr)
        print("in this run's JSON shape. Inspect the JSON and update is_miss()", file=sys.stderr)
        print("if needed.", file=sys.stderr)
        return 1

    emit_todo_yaml(misses, yaml_path, args.sample_size)
    print(f"\nWrote {yaml_path}", file=sys.stderr)
    print(f"Edit the file: set `bucket: a/b/c/d` per case (~{args.sample_size} cases, ~30s each).")
    print(f"Then run: python {pathlib.Path(__file__).name} --analyze")

    return 0


if __name__ == "__main__":
    sys.exit(main())
