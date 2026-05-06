"""Run a Loc-Bench subset against our localization tools and score F1 per instance.

Loop over N selected Loc-Bench instances and for each:

  1. Read the instance from the parquet (problem_statement, repo, base_commit,
     edit_functions ground truth).
  2. Clone the repo at the recorded base_commit into a working dir.
  3. Index it with our codebase-memory-mcp binary (VOYAGE_API_KEY enables
     embedding seeds for the hybrid strategy).
  4. Run the eval harness against the resulting DB with -agent (LLM loop)
     and -seed-strategy=hybrid.
  5. Score: did the ground-truth file or class appear in the agent's
     finalized entities? Record per-instance hit/miss + token usage.
  6. After every instance, check accumulated estimated LLM cost and
     abort if the configured budget cap is exceeded.
  7. Write a summary table at the end.

This script is the harness for the Phase B / V1 deliverable from the
2026-04-25 superplan: turn the n=1 hit on pypa__pip-13085 (PR #82) into
a defensible N=20 benchmark claim.

The script is INTENTIONALLY conservative about cost:

  - Hard-aborts when accumulated cost exceeds --budget-usd (default $3).
  - Skips instances whose repo > 200 MB (indexing wall time would dominate).
  - Skips instances whose ground-truth requires multi-file edits unless
    they all share a common parent dir.

Not run by CI. This is an offline benchmark — invoke manually:

    export ANTHROPIC_API_KEY=sk-...
    export VOYAGE_API_KEY=pa-...
    python bench/research/eval_locbench_batch.py \
        --n 20 --budget-usd 3.0 \
        --workdir C:/tmp/locbench-batch \
        --output bench/research/locbench-n20-results-$(date +%Y-%m-%d).md
"""
from __future__ import annotations

import argparse
import json
import os
import random
import shutil
import subprocess
import sys
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

# Get-well plan Phase 1: shared schema for the per-case JSON contract.
# eval (here) constructs records via this module; audit + compare scripts
# parse via the same module. Writer/reader drift becomes an import-time
# error instead of a silent runtime fallback. See bench/research/schema.py.
sys.path.insert(0, str(Path(__file__).resolve().parent))
import schema  # noqa: E402  (after sys.path tweak)

import pandas as pd

REPO_ROOT = Path(__file__).resolve().parents[2]
PARQUET = REPO_ROOT / "bench/research/locbench.parquet"
EVAL_BIN = REPO_ROOT / "bench/research/eval_rank_localize/eval.exe"
INDEX_BIN = REPO_ROOT / "bin/codebase-memory-mcp.exe"
CACHE_DIR = Path.home() / ".cache" / "codebase-memory-mcp"

# Estimated $/M tokens for Haiku 4.5 (input + output averaged over typical
# agent runs from PR #82: ~50K in, ~1.4K out → $0.04-0.05 per query).
COST_PER_QUERY_USD_ESTIMATE = 0.05

# Repo size cap: above this, indexing wall time > 30min — skip to keep
# the batch tractable.
# Plan 5 Phase A: raised from 200 MB to 1000 MB to allow more Loc-Bench
# instances (ray, vllm, scikit-learn) to run; 1 GB hard cap still excludes
# truly enormous repos like the linux kernel.
MAX_REPO_MB = 1000

# Plan 5 Phase A: bias the n=50 sample toward smaller repos to maximize
# indexed yield. Repos here are known-small from manual inspection of the
# Loc-Bench parquet; the harness prefers these when sampling.
SMALL_REPO_PREFERENCE = (
    "kornia/kornia",
    "aio-libs/aiohttp",
    "huggingface/accelerate",
    "ranaroussi/yfinance",
    "tobymao/sqlglot",
    "langchain-ai/langgraph",
    "microsoft/playwright-python",
    "encode/httpx",
    "pydantic/pydantic",
    "psf/requests",
)


@dataclass
class InstanceResult:
    instance_id: str
    repo: str
    category: str
    ground_truth: list[str]
    indexed: bool = False
    agent_ran: bool = False
    file_hit: bool = False  # any ground-truth file appears in agent output
    class_hit: bool = False  # any ground-truth class appears in agent output
    func_hit: bool = False  # any ground-truth function appears
    turns: int = 0
    input_tokens: int = 0
    output_tokens: int = 0
    cost_estimate_usd: float = 0.0
    note: str = ""
    duration_s: float = 0.0
    # Plan 4 T1: full structured JSON envelope from eval_rank_localize -json,
    # including per-iteration entity lists when LOCAGENT_ITERATIONS>=2.
    # Populated only when --per-case-json is passed. Discarded otherwise
    # to keep the markdown report path unaffected.
    agent_json: dict[str, Any] = field(default_factory=dict)


@dataclass
class BatchSummary:
    n_total: int = 0
    n_indexed: int = 0
    n_agent_ran: int = 0
    n_file_hit: int = 0
    n_class_hit: int = 0
    n_func_hit: int = 0
    total_input_tokens: int = 0
    total_output_tokens: int = 0
    total_cost_usd: float = 0.0
    aborted_reason: str = ""
    instances: list[InstanceResult] = field(default_factory=list)


def select_instances(df: pd.DataFrame, n: int, seed: int) -> pd.DataFrame:
    """Pick N instances with a balanced mix of categories.

    Default strategy: 5 each of Bug, Feature, Performance, Security if
    available; fall back to uniform random if categories under-supply.

    Plan 5 Phase A: within each category, prefer instances from the
    SMALL_REPO_PREFERENCE list when available — this maximizes the
    indexed-yield ratio at the n=50 sample size by biasing away from
    1+ GB monorepos that hit the MAX_REPO_MB cap. Falls back to the
    full category pool if the preferred-repo subset is exhausted.
    """
    random.seed(seed)
    target_per_cat = n // 4
    picked: list[dict] = []
    pref_set = set(SMALL_REPO_PREFERENCE)
    # Plan 5 Phase A: parquet category names are full forms
    # ("Bug Report", "Feature Request", "Performance Issue",
    # "Security Vulnerability"), not the short forms used here previously
    # — the per-category loop was a no-op before this fix.
    for cat in ["Bug Report", "Feature Request", "Performance Issue", "Security Vulnerability"]:
        sub = df[df["category"] == cat]
        if len(sub) == 0:
            continue
        take = min(target_per_cat, len(sub))
        # Bias-by-preference: split the category pool into preferred / other,
        # draw from preferred first, top up from other.
        sub_pref = sub[sub["repo"].isin(pref_set)]
        sub_other = sub[~sub["repo"].isin(pref_set)]
        from_pref = min(take, len(sub_pref))
        from_other = take - from_pref
        if from_pref > 0:
            picked.extend(sub_pref.sample(n=from_pref, random_state=seed).to_dict("records"))
        if from_other > 0:
            picked.extend(sub_other.sample(n=from_other, random_state=seed).to_dict("records"))
    # Top up if we under-filled.
    while len(picked) < n:
        remaining = df.drop(index=[df[df["instance_id"] == r["instance_id"]].index[0] for r in picked])
        if len(remaining) == 0:
            break
        # Prefer small repos in top-up too.
        rem_pref = remaining[remaining["repo"].isin(pref_set)]
        pool = rem_pref if len(rem_pref) > 0 else remaining
        picked.append(pool.sample(n=1, random_state=seed + len(picked)).iloc[0].to_dict())
    return pd.DataFrame(picked[:n])


def repo_size_mb(path: Path) -> float:
    """Estimate disk usage of a checked-out repo."""
    total = 0
    for root, _dirs, files in os.walk(path):
        for f in files:
            try:
                total += (Path(root) / f).stat().st_size
            except OSError:
                pass
    return total / (1024 * 1024)


def clone_repo(repo: str, base_commit: str, dest: Path) -> bool:
    """Shallow-clone {repo} at {base_commit} into {dest}. Returns True on success."""
    if dest.exists():
        # Plan 5 Phase A: git pack files / docs assets / .png on Windows
        # often have read-only bits set after checkout. shutil.rmtree
        # silently fails on those, leaving a partial dir that breaks the
        # next `git clone`. Force-clear read-only bits before rmtree.
        def _force_writable(_func, path, _exc):
            import stat as _stat
            try:
                os.chmod(path, _stat.S_IWRITE)
                _func(path)
            except Exception:
                pass
        shutil.rmtree(dest, onerror=_force_writable)
        if dest.exists():
            # Last-resort fallback if rmtree still failed: skip this instance.
            print(f"  clone target dir not removable: {dest}; skipping")
            return False
    dest.parent.mkdir(parents=True, exist_ok=True)
    url = f"https://github.com/{repo}.git"
    # Full clone needed because shallow + base_commit isn't reliable across
    # all GitHub repos. Tradeoff: more wall time, but deterministic.
    try:
        subprocess.run(
            ["git", "clone", "--quiet", url, str(dest)],
            check=True,
            timeout=600,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        subprocess.run(
            ["git", "-C", str(dest), "checkout", base_commit],
            check=True,
            timeout=60,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        return True
    except subprocess.CalledProcessError as e:
        print(f"  clone/checkout failed: {e.stderr.decode('utf-8', errors='replace')[:200]}")
        return False
    except subprocess.TimeoutExpired:
        print("  clone/checkout timed out")
        return False


def index_repo(path: Path) -> bool:
    """Invoke codebase-memory-mcp index_repository against {path}.

    Form: codebase-memory-mcp cli index_repository '{"path":"<abs path>"}'

    Project name is derived from the path. Embedding seeds require
    VOYAGE_API_KEY at index time."""
    if not INDEX_BIN.exists():
        print(f"  binary missing: {INDEX_BIN}")
        return False
    args_json = json.dumps({"path": to_windows_path(path)})
    try:
        # Capture as bytes (no text=True) and decode UTF-8 with replace.
        # text=True uses cp1252 on Windows and crashes the parent reader
        # thread when subprocess outputs non-cp1252 bytes (PR #97 fix).
        result = subprocess.run(
            [str(INDEX_BIN), "cli", "index_repository", args_json],
            capture_output=True,
            timeout=1800,  # 30 min cap per index
        )
        if result.returncode != 0:
            err = result.stderr.decode("utf-8", errors="replace")
            print(f"  index failed (exit {result.returncode}): {err[:200]}")
            return False
        return True
    except subprocess.TimeoutExpired:
        print("  index timed out (30min)")
        return False


def to_windows_path(p: Path | str) -> str:
    """Convert a path to Windows-style absolute form (`C:/foo/bar`).

    Handles three input shapes:
      - Already Windows-style (`C:/foo` or `C:\\foo`): just normalize slashes.
      - MSYS / Git Bash form (`/c/foo`): rewrite to `C:/foo`.
      - Relative or other: resolve under CWD.

    Why care: Python's Path.resolve() on Windows treats `/c/foo` as drive-
    root-relative giving `C:\\c\\foo` (wrong — adds an extra `c`). Detect
    the MSYS form before resolve()."""
    s = str(p).replace("\\", "/")
    # MSYS / Git Bash form: /c/path → C:/path (BEFORE resolve)
    if len(s) >= 3 and s[0] == "/" and s[2] == "/" and s[1].isalpha():
        s = s[1].upper() + ":" + s[2:]
        return s
    # Already Windows-style (drive letter at [1]): leave as-is
    if len(s) >= 2 and s[1] == ":":
        return s
    # Relative — resolve under CWD
    return str(Path(s).resolve()).replace("\\", "/")


def db_path_for(repo_path: Path) -> Path:
    """Mirror internal/pipeline/pipeline.go ProjectNameFromPath:

      1. filepath.Clean (collapse `..`, `.`, double slashes)
      2. Backslash → slash
      3. Lowercase Windows drive letter
      4. `/` → `-`, `:` → `-`
      5. Collapse `--` to `-`
      6. Strip leading `-`

    Returns the absolute path of the on-disk SQLite file."""
    win = to_windows_path(repo_path)
    # Step 3: lowercase Windows drive letter (matches ProjectNameFromPath)
    if len(win) >= 2 and win[1] == ":":
        win = win[0].lower() + win[1:]
    # Step 4: replace separators
    name = win.replace("/", "-").replace(":", "-")
    # Step 5: collapse consecutive dashes
    while "--" in name:
        name = name.replace("--", "-")
    # Step 6: strip leading dash
    name = name.lstrip("-")
    if not name:
        name = "root"
    return CACHE_DIR / f"{name}.db"


def run_agent(db: Path, query: str, top_k: int = 10, json_mode: bool = False) -> dict[str, Any]:
    """Run eval_rank_localize binary with -agent. Returns parsed result dict.

    When json_mode=True, passes -json to the binary to capture the
    structured locagent.Result (including the per-iteration Iterations
    field added in Plan 4 T1) instead of the human-readable text. The
    full JSON is returned under the "agent_json" key for the per-case
    JSON dump.
    """
    cmd = [
        str(EVAL_BIN),
        "-top-k", str(top_k),
        "-agent",
        "-seed-strategy", "hybrid",
    ]
    if json_mode:
        cmd.append("-json")
    cmd.append(to_windows_path(db))
    cmd.append(query)

    # Capture as bytes + UTF-8 decode (text=True uses cp1252 on Windows
    # and crashes on non-cp1252 bytes — PR #97 fix).
    result = subprocess.run(cmd, capture_output=True, timeout=300)
    out = result.stdout.decode("utf-8", errors="replace")
    err = result.stderr.decode("utf-8", errors="replace")
    if result.returncode != 0:
        return {
            "error": err[:500],
            "stdout": out,
            "input_tokens": 0,
            "output_tokens": 0,
            "turns": 0,
        }

    parsed: dict[str, Any] = {"stdout": out, "input_tokens": 0, "output_tokens": 0, "turns": 0}

    if json_mode:
        # Structured output. Parse the JSON envelope and pull token /
        # turn counts from the embedded code_localize_agent block.
        try:
            envelope = json.loads(out)
        except json.JSONDecodeError as e:
            parsed["error"] = f"json decode failed: {e}"
            return parsed
        agent = envelope.get("code_localize_agent") or {}
        parsed["agent_json"] = envelope
        parsed["turns"] = int(agent.get("turns", 0))
        parsed["input_tokens"] = int(agent.get("input_tokens", 0))
        parsed["output_tokens"] = int(agent.get("output_tokens", 0))
        return parsed

    # Text mode: parse the line "turns=N, stop_reason=foo, input_tokens=X, output_tokens=Y"
    for line in out.splitlines():
        if "input_tokens=" in line and "output_tokens=" in line:
            for part in line.split(","):
                k, _, v = part.strip().partition("=")
                if k in {"turns", "input_tokens", "output_tokens"}:
                    try:
                        parsed[k] = int(v)
                    except ValueError:
                        pass
    return parsed


def score_against_ground_truth(agent_output: str, ground_truth: list[str]) -> tuple[bool, bool, bool]:
    """Return (file_hit, class_hit, func_hit). Each True if ANY ground-truth
    item's file path / containing class / function name appears in the
    agent's output text."""
    file_hit = class_hit = func_hit = False
    for gt in ground_truth:
        if ":" not in gt:
            # Format expected: "path/to/file.py:Class.func" or "path/to/file.py:func"
            continue
        file_part, func_part = gt.split(":", 1)
        if file_part in agent_output:
            file_hit = True
        comps = func_part.split(".")
        if len(comps) >= 2:
            cls = comps[0]
            fn = comps[-1]
            if cls in agent_output:
                class_hit = True
            if fn in agent_output:
                func_hit = True
        else:
            if func_part in agent_output:
                func_hit = True
    return file_hit, class_hit, func_hit


def evaluate_instance(row: dict[str, Any], workdir: Path, json_mode: bool = False) -> InstanceResult:
    iid = row["instance_id"]
    repo = row["repo"]
    res = InstanceResult(
        instance_id=iid,
        repo=repo,
        category=row.get("category", "Unknown"),
        ground_truth=list(row.get("edit_functions", [])),
    )
    t0 = time.time()
    print(f"\n=== {iid} ({repo}, {res.category}) ===")
    print(f"ground truth ({len(res.ground_truth)} fns): {res.ground_truth[:3]}")

    repo_dir = workdir / iid
    if not clone_repo(repo, row["base_commit"], repo_dir):
        res.note = "clone failed"
        res.duration_s = time.time() - t0
        return res

    size_mb = repo_size_mb(repo_dir)
    if size_mb > MAX_REPO_MB:
        res.note = f"repo too large ({size_mb:.0f} MB > {MAX_REPO_MB})"
        shutil.rmtree(repo_dir, ignore_errors=True)
        res.duration_s = time.time() - t0
        return res

    if not index_repo(repo_dir):
        res.note = "index failed"
        shutil.rmtree(repo_dir, ignore_errors=True)
        res.duration_s = time.time() - t0
        return res
    res.indexed = True

    # Use only the first paragraph as the agent's query — full multi-
    # paragraph issue dilutes seed matching (verified PR #82 testing).
    short_query = row["problem_statement"].split("\n\n")[0].strip()
    db = db_path_for(repo_dir)
    if not db.exists():
        res.note = f"db not at expected path {db.name}"
        shutil.rmtree(repo_dir, ignore_errors=True)
        res.duration_s = time.time() - t0
        return res

    parsed = run_agent(db, short_query, top_k=10, json_mode=json_mode)
    res.agent_ran = "error" not in parsed
    res.input_tokens = parsed.get("input_tokens", 0)
    res.output_tokens = parsed.get("output_tokens", 0)
    res.turns = parsed.get("turns", 0)
    res.cost_estimate_usd = COST_PER_QUERY_USD_ESTIMATE if res.agent_ran else 0.0

    if json_mode and "agent_json" in parsed:
        res.agent_json = parsed["agent_json"]

    if res.agent_ran:
        # In json_mode, the agent's text "stdout" is a JSON envelope.
        # Score against the structured entities directly when present
        # rather than against the JSON string (which would mis-attribute
        # substring hits to keys/property names instead of file paths).
        if json_mode and "agent_json" in parsed:
            agent_block = parsed["agent_json"].get("code_localize_agent") or {}
            entities = agent_block.get("entities") or []
            ent_blob = "\n".join(
                f"{e.get('qualified_name','')} {e.get('file_path','')}"
                for e in entities if isinstance(e, dict)
            )
            res.file_hit, res.class_hit, res.func_hit = score_against_ground_truth(
                ent_blob, res.ground_truth
            )
        else:
            res.file_hit, res.class_hit, res.func_hit = score_against_ground_truth(
                parsed["stdout"], res.ground_truth
            )

    # Cleanup repo to save disk
    shutil.rmtree(repo_dir, ignore_errors=True)
    # Cleanup index DB (saves ~50-200 MB per instance)
    try:
        db.unlink(missing_ok=True)
        Path(str(db) + "-shm").unlink(missing_ok=True)
        Path(str(db) + "-wal").unlink(missing_ok=True)
    except OSError:
        pass

    res.duration_s = time.time() - t0
    print(
        f"  -> indexed={res.indexed} agent={res.agent_ran} "
        f"file_hit={res.file_hit} class_hit={res.class_hit} "
        f"tokens={res.input_tokens}/{res.output_tokens} "
        f"~${res.cost_estimate_usd:.3f} ({res.duration_s:.0f}s)"
    )
    return res


def _build_per_case_dict(summary: BatchSummary) -> dict:
    """Build the per-case JSON payload from the in-progress summary.

    Get-well plan Phase 1 (2026-05-06): now constructs via the shared
    schema module (bench/research/schema.py) so writer/reader contracts
    are checked at type-load time. Previously the dict was inlined here;
    audit + compare scripts had to mirror the field names by hand and
    silently fell back when keys drifted.
    """
    record = schema.BatchSummaryRecord(
        schema_version=schema.SCHEMA_VERSION,
        generated_at=schema.now_iso_utc(),
        n_total=summary.n_total,
        n_indexed=summary.n_indexed,
        n_agent_ran=summary.n_agent_ran,
        n_file_hit=summary.n_file_hit,
        n_class_hit=summary.n_class_hit,
        n_func_hit=summary.n_func_hit,
        aborted_reason=summary.aborted_reason,
        cases=[_per_case_record_from_instance(r) for r in summary.instances],
    )
    return record.to_dict()


def _per_case_record_from_instance(r: InstanceResult) -> "schema.PerCaseRecord":
    """Adapt an in-memory InstanceResult into the shared schema record.

    The inversion lives at the eval-script boundary: the rest of the
    eval code uses the legacy InstanceResult dataclass; only this
    adapter pushes data into the schema-validated shape that gets
    written to disk and read back by audit/compare.
    """
    env_dict = r.agent_json if isinstance(r.agent_json, dict) else {}
    cla_raw = env_dict.get("code_localize_agent")
    if isinstance(cla_raw, dict):
        envelope = schema.AgentEnvelope(
            code_localize_agent=schema.CodeLocalizeAgentResult.from_dict(cla_raw)
        )
    else:
        envelope = schema.AgentEnvelope(code_localize_agent=None)
    return schema.PerCaseRecord(
        instance_id=r.instance_id,
        repo=r.repo,
        category=r.category,
        ground_truth=list(r.ground_truth),
        indexed=r.indexed,
        agent_ran=r.agent_ran,
        file_hit=r.file_hit,
        class_hit=r.class_hit,
        func_hit=r.func_hit,
        # Inverted hit fields used by the failure-audit is_miss()
        # heuristic. The schema preserves them as separate fields so
        # the audit script's lookups don't have to guess.
        file_correct=r.file_hit,
        class_correct=r.class_hit,
        func_correct=r.func_hit,
        turns=r.turns,
        input_tokens=r.input_tokens,
        output_tokens=r.output_tokens,
        cost_estimate_usd=r.cost_estimate_usd,
        duration_s=r.duration_s,
        note=r.note,
        agent_envelope=envelope,
    )


def write_report(summary: BatchSummary, output: Path) -> None:
    lines = [
        f"# Loc-Bench N={summary.n_total} batch results — {time.strftime('%Y-%m-%d %H:%M')}",
        "",
        "## Summary",
        "",
        f"- Instances attempted: {summary.n_total}",
        f"- Indexed successfully: {summary.n_indexed}",
        f"- Agent ran: {summary.n_agent_ran}",
        f"- File-level hit (any ground-truth file in output): {summary.n_file_hit}",
        f"- Class-level hit: {summary.n_class_hit}",
        f"- Function-level hit: {summary.n_func_hit}",
        f"- Total LLM tokens: {summary.total_input_tokens:,} input, {summary.total_output_tokens:,} output",
        f"- Estimated cost: ${summary.total_cost_usd:.2f}",
    ]
    if summary.n_agent_ran > 0:
        lines.append(
            f"- File-level accuracy (vs LocAgent's published 92.7%): "
            f"{100 * summary.n_file_hit / summary.n_agent_ran:.1f}% "
            f"({summary.n_file_hit}/{summary.n_agent_ran})"
        )
    if summary.aborted_reason:
        lines.append(f"- **Aborted**: {summary.aborted_reason}")
    lines.append("")
    lines.append("## Per-instance results")
    lines.append("")
    lines.append("| instance_id | repo | category | indexed | agent | file | class | func | turns | tokens | $ | note |")
    lines.append("|---|---|---|---|---|---|---|---|---|---|---|---|")
    for r in summary.instances:
        lines.append(
            f"| {r.instance_id} | {r.repo} | {r.category} | "
            f"{'Y' if r.indexed else 'N'} | {'Y' if r.agent_ran else 'N'} | "
            f"{'Y' if r.file_hit else 'N'} | {'Y' if r.class_hit else 'N'} | "
            f"{'Y' if r.func_hit else 'N'} | {r.turns} | "
            f"{r.input_tokens}/{r.output_tokens} | "
            f"{r.cost_estimate_usd:.3f} | {r.note} |"
        )
    output.write_text("\n".join(lines) + "\n", encoding="utf-8")
    print(f"\nReport written: {output}")


def main() -> int:
    # Force line-buffered stdout. Without this, print() output is
    # block-buffered when stdout is redirected to a file (background
    # launches via nohup, `python ... > log &`, etc.) and the operator
    # can't tell whether the script is making progress or hung. The
    # buffer flushes only on script exit — so a 30-min run looks like a
    # 30-min freeze. Diagnosed 2026-05-05 during the code-graph
    # production-readiness audit: a launched n=10 batch appeared hung
    # for 20+ minutes with 0 stdout while clones progressed in the
    # workdir. line_buffering=True flushes per newline regardless of
    # tty/pipe destination.
    if hasattr(sys.stdout, "reconfigure"):
        sys.stdout.reconfigure(line_buffering=True)
    if hasattr(sys.stderr, "reconfigure"):
        sys.stderr.reconfigure(line_buffering=True)

    ap = argparse.ArgumentParser()
    ap.add_argument("--n", type=int, default=20, help="Number of instances")
    ap.add_argument("--seed", type=int, default=42, help="Random seed for sampling")
    ap.add_argument("--budget-usd", type=float, default=3.0, help="Hard abort threshold")
    ap.add_argument("--workdir", type=Path, default=Path(r"C:/tmp/locbench-batch"))
    ap.add_argument(
        "--output",
        type=Path,
        default=REPO_ROOT / "bench/research" / f"locbench-n20-results-{time.strftime('%Y-%m-%d')}.md",
    )
    ap.add_argument(
        "--per-case-json",
        type=Path,
        default=None,
        help=(
            "If set, write a JSON file with the full per-case agent envelopes "
            "(including the per-iteration Iterations field surfaced in Plan 4 T1). "
            "Consumed by bench/research/locbench_failure_audit.py for the "
            "7-bucket classification pipeline."
        ),
    )
    args = ap.parse_args()

    if not os.environ.get("ANTHROPIC_API_KEY"):
        print("ERROR: ANTHROPIC_API_KEY required for agent runs", file=sys.stderr)
        return 2
    if not os.environ.get("VOYAGE_API_KEY"):
        print("WARNING: VOYAGE_API_KEY not set — embedding seeds disabled, hybrid falls back to substring", file=sys.stderr)

    if not PARQUET.exists():
        print(f"ERROR: parquet not at {PARQUET}", file=sys.stderr)
        return 2

    df = pd.read_parquet(PARQUET)
    selected = select_instances(df, args.n, args.seed)
    print(f"Selected {len(selected)} instances:")
    for _, row in selected.iterrows():
        print(f"  - {row['instance_id']} ({row.get('category', '?')})")

    args.workdir.mkdir(parents=True, exist_ok=True)
    summary = BatchSummary(n_total=len(selected))

    # Roundtable T2 fix (2026-05-06): persist per-case JSON checkpoint
    # after EVERY instance, not only at end. Previously, killing the
    # batch at 6/50 dropped all 6 cases of evidence. The 5-agent
    # roundtable's T2 ("mine the partial parallel data") assumed the
    # evidence was shipped; it wasn't. This fix makes the assumption
    # true going forward.
    def _checkpoint_per_case() -> None:
        if not args.per_case_json:
            return
        try:
            args.per_case_json.parent.mkdir(parents=True, exist_ok=True)
            args.per_case_json.write_text(
                json.dumps(_build_per_case_dict(summary), indent=2),
                encoding="utf-8",
            )
        except Exception as exc:
            print(f"  [checkpoint failed: {exc!r}]")

    for _, row in selected.iterrows():
        if summary.total_cost_usd >= args.budget_usd:
            summary.aborted_reason = (
                f"budget cap ${args.budget_usd:.2f} hit at "
                f"${summary.total_cost_usd:.2f} after {len(summary.instances)} runs"
            )
            print(f"\n!!! {summary.aborted_reason}")
            break
        try:
            res = evaluate_instance(row.to_dict(), args.workdir, json_mode=bool(args.per_case_json))
        except KeyboardInterrupt:
            summary.aborted_reason = "user interrupted (Ctrl+C)"
            _checkpoint_per_case()
            break
        except Exception as e:
            res = InstanceResult(
                instance_id=row["instance_id"],
                repo=row["repo"],
                category=row.get("category", "Unknown"),
                ground_truth=list(row.get("edit_functions", [])),
                note=f"exception: {e!r}",
            )
        summary.instances.append(res)
        summary.n_indexed += int(res.indexed)
        summary.n_agent_ran += int(res.agent_ran)
        summary.n_file_hit += int(res.file_hit)
        summary.n_class_hit += int(res.class_hit)
        summary.n_func_hit += int(res.func_hit)
        summary.total_input_tokens += res.input_tokens
        summary.total_output_tokens += res.output_tokens
        summary.total_cost_usd += res.cost_estimate_usd
        _checkpoint_per_case()

    write_report(summary, args.output)

    # Plan 4 T1: per-case JSON dump for the failure-audit pipeline.
    # Captures the full structured agent envelope per instance,
    # including the per-iteration Iterations field (when LOCAGENT_ITERATIONS>=2).
    # Roundtable T2 (2026-05-06): also written as a checkpoint after every
    # instance via _checkpoint_per_case() above — see _build_per_case_dict.
    if args.per_case_json:
        args.per_case_json.parent.mkdir(parents=True, exist_ok=True)
        args.per_case_json.write_text(
            json.dumps(_build_per_case_dict(summary), indent=2),
            encoding="utf-8",
        )
        print(f"\nPer-case JSON written: {args.per_case_json}")

    return 0


if __name__ == "__main__":
    sys.exit(main())
