"""Multi-mode Loc-Bench comparison harness.

Runs the SAME instances through multiple localizer configurations and
emits a comparison table. Designed to answer the questions the N=20 batch
report (PR #89) flagged as unanswered:

  1. Does code_localize_agent actually beat the primitives?
     → Run substring-primitives, hybrid-primitives, hybrid-agent on
       the same instances. Compare hit counts.

  2. Did the #83/#84 tuning help, hurt, or no-op?
     → Pass --binary PATH to point at a pre-#83 build. Run the same
       primitives modes against the same instances. Compare hit counts.

  3. Does the agent's apparent 100% hold on large repos?
     → Pass --max-mb 1000 (or 0 for no cap) to include large repos
       previously excluded.

  4. Do Feature Request and Security categories work?
     → Pass --instances <comma-separated-ids> to target specific
       instances, or use --categories Feature,Security to bias sampling.

The harness is index-aware: if the SQLite DB for an instance already
exists, we skip re-cloning and re-indexing (saving 5-30 min per repo).
This makes follow-up runs cheap.

Output: one markdown report with per-instance per-mode hit results and
aggregate comparison.

Invocation:

    python bench/research/eval_locbench_compare.py \\
        --instances huggingface__accelerate-3279,kornia__kornia-3084,... \\
        --modes substring-primitives,hybrid-primitives,hybrid-agent \\
        --max-mb 1000 \\
        --output bench/research/locbench-compare-2026-04-25.md

Cost: only `hybrid-agent` mode burns LLM tokens. Substring/hybrid
primitives are zero-LLM-cost (they only call Voyage for embedding once
per query, ~$0.0002).
"""
from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
import time
from concurrent.futures import ProcessPoolExecutor, as_completed
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import pandas as pd

REPO_ROOT = Path(__file__).resolve().parents[2]
PARQUET = REPO_ROOT / "bench/research/locbench.parquet"
DEFAULT_EVAL_BIN = REPO_ROOT / "bench/research/eval_rank_localize/eval.exe"
DEFAULT_INDEX_BIN = REPO_ROOT / "bin/codebase-memory-mcp.exe"
CACHE_DIR = Path.home() / ".cache" / "codebase-memory-mcp"

# Modes the harness supports. Each runs the eval binary with specific flags.
MODE_FLAGS: dict[str, list[str]] = {
    "substring-primitives": ["-seed-strategy", "substring"],
    "hybrid-primitives": ["-seed-strategy", "hybrid"],
    "embedding-primitives": ["-seed-strategy", "embedding"],
    "hybrid-agent": ["-seed-strategy", "hybrid", "-agent"],
}

COST_PER_AGENT_QUERY_USD = 0.05  # Haiku 4.5 typical


@dataclass
class ModeResult:
    """One mode (substring/hybrid/agent) on one instance."""
    mode: str
    file_hit: bool = False
    class_hit: bool = False
    func_hit: bool = False
    rank_section: str = ""  # the relevant section of stdout for this mode
    duration_s: float = 0.0
    note: str = ""
    # Agent-only fields:
    turns: int = 0
    input_tokens: int = 0
    output_tokens: int = 0
    cost_usd: float = 0.0


@dataclass
class InstanceResult:
    instance_id: str
    repo: str
    category: str
    base_commit: str
    ground_truth: list[str]
    cloned: bool = False
    indexed: bool = False
    repo_size_mb: float = 0.0
    mode_results: list[ModeResult] = field(default_factory=list)
    note: str = ""


def to_windows_path(p: Path | str) -> str:
    s = str(p).replace("\\", "/")
    if len(s) >= 3 and s[0] == "/" and s[2] == "/" and s[1].isalpha():
        return s[1].upper() + ":" + s[2:]
    if len(s) >= 2 and s[1] == ":":
        return s
    return str(Path(s).resolve()).replace("\\", "/")


def db_path_for(repo_path: Path) -> Path:
    win = to_windows_path(repo_path)
    if len(win) >= 2 and win[1] == ":":
        win = win[0].lower() + win[1:]
    name = win.replace("/", "-").replace(":", "-")
    while "--" in name:
        name = name.replace("--", "-")
    name = name.lstrip("-")
    if not name:
        name = "root"
    return CACHE_DIR / f"{name}.db"


def repo_size_mb(path: Path) -> float:
    total = 0
    for root, _dirs, files in os.walk(path):
        for f in files:
            try:
                total += (Path(root) / f).stat().st_size
            except OSError:
                pass
    return total / (1024 * 1024)


def clone_repo(repo: str, base_commit: str, dest: Path) -> bool:
    """Clone {repo} at {base_commit} into {dest}.

    Uses init + fetch-by-sha first (fast for repos that allow
    uploadpack.allowAnySHA1InWant), falls back to full clone for repos
    where the base_commit is on a feature branch only."""
    if dest.exists():
        return True
    dest.parent.mkdir(parents=True, exist_ok=True)
    url = f"https://github.com/{repo}.git"
    # Strategy 1: init + fetch-by-sha (fast)
    try:
        subprocess.run(
            ["git", "init", "--quiet", str(dest)],
            check=True, timeout=30,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        subprocess.run(
            ["git", "-C", str(dest), "remote", "add", "origin", url],
            check=True, timeout=30,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        try:
            subprocess.run(
                ["git", "-C", str(dest), "fetch", "--quiet", "--depth=1", "origin", base_commit],
                check=True, timeout=600,
                stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            )
            subprocess.run(
                ["git", "-C", str(dest), "checkout", "FETCH_HEAD"],
                check=True, timeout=120,
                stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            )
            return True
        except subprocess.CalledProcessError:
            # Strategy 2: full clone, then checkout
            shutil.rmtree(dest, ignore_errors=True)
            subprocess.run(
                ["git", "clone", "--quiet", url, str(dest)],
                check=True, timeout=900,
                stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            )
            subprocess.run(
                ["git", "-C", str(dest), "checkout", base_commit],
                check=True, timeout=120,
                stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            )
            return True
    except subprocess.CalledProcessError as e:
        print(f"  clone failed: {e.stderr.decode('utf-8', errors='replace')[:200]}")
        shutil.rmtree(dest, ignore_errors=True)
        return False
    except subprocess.TimeoutExpired:
        print("  clone timed out")
        shutil.rmtree(dest, ignore_errors=True)
        return False


def index_repo(path: Path, index_bin: Path) -> bool:
    if not index_bin.exists():
        print(f"  index binary missing: {index_bin}")
        return False
    args_json = json.dumps({"path": to_windows_path(path)})
    try:
        # NOTE: capture as bytes (no text=True) and decode UTF-8 with
        # errors='replace'. Python's subprocess text=True uses cp1252
        # on Windows by default; indexer stdout often contains source
        # code excerpts with non-cp1252 bytes (e.g. 0x90 DCS or other
        # C1 control chars) which crashes the parent's reader thread
        # mid-run. Took out a worker on the 560 batch with
        # UnicodeDecodeError. Same fix pattern as platform-constraints.md
        # `subprocess_bytes_then_decode_utf8` invariant.
        result = subprocess.run(
            [str(index_bin), "cli", "index_repository", args_json],
            capture_output=True, timeout=3600,
        )
        if result.returncode != 0:
            err = result.stderr.decode("utf-8", errors="replace")
            print(f"  index failed (exit {result.returncode}): {err[:200]}")
            return False
        return True
    except subprocess.TimeoutExpired:
        print("  index timed out (1hr cap)")
        return False


def pick_query_from_issue(problem_statement: str) -> str:
    """Pick a query string from a Loc-Bench problem_statement.

    Prior version used `split('\\n\\n')[0]` which on issues with markdown
    headers (e.g. pandas-dev's "ENH: increase verbosity\\n### Feature
    Type") gave only the title + first heading line. Useless as a seed
    query.

    New strategy:
      1. Skip empty lines and lines that are only markdown headers (#),
         checkbox markers ([X], [ ]), or section dividers.
      2. Take the first ~1500 chars of the remaining content. Caps the
         token budget while giving the agent enough context to extract
         symbols, error messages, and identifiers itself.

    Note: this could grow into a real preprocessor (skip code fences,
    drop URLs, etc). Current implementation is the minimum that makes
    issues like pandas-dev__pandas-59900 viable."""
    lines = problem_statement.split("\n")
    keep: list[str] = []
    for line in lines:
        stripped = line.strip()
        if not stripped:
            continue
        # Skip pure markdown headers (any number of # at start)
        if stripped.startswith("#") and not any(c.isalpha() for c in stripped.replace("#", " ").replace(" ", "")):
            continue
        # Skip checkbox markers as a sole content
        if stripped in {"- [ ]", "- [X]", "- [x]", "_No response_"}:
            continue
        if stripped.startswith("- [ ]") or stripped.startswith("- [X]") or stripped.startswith("- [x]"):
            # Strip the checkbox prefix; the description after may be useful
            stripped = stripped[5:].strip()
            if not stripped:
                continue
        keep.append(stripped)
    text = "\n".join(keep)
    if len(text) > 1500:
        text = text[:1500]
    if not text:
        # Fallback: original first paragraph
        text = problem_statement.split("\n\n")[0].strip()
    return text


def normalize_path(p: str) -> str:
    """Normalize a file path for comparison: forward slashes, no trailing
    slash. Loc-Bench ground truth uses forward slashes always."""
    return p.replace("\\", "/").strip("/")


def score_entities(
    entities: list[dict[str, Any]],
    ground_truth: list[str],
) -> tuple[bool, bool, bool]:
    """Structured scorer.

    Each entity must have at least `qualified_name` (str) and `file_path`
    (str). Ground truth items are `path/to/file.py:Class.method` or
    `path/to/file.py:func`.

    Hit definitions:
      - file_hit: any entity's file_path equals any ground-truth file_part
      - class_hit (when ground truth has Class.method): any entity's
        file_path matches AND its qualified_name contains '.Class' as a
        suffix-component (or is itself the class)
      - func_hit: any entity's file_path matches AND its qualified_name
        ends with '.func' (or '.Class.func' when ground truth has class)

    All comparisons use forward-slash paths and exact equality on the
    file_path; qualified_name uses dotted-component containment so the
    project-prefix in code-graph QNs (e.g.
    'c-tmp-locbench-batch-X.backend.foo.Bar.baz') matches against the
    ground-truth tail ('foo.Bar.baz')."""
    file_hit = class_hit = func_hit = False
    norm_entities: list[tuple[str, str]] = []
    for ent in entities:
        qn = (ent.get("qualified_name") or "").strip()
        fp = normalize_path(ent.get("file_path") or "")
        if qn or fp:
            norm_entities.append((qn, fp))

    for gt in ground_truth:
        if ":" not in gt:
            continue
        gt_file_raw, gt_func = gt.split(":", 1)
        gt_file = normalize_path(gt_file_raw)
        comps = gt_func.split(".")
        cls: str | None = None
        fn: str
        if len(comps) >= 2:
            cls = comps[0]
            fn = ".".join(comps[1:])  # supports nested classes / dotted func
        else:
            fn = gt_func

        for qn, fp in norm_entities:
            if fp != gt_file:
                continue
            file_hit = True
            qn_lower = qn.lower()
            if cls is not None:
                # Class hit: qn ends with '.Class' or contains '.Class.'
                if qn_lower.endswith(f".{cls.lower()}") or f".{cls.lower()}." in qn_lower:
                    class_hit = True
                # Func hit: qn ends with '.Class.fn'
                if qn_lower.endswith(f".{cls.lower()}.{fn.lower()}"):
                    func_hit = True
            else:
                # Func hit: qn ends with '.fn'
                if qn_lower.endswith(f".{fn.lower()}"):
                    func_hit = True
    return file_hit, class_hit, func_hit


def run_mode(
    eval_bin: Path,
    db: Path,
    query: str,
    mode: str,
    ground_truth: list[str],
    top_k: int = 10,
) -> ModeResult:
    """Run one mode against an existing DB and score it.

    Uses the eval binary's `-json` mode and structured scoring (matches
    against entity qualified_name + file_path), not stdout substrings."""
    res = ModeResult(mode=mode)
    flags = MODE_FLAGS[mode]
    cmd = [
        str(eval_bin),
        "-json",
        "-top-k", str(top_k),
        "-depth", "3",
        *flags,
        to_windows_path(db),
        query,
    ]
    t0 = time.time()
    try:
        # bytes + UTF-8 decode (see index_repo for context)
        result = subprocess.run(cmd, capture_output=True, timeout=300)
    except subprocess.TimeoutExpired:
        res.note = "eval timed out"
        res.duration_s = time.time() - t0
        return res
    res.duration_s = time.time() - t0
    stdout_text = result.stdout.decode("utf-8", errors="replace")
    stderr_text = result.stderr.decode("utf-8", errors="replace")
    if result.returncode != 0:
        res.note = f"exit {result.returncode}: {stderr_text[:200]}"
        return res

    try:
        data = json.loads(stdout_text)
    except json.JSONDecodeError as e:
        res.note = f"json parse: {e}"
        return res

    # Build the entity list for this mode.
    if mode == "hybrid-agent":
        agent = data.get("code_localize_agent") or {}
        if data.get("code_localize_agent_error"):
            res.note = data["code_localize_agent_error"][:200]
            return res
        # Agent's entities have qualified_name and file_path.
        entities = list(agent.get("entities") or [])
        res.turns = int(agent.get("turns") or 0)
        res.input_tokens = int(agent.get("input_tokens") or 0)
        res.output_tokens = int(agent.get("output_tokens") or 0)
        res.cost_usd = COST_PER_AGENT_QUERY_USD if entities else 0.0
    else:
        # Primitives modes: union of rank_by_query + code_localize
        entities = list(data.get("rank_by_query") or []) + list(data.get("code_localize") or [])
    # Compact summary of what was scored, for the per-instance report.
    res.rank_section = json.dumps(
        [
            {"q": e.get("qualified_name", "")[-80:], "f": e.get("file_path", "")}
            for e in entities[:5]
        ]
    )
    res.file_hit, res.class_hit, res.func_hit = score_entities(entities, ground_truth)
    return res


def select_instances(
    df: pd.DataFrame,
    n: int,
    seed: int,
    explicit_ids: list[str] | None,
    categories: list[str] | None,
) -> pd.DataFrame:
    if explicit_ids:
        return df[df["instance_id"].isin(explicit_ids)].copy()
    if categories:
        sub = df[df["category"].isin(categories)]
        return sub.sample(n=min(n, len(sub)), random_state=seed).copy()
    # If user requested >= total, just return everything (avoids the
    # balanced-sampler under-fill problem when smaller categories cap
    # the per-cat target — e.g. n=560 on Loc-Bench V1 was returning 448
    # because Performance and Security categories are smaller than n/4).
    if n >= len(df):
        return df.copy()
    # Default: balanced across all 4 categories, with top-up when smaller
    # categories underfill their quota.
    target = n // 4
    rows: list[dict[str, Any]] = []
    seen_ids: set[str] = set()
    for cat in ["Bug Report", "Feature Request", "Performance Issue", "Security Vulnerability"]:
        sub = df[df["category"] == cat]
        if len(sub) == 0:
            continue
        picked = sub.sample(n=min(target, len(sub)), random_state=seed).to_dict("records")
        for p in picked:
            if p["instance_id"] not in seen_ids:
                rows.append(p)
                seen_ids.add(p["instance_id"])
    # Top up: fill remaining slots from any category that still has
    # un-picked rows. Use a deterministic shuffle.
    if len(rows) < n:
        remaining = df[~df["instance_id"].isin(seen_ids)]
        if len(remaining) > 0:
            extra = remaining.sample(
                n=min(n - len(rows), len(remaining)),
                random_state=seed + 1,
            ).to_dict("records")
            for p in extra:
                rows.append(p)
                seen_ids.add(p["instance_id"])
    return pd.DataFrame(rows[:n])


def evaluate_instance(
    row: dict[str, Any],
    workdir: Path,
    modes: list[str],
    eval_bin: Path,
    index_bin: Path,
    max_mb: float,
    keep_index: bool,
    keep_clone: bool,
) -> InstanceResult:
    iid = row["instance_id"]
    repo = row["repo"]
    res = InstanceResult(
        instance_id=iid,
        repo=repo,
        category=row.get("category", "Unknown"),
        base_commit=row["base_commit"],
        ground_truth=list(row.get("edit_functions", [])),
    )
    print(f"\n=== {iid} ({repo}, {res.category}) ===")
    print(f"  ground truth ({len(res.ground_truth)}): {res.ground_truth[:2]}")

    repo_dir = workdir / iid
    db = db_path_for(repo_dir)

    # Step 1: clone (skip if cached + flag set)
    if repo_dir.exists() and keep_clone:
        print(f"  clone: cached at {repo_dir}")
        res.cloned = True
    else:
        print("  clone: starting...")
        if clone_repo(repo, row["base_commit"], repo_dir):
            res.cloned = True
            print("  clone: done")
        else:
            res.note = "clone failed"
            return res

    # Step 2: size check
    res.repo_size_mb = repo_size_mb(repo_dir)
    if max_mb > 0 and res.repo_size_mb > max_mb:
        res.note = f"repo too large ({res.repo_size_mb:.0f} MB > {max_mb:.0f})"
        if not keep_clone:
            shutil.rmtree(repo_dir, ignore_errors=True)
        return res

    # Step 3: index (skip if DB cached + flag set)
    if db.exists() and keep_index:
        print(f"  index: cached at {db.name}")
        res.indexed = True
    else:
        print("  index: starting...")
        if index_repo(repo_dir, index_bin):
            res.indexed = True
            print("  index: done")
        else:
            res.note = "index failed"
            return res

    if not db.exists():
        res.note = f"db not at {db.name}"
        return res

    # Step 4: run each mode
    short_query = pick_query_from_issue(row["problem_statement"])
    for mode in modes:
        print(f"  mode {mode}: ", end="", flush=True)
        m = run_mode(eval_bin, db, short_query, mode, res.ground_truth)
        res.mode_results.append(m)
        print(
            f"file={'Y' if m.file_hit else 'N'} "
            f"class={'Y' if m.class_hit else 'N'} "
            f"func={'Y' if m.func_hit else 'N'} "
            f"({m.duration_s:.0f}s"
            + (f", {m.input_tokens}/{m.output_tokens}tok ${m.cost_usd:.3f}" if m.mode == "hybrid-agent" else "")
            + (f", note={m.note}" if m.note else "")
            + ")"
        )

    # Cleanup if not keeping
    if not keep_index:
        try:
            db.unlink(missing_ok=True)
            Path(str(db) + "-shm").unlink(missing_ok=True)
            Path(str(db) + "-wal").unlink(missing_ok=True)
        except OSError:
            pass
    if not keep_clone:
        shutil.rmtree(repo_dir, ignore_errors=True)
    return res


def write_report(
    results: list[InstanceResult],
    modes: list[str],
    output: Path,
    binary_label: str,
    max_mb: float,
) -> None:
    lines: list[str] = []
    lines.append(f"# Loc-Bench multi-mode comparison — {time.strftime('%Y-%m-%d %H:%M')}")
    lines.append("")
    lines.append(f"**Binary:** {binary_label}")
    lines.append(f"**Modes compared:** {', '.join(modes)}")
    lines.append(f"**Repo size cap:** {max_mb:.0f} MB" + (" (no cap)" if max_mb == 0 else ""))
    lines.append("")

    # Aggregate per-mode hit counts (only on instances that ran ALL modes)
    mode_aggregate: dict[str, dict[str, float]] = {m: {"attempted": 0, "file": 0, "class": 0, "func": 0, "cost": 0.0} for m in modes}
    n_indexed = 0
    for r in results:
        if not r.indexed:
            continue
        n_indexed += 1
        for m in r.mode_results:
            agg = mode_aggregate[m.mode]
            agg["attempted"] += 1
            if m.file_hit:
                agg["file"] += 1
            if m.class_hit:
                agg["class"] += 1
            if m.func_hit:
                agg["func"] += 1
            agg["cost"] += m.cost_usd

    lines.append("## Aggregate")
    lines.append("")
    lines.append(f"Instances attempted: {len(results)} | Indexed: {n_indexed}")
    lines.append("")
    lines.append("| Mode | Attempted | File hits | Class hits | Func hits | Total $ |")
    lines.append("|---|---|---|---|---|---|")
    for m in modes:
        agg = mode_aggregate[m]
        att = agg["attempted"]
        if att == 0:
            lines.append(f"| {m} | 0 | - | - | - | - |")
            continue
        lines.append(
            f"| {m} | {att} | {agg['file']}/{att} ({100*agg['file']/att:.0f}%) | "
            f"{agg['class']}/{att} ({100*agg['class']/att:.0f}%) | "
            f"{agg['func']}/{att} ({100*agg['func']/att:.0f}%) | "
            f"${agg['cost']:.2f} |"
        )
    lines.append("")

    lines.append("## Per-instance details")
    lines.append("")
    lines.append("| instance | category | size (MB) | indexed | " + " | ".join(f"{m} F/C/Fn" for m in modes) + " | note |")
    lines.append("|" + "|".join(["---"] * (4 + len(modes) + 1)) + "|")
    for r in results:
        cells = [r.instance_id, r.category, f"{r.repo_size_mb:.0f}", "Y" if r.indexed else "N"]
        for m in modes:
            mr = next((x for x in r.mode_results if x.mode == m), None)
            if mr is None:
                cells.append("-")
            else:
                cells.append(
                    f"{'Y' if mr.file_hit else 'N'}/"
                    f"{'Y' if mr.class_hit else 'N'}/"
                    f"{'Y' if mr.func_hit else 'N'}"
                )
        cells.append(r.note)
        lines.append("| " + " | ".join(cells) + " |")
    lines.append("")

    output.write_text("\n".join(lines) + "\n", encoding="utf-8")
    print(f"\nReport: {output}")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--instances", help="Comma-separated instance IDs (overrides --n)")
    ap.add_argument("--n", type=int, default=20)
    ap.add_argument("--seed", type=int, default=42)
    ap.add_argument("--categories", help="Comma-separated category names to filter")
    ap.add_argument(
        "--modes",
        default="substring-primitives,hybrid-primitives,hybrid-agent",
        help="Comma-separated mode list",
    )
    ap.add_argument("--max-mb", type=float, default=1000, help="Repo size cap (0 = none)")
    ap.add_argument("--workdir", type=Path, default=Path(r"C:/tmp/locbench-batch"))
    ap.add_argument("--output", type=Path, required=True)
    ap.add_argument("--eval-bin", type=Path, default=DEFAULT_EVAL_BIN)
    ap.add_argument("--index-bin", type=Path, default=DEFAULT_INDEX_BIN)
    ap.add_argument("--binary-label", default="current main", help="Label for the report")
    ap.add_argument("--keep-index", action="store_true", help="Don't delete DB after run")
    ap.add_argument("--keep-clone", action="store_true", help="Don't delete clone after run")
    ap.add_argument(
        "--workers",
        type=int,
        default=1,
        help="Parallel worker count (1 = sequential). Each worker gets its own clone subdir.",
    )
    args = ap.parse_args()

    modes = [m.strip() for m in args.modes.split(",") if m.strip()]
    for m in modes:
        if m not in MODE_FLAGS:
            print(f"unknown mode: {m}; valid: {list(MODE_FLAGS)}", file=sys.stderr)
            return 2
    if "hybrid-agent" in modes and not os.environ.get("ANTHROPIC_API_KEY"):
        print("ERROR: ANTHROPIC_API_KEY required for hybrid-agent mode", file=sys.stderr)
        return 2

    df = pd.read_parquet(PARQUET)
    print(f"Loc-Bench instances: {len(df)}")
    print(f"Categories: {sorted(df['category'].unique())}")

    explicit = [s.strip() for s in args.instances.split(",")] if args.instances else None
    cats = [c.strip() for c in args.categories.split(",")] if args.categories else None
    selected = select_instances(df, args.n, args.seed, explicit, cats)
    print(f"Selected {len(selected)} instance(s)")

    args.workdir.mkdir(parents=True, exist_ok=True)
    results: list[InstanceResult] = []

    rows = [row.to_dict() for _, row in selected.iterrows()]

    # Checkpointing: write the report after every instance completes,
    # not just at end-of-run. Long benchmarks (n=560 took >2 hours
    # before being killed) need progress preserved across kills. The
    # final report is identical; checkpointing just rewrites it
    # incrementally so a kill at instance K leaves K rows of data.
    def _checkpoint() -> None:
        ordered = sorted(results, key=lambda r: order.get(r.instance_id, 9999))
        write_report(ordered, modes, args.output, args.binary_label, args.max_mb)

    order = {row["instance_id"]: i for i, row in enumerate(rows)}

    if args.workers <= 1:
        # Sequential path
        for row in rows:
            try:
                res = evaluate_instance(
                    row, args.workdir, modes,
                    args.eval_bin, args.index_bin,
                    args.max_mb, args.keep_index, args.keep_clone,
                )
            except KeyboardInterrupt:
                print("\n!!! interrupted")
                break
            except Exception as e:
                res = InstanceResult(
                    instance_id=row["instance_id"],
                    repo=row["repo"],
                    category=row.get("category", "Unknown"),
                    base_commit=row["base_commit"],
                    ground_truth=list(row.get("edit_functions", [])),
                    note=f"exception: {e!r}",
                )
            results.append(res)
            _checkpoint()
    else:
        # Parallel path. Each worker gets its own subdir under workdir to
        # avoid clone collisions on repos that happen to share names. The
        # DB cache dir is shared but each repo has a distinct path-derived
        # filename, so concurrent indexes don't conflict.
        print(f"Running with {args.workers} parallel workers...")
        # All workers share the parent workdir. Each instance has a
        # unique subdir name (the instance_id) so there's no clone
        # collision. Sharing the workdir means workers see and re-use
        # any DB the previous run cached.
        worker_args = [
            (
                row, str(args.workdir), modes,
                str(args.eval_bin), str(args.index_bin),
                args.max_mb, args.keep_index, args.keep_clone,
            )
            for row in rows
        ]
        with ProcessPoolExecutor(max_workers=args.workers) as ex:
            futures = {ex.submit(_evaluate_one_worker, *a): a[0]["instance_id"] for a in worker_args}
            for fut in as_completed(futures):
                iid = futures[fut]
                try:
                    res = fut.result()
                except Exception as e:
                    res = InstanceResult(
                        instance_id=iid,
                        repo="",
                        category="",
                        base_commit="",
                        ground_truth=[],
                        note=f"worker exception: {e!r}",
                    )
                results.append(res)
                _checkpoint()
        # Final sort (checkpoint already wrote sorted output)
        results.sort(key=lambda r: order.get(r.instance_id, 9999))

    write_report(results, modes, args.output, args.binary_label, args.max_mb)
    return 0


def _evaluate_one_worker(
    row: dict[str, Any],
    workdir_str: str,
    modes: list[str],
    eval_bin_str: str,
    index_bin_str: str,
    max_mb: float,
    keep_index: bool,
    keep_clone: bool,
) -> InstanceResult:
    """Worker entrypoint for ProcessPoolExecutor. Pickle-friendly args."""
    workdir = Path(workdir_str)
    workdir.mkdir(parents=True, exist_ok=True)
    return evaluate_instance(
        row, workdir, modes,
        Path(eval_bin_str), Path(index_bin_str),
        max_mb, keep_index, keep_clone,
    )


if __name__ == "__main__":
    sys.exit(main())
