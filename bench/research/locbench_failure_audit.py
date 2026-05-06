"""Plan 4 T1: Loc-Bench failure-audit harness with 7-bucket taxonomy.

Roundtable-converged successor to the Plan 1 C1 4-bucket harness. Loads
Loc-Bench eval results (with per-iteration data when available from the
Iterations field added to locagent.Result in this PR), identifies
misses, auto-proposes a bucket via heuristics, and emits a per-case
YAML scaffold for human confirmation.

7-bucket taxonomy (META_SYNTHESIS.md F1, 2026-05-06 roundtable):

  indirect_call_required: predicted wrong AND the correct entity is
    reachable only via dynamic dispatch (closure, fn pointer, trait
    object, interface method, **kwargs). code-graph's CALLS extractor
    does not emit edges for these. Graph IS the bottleneck.

  import_resolution_miss: agent never found the correct file because
    IMPORTS resolution missed the cross-module link. Common in nested
    src/ Python and Rust use-crate paths.

  scope_collision: agent picked wrong entity because two entities
    share a short name (e.g. time.Now vs store.Now). Suffix-match
    collision in resolveViaNameLookup. Graph would resolve with
    import context.

  embedding_recall_miss: hybrid seeds didn't surface the correct
    file. Voyage cosine too low; file content didn't match issue
    vocabulary.

  agent_loop_failure: agent terminated on max_turns or no_finalize.
    LLM-side failure, not graph-side.

  oracle_gap: case appears as a miss but the ground-truth path is
    incorrect or missing in Loc-Bench. System was right; oracle wrong.

  node_absent: correct entity isn't in the indexed graph at all
    (generated file, vendored dep, indexer-skipped file).

Roundtable decision rule (>=60% threshold):
  indirect_call_required → INDIRECT_CALLS v0.4/v0.5 + cross-language
  import_resolution_miss → resolver fix
  scope_collision → Go oracle gap fix + suffix-match import-context gating
  embedding_recall_miss → Voyage model upgrade or hybrid retune
  agent_loop_failure → prompt revision OR Sonnet upgrade
  oracle_gap → Loc-Bench fixture update upstream
  node_absent → indexer file-coverage audit

Auto-proposal heuristics (see propose_bucket):
  - StopReason in {max_turns, no_finalize} → agent_loop_failure
  - expected file in predicted but expected function not → scope_collision
  - expected file NOT in any predicted, no iteration found it →
    import_resolution_miss / embedding_recall_miss / node_absent
  - per-iteration data present and Iterations[1] rescued the expected
    entity → emit "rescued_by_iter_N: True" tag (bucket reflects the
    underlying class, not the rescue)

Usage:
    python bench/research/locbench_failure_audit.py
        Loads latest results, emits locbench_failure_audit_TODO.yaml
        with auto-proposed buckets for human confirmation.

    python bench/research/locbench_failure_audit.py --analyze
        Reads back the confirmed YAML, computes bucket distribution,
        prints decision-rule outcome.

    python bench/research/locbench_failure_audit.py --baseline NAME
        Use a specific baseline filename (without .json suffix) instead
        of the latest.
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

# 7-bucket taxonomy from the 2026-05-06 roundtable. Keep the names
# stable: changing them invalidates accumulated audit history.
BUCKETS = [
    "indirect_call_required",
    "import_resolution_miss",
    "scope_collision",
    "embedding_recall_miss",
    "agent_loop_failure",
    "oracle_gap",
    "node_absent",
]

BUCKET_DESCRIPTIONS = {
    "indirect_call_required":
        "Correct entity reachable only via dynamic dispatch (closure, fn pointer, "
        "trait object, interface method, **kwargs); CALLS extractor doesn't emit.",
    "import_resolution_miss":
        "IMPORTS resolution missed the cross-module link.",
    "scope_collision":
        "Two entities share a short name; suffix-match resolved to wrong one.",
    "embedding_recall_miss":
        "Voyage cosine too low; correct file's content didn't match issue vocabulary.",
    "agent_loop_failure":
        "LLM agent terminated on max_turns or no_finalize without confident answer.",
    "oracle_gap":
        "Loc-Bench ground-truth is incorrect or incomplete; system was right.",
    "node_absent":
        "Correct entity isn't in the indexed graph (generated file, vendored, skipped).",
}

# Decision-rule actions per dominant bucket. Roundtable convergence,
# 2026-05-06 META_SYNTHESIS F1.
BUCKET_ACTIONS = {
    "indirect_call_required":
        "INDIRECT_CALLS v0.4/v0.5 (fn-pointer-as-arg, **kwargs) + cross-language "
        "coverage (Go interface dispatch, Rust trait-object dispatch).",
    "import_resolution_miss":
        "Resolver fix: extend cross-module import resolution. Python nested-src/ "
        "layouts and Rust `use crate::...` paths are the common gap surfaces.",
    "scope_collision":
        "Go oracle gap fix (extend go-ast oracle for CGO + pointer/value "
        "receivers) + suffix-match import-context gating in resolveViaNameLookup.",
    "embedding_recall_miss":
        "Voyage embedding model upgrade or hybrid weight retune. Increase "
        "embedding seed top-K, or push Voyage cosine threshold lower.",
    "agent_loop_failure":
        "Prompt revision (LOCAGENT_PROMPT_VARIANT) or model upgrade (Sonnet/3.5). "
        "Investigate per-language max-turn distribution.",
    "oracle_gap":
        "Loc-Bench fixture update upstream. Curate a per-fixture allowlist of "
        "known-incorrect ground truths to subtract from accuracy denominator.",
    "node_absent":
        "Indexer file-coverage audit. Compare on-disk file inventory vs indexed "
        "node count; identify and patch the skip rules causing the gap.",
}


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


def expected_files(case: dict[str, Any]) -> list[str]:
    """Extract expected file paths from the case in a tolerant way."""
    for key in ("expected_paths", "expected", "ground_truth", "edit_functions"):
        v = case.get(key)
        if isinstance(v, list):
            return [str(x) for x in v]
        if isinstance(v, str):
            return [v]
    return []


def predicted_files(case: dict[str, Any]) -> list[str]:
    """Extract predicted file paths from the case in a tolerant way."""
    for key in ("predicted_paths", "predicted", "output"):
        v = case.get(key)
        if isinstance(v, list):
            # If list of dicts (entities), pull file_path
            if v and isinstance(v[0], dict):
                return [str(e.get("file_path", "")) for e in v if e.get("file_path")]
            return [str(x) for x in v]
        if isinstance(v, str):
            return [v]
    # Fall back to the locagent.Result-shaped agent.entities array.
    # Plan 5 Phase A: also look inside agent_envelope.code_localize_agent
    # since that's the shape eval_locbench_batch.py writes via --per-case-json.
    agent = (
        case.get("code_localize_agent")
        or case.get("agent")
        or (case.get("agent_envelope") or {}).get("code_localize_agent")
        or (case.get("agent_envelope") or {}).get("agent")
    )
    if isinstance(agent, dict):
        ents = agent.get("entities", [])
        if isinstance(ents, list):
            return [str(e.get("file_path", "")) for e in ents if isinstance(e, dict) and e.get("file_path")]
    return []


def iter_entity_lists(case: dict[str, Any]) -> list[list[dict[str, Any]]]:
    """Extract per-iteration entity lists, when surfaced. Matches the
    Iterations field added to locagent.Result in this PR."""
    agent = (
        case.get("code_localize_agent")
        or case.get("agent")
        or (case.get("agent_envelope") or {}).get("code_localize_agent")
        or (case.get("agent_envelope") or {}).get("agent")
        or case
    )
    iters = agent.get("iterations") if isinstance(agent, dict) else None
    if not isinstance(iters, list):
        return []
    out: list[list[dict[str, Any]]] = []
    for iter_list in iters:
        if isinstance(iter_list, list):
            out.append([e for e in iter_list if isinstance(e, dict)])
    return out


def stop_reason(case: dict[str, Any]) -> str:
    """Extract the agent's stop_reason."""
    agent = (
        case.get("code_localize_agent")
        or case.get("agent")
        or (case.get("agent_envelope") or {}).get("code_localize_agent")
        or (case.get("agent_envelope") or {}).get("agent")
        or case
    )
    if isinstance(agent, dict):
        sr = agent.get("stop_reason")
        if isinstance(sr, str):
            return sr
    return ""


def rescue_check(expected: list[str], iters: list[list[dict[str, Any]]]) -> tuple[bool, int]:
    """Did a later iteration surface an expected file that an earlier
    one missed? Returns (rescued, rescuer_iter_index_0_based)."""
    if len(iters) < 2:
        return False, -1
    seen_in_earlier: set[str] = set()
    for fp in expected:
        for idx, iter_list in enumerate(iters):
            iter_files = {str(e.get("file_path", "")) for e in iter_list}
            present = any(fp in f or f in fp for f in iter_files if f)
            if present:
                if seen_in_earlier and idx > 0:
                    return True, idx
                if idx == 0:
                    seen_in_earlier.add(fp)
                else:
                    return True, idx
    return False, -1


def propose_bucket(case: dict[str, Any]) -> tuple[str, str]:
    """Auto-propose a bucket for a case based on heuristics. Returns
    (bucket_name, rationale_string). Bucket name is one of the 7
    canonical names; if the heuristics don't fire confidently, returns
    ("TODO", reason).
    """
    sr = stop_reason(case)
    if sr in {"max_turns", "no_finalize", "error", "partial_consistency"}:
        return "agent_loop_failure", f"stop_reason={sr!r}"

    expected = expected_files(case)
    predicted = predicted_files(case)
    if not expected or not predicted:
        return "TODO", "missing expected/predicted; manually classify"

    # Normalize: strip path prefixes for cross-platform compare.
    def _norm(p: str) -> str:
        return p.replace("\\", "/").lstrip("./").lower()

    exp_n = [_norm(e) for e in expected]
    pred_n = [_norm(p) for p in predicted]

    # Heuristic 1: expected file IS in predicted top-K → file-correct,
    # function-wrong → likely scope_collision (or oracle).
    file_hit = any(any(en in pn or pn in en for pn in pred_n) for en in exp_n)
    if file_hit:
        # Could be scope_collision (sibling miss) or oracle_gap (the
        # function we predicted is the right one but Loc-Bench's
        # ground-truth function is mis-labeled). Default to
        # scope_collision; human confirms.
        return "scope_collision", "expected_file_in_predicted; likely sibling miss"

    # Heuristic 2: expected file NOT in any predicted across iters.
    iters = iter_entity_lists(case)
    found_in_any_iter = False
    for iter_list in iters:
        iter_files = {_norm(str(e.get("file_path", ""))) for e in iter_list}
        if any(any(en in f or f in en for f in iter_files if f) for en in exp_n):
            found_in_any_iter = True
            break
    if not found_in_any_iter:
        # Either node_absent, embedding_recall_miss, or
        # import_resolution_miss. Without graph state we can't
        # distinguish; flag for human review with the most-common
        # default in roundtable findings (embedding_recall_miss).
        return "embedding_recall_miss", "expected file absent from all iterations; could be node_absent or import_resolution_miss"

    # Heuristic 3: file appeared in some iteration but not in final
    # aggregate — fell out during MRR re-rank. Likely embedding miss
    # or hybrid weight issue.
    return "embedding_recall_miss", "file in some iteration but dropped from aggregate"


def case_summary(case: dict[str, Any]) -> dict[str, Any]:
    """Extract the human-relevant fields plus auto-proposed bucket and
    per-iteration rescue tag."""
    issue = case.get("issue", case.get("query", case.get("problem_statement", "")))
    expected = expected_files(case)
    predicted = predicted_files(case)
    iters = iter_entity_lists(case)
    rescued, rescuer_idx = rescue_check(expected, iters)
    proposed, rationale = propose_bucket(case)
    return {
        "id": case.get("id", case.get("case_id", case.get("instance_id", "?"))),
        "issue_excerpt": str(issue)[:200] + ("..." if len(str(issue)) > 200 else ""),
        "expected": expected,
        "predicted": predicted[:5],  # cap at 5 to keep YAML scannable
        "stop_reason": stop_reason(case),
        "iterations_count": len(iters),
        "rescued_by_iter": (rescuer_idx + 1) if rescued else 0,
        "proposed_bucket": proposed,
        "proposal_rationale": rationale,
        "bucket": proposed,  # human can edit
        "human_rationale": "TODO",
    }


def emit_todo_yaml(misses: list[dict[str, Any]], output_path: pathlib.Path, sample_size: int) -> None:
    """Write a human-classifiable YAML scaffold."""
    sampled = misses[:sample_size]
    bucket_options = ", ".join(BUCKETS)
    lines = [
        "# Loc-Bench failure-audit classification scaffold (Plan 4 T1).",
        "# 7-bucket taxonomy from the 2026-05-06 roundtable.",
        "#",
        "# For each case, set `bucket` to one of:",
        f"#   {bucket_options}",
        "#",
        "# The harness auto-proposes a bucket; confirm or override.",
        "# Edit `human_rationale` from TODO to your one-line reasoning.",
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
        lines.append(f"    stop_reason: {json.dumps(c['stop_reason'])}")
        lines.append(f"    iterations_count: {c['iterations_count']}")
        lines.append(f"    rescued_by_iter: {c['rescued_by_iter']}")
        lines.append(f"    proposed_bucket: {json.dumps(c['proposed_bucket'])}")
        lines.append(f"    proposal_rationale: {json.dumps(c['proposal_rationale'])}")
        lines.append(f"    bucket: {json.dumps(c['bucket'])}")
        lines.append(f"    human_rationale: {json.dumps(c['human_rationale'])}")
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
    rescue_counts: Counter = Counter()
    classified = 0
    for line in text.splitlines():
        s = line.strip()
        if s.startswith("bucket:"):
            val = s.split(":", 1)[1].strip().strip('"').strip("'")
            if val in BUCKETS:
                buckets[val] += 1
                classified += 1
            elif val and val != "TODO":
                buckets["unknown:" + val] += 1
        elif s.startswith("rescued_by_iter:"):
            val = s.split(":", 1)[1].strip()
            try:
                if int(val) > 0:
                    rescue_counts["rescued"] += 1
                else:
                    rescue_counts["not_rescued"] += 1
            except ValueError:
                pass

    if classified == 0:
        print(f"No cases classified yet in {yaml_path}", file=sys.stderr)
        print(
            f"Edit the file: confirm `bucket` per case (auto-proposed values are pre-filled).",
            file=sys.stderr,
        )
        return 1

    print(f"=== Loc-Bench failure-audit (Plan 4 T1) ===\n")
    print(f"Classified cases: {classified}")
    if rescue_counts:
        rescued = rescue_counts.get("rescued", 0)
        total = rescued + rescue_counts.get("not_rescued", 0)
        if total:
            print(f"Rescued by iter>=2: {rescued}/{total} ({100*rescued/total:.1f}%)")
    print()

    max_count = max((buckets.get(b, 0) for b in BUCKETS), default=0)
    for bucket in BUCKETS:
        count = buckets.get(bucket, 0)
        pct = 100.0 * count / classified
        bar = "#" * int(40 * count / max(1, max_count))
        desc = BUCKET_DESCRIPTIONS[bucket]
        print(f"  {bucket:24s} {count:>4} ({pct:>5.1f}%) {bar}")
        print(f"    {desc}")

    # Decision rule
    print("\n=== Decision rule outcome ===")
    threshold = 0.60
    for bucket in BUCKETS:
        if buckets.get(bucket, 0) / classified >= threshold:
            print(
                f"  Bucket {bucket} at "
                f"{100*buckets.get(bucket,0)/classified:.1f}% (>= {100*threshold:.0f}% threshold)"
            )
            print(f"  -> {BUCKET_ACTIONS[bucket]}")
            return 0

    print("  No single bucket dominates (none >=60%).")
    print("  Recommendation: extend audit sample, OR investigate the top-2 buckets")
    print("  in parallel — they're likely connected (e.g. import_resolution_miss")
    print("  and embedding_recall_miss often co-occur).")
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

    # Per-bucket auto-proposal preview
    proposals: Counter = Counter()
    rescued = 0
    for m in misses:
        proposals[m["proposed_bucket"]] += 1
        if m["rescued_by_iter"] > 0:
            rescued += 1
    print("  Auto-proposed bucket distribution (pre-confirmation):", file=sys.stderr)
    for b, n in proposals.most_common():
        print(f"    {b:30s} {n:>4}", file=sys.stderr)
    if rescued:
        print(f"  Rescued-by-iter>=2: {rescued}/{len(misses)} ({100*rescued/len(misses):.1f}%)", file=sys.stderr)

    emit_todo_yaml(misses, yaml_path, args.sample_size)
    print(f"\nWrote {yaml_path}", file=sys.stderr)
    print(f"Confirm proposals or override: {yaml_path}")
    print(f"Then run: python {pathlib.Path(__file__).name} --analyze")

    return 0


if __name__ == "__main__":
    sys.exit(main())
