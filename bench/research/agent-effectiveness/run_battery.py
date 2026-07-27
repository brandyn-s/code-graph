"""Run the agent-effectiveness battery.

Iterates questions.json × corpus.json, invokes the agent (via the
arxiv-bench agent_runner.py primitives), scores both continuously
(arxiv-bench scorer.py) and per-category (category_judges.py), writes
JSONL with one row per (question, target) pair.

Loud-failures default per ~/.claude/projects/.../memory/feedback_loud-failures-and-recovery-default.md:
  - Per-Q [OK]/[FAIL]/[SKIP] status
  - JSONL append after each Q (interruption-safe)
  - End-of-run summary with per-category pass rates
  - Exit code = (per-category failures + non-zero-aggregate)
  - --resume / --no-skip / --budget-usd cap supported

Usage:
    python run_battery.py                          # all questions, all corpora
    python run_battery.py --filter-category 1      # only banner-interference questions
    python run_battery.py --filter-question 12,13  # specific question IDs
    python run_battery.py --budget-usd 5           # abort if total cost exceeds $5
    python run_battery.py --no-skip                # re-run already-completed Qs
    python run_battery.py --strict-contract         # fail on any category-6 issue
    python run_battery.py --quiet                  # only print summary
"""
from __future__ import annotations

import argparse
from dataclasses import dataclass
import json
import os
import subprocess
import sys
import time
from pathlib import Path

from project_id import project_name_from_path

try:
    sys.stdout.reconfigure(encoding="utf-8")  # type: ignore[attr-defined]
    sys.stderr.reconfigure(encoding="utf-8")  # type: ignore[attr-defined]
except (AttributeError, OSError):
    pass

HERE = Path(__file__).parent
ARXIV = HERE.parent / "arxiv-bench"

# Make arxiv-bench primitives importable. The Anthropic-dependent modules are
# imported lazily by run_llm_question so category 6 remains dependency-free.
sys.path.insert(0, str(ARXIV))

# Local
from category_judges import run_category_judge, judge_output_shape  # type: ignore  # noqa: E402

RESULTS_FILE = Path(
    os.environ.get(
        "AGENT_EFFECTIVENESS_RESULTS_FILE",
        HERE / "results.jsonl",
    )
)
QUESTIONS_FILE = HERE / "questions.json"
CORPUS_FILE = HERE / "corpus.json"
ALLOWLIST_FILE = ARXIV / "tool_allowlist.json"


# --- Pricing ---

# Opus 4.7 per Anthropic docs (as of 2026-05-12): $15/MTok input, $75/MTok output
# Haiku 4.5: $1.25/MTok input, $5/MTok output (used for judge by default)
OPUS_PRICE = {"input": 15 / 1_000_000, "output": 75 / 1_000_000}
HAIKU_PRICE = {"input": 1.25 / 1_000_000, "output": 5 / 1_000_000}


def estimate_cost(row: dict) -> float:
    """Estimate USD cost from token counts. Agent uses Opus 4.7 by default;
    judge uses Opus 4.7 too unless caller overrode."""
    agent_in = row.get("agent_input_tokens", 0) or 0
    agent_out = row.get("agent_output_tokens", 0) or 0
    judge_in = row.get("judge_input_tokens", 0) or 0
    judge_out = row.get("judge_output_tokens", 0) or 0
    # Both default to Opus 4.7 in arxiv-bench primitives
    return (
        agent_in * OPUS_PRICE["input"]
        + agent_out * OPUS_PRICE["output"]
        + judge_in * OPUS_PRICE["input"]
        + judge_out * OPUS_PRICE["output"]
    )


# --- Loaders ---

def load_questions() -> dict:
    with open(QUESTIONS_FILE, encoding="utf-8") as f:
        return json.load(f)


def load_targets() -> dict[str, dict]:
    """Load corpus targets, with optional env-var path overrides.

    The corpus file hard-codes developer-machine paths and the
    project_id derived from them. CI clones fixtures to different
    paths (e.g. $HOME/fixture/ripgrep) so the stored project_id never
    matches and every schema-validation question fails because the
    `project` arg points at a non-existent project.

    For each target, if the env var `CORPUS_<TARGET_ID_UPPER>_PATH`
    is set (e.g. CORPUS_RIPGREP_PATH=/home/runner/fixture/ripgrep),
    override `path` and recompute `project_id` from the override
    path via project_name_from_path. Targets without the override
    keep their corpus-file values for developer-local runs.
    """
    with open(CORPUS_FILE, encoding="utf-8") as f:
        corpus = json.load(f)
    out: dict[str, dict] = {}
    for t in corpus["targets"]:
        target = dict(t)  # don't mutate the corpus file's in-memory copy
        env_key = f"CORPUS_{target['id'].upper()}_PATH"
        override = os.environ.get(env_key)
        if override:
            target["path"] = override
            target["project_id"] = project_name_from_path(override)
        out[target["id"]] = target
    return out


def load_allowlist() -> list[str]:
    with open(ALLOWLIST_FILE, encoding="utf-8") as f:
        data = json.load(f)
    return data["primary_14_tool_match"]["tools"]


def load_completed() -> set[tuple[int, str]]:
    """Set of (qid, target_id) tuples already completed OK."""
    completed: set[tuple[int, str]] = set()
    if not RESULTS_FILE.exists():
        return completed
    with open(RESULTS_FILE, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                row = json.loads(line)
                if row.get("agent_status") == "ok":
                    completed.add((row["question_id"], row["target_id"]))
            except json.JSONDecodeError:
                continue
    return completed


def append_row(row: dict) -> None:
    with open(RESULTS_FILE, "a", encoding="utf-8") as f:
        f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.flush()


# --- Direct CLI invocation (for schema-validation questions) ---

BINARY = Path(os.environ.get(
    "CODE_GRAPH_BIN",
    str(Path.home() / "Documents" / "GitHub" / "code-graph" / "bin" / "codebase-memory-mcp.exe"),
))


@dataclass(frozen=True)
class ToolCLIResult:
    stdout: str
    stderr: str
    returncode: int


def invoke_tool_cli(tool: str, args: dict) -> ToolCLIResult:
    """Direct CLI invocation, preserving stdout, stderr, and process status."""
    json_args = json.dumps(args)
    result = subprocess.run(
        [str(BINARY), "cli", "--raw", tool, json_args],
        capture_output=True,
        timeout=60,
    )
    return ToolCLIResult(
        stdout=result.stdout.decode("utf-8", errors="replace"),
        stderr=result.stderr.decode("utf-8", errors="replace"),
        returncode=result.returncode,
    )


# --- Question runners ---

def run_llm_question(q: dict, target: dict, allowlist: list[str]) -> dict:
    """Run an LLM-graded question. Returns full results row."""
    from agent_runner import run_question  # type: ignore
    from scorer import score_response  # type: ignore

    project_id = target["project_id"]
    start = time.time()

    try:
        agent_result = run_question(
            lang_id=target["id"],
            question_id=q["id"],
            project_id=project_id,
            question_text=q["question"],
            tool_allowlist=allowlist,
            backend="ours",
        )
    except Exception as e:
        return {
            "question_id": q["id"],
            "target_id": target["id"],
            "category": q["category"],
            "agent_status": "fail",
            "agent_error": f"{type(e).__name__}: {e}",
            "judge_status": "skipped",
            "elapsed_s": round(time.time() - start, 2),
        }

    agent_ok = agent_result.get("error") is None and agent_result.get("response")
    if not agent_ok:
        return {
            "question_id": q["id"],
            "target_id": target["id"],
            "category": q["category"],
            "agent_status": "fail",
            "agent_error": agent_result.get("error") or "empty response",
            "agent_response": agent_result.get("response", ""),
            "judge_status": "skipped",
            "elapsed_s": round(time.time() - start, 2),
        }

    judge_result = score_response(
        lang_id=target["id"],
        question_id=q["id"],
        question_text=q["question"],
        scoring_criteria=q["scoring_criteria"],
        response=agent_result["response"],
        tool_calls=agent_result["tool_calls"],
        tools_used=agent_result["tools_used"],
    )

    judge_ok = judge_result.get("error") is None and judge_result.get("score") is not None

    row = {
        "question_id": q["id"],
        "target_id": target["id"],
        "category": q["category"],
        "kind": "llm",
        "agent_status": "ok",
        "agent_response": agent_result["response"],
        "agent_tool_calls": agent_result["tool_calls"],
        "agent_tools_used": [
            {"name": t["name"], "ok": t.get("ok", True)} for t in agent_result["tools_used"]
        ],
        "agent_input_tokens": agent_result["input_tokens"],
        "agent_output_tokens": agent_result["output_tokens"],
        "agent_elapsed_s": agent_result["elapsed_s"],
        "agent_stop_reason": agent_result["stop_reason"],
        "agent_model": agent_result["model"],
        "judge_status": "ok" if judge_ok else "fail",
        "judge_error": judge_result.get("error"),
        "score": judge_result.get("score"),
        "classification": judge_result.get("classification"),
        "judge_rationale": judge_result.get("rationale"),
        "judge_input_tokens": judge_result.get("judge_input_tokens", 0),
        "judge_output_tokens": judge_result.get("judge_output_tokens", 0),
        "elapsed_s": round(time.time() - start, 2),
        "ts": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }

    # Category-specific binary judge on top of continuous score
    cat_judge = run_category_judge(q, row)
    row["category_signal_caught"] = cat_judge["signal_caught"]
    row["category_evidence"] = cat_judge["evidence"]

    return row


def run_schema_question(q: dict, target: dict) -> dict:
    """Run a schema-validation question. No LLM. Direct CLI call.

    Substitutes target.project_id into q.args['project'] so the same
    question template works across multiple corpus targets (psm in dev,
    ripgrep in CI).
    """
    start = time.time()
    args = dict(q["args"])
    if "project" in args:
        args["project"] = target["project_id"]
    invocation = invoke_tool_cli(q["tool"], args)
    elapsed = round(time.time() - start, 2)

    judge = judge_output_shape(q, invocation.stdout)
    process_failed = invocation.returncode != 0
    evidence = judge["evidence"]
    agent_error = None
    if process_failed:
        agent_error = f"tool process exited {invocation.returncode}"
        stderr = invocation.stderr.strip()
        if stderr:
            agent_error += f" (stderr={stderr[:300]!r})"
        evidence = f"{agent_error}; {evidence}"

    row = {
        "question_id": q["id"],
        "target_id": target["id"],
        "category": q["category"],
        "kind": "schema",
        "tool": q["tool"],
        "raw_response_bytes": len(invocation.stdout),
        "raw_response_head": invocation.stdout[:300],
        "tool_exit_code": invocation.returncode,
        "tool_stderr": invocation.stderr,
        "signal_caught": process_failed or judge["signal_caught"],
        "evidence": evidence,
        "agent_status": "fail" if process_failed else "ok",
        "judge_status": "ok",
        "elapsed_s": elapsed,
        "ts": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }
    if agent_error is not None:
        row["agent_error"] = agent_error
    return row


# --- Main ---

def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--filter-category", type=int, default=None,
                        help="Only run questions in this category")
    parser.add_argument("--filter-question", default=None,
                        help="Comma-separated question IDs")
    parser.add_argument("--filter-target", default=None,
                        help="Comma-separated target IDs (psm, ripgrep)")
    parser.add_argument("--budget-usd", type=float, default=None,
                        help="Abort run if accumulated cost exceeds this")
    parser.add_argument("--no-skip", action="store_true",
                        help="Re-run already-completed questions")
    parser.add_argument(
        "--strict-contract",
        action="store_true",
        default=os.environ.get("AGENT_EFFECTIVENESS_STRICT_CONTRACT") == "1",
        help=(
            "Category 6 only: fail on zero selected/executed checks, skips, "
            "runner failures, or any schema signal"
        ),
    )
    parser.add_argument("--quiet", action="store_true")
    args = parser.parse_args()
    if args.strict_contract and args.filter_category != 6:
        parser.error("--strict-contract requires --filter-category 6")

    data = load_questions()
    targets = load_targets()
    allowlist = load_allowlist()
    completed = load_completed() if not args.no_skip else set()

    qid_filter: set[int] | None = None
    if args.filter_question:
        qid_filter = set(int(x) for x in args.filter_question.split(","))

    target_filter: set[str] | None = None
    if args.filter_target:
        target_filter = set(args.filter_target.split(","))

    # Build (question, target) work list
    work = []
    for q in data["questions"]:
        if args.filter_category and q.get("category") != args.filter_category:
            continue
        if qid_filter and q["id"] not in qid_filter:
            continue
        for tid in q["targets"]:
            if target_filter and tid not in target_filter:
                continue
            if tid not in targets:
                print(f"[WARN] question {q['id']} references unknown target {tid}", file=sys.stderr)
                continue
            work.append((q, targets[tid]))

    print(f"=== AGENT-EFFECTIVENESS BATTERY ===")
    print(f"work items: {len(work)}")
    print(f"already completed: {len(completed)} (skipping unless --no-skip)")
    if args.budget_usd:
        print(f"budget cap: ${args.budget_usd:.2f}")
    print()

    ok = 0
    fail = 0
    skip = 0
    signals = 0  # category-specific failures
    cost = 0.0

    for i, (q, target) in enumerate(work, 1):
        key = (q["id"], target["id"])
        if key in completed:
            print(f"[{i}/{len(work)}] [SKIP] Q{q['id']} cat={q['category']} target={target['id']}")
            skip += 1
            continue

        kind = q.get("kind", "llm")
        if not args.quiet:
            print(f"[{i}/{len(work)}] [RUN] Q{q['id']} cat={q['category']} target={target['id']} kind={kind}", flush=True)

        try:
            if kind == "schema":
                row = run_schema_question(q, target)
            else:
                row = run_llm_question(q, target, allowlist)
        except Exception as e:
            print(f"[{i}/{len(work)}] [FAIL-RUNNER] Q{q['id']}: {type(e).__name__}: {e}", file=sys.stderr)
            fail += 1
            continue

        append_row(row)

        # Cost tracking
        q_cost = estimate_cost(row) if kind == "llm" else 0.0
        cost += q_cost

        signal_caught = row.get("signal_caught", row.get("category_signal_caught", False))
        score = row.get("score")
        agent_status = row.get("agent_status", "?")

        # Loud per-Q status line
        score_repr = f"{score:.2f}" if isinstance(score, (int, float)) else "n/a"
        sig_repr = "⚠ SIGNAL" if signal_caught else "ok"
        print(
            f"[{i}/{len(work)}] [{'OK' if agent_status == 'ok' else 'FAIL'}] "
            f"Q{q['id']} cat={q['category']} score={score_repr} {sig_repr} "
            f"cost=${q_cost:.3f} acc=${cost:.2f}",
            flush=True,
        )

        if agent_status == "ok":
            ok += 1
            if signal_caught:
                signals += 1
        else:
            fail += 1

        # Budget enforcement
        if args.budget_usd and cost >= args.budget_usd:
            print(f"\n[BUDGET] Reached ${cost:.2f} (cap ${args.budget_usd:.2f}); aborting.",
                  file=sys.stderr)
            break

    print()
    print("=== SUMMARY ===")
    print(f"ok: {ok}")
    print(f"fail: {fail}")
    print(f"skip: {skip}")
    print(f"signals (category failure modes caught): {signals}")
    print(f"total cost: ${cost:.2f}")
    print(f"results: {RESULTS_FILE}")

    # Aggregate per-category pass rate
    cat_signals: dict[int, int] = {}
    cat_total: dict[int, int] = {}
    if RESULTS_FILE.exists():
        with open(RESULTS_FILE, encoding="utf-8") as f:
            for line in f:
                if not line.strip():
                    continue
                try:
                    r = json.loads(line)
                except json.JSONDecodeError:
                    continue
                c = r.get("category")
                if c is None:
                    continue
                cat_total[c] = cat_total.get(c, 0) + 1
                sig = r.get("signal_caught", r.get("category_signal_caught", False))
                if sig:
                    cat_signals[c] = cat_signals.get(c, 0) + 1

    if cat_total:
        print()
        print("Per-category pass rate (1 - signals/total):")
        for c in sorted(cat_total):
            total = cat_total[c]
            sigs = cat_signals.get(c, 0)
            rate = 1.0 - (sigs / total if total else 0)
            print(f"  category {c}: {rate:.0%} ({total - sigs}/{total} pass)")

    # Exit-code policy
    # Non-zero if any category < 60% pass rate, or aggregate score < 0.80, or
    # >1 max_turns_exhausted, or budget exceeded.
    category_floor_failed = any(
        (cat_total[c] - cat_signals.get(c, 0)) / cat_total[c] < 0.60
        for c in cat_total if cat_total[c] > 0
    )
    exit_code = 0
    if fail > 0:
        exit_code = 1
        print(f"\n[FAIL] {fail} questions failed at the runner level.")
    if category_floor_failed:
        exit_code = 2
        print(f"\n[FAIL] At least one category dropped below 60% pass rate.")

    if args.strict_contract:
        strict_failures: list[str] = []
        if not work:
            strict_failures.append("zero selected checks")
        if ok + fail == 0:
            strict_failures.append("zero executed checks")
        if skip:
            strict_failures.append(f"{skip} skipped checks")
        if fail:
            strict_failures.append(f"{fail} runner failures")
        if signals:
            strict_failures.append(
                f"{signals} category-6 signal"
                f"{'s' if signals != 1 else ''}"
            )
        if strict_failures:
            exit_code = 3
            print(
                "\n[FAIL] Strict category-6 contract: "
                + "; ".join(strict_failures)
                + "."
            )
        else:
            print(f"\n[OK] Strict category-6 contract: {ok}/{len(work)} checks passed.")

    return exit_code


if __name__ == "__main__":
    sys.exit(main())
