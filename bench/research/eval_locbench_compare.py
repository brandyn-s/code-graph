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
import hashlib
import json
import os
import re
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

# Stable snapshot cache for Loc-Bench instances (2026-05-12).
# Motivated by the finding that 67 of 200 Loc-Bench instances are
# permanently unreachable via live GitHub clone (PR base_commits
# garbage-collected after PR closure). The cache stores tarballs of
# successfully-cloned repos at SHA so future runs are reproducible
# even if the source GitHub ref is gone.
SNAPSHOT_CACHE_DIR = Path.home() / ".cache" / "locbench-snapshots"
SWH_API_BASE = "https://archive.softwareheritage.org/api/1"

# Module-level globals so worker subprocesses (which pickle the
# function call site, not the args namespace) see the user's flag
# choices for the snapshot-cache + SWH strategies. Set by main() after
# argparse runs.
_USE_SNAPSHOT_CACHE: bool = True
_USE_SOFTWARE_HERITAGE: bool = False

# SCORER_SCHEMA_VERSION — bump when score_entities changes shape or
# semantics. Provenance comparison treats this as a hard equality
# field: two reports with different scorer schemas cannot be compared.
# History:
#   1: initial schema (file/class/func tuple)
#   2: ACC-012 fix — class_hit treats module-level GTs as scope_hit
SCORER_SCHEMA_VERSION = 2

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


# ─── Fix 3: per-instance resumable checkpoint (added 2026-05-11) ────────
# Persist InstanceResult to <workdir>/<iid>/.eval-checkpoint.json on
# successful run; on next invocation, skip if the checkpoint shows the
# instance already completed (i.e., at least one mode_result with non-empty
# `mode`). Eliminates the $0.05 × N cost of re-running the agent on
# instances that already succeeded in a prior attempt.
CHECKPOINT_NAME = ".eval-checkpoint.json"


def _result_to_json(res: "InstanceResult") -> dict:
    return {
        "schema_version": 1,
        "instance_id": res.instance_id,
        "repo": res.repo,
        "category": res.category,
        "base_commit": res.base_commit,
        "ground_truth": list(res.ground_truth),
        "cloned": res.cloned,
        "indexed": res.indexed,
        "repo_size_mb": res.repo_size_mb,
        "note": res.note,
        "mode_results": [
            {
                "mode": m.mode,
                "file_hit": m.file_hit,
                "class_hit": m.class_hit,
                "func_hit": m.func_hit,
                "rank_section": m.rank_section,
                "duration_s": m.duration_s,
                "note": m.note,
                "turns": m.turns,
                "input_tokens": m.input_tokens,
                "output_tokens": m.output_tokens,
                "cost_usd": m.cost_usd,
            }
            for m in res.mode_results
        ],
    }


def _result_from_json(d: dict) -> "InstanceResult":
    res = InstanceResult(
        instance_id=d["instance_id"],
        repo=d["repo"],
        category=d.get("category", "Unknown"),
        base_commit=d["base_commit"],
        ground_truth=list(d.get("ground_truth", [])),
        cloned=bool(d.get("cloned", False)),
        indexed=bool(d.get("indexed", False)),
        repo_size_mb=float(d.get("repo_size_mb", 0.0)),
        note=str(d.get("note", "")),
    )
    for m in d.get("mode_results", []):
        res.mode_results.append(ModeResult(
            mode=m["mode"],
            file_hit=bool(m.get("file_hit", False)),
            class_hit=bool(m.get("class_hit", False)),
            func_hit=bool(m.get("func_hit", False)),
            rank_section=str(m.get("rank_section", "")),
            duration_s=float(m.get("duration_s", 0.0)),
            note=str(m.get("note", "")),
            turns=int(m.get("turns", 0)),
            input_tokens=int(m.get("input_tokens", 0)),
            output_tokens=int(m.get("output_tokens", 0)),
            cost_usd=float(m.get("cost_usd", 0.0)),
        ))
    return res


def _load_checkpoint(repo_dir: Path, required_modes: list[str]) -> "InstanceResult | None":
    """Return cached InstanceResult if checkpoint exists AND has results for
    all required_modes. Returns None otherwise (run from scratch).
    """
    cp = repo_dir / CHECKPOINT_NAME
    if not cp.exists():
        return None
    try:
        d = json.loads(cp.read_text(encoding="utf-8"))
    except (json.JSONDecodeError, OSError):
        return None
    if d.get("schema_version") != 1:
        return None
    res = _result_from_json(d)
    # Require all requested modes to be present in the checkpoint;
    # otherwise re-run so partial state doesn't masquerade as complete.
    present = {m.mode for m in res.mode_results}
    if not all(m in present for m in required_modes):
        return None
    return res


def _save_checkpoint(repo_dir: Path, res: "InstanceResult") -> None:
    """Persist InstanceResult to <repo_dir>/.eval-checkpoint.json. Best-effort;
    failures are logged but not raised (a missing checkpoint just means the
    next run will redo this instance — no correctness impact).
    """
    cp = repo_dir / CHECKPOINT_NAME
    try:
        repo_dir.mkdir(parents=True, exist_ok=True)
        cp.write_text(json.dumps(_result_to_json(res), indent=2), encoding="utf-8")
    except OSError as e:
        print(f"  checkpoint save failed (non-fatal): {e}")


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


_PR_NUMBER_RE = re.compile(r"-(\d+)$")


def _instance_pr_number(instance_id: str) -> str | None:
    """Extract PR number from a Loc-Bench instance ID.

    Instance IDs follow the SWE-Bench convention: 'org__repo-NNNN' where
    NNNN is the issue or PR number. Loc-Bench instances are PR-derived so
    NNNN is typically a valid PR number on GitHub.
    """
    m = _PR_NUMBER_RE.search(instance_id)
    return m.group(1) if m else None


def _check_reachable(repo: str, base_commit: str, timeout: int = 30) -> bool:
    """Fix B helper: check if `git ls-remote --exit-code <url> <sha>` succeeds.

    A successful exit means the SHA is reachable as a ref on the remote.
    This catches the failure mode where a PR base_commit is no longer
    referenceable (deleted PR branch, force-push, garbage-collected).

    Returns True if reachable, False otherwise. Also returns True (safe
    default) on unexpected errors so we don't pre-exclude on transient
    network glitches.
    """
    url = f"https://github.com/{repo}.git"
    try:
        result = subprocess.run(
            ["git", "ls-remote", "--exit-code", url, base_commit],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            timeout=timeout,
        )
        # exit 0 = found; exit 2 = not found; other = error (assume reachable)
        if result.returncode == 2:
            return False
        return True
    except subprocess.TimeoutExpired:
        return True  # transient — let the clone retry handle it
    except Exception:
        return True


def preflight_reachability(rows: list[dict], workers: int = 8) -> tuple[list[dict], list[dict]]:
    """Fix B: parallel reachability check on every row's (repo, base_commit).

    Returns (reachable, unreachable) row lists.

    Wall: ~0.5s/row with workers=8 (network-bound). Costs zero in $ since
    `git ls-remote` doesn't clone anything.
    """
    from concurrent.futures import ThreadPoolExecutor, as_completed as _aw

    print(f"[preflight-reachability] checking {len(rows)} instances...", flush=True)
    t0 = time.time()
    reachable: list[dict] = []
    unreachable: list[dict] = []
    with ThreadPoolExecutor(max_workers=workers) as ex:
        future_to_row = {
            ex.submit(_check_reachable, r["repo"], r["base_commit"]): r
            for r in rows
        }
        for fut in _aw(future_to_row):
            row = future_to_row[fut]
            try:
                ok = fut.result()
            except Exception:
                ok = True  # safe default
            if ok:
                reachable.append(row)
            else:
                unreachable.append(row)
    elapsed = time.time() - t0
    print(
        f"[preflight-reachability] done in {elapsed:.1f}s — "
        f"{len(reachable)} reachable, {len(unreachable)} unreachable",
        flush=True,
    )
    if unreachable:
        print("  unreachable instances (will be PRE-EXCLUDED):", flush=True)
        for r in unreachable[:20]:
            print(f"    - {r['instance_id']} ({r['repo']} @ {r['base_commit'][:8]})", flush=True)
        if len(unreachable) > 20:
            print(f"    ... and {len(unreachable) - 20} more", flush=True)
    return reachable, unreachable


def _snapshot_tarball_path(repo: str, base_commit: str) -> Path:
    """Local cache path for a (repo, base_commit) tarball.

    Path A (2026-05-12) — stable-snapshot cache. Once we've successfully
    cloned a repo at a given SHA, we tarball it here so future runs are
    independent of live GitHub state. Filename format:
        <org>__<name>__<sha>.tar.gz
    Path-safe: the org__name pattern matches Loc-Bench's instance_id
    convention so cache hits are findable by inspection.
    """
    safe = repo.replace("/", "__").replace("\\", "__")
    return SNAPSHOT_CACHE_DIR / f"{safe}__{base_commit}.tar.gz"


def _try_snapshot_cache(repo: str, base_commit: str, dest: Path) -> bool:
    """Strategy 0 (Path A): extract from local snapshot cache if present.

    The fastest possible clone — pure disk read, no network, no git
    operations. Returns True if cache hit + extraction succeeded.
    """
    tarball = _snapshot_tarball_path(repo, base_commit)
    if not tarball.exists():
        return False
    import tarfile
    try:
        dest.parent.mkdir(parents=True, exist_ok=True)
        with tarfile.open(tarball, "r:gz") as tf:
            tf.extractall(dest, filter="data")
        return dest.exists()
    except (OSError, tarfile.TarError) as e:
        print(f"  snapshot-cache extract failed: {e}")
        shutil.rmtree(dest, ignore_errors=True)
        return False


def _save_snapshot_cache(repo: str, base_commit: str, src: Path) -> None:
    """Save a successful clone to the snapshot cache so future runs reuse it.

    Best-effort: failure to write the cache is non-fatal (eval still has
    the working clone in src). Called after any successful clone via
    strategies 1/2/3/4 — NOT when strategy 0 already hit.
    """
    tarball = _snapshot_tarball_path(repo, base_commit)
    if tarball.exists():
        return
    import tarfile
    try:
        SNAPSHOT_CACHE_DIR.mkdir(parents=True, exist_ok=True)
        # Write to .tmp first then rename for atomicity
        tmp = tarball.with_suffix(".tar.gz.tmp")
        with tarfile.open(tmp, "w:gz") as tf:
            tf.add(src, arcname=".")
        tmp.replace(tarball)
        print(f"  snapshot saved: {tarball.name}")
    except (OSError, tarfile.TarError) as e:
        print(f"  snapshot save failed (non-fatal): {e}")


def _try_software_heritage(repo: str, base_commit: str, dest: Path) -> bool:
    """Strategy 4 (Path A): fetch via Software Heritage archive.

    SWH archives every public GitHub commit. When live GitHub has GC'd
    the ref, SWH may still have it. We use the vault API to request a
    git-bare tarball.

    SWH vault is async — initial request returns 200 with status, must
    poll until status='done'. We do at most 6 poll attempts, 10s apart
    (~60s wall max). If the vault prep takes longer or the revision
    isn't archived, we give up and let the caller fall through.

    Requires `urllib` (stdlib) and `tarfile` (stdlib). No third-party
    deps. Returns True on successful extract.
    """
    import urllib.request
    import urllib.error
    import json as _json
    import tarfile

    swhid = f"swh:1:rev:{base_commit}"
    vault_url = f"{SWH_API_BASE}/vault/git-bare/{swhid}/"

    print(f"  trying Software Heritage vault: {swhid}")

    # Step 1: kick off (or check existing) vault preparation
    try:
        # POST to start the cook; if already done, returns status=done
        req = urllib.request.Request(vault_url, method="POST")
        with urllib.request.urlopen(req, timeout=30) as resp:
            payload = _json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as e:
        if e.code == 404:
            print(f"  SWH: revision not archived ({e.code})")
            return False
        print(f"  SWH POST failed: HTTP {e.code}")
        return False
    except (urllib.error.URLError, OSError) as e:
        print(f"  SWH POST failed: {e}")
        return False

    status = payload.get("status", "unknown")
    fetch_url = payload.get("fetch_url")

    # Step 2: poll until status=done (max ~60s)
    for poll in range(6):
        if status == "done" and fetch_url:
            break
        if status == "failed":
            print(f"  SWH vault prep failed: {payload.get('progress_message', '')}")
            return False
        time.sleep(10)
        try:
            with urllib.request.urlopen(vault_url, timeout=30) as resp:
                payload = _json.loads(resp.read().decode("utf-8"))
            status = payload.get("status", "unknown")
            fetch_url = payload.get("fetch_url")
        except (urllib.error.URLError, urllib.error.HTTPError, OSError) as e:
            print(f"  SWH poll #{poll+1} failed: {e}")
            return False
    if status != "done" or not fetch_url:
        print(f"  SWH vault not ready after 60s (status={status}); giving up")
        return False

    # Step 3: download the git-bare tarball + extract
    dest.parent.mkdir(parents=True, exist_ok=True)
    bare_tar = dest.parent / f".swh-{base_commit[:12]}.tar"
    try:
        with urllib.request.urlopen(fetch_url, timeout=300) as resp:
            bare_tar.write_bytes(resp.read())
        with tarfile.open(bare_tar, "r:*") as tf:
            tf.extractall(dest.parent / f".swh-{base_commit[:12]}-bare", filter="data")
        bare_dir = dest.parent / f".swh-{base_commit[:12]}-bare"
        # The bare dir layout is <root>/<sha>.git/ — find it
        bare_subdirs = [d for d in bare_dir.iterdir() if d.is_dir() and d.name.endswith(".git")]
        if not bare_subdirs:
            print(f"  SWH tarball missing .git directory")
            return False
        bare_repo = bare_subdirs[0]
        # Clone the bare repo into dest with checkout at the commit
        subprocess.run(
            ["git", "clone", "--quiet", str(bare_repo), str(dest)],
            check=True, timeout=120,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        subprocess.run(
            ["git", "-C", str(dest), "checkout", base_commit],
            check=True, timeout=60,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        # Cleanup intermediate artifacts
        bare_tar.unlink(missing_ok=True)
        shutil.rmtree(bare_dir, ignore_errors=True)
        return True
    except (subprocess.CalledProcessError, subprocess.TimeoutExpired,
            urllib.error.URLError, urllib.error.HTTPError,
            tarfile.TarError, OSError) as e:
        print(f"  SWH fetch/extract failed: {e}")
        bare_tar.unlink(missing_ok=True)
        shutil.rmtree(dest.parent / f".swh-{base_commit[:12]}-bare", ignore_errors=True)
        shutil.rmtree(dest, ignore_errors=True)
        return False


def clone_repo(repo: str, base_commit: str, dest: Path,
               instance_id: str | None = None,
               use_snapshot_cache: bool = True,
               use_software_heritage: bool = False) -> bool:
    """Clone {repo} at {base_commit} into {dest}.

    Strategy order (added 2026-05-12 — Path A stable-snapshot work):
      0. Local snapshot cache hit → extract tarball (instant, no network)
      1-3. _clone_repo_once: git fetch-by-sha → full clone → pull/N/head
      4. (optional, --use-software-heritage) Software Heritage vault

    Fix C (2026-05-12): wraps strategies 1-3 in a retry-once loop. On
    failure, sleep 30s and retry. Catches transient GitHub blips.

    On any successful non-cache clone, the result is saved to the
    snapshot cache for future runs.
    """
    if dest.exists():
        return True

    # Strategy 0: local snapshot cache
    if use_snapshot_cache and _try_snapshot_cache(repo, base_commit, dest):
        print(f"  clone: hit snapshot cache")
        return True

    # Strategies 1-3 via the existing retry-once wrapper
    for attempt in range(2):
        if attempt == 1:
            print(f"  clone: retrying after 30s (attempt 2 of 2)")
            time.sleep(30)
            shutil.rmtree(dest, ignore_errors=True)
        if _clone_repo_once(repo, base_commit, dest, instance_id):
            if use_snapshot_cache:
                _save_snapshot_cache(repo, base_commit, dest)
            return True

    # Strategy 4: Software Heritage (only if explicitly enabled — slow async)
    if use_software_heritage:
        if _try_software_heritage(repo, base_commit, dest):
            if use_snapshot_cache:
                _save_snapshot_cache(repo, base_commit, dest)
            return True

    return False


def _clone_repo_once(repo: str, base_commit: str, dest: Path, instance_id: str | None = None) -> bool:
    """One full clone-attempt pass: strategies 1+2+3.

    Tries three strategies in order:
      1. init + fetch-by-sha (fast; needs uploadpack.allowAnySHA1InWant on
         the GitHub server side — works for most main-branch SHAs).
      2. full clone of default branch + checkout base_commit.
      3. (added 2026-05-11 after attempt-2 saw 143 'clone failed' on Feature
         Request instances): when an instance_id is provided AND it ends in
         '-NNNN', try `git fetch origin pull/NNNN/head` and then checkout
         the base_commit. Many Loc-Bench Feature Request instances have
         base_commits on PR branches GitHub no longer exposes for arbitrary
         SHA fetch but DOES expose via the `pull/N/head` ref.
    """
    if dest.exists():
        return True
    dest.parent.mkdir(parents=True, exist_ok=True)
    url = f"https://github.com/{repo}.git"
    last_err = ""

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
        except subprocess.CalledProcessError as e:
            last_err = "strategy1 fetch-by-sha: " + e.stderr.decode("utf-8", errors="replace")[:160]
            # Strategy 2: full clone, then checkout
            shutil.rmtree(dest, ignore_errors=True)
            try:
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
            except subprocess.CalledProcessError as e2:
                last_err = "strategy2 full-clone-then-checkout: " + e2.stderr.decode("utf-8", errors="replace")[:160]

    except subprocess.CalledProcessError as e:
        last_err = "strategy1 init/remote: " + e.stderr.decode("utf-8", errors="replace")[:160]
    except subprocess.TimeoutExpired:
        last_err = "strategy1/2 timed out"
        shutil.rmtree(dest, ignore_errors=True)

    # Strategy 3: PR branch fetch (added 2026-05-11). Only fires when
    # strategies 1+2 failed AND we can extract a PR number from instance_id.
    pr_number = _instance_pr_number(instance_id) if instance_id else None
    if pr_number:
        shutil.rmtree(dest, ignore_errors=True)
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
            subprocess.run(
                ["git", "-C", str(dest), "fetch", "--quiet", "--depth=50",
                 "origin", f"pull/{pr_number}/head:pr-{pr_number}"],
                check=True, timeout=600,
                stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            )
            # The base_commit should now be reachable from the fetched PR ref
            subprocess.run(
                ["git", "-C", str(dest), "checkout", base_commit],
                check=True, timeout=120,
                stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            )
            return True
        except subprocess.CalledProcessError as e3:
            last_err = (
                "strategy3 pull/" + pr_number + "/head: "
                + e3.stderr.decode("utf-8", errors="replace")[:160]
            )
        except subprocess.TimeoutExpired:
            last_err = f"strategy3 pull/{pr_number}/head: timed out"

    print(f"  clone failed: {last_err}")
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
                # GT has no class component — the enclosing scope IS the
                # module/file. Treat class_hit as scope_hit: when the agent
                # identified the right file, it has identified the correct
                # enclosing scope. Without this fix, ~34% of Loc-Bench
                # Python instances (those targeting module-level functions)
                # would force class_hit=False regardless of agent output —
                # an instrument bug discovered 2026-05-04 (the column was
                # incorrectly reported as -24pp behind LocAgent's "module"
                # column when in fact we were measuring different metrics).
                class_hit = True
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
        # 2026-05-03: bumped 300s -> 600s after n=560 partial run timed out on
        # vllm-project__vllm-9390 (huge index). Big repos in the corpus
        # (vllm, scikit-learn, pandas, matplotlib) need more headroom for the
        # agent loop's BFS calls.
        result = subprocess.run(cmd, capture_output=True, timeout=600)
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

    # Fix 3: resumable state — if a checkpoint exists and covers all
    # requested modes, return it without re-running the agent.
    if keep_clone or keep_index:
        cached_res = _load_checkpoint(repo_dir, modes)
        if cached_res is not None:
            print(f"  CHECKPOINT HIT — skipping (cached {len(cached_res.mode_results)} mode result(s))")
            for m in cached_res.mode_results:
                print(
                    f"  mode {m.mode}: file={'Y' if m.file_hit else 'N'} "
                    f"class={'Y' if m.class_hit else 'N'} "
                    f"func={'Y' if m.func_hit else 'N'} "
                    f"({m.duration_s:.0f}s, cached)"
                )
            return cached_res

    # Step 1: clone (skip if cached + flag set)
    if repo_dir.exists() and keep_clone:
        print(f"  clone: cached at {repo_dir}")
        res.cloned = True
    else:
        print("  clone: starting...")
        # Path A flags propagate via module-level globals (set in main()
        # before the parallel pool spawns; pickled into workers).
        if clone_repo(
            repo, row["base_commit"], repo_dir,
            instance_id=iid,
            use_snapshot_cache=globals().get("_USE_SNAPSHOT_CACHE", True),
            use_software_heritage=globals().get("_USE_SOFTWARE_HERITAGE", False),
        ):
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

    # Fix 3: save checkpoint after successful mode runs. We only persist
    # when keep_clone is True — if the user wants ephemeral clones, there's
    # nowhere stable to put the checkpoint.
    if keep_clone and res.mode_results:
        _save_checkpoint(repo_dir, res)

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


def _file_sha256_short(path: Path) -> str:
    """Return short (12-char) sha256 of a file, or '<missing>' if absent."""
    if not path.exists():
        return "<missing>"
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()[:12]


def _git_sha_short() -> str:
    """Return the harness repo's HEAD SHA (12 char), or '<no-git>' if not a repo."""
    try:
        out = subprocess.run(
            ["git", "rev-parse", "HEAD"],
            cwd=str(REPO_ROOT),
            capture_output=True,
            text=True,
            timeout=5,
        )
        if out.returncode == 0:
            return out.stdout.strip()[:12]
    except (subprocess.SubprocessError, FileNotFoundError):
        pass
    return "<no-git>"


def _compute_provenance_manifest(
    eval_bin: Path,
    index_bin: Path,
    modes: list[str],
    max_mb: float,
    n_attempted: int,
    n_indexed: int,
) -> dict[str, str]:
    """Family B leg of the measurement-discipline pay-down. Compute
    provenance fields for the report manifest. These fields capture
    the generation context of the published numbers so two reports
    can be compared meaningfully.

    Catches the bug-class where two reports look comparable on the
    surface (same metric, same benchmark, similar percentages) but
    were generated against different binary/index/dataset versions
    — incident #1 (stale post-hoc baseline) and incidents #5-7
    (parallel-session stale-cache baseline) all share this shape.

    Per the back-port, Family B catches 4/7 documented incidents —
    the largest count of any single gate. Cannot be deferred.
    """
    return {
        "harness_sha": _git_sha_short(),
        "scorer_schema": str(SCORER_SCHEMA_VERSION),
        "eval_bin_sha": _file_sha256_short(eval_bin),
        "index_bin_sha": _file_sha256_short(index_bin),
        "dataset_sha": _file_sha256_short(PARQUET),
        "agent_iterations": os.environ.get("LOCAGENT_ITERATIONS", "1"),
        "modes": ",".join(modes),
        "max_mb": str(int(max_mb)),
        "n_attempted": str(n_attempted),
        "n_indexed": str(n_indexed),
        "timestamp_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }


# Manifest fields that MUST agree between two reports for them to be
# meaningfully comparable. Other fields (timestamp, n_attempted) can
# differ legitimately (e.g. two runs at different times on the same
# instance set).
PROVENANCE_COMPARE_KEYS = (
    "harness_sha",
    "scorer_schema",
    "eval_bin_sha",
    "index_bin_sha",
    "dataset_sha",
    "agent_iterations",
    "modes",
    "max_mb",
)


def _render_provenance_table(manifest: dict[str, str]) -> list[str]:
    """Render the manifest as a markdown table for embedding in the
    report header. Order is fixed for stable parsing."""
    order = [
        "harness_sha", "scorer_schema", "eval_bin_sha", "index_bin_sha",
        "dataset_sha", "agent_iterations", "modes", "max_mb",
        "n_attempted", "n_indexed", "timestamp_utc",
    ]
    lines = ["## Provenance manifest", ""]
    lines.append("| field | value |")
    lines.append("|---|---|")
    for k in order:
        v = manifest.get(k, "")
        lines.append(f"| {k} | `{v}` |")
    lines.append("")
    return lines


def _parse_provenance_manifest(report_path: Path) -> dict[str, str] | None:
    """Parse a Provenance manifest table out of an existing report
    markdown file. Returns None if the report doesn't have a manifest
    (e.g. older report from before Family B shipped)."""
    if not report_path.exists():
        return None
    try:
        text = report_path.read_text(encoding="utf-8")
    except OSError:
        return None
    lines = text.splitlines()
    in_table = False
    fields: dict[str, str] = {}
    for line in lines:
        if line.startswith("## Provenance manifest"):
            in_table = True
            continue
        if in_table:
            if line.startswith("##"):  # next section
                break
            if line.startswith("|") and not line.startswith("|---") and not line.startswith("| field"):
                parts = [p.strip() for p in line.strip().strip("|").split("|")]
                if len(parts) >= 2:
                    key = parts[0]
                    # Strip backticks from value
                    val = parts[1].strip("`")
                    fields[key] = val
    return fields if fields else None


def _check_provenance_match(
    current_manifest: dict[str, str],
    baseline_manifest: dict[str, str],
    accept_provenance_mismatch: str | None = None,
) -> list[str]:
    """Family B leg of the measurement-discipline pay-down. Compare
    current run's provenance to a baseline report's provenance.
    REFUSE if any compared field differs (per PROVENANCE_COMPARE_KEYS),
    unless --accept-provenance-mismatch REASON is set.

    Catches the bug-class where two reports' percentages are compared
    on the assumption they used the same binary/index/dataset/scorer
    — the most common shape across incidents 1, 5, 6, 7.
    """
    out: list[str] = []
    for key in PROVENANCE_COMPARE_KEYS:
        cur = current_manifest.get(key, "<absent>")
        base = baseline_manifest.get(key, "<absent>")
        if cur != base:
            msg = (
                f"provenance mismatch on '{key}': current=`{cur}` baseline=`{base}`. "
                f"Comparing numbers across this mismatch may compare apples to oranges."
            )
            if accept_provenance_mismatch:
                out.append(
                    f"[ACCEPTED via --accept-provenance-mismatch] {msg} "
                    f"(reason: {accept_provenance_mismatch})"
                )
            else:
                out.append(
                    f"REFUSE: {msg} Pass `--accept-provenance-mismatch \"REASON\"` "
                    f"to override after verifying the difference is benign."
                )
    return out


def _check_report_invariants(
    results: list[InstanceResult],
    modes: list[str],
    accept_non_monotone: str | None = None,
    allow_unexplained_cells: bool = False,
    external_comparator: str | None = None,
    metric_equivalence_note: Path | None = None,
    current_manifest: dict[str, str] | None = None,
    baseline_manifest: dict[str, str] | None = None,
    accept_provenance_mismatch: str | None = None,
) -> list[str]:
    """Mechanical refusal gate (Family C of the measurement-discipline
    pay-down). Returns a list of human-readable violation strings.
    Empty list = report is clean to publish.

    Three gates:

    1. **Monotonicity** — for each mode, file_pct >= class_pct >= func_pct
       within 5pp tolerance. Loc-Bench has a hierarchy: getting the file
       right is necessary but not sufficient for getting the class right,
       which is necessary but not sufficient for getting the function
       right. A non-monotone result indicates either a real semantic gap
       or — more commonly per the 2026-05-04 backport — a scoring bug
       (e.g. ACC-012, where class_hit was forced False for module-level
       GTs). Override with `--accept-non-monotone REASON`.

    2. **Cell-mass** — for each granularity column, if any category
       holds ≥30% of total misses in that column, the cell is "dominant"
       and must be classified (instrument bug vs real failure mode) per
       the verify-instrument-before-fix T1 rule before the report can
       be published. Override with `--allow-unexplained-cells` (only
       valid for exploratory runs, not for any published comparison).

    3. **External-comparator equivalence** — if `--external-comparator
       NAME` is set, `--metric-equivalence-note PATH` must also be set
       (pointing to a doc that verifies our metric definitions match
       the external system's). Catches the failure shape of incident
       #4: comparing our `class` Acc@10 to LocAgent's `module` Acc@10
       column where the underlying definitions diverge.
    """
    violations: list[str] = []

    indexed = [r for r in results if r.indexed]
    n = len(indexed)
    if n == 0:
        # No data to check yet (early checkpoint); skip
        return violations

    # GATE 1: Monotonicity per mode
    TOL = 0.05  # 5pp tolerance for ties / very-small samples
    for mode in modes:
        f = c = fn = 0
        att = 0
        for r in indexed:
            mr = next((x for x in r.mode_results if x.mode == mode), None)
            if mr is None:
                continue
            att += 1
            if mr.file_hit:
                f += 1
            if mr.class_hit:
                c += 1
            if mr.func_hit:
                fn += 1
        if att == 0:
            continue
        f_pct, c_pct, fn_pct = f / att, c / att, fn / att
        # file >= class >= func (within tolerance)
        if c_pct > f_pct + TOL:
            msg = (
                f"non-monotone {mode}: class={c_pct:.1%} > file={f_pct:.1%} "
                f"(violation > 5pp tolerance). Class hit cannot exceed file "
                f"hit on Loc-Bench; this is the shape that surfaced ACC-012."
            )
            if accept_non_monotone:
                violations.append(f"[ACCEPTED via --accept-non-monotone] {msg} (reason: {accept_non_monotone})")
            else:
                violations.append(
                    f"REFUSE: {msg} Pass `--accept-non-monotone \"REASON\"` "
                    f"to override after verifying instrument."
                )
        if fn_pct > c_pct + TOL:
            msg = (
                f"non-monotone {mode}: func={fn_pct:.1%} > class={c_pct:.1%} "
                f"(violation > 5pp tolerance). Function hit cannot exceed "
                f"class hit on Loc-Bench; this is exactly the ACC-012 shape."
            )
            if accept_non_monotone:
                violations.append(f"[ACCEPTED via --accept-non-monotone] {msg} (reason: {accept_non_monotone})")
            else:
                violations.append(
                    f"REFUSE: {msg} Pass `--accept-non-monotone \"REASON\"` "
                    f"to override after verifying instrument."
                )

    # GATE 2: Cell-mass — any category holding ≥30% of misses per column
    if not allow_unexplained_cells:
        for mode in modes:
            for col, getter in (
                ("file", lambda mr: mr.file_hit),
                ("class", lambda mr: mr.class_hit),
                ("func", lambda mr: mr.func_hit),
            ):
                cat_misses: dict[str, int] = {}
                total_misses = 0
                for r in indexed:
                    mr = next((x for x in r.mode_results if x.mode == mode), None)
                    if mr is None or getter(mr):
                        continue  # hit, not a miss
                    cat = r.category or "Unknown"
                    cat_misses[cat] = cat_misses.get(cat, 0) + 1
                    total_misses += 1
                if total_misses < 5:
                    continue  # too few misses to be meaningful
                for cat, miss_count in cat_misses.items():
                    share = miss_count / total_misses
                    if share >= 0.30:
                        violations.append(
                            f"REFUSE: dominant-cell on {mode}.{col}: category "
                            f"'{cat}' holds {miss_count}/{total_misses} = "
                            f"{share:.0%} of misses (≥30% threshold). Per the "
                            f"verify-instrument-before-fix T1 rule, sample 3-5 "
                            f"misses from this cell and classify INSTRUMENT vs "
                            f"REAL before publishing. Pass "
                            f"`--allow-unexplained-cells` for exploratory runs only."
                        )

    # GATE 4 (Family B): Provenance comparison against baseline report
    if current_manifest and baseline_manifest:
        violations.extend(
            _check_provenance_match(
                current_manifest, baseline_manifest, accept_provenance_mismatch
            )
        )

    # GATE 3: External-comparator equivalence pairing
    if external_comparator and not metric_equivalence_note:
        violations.append(
            f"REFUSE: --external-comparator '{external_comparator}' set but "
            f"--metric-equivalence-note PATH not provided. Comparing our "
            f"metrics to an external system's published numbers requires "
            f"a documented metric-definition equivalence check (incident "
            f"#4 shape: our `class` is not LocAgent's `module`). Provide a "
            f"path to a doc that verifies the metrics measure the same thing."
        )
    if metric_equivalence_note and not metric_equivalence_note.exists():
        violations.append(
            f"REFUSE: --metric-equivalence-note path {metric_equivalence_note} "
            f"does not exist."
        )

    return violations


def write_report(
    results: list[InstanceResult],
    modes: list[str],
    output: Path,
    binary_label: str,
    max_mb: float,
    accept_non_monotone: str | None = None,
    allow_unexplained_cells: bool = False,
    external_comparator: str | None = None,
    metric_equivalence_note: Path | None = None,
    eval_bin: Path | None = None,
    index_bin: Path | None = None,
    baseline_report: Path | None = None,
    accept_provenance_mismatch: str | None = None,
) -> None:
    lines: list[str] = []
    lines.append(f"# Loc-Bench multi-mode comparison — {time.strftime('%Y-%m-%d %H:%M')}")
    lines.append("")
    lines.append(f"**Binary:** {binary_label}")
    lines.append(f"**Modes compared:** {', '.join(modes)}")
    lines.append(f"**Repo size cap:** {max_mb:.0f} MB" + (" (no cap)" if max_mb == 0 else ""))
    lines.append("")

    # Family B: provenance manifest. Embed at the top of every report
    # so two reports can be compared meaningfully. Cannot be deferred
    # per the back-port (catches 4/7 documented incidents — most of
    # any single gate).
    n_indexed_pre = sum(1 for r in results if r.indexed)
    current_manifest = _compute_provenance_manifest(
        eval_bin or DEFAULT_EVAL_BIN,
        index_bin or DEFAULT_INDEX_BIN,
        modes,
        max_mb,
        n_attempted=len(results),
        n_indexed=n_indexed_pre,
    )
    baseline_manifest = (
        _parse_provenance_manifest(baseline_report) if baseline_report else None
    )
    lines.extend(_render_provenance_table(current_manifest))

    # Mechanical refusal gate (Family C + Family B comparison).
    # Violations embed at the top of the report; the runner prints a
    # stderr warning at end-of-run if any unaccepted violations remain.
    # The report still writes (so checkpointing works) but is clearly
    # marked as REFUSED.
    violations = _check_report_invariants(
        results, modes,
        accept_non_monotone=accept_non_monotone,
        allow_unexplained_cells=allow_unexplained_cells,
        external_comparator=external_comparator,
        metric_equivalence_note=metric_equivalence_note,
        current_manifest=current_manifest,
        baseline_manifest=baseline_manifest,
        accept_provenance_mismatch=accept_provenance_mismatch,
    )
    unaccepted = [v for v in violations if v.startswith("REFUSE:")]
    if violations:
        lines.append("## ⚠ Report invariants check")
        lines.append("")
        if unaccepted:
            lines.append(
                "**This report is REFUSED for publication or external comparison** "
                "until the violations below are either fixed or explicitly accepted "
                "with the appropriate override flag. The data below is preserved "
                "for debugging only — do not cite these numbers."
            )
            lines.append("")
        for v in violations:
            lines.append(f"- {v}")
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

    # Fix 5: stratified failure reporting (added 2026-05-11).
    # Surfaces clone/index/agent funnel and per-category failure breakdown
    # so a reader can distinguish "57 instances cloned successfully then
    # ran cleanly" from "143 didn't clone, 57 of the rest ran cleanly" —
    # the second framing was hidden behind generic "Indexed: 57" before.
    n_cloned = sum(1 for r in results if r.cloned)
    n_attempted = len(results)
    n_agent_ran = sum(
        1 for r in results
        if any(mr.mode == "hybrid-agent" for mr in r.mode_results)
    )

    # Categorize note strings into failure buckets
    fail_buckets: dict[str, list[InstanceResult]] = {
        "clone failed": [],
        "too large": [],
        "index failed": [],
        "worker exception": [],
        "other": [],
    }
    for r in results:
        if r.cloned and r.indexed and r.mode_results:
            continue  # success
        note = (r.note or "").lower()
        if "clone failed" in note:
            fail_buckets["clone failed"].append(r)
        elif "too large" in note:
            fail_buckets["too large"].append(r)
        elif "index failed" in note or "db not at" in note:
            fail_buckets["index failed"].append(r)
        elif "worker exception" in note:
            fail_buckets["worker exception"].append(r)
        else:
            fail_buckets["other"].append(r)

    lines.append("## Funnel (Fix 5: stratified failure reporting)")
    lines.append("")
    lines.append("| Stage | n | % of attempted |")
    lines.append("|---|---:|---:|")
    pct = lambda n: f"{100*n/n_attempted:.1f}%" if n_attempted else "—"
    lines.append(f"| Attempted | {n_attempted} | 100.0% |")
    lines.append(f"| Cloned | {n_cloned} | {pct(n_cloned)} |")
    lines.append(f"| Indexed | {n_indexed} | {pct(n_indexed)} |")
    lines.append(f"| Agent ran | {n_agent_ran} | {pct(n_agent_ran)} |")
    lines.append("")

    total_failures = sum(len(v) for v in fail_buckets.values())
    if total_failures:
        lines.append("### Failure breakdown by stage")
        lines.append("")
        lines.append("| Failure mode | n |")
        lines.append("|---|---:|")
        for bucket, items in fail_buckets.items():
            if items:
                lines.append(f"| {bucket} | {len(items)} |")
        lines.append("")

        # Per-category failure breakdown — catches "all FR clone-fail" pattern
        # that was the 2026-05-11 attempt-2 finding.
        cat_failure: dict[str, dict[str, int]] = {}
        for bucket, items in fail_buckets.items():
            for r in items:
                cat = r.category or "Unknown"
                cat_failure.setdefault(cat, {})
                cat_failure[cat][bucket] = cat_failure[cat].get(bucket, 0) + 1
        if cat_failure:
            lines.append("### Failure breakdown by category × stage")
            lines.append("")
            buckets_present = sorted({b for cf in cat_failure.values() for b in cf})
            lines.append("| Category | " + " | ".join(buckets_present) + " |")
            lines.append("|" + "|".join(["---"] + ["---:"] * len(buckets_present)) + "|")
            for cat in sorted(cat_failure):
                row = [cat]
                for b in buckets_present:
                    row.append(str(cat_failure[cat].get(b, 0)))
                lines.append("| " + " | ".join(row) + " |")
            lines.append("")

    # Methodology caveat when stage-attrition is high.
    indexed_pct = (n_indexed / n_attempted) if n_attempted else 1.0
    if indexed_pct < 0.9:
        lines.append("> **Methodology caveat**: n_indexed / n_attempted = "
                     f"{n_indexed}/{n_attempted} ({indexed_pct*100:.0f}%). "
                     "Aggregate hit-rates above are computed against the "
                     "indexed subset, NOT the attempted set. Results are "
                     "NOT directly comparable to baselines where indexed == "
                     "attempted unless the missing instances are random "
                     "wrt difficulty. See failure breakdown above to assess.")
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
    # ─── Fail-fast fixes (Fixes A+B+D, 2026-05-12) ─────────────────────
    # Motivated by the 2026-05-11/12 re-baseline session where 143/200
    # instances clone-failed silently in attempt 2 (50 min wasted before
    # I noticed). These flags add real-time abort, pre-flight pruning,
    # and progress-summary output so the failure mode surfaces immediately
    # instead of after-the-fact in results.md.
    ap.add_argument(
        "--abort-on-clone-fail-rate", type=float, default=0.3,
        help="Fix A: abort the run if clone-failure rate exceeds this "
             "fraction after at least 10 instances have been attempted. "
             "Default 0.3 (= 30%%). Set to 1.0 to disable. Aborts with "
             "exit code 4 and prints rate + count to stderr.",
    )
    ap.add_argument(
        "--preflight-reachability", action="store_true",
        help="Fix B: before the main loop, parallel `git ls-remote "
             "--exit-code <repo> <base_commit>` for every instance. "
             "Mark unreachable instances as PRE-EXCLUDED and skip them; "
             "report up-front. Adds ~0.5s/instance with workers=8. "
             "Strongly recommended for n>=50.",
    )
    ap.add_argument(
        "--progress-every", type=int, default=10,
        help="Fix D: emit `[progress] M/N attempted, X cloned, Y indexed, "
             "Z agent_ran, F clone_failed` line every N completed "
             "instances. Default 10. Set to 0 to disable.",
    )
    # ─── Path A: stable snapshot cache (2026-05-12) ────────────────────
    # Motivated by the finding that 67/200 Loc-Bench instances are
    # permanently unreachable via live GitHub clone (PRs closed, refs
    # GC'd). Local cache + Software Heritage fallback give reproducible
    # runs even after the live GitHub state decays.
    ap.add_argument(
        "--no-snapshot-cache", action="store_true",
        help="Path A: disable the local snapshot cache (~/.cache/locbench-snapshots/). "
             "By default the harness checks the cache before any clone "
             "(strategy 0) and saves successful clones to it. Disable only "
             "for debugging — the cache is the primary mechanism for "
             "reproducibility across GitHub-decay events.",
    )
    ap.add_argument(
        "--use-software-heritage", action="store_true",
        help="Path A: enable Software Heritage vault fallback (strategy 4). "
             "After all GitHub-based strategies (1-3) fail, query SWH for "
             "an archived copy of the commit and fetch via the vault API. "
             "Adds ~30-120s wait per fallback (vault prep is async). Off "
             "by default to keep n=200 runs fast; turn on when re-baselining "
             "and you want maximum coverage on hard-to-clone instances.",
    )
    ap.add_argument(
        "--workers",
        type=int,
        default=1,
        help="Parallel worker count (1 = sequential). Each worker gets its own clone subdir.",
    )

    # Family C — mechanical refusal gate flags
    ap.add_argument(
        "--accept-non-monotone",
        type=str,
        default=None,
        metavar="REASON",
        help=(
            "Accept non-monotone hierarchy (e.g. class > file or func > class) "
            "with a documented reason. Required override when monotonicity check fires."
        ),
    )
    ap.add_argument(
        "--allow-unexplained-cells",
        action="store_true",
        help=(
            "Bypass cell-mass dominant-cell check (≥30% of misses in one "
            "category × column). Only valid for exploratory runs, NOT for "
            "any published comparison. Per verify-instrument-before-fix, "
            "dominant cells should be sampled and classified before publication."
        ),
    )
    ap.add_argument(
        "--external-comparator",
        type=str,
        default=None,
        metavar="NAME",
        help=(
            "Name of an external system whose published numbers this report "
            "compares against (e.g. 'locagent', 'repomem', 'swerank'). "
            "When set, --metric-equivalence-note PATH must also be provided."
        ),
    )
    ap.add_argument(
        "--metric-equivalence-note",
        type=Path,
        default=None,
        metavar="PATH",
        help=(
            "Path to a doc that verifies our metric definitions match the "
            "external comparator's. Required pair with --external-comparator."
        ),
    )

    # Family B — provenance manifest comparison flags
    ap.add_argument(
        "--baseline-report",
        type=Path,
        default=None,
        metavar="PATH",
        help=(
            "Path to a prior report whose provenance manifest should be "
            "compared against this run's. If any compared field differs "
            "(harness SHA, eval/index binary SHA, dataset SHA, scorer "
            "schema, agent_iterations, modes, max_mb), report is REFUSED "
            "unless --accept-provenance-mismatch REASON is set. Catches "
            "the bug-class where two reports' percentages are compared "
            "across mismatched generation contexts (incidents 1, 5, 6, 7)."
        ),
    )
    ap.add_argument(
        "--accept-provenance-mismatch",
        type=str,
        default=None,
        metavar="REASON",
        help=(
            "Accept provenance mismatch with a documented reason. Required "
            "override when --baseline-report mismatches the current run."
        ),
    )

    args = ap.parse_args()

    # Path A: propagate snapshot-cache + SWH flags into module-level
    # globals so worker subprocesses (which pickle the function call
    # site, not the args namespace) can see the user's choices.
    global _USE_SNAPSHOT_CACHE, _USE_SOFTWARE_HERITAGE
    _USE_SNAPSHOT_CACHE = not getattr(args, "no_snapshot_cache", False)
    _USE_SOFTWARE_HERITAGE = bool(getattr(args, "use_software_heritage", False))
    if _USE_SNAPSHOT_CACHE:
        SNAPSHOT_CACHE_DIR.mkdir(parents=True, exist_ok=True)
        existing = list(SNAPSHOT_CACHE_DIR.glob("*.tar.gz"))
        if existing:
            print(f"[snapshot-cache] {len(existing)} cached tarball(s) "
                  f"in {SNAPSHOT_CACHE_DIR} — strategy 0 will use these "
                  f"before any live clone")
    if _USE_SOFTWARE_HERITAGE:
        print(f"[software-heritage] strategy 4 enabled — SWH vault fallback "
              f"for GitHub-GC'd revisions")

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

    # Fix B: pre-flight reachability check (2026-05-12).
    # Strips instances whose base_commit can't be reached via git ls-remote
    # BEFORE we spend wall time trying to clone them. The excluded rows
    # are appended to results with note=PRE-EXCLUDED so the funnel reports
    # them honestly.
    preflight_excluded: list[InstanceResult] = []
    if getattr(args, "preflight_reachability", False):
        rows, unreachable = preflight_reachability(rows, workers=max(args.workers * 2, 8))
        for ur in unreachable:
            preflight_excluded.append(InstanceResult(
                instance_id=ur["instance_id"],
                repo=ur["repo"],
                category=ur.get("category", "Unknown"),
                base_commit=ur["base_commit"],
                ground_truth=list(ur.get("edit_functions", [])),
                note="PRE-EXCLUDED: base_commit unreachable via git ls-remote",
            ))

    # Checkpointing: write the report after every instance completes,
    # not just at end-of-run. Long benchmarks (n=560 took >2 hours
    # before being killed) need progress preserved across kills. The
    # final report is identical; checkpointing just rewrites it
    # incrementally so a kill at instance K leaves K rows of data.
    def _checkpoint() -> None:
        ordered = sorted(results, key=lambda r: order.get(r.instance_id, 9999))
        write_report(
            ordered, modes, args.output, args.binary_label, args.max_mb,
            accept_non_monotone=args.accept_non_monotone,
            allow_unexplained_cells=args.allow_unexplained_cells,
            external_comparator=args.external_comparator,
            metric_equivalence_note=args.metric_equivalence_note,
            eval_bin=args.eval_bin,
            index_bin=args.index_bin,
            baseline_report=args.baseline_report,
            accept_provenance_mismatch=args.accept_provenance_mismatch,
        )

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
        # Fixes A+D state (2026-05-12)
        abort_threshold = float(args.abort_on_clone_fail_rate)
        progress_every = int(args.progress_every)
        aborted = False
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

                # Fix D: periodic progress summary line
                if progress_every > 0 and (len(results) % progress_every == 0):
                    n_done = len(results)
                    n_cloned = sum(1 for r in results if r.cloned)
                    n_indexed = sum(1 for r in results if r.indexed)
                    n_agent = sum(
                        1 for r in results
                        if any(mr.mode == "hybrid-agent" for mr in r.mode_results)
                    )
                    n_clone_failed = sum(
                        1 for r in results if "clone failed" in (r.note or "").lower()
                    )
                    fail_rate = n_clone_failed / n_done if n_done else 0.0
                    print(
                        f"[progress] {n_done}/{len(worker_args)} attempted, "
                        f"{n_cloned} cloned, {n_indexed} indexed, "
                        f"{n_agent} agent_ran, {n_clone_failed} clone_failed "
                        f"(rate {fail_rate*100:.1f}%)",
                        flush=True,
                    )

                # Fix A: real-time abort if clone-failure rate is too high
                n_done = len(results)
                if n_done >= 10 and abort_threshold < 1.0:
                    n_clone_failed = sum(
                        1 for r in results if "clone failed" in (r.note or "").lower()
                    )
                    fail_rate = n_clone_failed / n_done
                    if fail_rate > abort_threshold:
                        print(
                            f"\n[FATAL] Fix A abort: clone-failure rate "
                            f"{fail_rate*100:.1f}% ({n_clone_failed}/{n_done}) "
                            f"exceeds --abort-on-clone-fail-rate threshold "
                            f"{abort_threshold*100:.1f}%. "
                            f"Cancelling remaining {len(worker_args)-n_done} workers. "
                            f"Diagnose with `git ls-remote` on a sample failed instance, "
                            f"check GitHub status, or re-run with "
                            f"--abort-on-clone-fail-rate=1.0 to disable this gate.",
                            file=sys.stderr,
                            flush=True,
                        )
                        for f in futures:
                            f.cancel()
                        aborted = True
                        break
        # Merge any Fix-B pre-excluded rows in so the funnel reports them
        if preflight_excluded:
            results.extend(preflight_excluded)
        # Final sort (checkpoint already wrote sorted output)
        results.sort(key=lambda r: order.get(r.instance_id, 9999))

        if aborted:
            # Write whatever results we have so the user can see the funnel
            write_report(
                results, modes, args.output, args.binary_label, args.max_mb,
                accept_non_monotone=args.accept_non_monotone,
                allow_unexplained_cells=True,  # bypass refusal — this is an abort report
                external_comparator=args.external_comparator,
                metric_equivalence_note=args.metric_equivalence_note,
                eval_bin=args.eval_bin,
                index_bin=args.index_bin,
                baseline_report=args.baseline_report,
                accept_provenance_mismatch=args.accept_provenance_mismatch,
            )
            return 4

    write_report(
        results, modes, args.output, args.binary_label, args.max_mb,
        accept_non_monotone=args.accept_non_monotone,
        allow_unexplained_cells=args.allow_unexplained_cells,
        external_comparator=args.external_comparator,
        metric_equivalence_note=args.metric_equivalence_note,
        eval_bin=args.eval_bin,
        index_bin=args.index_bin,
        baseline_report=args.baseline_report,
        accept_provenance_mismatch=args.accept_provenance_mismatch,
    )

    # Family C + B: print stderr warning at end-of-run if any
    # unaccepted violations remain. The report itself has the
    # violations embedded at the top; this is the runtime signal.
    n_idx = sum(1 for r in results if r.indexed)
    final_manifest = _compute_provenance_manifest(
        args.eval_bin, args.index_bin, modes, args.max_mb,
        n_attempted=len(results), n_indexed=n_idx,
    )
    final_baseline = (
        _parse_provenance_manifest(args.baseline_report)
        if args.baseline_report else None
    )
    final_violations = _check_report_invariants(
        results, modes,
        accept_non_monotone=args.accept_non_monotone,
        allow_unexplained_cells=args.allow_unexplained_cells,
        external_comparator=args.external_comparator,
        metric_equivalence_note=args.metric_equivalence_note,
        current_manifest=final_manifest,
        baseline_manifest=final_baseline,
        accept_provenance_mismatch=args.accept_provenance_mismatch,
    )
    unaccepted = [v for v in final_violations if v.startswith("REFUSE:")]
    if unaccepted:
        print(
            f"\n!!! REPORT REFUSED: {len(unaccepted)} unaccepted invariant "
            f"violation(s). See top of {args.output} for details. Do NOT "
            f"cite these numbers in any external comparison.",
            file=sys.stderr,
        )
        return 3  # distinct exit code for invariant failure

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
