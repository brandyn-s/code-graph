"""C3 step 4 — 5-query episodic-memory ablation.

For each of 5 selected Loc-Bench instances (already indexed), runs the
agent twice via eval_rank_localize/eval.exe:
  - control: LOCAGENT_EPISODIC_MEMORY unset
  - treatment: LOCAGENT_EPISODIC_MEMORY=1

Compares top-K agent finalize entities against ground truth (edit_functions).
Reports per-instance file/class/func hits + agent transcripts evidence
that the episodic section is influencing the agent.

Cost: 5 instances × 2 configs × ~$0.05/run = ~$0.50 at Haiku 4.5 (per
LOCAGENT_ITERATIONS=2 default).

Selected instances (must have indexed DBs at
~/.cache/code-graph/c-tmp-locbench-ab-work-iter1-{instance}.db):
  - django/django: skipped (not pre-indexed locally)
  - chosen 5 below from c-tmp-locbench-* cache:
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path

import pyarrow.parquet as pq

REPO_ROOT = Path(__file__).resolve().parents[2]
EVAL_BIN = REPO_ROOT / "bench/research/eval_rank_localize/eval.exe"
PARQUET = REPO_ROOT / "bench/research/locbench.parquet"
DB_DIR = Path.home() / ".cache" / "code-graph"
DB_PREFIX = "c-tmp-locbench-ab-work-iter1-"

DEFAULT_INSTANCES = [
    "pandas-dev__pandas-22762",
    "python__mypy-18163",
    "vllm-project__vllm-10903",
    "PrefectHQ__prefect-16117",
    "scipy__scipy-22106",
]


def load_instance(instance_id: str) -> dict:
    table = pq.read_table(PARQUET, filters=[("instance_id", "=", instance_id)])
    if table.num_rows == 0:
        raise SystemExit(f"instance {instance_id} not in parquet")
    row = table.to_pylist()[0]
    return row


def run_agent(db: Path, query: str, episodic_on: bool, top_k: int = 10) -> dict:
    """Run eval.exe -agent against the DB with the query.

    Returns parsed JSON output dict (with 'code_localize_agent' key).
    """
    env = os.environ.copy()
    if episodic_on:
        env["LOCAGENT_EPISODIC_MEMORY"] = "1"
    else:
        env.pop("LOCAGENT_EPISODIC_MEMORY", None)
    cmd = [
        str(EVAL_BIN),
        "-agent",
        "-json",
        "-top-k", str(top_k),
        "-depth", "3",
        str(db),
        query,
    ]
    result = subprocess.run(cmd, capture_output=True, env=env, timeout=600)
    if result.returncode != 0:
        return {"error": result.stderr.decode("utf-8", errors="replace")[:500]}
    try:
        return json.loads(result.stdout.decode("utf-8", errors="replace"))
    except json.JSONDecodeError as e:
        return {"error": f"json: {e}"}


def score(entities: list[dict], gt_funcs: list[str]) -> dict:
    """Did any predicted entity hit the GT file/class/function?"""
    file_hit = class_hit = func_hit = False
    matched = []
    for e in entities:
        qn = (e.get("qualified_name") or "").lower()
        fp = (e.get("file_path") or "").lower()
        for gt in gt_funcs:
            gtl = gt.lower()
            parts = gtl.split(".")
            fn = parts[-1] if parts else gtl
            cls = parts[-2] if len(parts) > 1 else None
            if gtl.replace(".", "/") in fp.replace(".py", "").replace("\\", "/"):
                file_hit = True
                matched.append((e.get("qualified_name"), gt, "file"))
            if cls and (qn.endswith("." + cls.lower()) or cls.lower() in qn):
                class_hit = True
                matched.append((e.get("qualified_name"), gt, "class"))
            if qn.endswith("." + fn):
                func_hit = True
                matched.append((e.get("qualified_name"), gt, "func"))
    return {
        "file_hit": file_hit,
        "class_hit": class_hit,
        "func_hit": func_hit,
        "matches": matched[:5],
    }


def has_episodic_in_transcript(result: dict) -> tuple[bool, list[str]]:
    """Inspect the agent transcript for episodic-related entries."""
    agent = result.get("code_localize_agent") or {}
    transcript = agent.get("transcript") or []
    found = []
    for entry in transcript:
        kind = entry.get("kind", "")
        if kind in ("episodic", "episodic_error"):
            found.append(f"{kind}: {entry.get('summary', '')[:160]}")
    return len(found) > 0, found


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--instances", nargs="+", default=DEFAULT_INSTANCES)
    ap.add_argument("--output", type=Path, default=REPO_ROOT / "bench/research/c3-ablation-results.md")
    args = ap.parse_args()

    if not EVAL_BIN.exists():
        raise SystemExit(f"eval.exe not found at {EVAL_BIN}; rebuild first")
    if not os.environ.get("ANTHROPIC_API_KEY"):
        raise SystemExit("ANTHROPIC_API_KEY not set")
    if not os.environ.get("VOYAGE_API_KEY"):
        raise SystemExit("VOYAGE_API_KEY not set (needed for episodic embed)")

    rows = []
    for inst_id in args.instances:
        db = DB_DIR / f"{DB_PREFIX}{inst_id}.db"
        if not db.exists():
            print(f"SKIP {inst_id}: db missing at {db}", file=sys.stderr)
            continue
        info = load_instance(inst_id)
        problem = info.get("problem_statement") or ""
        gt_funcs = list(info.get("edit_functions") or [])
        if not problem or not gt_funcs:
            print(f"SKIP {inst_id}: no problem_statement or edit_functions", file=sys.stderr)
            continue

        print(f"\n=== {inst_id} ===", file=sys.stderr)
        print(f"GT functions: {gt_funcs[:3]}{'...' if len(gt_funcs) > 3 else ''}", file=sys.stderr)
        print(f"Problem: {problem[:120].replace(chr(10), ' ')}...", file=sys.stderr)

        # Truncate query for cleaner agent input
        query = problem[:6000]

        for label, episodic_on in [("control", False), ("treatment", True)]:
            print(f"  -> {label} (episodic={episodic_on})...", file=sys.stderr)
            result = run_agent(db, query, episodic_on)
            if "error" in result:
                print(f"     ERROR: {result['error'][:200]}", file=sys.stderr)
                rows.append({
                    "instance": inst_id, "config": label,
                    "error": result["error"][:200],
                })
                continue
            agent = result.get("code_localize_agent") or {}
            entities = agent.get("entities") or []
            scored = score(entities, gt_funcs)
            has_ep, ep_lines = has_episodic_in_transcript(result)
            rows.append({
                "instance": inst_id,
                "config": label,
                "stop_reason": agent.get("stop_reason"),
                "turns": agent.get("turns"),
                "input_tokens": agent.get("input_tokens"),
                "output_tokens": agent.get("output_tokens"),
                "file_hit": scored["file_hit"],
                "class_hit": scored["class_hit"],
                "func_hit": scored["func_hit"],
                "episodic_in_transcript": has_ep,
                "episodic_lines": ep_lines,
                "top_3_qns": [e.get("qualified_name") for e in entities[:3]],
            })
            print(f"     hits: file={scored['file_hit']} class={scored['class_hit']} func={scored['func_hit']}", file=sys.stderr)
            print(f"     episodic_in_transcript={has_ep}, lines={ep_lines}", file=sys.stderr)

    # Aggregate + write report
    args.output.parent.mkdir(parents=True, exist_ok=True)
    with args.output.open("w", encoding="utf-8") as f:
        f.write("# C3 step 4 — 5-query episodic-memory ablation\n\n")
        f.write("Per-instance comparison: control (LOCAGENT_EPISODIC_MEMORY unset) vs treatment (LOCAGENT_EPISODIC_MEMORY=1).\n\n")
        f.write("Confirms the prompt change is doing something measurable before paying for C5's full n=200 re-run.\n\n")
        f.write("| Instance | Config | File | Class | Func | Episodic? | Stop | Turns | Input tok | Output tok |\n")
        f.write("|---|---|---|---|---|---|---|---|---|---|\n")
        for r in rows:
            if "error" in r:
                f.write(f"| {r['instance']} | {r['config']} | ERR | ERR | ERR | - | - | - | - | - |\n")
                continue
            f.write(f"| {r['instance']} | {r['config']} | {'Y' if r['file_hit'] else 'N'} | {'Y' if r['class_hit'] else 'N'} | {'Y' if r['func_hit'] else 'N'} | {'Y' if r['episodic_in_transcript'] else 'N'} | {r['stop_reason']} | {r['turns']} | {r['input_tokens']} | {r['output_tokens']} |\n")
        f.write("\n## Top-3 entities per run\n\n")
        for r in rows:
            if "error" in r:
                continue
            f.write(f"**{r['instance']} / {r['config']}**: {r['top_3_qns']}\n\n")

        # Aggregates
        f.write("\n## Aggregate hit rates\n\n")
        for cfg in ("control", "treatment"):
            cfg_rows = [r for r in rows if r.get("config") == cfg and "error" not in r]
            n = len(cfg_rows)
            if n == 0:
                continue
            file_pct = 100 * sum(r["file_hit"] for r in cfg_rows) / n
            class_pct = 100 * sum(r["class_hit"] for r in cfg_rows) / n
            func_pct = 100 * sum(r["func_hit"] for r in cfg_rows) / n
            f.write(f"- **{cfg}** (n={n}): file={file_pct:.0f}% class={class_pct:.0f}% func={func_pct:.0f}%\n")

    print(f"\nWrote: {args.output}", file=sys.stderr)


if __name__ == "__main__":
    main()
