"""Run a Loc-Bench subset against our localization tools and score F1 per instance.

Loop over N selected Loc-Bench instances and for each:

  1. Read the instance from the parquet (problem_statement, repo, base_commit,
     edit_functions ground truth).
  2. Clone the repo at the recorded base_commit into a working dir.
  3. Index it with our code-graph binary (VOYAGE_API_KEY enables
     embedding seeds for the hybrid strategy).
  4. Run the eval harness against the resulting DB with -agent (LLM loop)
     and -seed-strategy=hybrid.
  5. Score: did the ground-truth file or class appear in the agent's
     finalized entities? Record per-instance hit/miss + token usage.
  6. After every instance, check accumulated estimated LLM cost and
     stop if the configured advisory threshold is exceeded.
  7. Write a summary table at the end.

This script is the harness for the Phase B / V1 deliverable from the
2026-04-25 superplan: turn the n=1 hit on pypa__pip-13085 (PR #82) into
a defensible N=20 benchmark claim.

The script is INTENTIONALLY conservative about cost:

  - Stops when accumulated estimated cost exceeds --budget-usd (default $3).
    This is not a provider-enforced billing cap and may overshoot by one case.
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
import hashlib
import json
import math
import os
import random
import shutil
import subprocess
import sys
import time
from dataclasses import dataclass, field
from decimal import Decimal, InvalidOperation
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
# Binary names are platform-dependent (the harness originally ran on
# Windows; .exe-less names are the macOS/Linux builds of the same code).
_BIN_EXT = ".exe" if os.name == "nt" else ""
EVAL_BIN = REPO_ROOT / f"bench/research/eval_rank_localize/eval{_BIN_EXT}"
INDEX_BIN = REPO_ROOT / f"bin/code-graph{_BIN_EXT}"
CACHE_DIR = Path.home() / ".cache" / "code-graph"

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
RUNTIME_CHILD_ENV_KEYS = {
    "HOME",
    "HTTPS_PROXY",
    "HTTP_PROXY",
    "LANG",
    "LC_ALL",
    "NO_PROXY",
    "PATH",
    "SSL_CERT_DIR",
    "SSL_CERT_FILE",
    "SYSTEMROOT",
    "SystemRoot",
    "TEMP",
    "TMP",
    "TMPDIR",
    "USERPROFILE",
    "WINDIR",
}
GRAPH_INDEX_CHILD_ENV_KEYS = RUNTIME_CHILD_ENV_KEYS | {
    "VOYAGE_API_KEY",
    "VOYAGE_EMBED_MODEL",
}
GRAPH_AGENT_CHILD_ENV_KEYS = RUNTIME_CHILD_ENV_KEYS | {
    "ANTHROPIC_API_KEY",
    "ANTHROPIC_MODEL",
    "LOCAGENT_ITERATIONS",
    "VOYAGE_API_KEY",
    "VOYAGE_EMBED_MODEL",
}


def _sha256_file(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def expected_graph_checkpoint_contract(
    *,
    graph_sha: str,
    canonical_pin: Path,
    parquet: Path,
    repository: str,
    expected_instance_ids: list[str],
    model: str,
    embedding_model: str,
    iterations: int,
    score_depth: int,
    graph_budget_usd: str,
) -> dict[str, Any]:
    """Derive the complete graph contract independently from runtime inputs."""
    if not (
        len(graph_sha) == 40
        and all(character in "0123456789abcdef" for character in graph_sha)
    ):
        raise ValueError("graph SHA must be a lowercase 40-character Git SHA")
    if not repository or not model or not embedding_model:
        raise ValueError("repository and model identities are required")
    if iterations < 1 or score_depth < 1:
        raise ValueError("iterations and score depth must be positive")
    try:
        budget = Decimal(graph_budget_usd)
    except InvalidOperation as exc:
        raise ValueError("graph budget must be an exact decimal") from exc
    if not budget.is_finite() or budget < 0:
        raise ValueError("graph budget must be a finite nonnegative decimal")
    if len(expected_instance_ids) != len(set(expected_instance_ids)):
        raise ValueError("graph checkpoint contract IDs must be unique")
    return {
        "schema_version": 1,
        "arm": "graph",
        "graph_sha": graph_sha,
        "pin_sha256": _sha256_file(canonical_pin),
        "dataset_sha256": _sha256_file(parquet),
        "repository": repository,
        "expected_instance_ids": list(expected_instance_ids),
        "model": model,
        "embedding_model": embedding_model,
        "iterations": iterations,
        "score_depth": score_depth,
        "graph_budget_usd": graph_budget_usd,
        "harness_sha256": _sha256_file(Path(__file__).resolve()),
        "scorer_sha256": _sha256_file(
            Path(__file__).resolve().parent / "pilot_compare.py"
        ),
    }


def validate_graph_checkpoint_contract(
    observed: object,
    expected: dict[str, Any],
) -> dict[str, Any]:
    if observed != expected:
        raise ValueError(
            "graph checkpoint contract does not match the bound runtime inputs"
        )
    return dict(expected)


def write_graph_checkpoint(path: Path, payload: dict[str, Any]) -> None:
    """Atomically replace and durably fsync a graph checkpoint."""
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.{os.getpid()}.tmp")
    try:
        with temporary.open("w", encoding="utf-8") as handle:
            json.dump(payload, handle, indent=2)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
        directory_fd = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    finally:
        temporary.unlink(missing_ok=True)


def allowlisted_child_env(
    allowed_keys: set[str] | frozenset[str],
    *,
    overrides: dict[str, str] | None = None,
) -> dict[str, str]:
    """Build a positive-allowlist environment for one child process."""
    child_env = {
        name: value
        for name, value in os.environ.items()
        if name in allowed_keys
    }
    if overrides:
        unexpected = sorted(set(overrides) - set(allowed_keys))
        if unexpected:
            raise ValueError(f"child environment override is not allowlisted: {unexpected}")
        child_env.update(overrides)
    return child_env


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
    failure_class: str = ""
    failure_code: str = ""
    duration_s: float = 0.0
    latency_s: dict[str, float] = field(default_factory=dict)
    # Plan 4 T1: full structured JSON envelope from eval_rank_localize -json,
    # including per-iteration entity lists when LOCAGENT_ITERATIONS>=2.
    # Populated only when --per-case-json is passed. Discarded otherwise
    # to keep the markdown report path unaffected.
    agent_json: dict[str, Any] = field(default_factory=dict)
    index_identity: dict[str, Any] = field(default_factory=dict)
    embedding_identity: dict[str, Any] = field(default_factory=dict)
    attempts: list[dict[str, Any]] = field(default_factory=list)


@dataclass(frozen=True)
class GraphIndexOutcome:
    success: bool
    error: str = ""
    failure_class: str = ""
    failure_code: str = ""
    index_identity: dict[str, Any] = field(default_factory=dict)
    embedding_count: int = 0
    embedding_model: str = ""

    def __bool__(self) -> bool:
        return self.success


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

    def record(self, result: InstanceResult) -> bool:
        self.instances.append(result)
        self.n_indexed += int(result.indexed)
        self.n_agent_ran += int(result.agent_ran)
        self.n_file_hit += int(result.file_hit)
        self.n_class_hit += int(result.class_hit)
        self.n_func_hit += int(result.func_hit)
        self.total_input_tokens += result.input_tokens
        self.total_output_tokens += result.output_tokens
        self.total_cost_usd += result.cost_estimate_usd
        if result.failure_class == "invalid_experiment":
            self.aborted_reason = (
                f"invalid_experiment:{result.failure_code}:{result.instance_id}"
            )
            return False
        return True


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


def _remove_clone_destination(dest: Path) -> bool:
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
            print(f"  clone target dir not removable: {dest}; skipping")
            return False
    return True


def _is_transient_clone_error(message: str) -> bool:
    normalized = message.lower()
    return any(
        marker in normalized
        for marker in (
            "timed out",
            "timeout",
            "connection reset",
            "connection aborted",
            "connection refused",
            "could not resolve host",
            "temporary failure",
            "temporarily unavailable",
            "http 429",
            "http 500",
            "http 502",
            "http 503",
            "http 504",
        )
    )


def clone_repo(
    repo: str,
    base_commit: str,
    dest: Path,
    *,
    max_attempts: int = 3,
    attempts_out: list[dict] | None = None,
) -> bool:
    """Clone and checkout with bounded retries for transient network failures."""
    if max_attempts < 1:
        raise ValueError("max_attempts must be positive")
    attempts = attempts_out if attempts_out is not None else []
    if not _remove_clone_destination(dest):
        return False
    dest.parent.mkdir(parents=True, exist_ok=True)
    url = f"https://github.com/{repo}.git"
    # Full clone needed because shallow + base_commit isn't reliable across
    # all GitHub repos. Tradeoff: more wall time, but deterministic.
    clone_command = ["git", "clone", "--quiet", url, str(dest)]
    for attempt_number in range(1, max_attempts + 1):
        try:
            subprocess.run(
                clone_command,
                check=True,
                timeout=600,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                env=allowlisted_child_env(RUNTIME_CHILD_ENV_KEYS),
            )
        except subprocess.CalledProcessError as exc:
            message = exc.stderr.decode("utf-8", errors="replace")[:300]
            transient = _is_transient_clone_error(message)
            retry = transient and attempt_number < max_attempts
            attempts.append(
                {
                    "operation": "clone",
                    "attempt": attempt_number,
                    "outcome": "error",
                    "error": message,
                    "transient": transient,
                    "retry": retry,
                }
            )
            if not retry:
                print(f"  clone failed: {message[:200]}")
                _remove_clone_destination(dest)
                return False
            if not _remove_clone_destination(dest):
                return False
            time.sleep(2 ** (attempt_number - 1))
        except subprocess.TimeoutExpired:
            message = "clone timed out"
            retry = attempt_number < max_attempts
            attempts.append(
                {
                    "operation": "clone",
                    "attempt": attempt_number,
                    "outcome": "error",
                    "error": message,
                    "transient": True,
                    "retry": retry,
                }
            )
            if not retry:
                print("  clone timed out")
                _remove_clone_destination(dest)
                return False
            if not _remove_clone_destination(dest):
                return False
            time.sleep(2 ** (attempt_number - 1))
        else:
            attempts.append(
                {
                    "operation": "clone",
                    "attempt": attempt_number,
                    "outcome": "success",
                    "retry": False,
                }
            )
            break

    try:
        subprocess.run(
            ["git", "-C", str(dest), "checkout", base_commit],
            check=True,
            timeout=60,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            env=allowlisted_child_env(RUNTIME_CHILD_ENV_KEYS),
        )
    except subprocess.CalledProcessError as exc:
        message = exc.stderr.decode("utf-8", errors="replace")[:300]
        attempts.append(
            {
                "operation": "checkout",
                "attempt": 1,
                "outcome": "error",
                "error": message,
                "transient": False,
                "retry": False,
            }
        )
        print(f"  checkout failed: {message[:200]}")
        return False
    except subprocess.TimeoutExpired:
        attempts.append(
            {
                "operation": "checkout",
                "attempt": 1,
                "outcome": "error",
                "error": "checkout timed out",
                "transient": False,
                "retry": False,
            }
        )
        print("  checkout timed out")
        return False
    attempts.append(
        {
            "operation": "checkout",
            "attempt": 1,
            "outcome": "success",
            "retry": False,
        }
    )
    return True


def _is_lower_hex(value: object, length: int) -> bool:
    return (
        isinstance(value, str)
        and len(value) == length
        and all(character in "0123456789abcdef" for character in value)
    )


def _validate_index_identity(identity: object) -> str:
    if not isinstance(identity, dict):
        return "index identity is absent"
    if identity.get("schema_version") != 1:
        return "index identity schema_version is not 1"
    for field_name in ("repository_id", "checkout_id", "index_generation"):
        if not _is_lower_hex(identity.get(field_name), 64):
            return f"index identity {field_name} is not a lowercase SHA-256"
    revision = identity.get("source_revision")
    if revision != "unborn" and not (
        _is_lower_hex(revision, 40) or _is_lower_hex(revision, 64)
    ):
        return "index identity source_revision is not a Git object ID"
    dirty = identity.get("dirty_fingerprint")
    if dirty != "clean" and not _is_lower_hex(dirty, 64):
        return "index identity dirty_fingerprint is invalid"
    expected_generation = hashlib.sha256(
        (
            f"{identity['repository_id']}\0{revision}\0"
            f"{identity['dirty_fingerprint']}"
        ).encode()
    ).hexdigest()
    if identity["index_generation"] != expected_generation:
        return "index identity index_generation does not match its source fields"
    captured_at = identity.get("captured_at")
    if not isinstance(captured_at, str) or not captured_at.endswith("Z"):
        return "index identity captured_at is not a UTC timestamp"
    return ""


def index_repo(path: Path) -> GraphIndexOutcome:
    """Invoke code-graph index_repository against {path}.

    Form: code-graph cli index_repository '{"path":"<abs path>"}'

    Project name is derived from the path. Embedding seeds require
    VOYAGE_API_KEY at index time."""
    if not INDEX_BIN.exists():
        print(f"  binary missing: {INDEX_BIN}")
        return GraphIndexOutcome(
            False,
            f"binary missing: {INDEX_BIN}",
            failure_class="infrastructure",
            failure_code="index_binary_missing",
        )
    args_json = json.dumps({"path": to_windows_path(path)})
    child_env = allowlisted_child_env(GRAPH_INDEX_CHILD_ENV_KEYS)
    try:
        # Capture as bytes (no text=True) and decode UTF-8 with replace.
        # text=True uses cp1252 on Windows and crashes the parent reader
        # thread when subprocess outputs non-cp1252 bytes (PR #97 fix).
        result = subprocess.run(
            [str(INDEX_BIN), "cli", "--raw", "index_repository", args_json],
            capture_output=True,
            timeout=1800,  # 30 min cap per index
            env=child_env,
        )
        if result.returncode != 0:
            err = result.stderr.decode("utf-8", errors="replace")
            # Tail, not head: the actionable error (panic, locked DB, OOM) is
            # at the END of stderr; the head is startup noise. 200-char head
            # truncation hid the real jax failure during the 2026-06-11 pilot.
            print(f"  index failed (exit {result.returncode}): ...{err[-1500:]}")
            return GraphIndexOutcome(
                False,
                f"index failed (exit {result.returncode})",
                failure_class="infrastructure",
                failure_code="index_process_failed",
            )
        try:
            payload = json.loads(result.stdout.decode("utf-8", errors="replace"))
        except json.JSONDecodeError as exc:
            error = f"index response is not JSON: {exc}"
            print(f"  {error}")
            return GraphIndexOutcome(
                False,
                error,
                failure_class="infrastructure",
                failure_code="index_response_invalid",
            )
        identity_error = _validate_index_identity(payload.get("index_identity"))
        if payload.get("identity_status") != "captured" or identity_error:
            error = identity_error or (
                f"index identity status is {payload.get('identity_status')!r}"
            )
            print(f"  {error}")
            return GraphIndexOutcome(
                False,
                error,
                failure_class="invalid_experiment",
                failure_code="index_identity_invalid",
            )
        if payload.get("embedding_status") != "captured":
            error = f"embedding inventory status is {payload.get('embedding_status')!r}"
            print(f"  {error}")
            return GraphIndexOutcome(
                False,
                error,
                failure_class="invalid_experiment",
                failure_code="embedding_identity_invalid",
            )
        embedding_count = payload.get("embedding_count")
        embedding_models = payload.get("embedding_models")
        if (
            isinstance(embedding_count, bool)
            or not isinstance(embedding_count, int)
            or embedding_count < 0
            or not isinstance(embedding_models, dict)
        ):
            error = "embedding inventory is malformed"
            print(f"  {error}")
            return GraphIndexOutcome(
                False,
                error,
                failure_class="invalid_experiment",
                failure_code="embedding_identity_invalid",
            )
        expected_model = child_env.get("VOYAGE_EMBED_MODEL") or "voyage-code-3"
        if child_env.get("VOYAGE_API_KEY"):
            if embedding_count < 1 or embedding_models != {
                expected_model: embedding_count
            }:
                error = (
                    "semantic embedding identity mismatch: "
                    f"count={embedding_count} models={embedding_models!r} "
                    f"expected_model={expected_model!r}"
                )
                print(f"  {error}")
                return GraphIndexOutcome(
                    False,
                    error,
                    failure_class="invalid_experiment",
                    failure_code="embedding_identity_mismatch",
                )
        return GraphIndexOutcome(
            True,
            index_identity=dict(payload["index_identity"]),
            embedding_count=embedding_count,
            embedding_model=expected_model if embedding_count else "",
        )
    except subprocess.TimeoutExpired:
        print("  index timed out (30min)")
        return GraphIndexOutcome(
            False,
            "index timed out (30min)",
            failure_class="infrastructure",
            failure_code="index_timeout",
        )


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


class GraphAgentEnvelopeError(ValueError):
    """A zero-exit graph-agent response that cannot be scored faithfully."""


def validate_graph_agent_envelope(
    envelope: object,
    *,
    top_k: int,
) -> dict[str, Any]:
    if not isinstance(envelope, dict):
        raise GraphAgentEnvelopeError("graph-agent envelope is not an object")
    agent = envelope.get("code_localize_agent")
    if not isinstance(agent, dict):
        raise GraphAgentEnvelopeError(
            "graph-agent envelope has no structured code_localize_agent result"
        )
    entities = agent.get("entities")
    if not isinstance(entities, list):
        raise GraphAgentEnvelopeError("graph-agent entities are not a list")
    if len(entities) > top_k:
        raise GraphAgentEnvelopeError(
            "graph-agent entities exceed the requested rank depth"
        )
    for rank, entity in enumerate(entities, start=1):
        if not isinstance(entity, dict):
            raise GraphAgentEnvelopeError(
                f"graph-agent entity at rank {rank} is not an object"
            )
        for field_name in ("qualified_name", "file_path"):
            value = entity.get(field_name)
            if not isinstance(value, str) or not value:
                raise GraphAgentEnvelopeError(
                    f"graph-agent entity at rank {rank} has invalid {field_name}"
                )
        if "label" in entity and not isinstance(entity["label"], str):
            raise GraphAgentEnvelopeError(
                f"graph-agent entity at rank {rank} has invalid label"
            )
    for field_name in ("turns", "input_tokens", "output_tokens"):
        value = agent.get(field_name)
        if (
            isinstance(value, bool)
            or not isinstance(value, int)
            or value < 0
        ):
            raise GraphAgentEnvelopeError(
                f"graph-agent {field_name} is not a nonnegative integer"
            )
    stop_reason = agent.get("stop_reason")
    if not isinstance(stop_reason, str) or not stop_reason:
        raise GraphAgentEnvelopeError("graph-agent stop_reason is absent")
    return agent


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
    result = subprocess.run(
        cmd,
        capture_output=True,
        timeout=300,
        env=allowlisted_child_env(GRAPH_AGENT_CHILD_ENV_KEYS),
    )
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
            parsed["failure_class"] = "invalid_experiment"
            parsed["failure_code"] = "agent_envelope_invalid"
            return parsed
        agent = (
            envelope.get("code_localize_agent") or {}
            if isinstance(envelope, dict)
            else {}
        )
        parsed["agent_json"] = envelope
        if isinstance(agent, dict):
            parsed["turns"] = agent.get("turns", 0)
            parsed["input_tokens"] = agent.get("input_tokens", 0)
            parsed["output_tokens"] = agent.get("output_tokens", 0)
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


def evaluate_instance(
    row: dict[str, Any],
    workdir: Path,
    json_mode: bool = False,
    score_depth: int = 10,
) -> InstanceResult:
    iid = row["instance_id"]
    repo = row["repo"]
    res = InstanceResult(
        instance_id=iid,
        repo=repo,
        category=row.get("category", "Unknown"),
        ground_truth=list(row.get("edit_functions", [])),
    )
    t0 = time.monotonic()

    def finalize_latency() -> None:
        res.duration_s = time.monotonic() - t0
        res.latency_s["total"] = res.duration_s

    print(f"\n=== {iid} ({repo}, {res.category}) ===")
    print(f"ground truth ({len(res.ground_truth)} fns): {res.ground_truth[:3]}")

    repo_dir = workdir / iid
    clone_attempts: list[dict] = []
    clone_started = time.monotonic()
    clone_succeeded = clone_repo(
        repo,
        row["base_commit"],
        repo_dir,
        attempts_out=clone_attempts,
    )
    res.latency_s["clone"] = time.monotonic() - clone_started
    if not clone_succeeded:
        res.attempts = clone_attempts
        res.failure_class = "infrastructure"
        res.failure_code = "clone_failed"
        res.note = "clone failed"
        finalize_latency()
        return res
    res.attempts = clone_attempts

    size_mb = repo_size_mb(repo_dir)
    if size_mb > MAX_REPO_MB:
        res.failure_class = "measured_outcome"
        res.failure_code = "repo_too_large"
        res.note = f"repo too large ({size_mb:.0f} MB > {MAX_REPO_MB})"
        shutil.rmtree(repo_dir, ignore_errors=True)
        finalize_latency()
        return res

    index_started = time.monotonic()
    index_outcome = index_repo(repo_dir)
    res.latency_s["index"] = time.monotonic() - index_started
    if not index_outcome:
        res.failure_class = index_outcome.failure_class
        res.failure_code = index_outcome.failure_code
        res.note = (
            index_outcome.error
            if index_outcome.failure_class
            else f"index failed: {index_outcome.error}"
        )[:300]
        shutil.rmtree(repo_dir, ignore_errors=True)
        # Clean the (possibly half-written) DB on the FAILURE path too. A
        # killed BulkWrite leaves a .bulkwrite-crash-marker; the next index
        # of the same path then refuses with Mode 7 corruption (exit 1) —
        # one crashed instance permanently poisons its own retries
        # (observed 2026-06-11: jax SIGTERM -> marker -> every retry
        # failed instantly until the DB + marker were removed).
        failed_db = db_path_for(repo_dir)
        for suffix in ("", "-shm", "-wal", ".bulkwrite-crash-marker"):
            try:
                Path(str(failed_db) + suffix).unlink(missing_ok=True)
            except OSError:
                pass
        finalize_latency()
        return res
    if (
        index_outcome.index_identity.get("source_revision") != row["base_commit"]
        or index_outcome.index_identity.get("dirty_fingerprint") != "clean"
    ):
        res.failure_class = "invalid_experiment"
        res.failure_code = "index_identity_mismatch"
        res.note = "index identity does not match the clean pinned checkout"
        shutil.rmtree(repo_dir, ignore_errors=True)
        finalize_latency()
        return res
    res.indexed = True
    res.index_identity = dict(index_outcome.index_identity)
    res.embedding_identity = {
        "status": "captured",
        "count": index_outcome.embedding_count,
        "model": index_outcome.embedding_model,
    }

    # Use only the first paragraph as the agent's query — full multi-
    # paragraph issue dilutes seed matching (verified PR #82 testing).
    short_query = row["problem_statement"].split("\n\n")[0].strip()
    db = db_path_for(repo_dir)
    if not db.exists():
        res.failure_class = "infrastructure"
        res.failure_code = "index_database_missing"
        res.note = f"db not at expected path {db.name}"
        shutil.rmtree(repo_dir, ignore_errors=True)
        finalize_latency()
        return res

    query_started = time.monotonic()
    parsed = run_agent(db, short_query, top_k=score_depth, json_mode=json_mode)
    res.latency_s["marginal_query"] = time.monotonic() - query_started
    if "error" in parsed:
        parsed_failure_class = parsed.get("failure_class")
        parsed_failure_code = parsed.get("failure_code")
        if (
            parsed_failure_class in {"invalid_experiment", "infrastructure"}
            and isinstance(parsed_failure_code, str)
            and parsed_failure_code
        ):
            res.failure_class = parsed_failure_class
            res.failure_code = parsed_failure_code
        else:
            res.failure_class = "infrastructure"
            res.failure_code = "agent_failed"
        res.note = str(parsed.get("error", "agent failed"))[:300]
    elif json_mode:
        try:
            agent = validate_graph_agent_envelope(
                parsed.get("agent_json"),
                top_k=score_depth,
            )
        except GraphAgentEnvelopeError as exc:
            res.failure_class = "invalid_experiment"
            res.failure_code = "agent_envelope_invalid"
            res.note = str(exc)[:300]
        else:
            res.agent_ran = True
            res.turns = agent["turns"]
            res.input_tokens = agent["input_tokens"]
            res.output_tokens = agent["output_tokens"]
            res.agent_json = dict(parsed["agent_json"])
    else:
        res.agent_ran = True
        res.input_tokens = parsed.get("input_tokens", 0)
        res.output_tokens = parsed.get("output_tokens", 0)
        res.turns = parsed.get("turns", 0)
    # Token-metered cost (Haiku 4.5: $1/M input, $5/M output — the agent's
    # default model). The flat $0.05 estimate underbooked heavy instances
    # ~20x (jax, 2026-06-11: 989K input tokens ≈ $1.02 actual), which made
    # the --budget-usd gate blind to real spend. Cache reads aren't
    # decomposed in the envelope, so this bills them at the full input
    # rate — a conservative overestimate.
    if res.agent_ran and (res.input_tokens or res.output_tokens):
        res.cost_estimate_usd = res.input_tokens * 1.00 / 1e6 + res.output_tokens * 5.00 / 1e6
    else:
        res.cost_estimate_usd = COST_PER_QUERY_USD_ESTIMATE if res.agent_ran else 0.0

    if res.agent_ran:
        # In json_mode, the agent's text "stdout" is a JSON envelope.
        # Score against the structured entities directly when present
        # rather than against the JSON string (which would mis-attribute
        # substring hits to keys/property names instead of file paths).
        if json_mode and res.agent_json:
            agent_block = res.agent_json["code_localize_agent"]
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

    finalize_latency()
    print(
        f"  -> indexed={res.indexed} agent={res.agent_ran} "
        f"file_hit={res.file_hit} class_hit={res.class_hit} "
        f"tokens={res.input_tokens}/{res.output_tokens} "
        f"~${res.cost_estimate_usd:.3f} ({res.duration_s:.0f}s)"
    )
    return res


def build_exception_result(
    row: dict[str, Any] | pd.Series,
    error: Exception,
) -> InstanceResult:
    """Represent an unexpected per-case exception without scoring it as a miss."""
    return InstanceResult(
        instance_id=row["instance_id"],
        repo=row["repo"],
        category=row.get("category", "Unknown"),
        ground_truth=list(row.get("edit_functions", [])),
        failure_class="infrastructure",
        failure_code="unhandled_case_exception",
        note=f"exception: {error!r}",
    )


def _build_per_case_dict(
    summary: BatchSummary,
    *,
    checkpoint_contract: dict | None = None,
) -> dict:
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
        checkpoint_contract=dict(checkpoint_contract or {}),
    )
    return record.to_dict()


def _instance_result_from_record(record: "schema.PerCaseRecord") -> InstanceResult:
    serialized = record.to_dict()
    return InstanceResult(
        instance_id=record.instance_id,
        repo=record.repo,
        category=record.category,
        ground_truth=list(record.ground_truth),
        indexed=record.indexed,
        agent_ran=record.agent_ran,
        file_hit=record.file_hit,
        class_hit=record.class_hit,
        func_hit=record.func_hit,
        turns=record.turns,
        input_tokens=record.input_tokens,
        output_tokens=record.output_tokens,
        cost_estimate_usd=record.cost_estimate_usd,
        note=record.note,
        failure_class=record.failure_class,
        failure_code=record.failure_code,
        duration_s=record.duration_s,
        latency_s=dict(record.latency_s),
        agent_json=dict(serialized["agent_envelope"]),
        index_identity=dict(record.index_identity),
        embedding_identity=dict(record.embedding_identity),
        attempts=list(record.attempts),
    )


def load_graph_resume_instances(
    path: Path,
    checkpoint_contract: dict,
    expected_instance_ids: list[str],
) -> list[InstanceResult]:
    """Load a prior raw graph shard only under an identical run contract."""
    if not path.is_file():
        raise ValueError(f"graph resume checkpoint is missing: {path}")
    payload = json.loads(path.read_text(encoding="utf-8"))
    record = schema.BatchSummaryRecord.from_dict(payload)
    if record.checkpoint_contract != checkpoint_contract:
        raise ValueError("graph resume checkpoint contract does not match this run")
    if (
        record.aborted_reason.startswith("invalid_experiment:")
        or any(
            case.failure_class == "invalid_experiment"
            for case in record.cases
        )
    ):
        raise ValueError(
            "graph resume checkpoint contains a persisted invalid_experiment abort"
        )
    for case in record.cases:
        cost = case.cost_estimate_usd
        if (
            isinstance(cost, bool)
            or not isinstance(cost, (int, float))
            or not math.isfinite(float(cost))
            or float(cost) < 0.0
        ):
            raise ValueError(
                f"graph resume case {case.instance_id} has invalid persisted cost"
            )
    ids = [case.instance_id for case in record.cases]
    duplicates = sorted(
        {instance_id for instance_id in ids if ids.count(instance_id) > 1}
    )
    if duplicates:
        raise ValueError(f"graph resume checkpoint has duplicate IDs: {duplicates}")
    extras = sorted(set(ids) - set(expected_instance_ids))
    if extras:
        raise ValueError(f"graph resume checkpoint has unexpected IDs: {extras}")
    return [_instance_result_from_record(case) for case in record.cases]


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
        failure_class=r.failure_class,
        failure_code=r.failure_code,
        latency_s=dict(r.latency_s),
        note=r.note,
        agent_envelope=envelope,
        index_identity=dict(r.index_identity),
        embedding_identity=dict(r.embedding_identity),
        attempts=list(r.attempts),
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
    ap.add_argument(
        "--instances", type=Path, default=None,
        help="Run a FIXED instance set from a pin JSON (locbench_reachability.py "
             "output; reads pinned_instance_ids) instead of a fresh random sample, "
             "so re-baselines are apples-to-apples over time. Ignores --n/--seed.")
    ap.add_argument(
        "--parquet", type=Path, default=None,
        help="Override the Loc-Bench parquet path (default: bench/research/"
             "locbench.parquet). Used by the SweRank pre-filter pilot to feed "
             "an arm-B parquet whose problem_statement has a retrieval "
             "candidate block prepended, keeping every other knob identical.")
    ap.add_argument(
        "--budget-usd",
        type=Decimal,
        default=Decimal("3.0"),
        help="Advisory estimated-cost stop threshold (not a provider hard cap)",
    )
    ap.add_argument(
        "--canonical-pin",
        type=Path,
        default=None,
        help="Canonical matched-depth pin used to bind checkpoint provenance",
    )
    ap.add_argument(
        "--repository",
        default=None,
        help="Exact repository identity for a contracted graph shard",
    )
    ap.add_argument(
        "--graph-sha",
        default=None,
        help="Exact code-graph commit for a contracted graph shard",
    )
    ap.add_argument(
        "--score-depth",
        type=int,
        default=10,
        help="Requested graph-agent rank depth",
    )
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
    ap.add_argument(
        "--checkpoint-contract",
        type=Path,
        default=None,
        help="Exact run-contract JSON persisted into raw per-case checkpoints",
    )
    ap.add_argument(
        "--resume-per-case-json",
        type=Path,
        default=None,
        help="Prior raw checkpoint to resume after exact contract validation",
    )
    args = ap.parse_args()

    if not os.environ.get("ANTHROPIC_API_KEY"):
        print("ERROR: ANTHROPIC_API_KEY required for agent runs", file=sys.stderr)
        return 2
    if not os.environ.get("VOYAGE_API_KEY"):
        print("WARNING: VOYAGE_API_KEY not set — embedding seeds disabled, hybrid falls back to substring", file=sys.stderr)

    parquet = args.parquet or PARQUET
    if not parquet.exists():
        print(f"ERROR: parquet not at {parquet}", file=sys.stderr)
        return 2

    df = pd.read_parquet(parquet)
    if args.instances:
        # Pinned-subset mode: run a FIXED instance set (e.g. the reachable
        # subset from locbench_reachability.py) so re-baselines compare the same
        # instances over time, instead of a fresh random sample that also pulls
        # in GC'd (clone-failing) instances.
        pin = json.loads(args.instances.read_text(encoding="utf-8"))
        ids = pin.get("pinned_instance_ids", pin) if isinstance(pin, dict) else pin
        selected_by_id = df.set_index("instance_id", drop=False)
        selected = selected_by_id.loc[
            [instance_id for instance_id in ids if instance_id in selected_by_id.index]
        ]
        print(f"Pinned mode: {len(selected)}/{len(ids)} pinned instances present in parquet")
    else:
        selected = select_instances(df, args.n, args.seed)
    print(f"Selected {len(selected)} instances:")
    for _, row in selected.iterrows():
        print(f"  - {row['instance_id']} ({row.get('category', '?')})")

    args.workdir.mkdir(parents=True, exist_ok=True)
    summary = BatchSummary(n_total=len(selected))
    selected_ids = [str(instance_id) for instance_id in selected["instance_id"]]
    checkpoint_contract: dict = {}
    if args.checkpoint_contract is not None:
        try:
            raw_contract = json.loads(
                args.checkpoint_contract.read_text(encoding="utf-8")
            )
        except (OSError, json.JSONDecodeError) as exc:
            print(f"ERROR: checkpoint contract is unreadable: {exc}", file=sys.stderr)
            return 2
        if not isinstance(raw_contract, dict):
            print("ERROR: checkpoint contract must be a JSON object", file=sys.stderr)
            return 2
        contract_ids = raw_contract.get("expected_instance_ids")
        if (
            not isinstance(contract_ids, list)
            or len(contract_ids) != len(selected_ids)
            or set(map(str, contract_ids)) != set(selected_ids)
        ):
            print(
                "ERROR: checkpoint contract IDs do not match the selected shard",
                file=sys.stderr,
            )
            return 2
        if (
            args.canonical_pin is None
            or args.repository is None
            or args.graph_sha is None
        ):
            print(
                "ERROR: contracted graph runs require --canonical-pin, "
                "--repository, and --graph-sha",
                file=sys.stderr,
            )
            return 2
        if any(str(repo) != args.repository for repo in selected["repo"]):
            print(
                "ERROR: selected graph shard does not match --repository",
                file=sys.stderr,
            )
            return 2
        try:
            iterations = int(os.environ.get("LOCAGENT_ITERATIONS", "1"))
            expected_contract = expected_graph_checkpoint_contract(
                graph_sha=args.graph_sha,
                canonical_pin=args.canonical_pin,
                parquet=parquet,
                repository=args.repository,
                expected_instance_ids=selected_ids,
                model=os.environ.get(
                    "ANTHROPIC_MODEL",
                    "claude-haiku-4-5-20251001",
                ),
                embedding_model=os.environ.get(
                    "VOYAGE_EMBED_MODEL",
                    "voyage-code-3",
                ),
                iterations=iterations,
                score_depth=args.score_depth,
                graph_budget_usd=str(args.budget_usd),
            )
            checkpoint_contract = validate_graph_checkpoint_contract(
                raw_contract,
                expected_contract,
            )
        except (OSError, ValueError) as exc:
            print(f"ERROR: graph checkpoint contract rejected: {exc}", file=sys.stderr)
            return 2
    if args.resume_per_case_json is not None and not checkpoint_contract:
        print(
            "ERROR: --resume-per-case-json requires --checkpoint-contract",
            file=sys.stderr,
        )
        return 2
    if checkpoint_contract and args.per_case_json is None:
        print(
            "ERROR: contracted graph runs require --per-case-json",
            file=sys.stderr,
        )
        return 2
    if args.resume_per_case_json is not None:
        try:
            summary.instances.extend(
                load_graph_resume_instances(
                    args.resume_per_case_json,
                    checkpoint_contract,
                    list(map(str, checkpoint_contract["expected_instance_ids"])),
                )
            )
        except (OSError, ValueError, TypeError, KeyError, json.JSONDecodeError) as exc:
            print(f"ERROR: graph resume checkpoint rejected: {exc}", file=sys.stderr)
            return 2
        print(f"Resumed {len(summary.instances)} graph cases from prior checkpoint")

    resumed_instances = list(summary.instances)
    summary.instances.clear()
    for resumed in resumed_instances:
        summary.record(resumed)
    resumed_ids = {instance.instance_id for instance in summary.instances}

    # Roundtable T2 fix (2026-05-06): persist per-case JSON checkpoint
    # after EVERY instance, not only at end. Previously, killing the
    # batch at 6/50 dropped all 6 cases of evidence. The 5-agent
    # roundtable's T2 ("mine the partial parallel data") assumed the
    # evidence was shipped; it wasn't. This fix makes the assumption
    # true going forward.
    def _checkpoint_per_case() -> None:
        if not args.per_case_json:
            return
        write_graph_checkpoint(
            args.per_case_json,
            _build_per_case_dict(
                summary,
                checkpoint_contract=checkpoint_contract,
            ),
        )

    checkpoint_failed = False
    try:
        _checkpoint_per_case()
    except Exception as exc:
        print(
            f"ERROR: initial graph checkpoint failed: {exc!r}",
            file=sys.stderr,
        )
        return 2
    for _, row in selected.iterrows():
        if summary.aborted_reason:
            break
        if row["instance_id"] in resumed_ids:
            print(f"\n=== resume: {row['instance_id']} already checkpointed ===")
            continue
        if Decimal(str(summary.total_cost_usd)) >= args.budget_usd:
            summary.aborted_reason = (
                f"estimated-cost threshold ${args.budget_usd:.2f} hit at "
                f"${summary.total_cost_usd:.2f} after {len(summary.instances)} runs"
            )
            print(f"\n!!! {summary.aborted_reason}")
            break
        try:
            res = evaluate_instance(
                row.to_dict(),
                args.workdir,
                json_mode=bool(args.per_case_json),
                score_depth=args.score_depth,
            )
        except KeyboardInterrupt:
            summary.aborted_reason = "user interrupted (Ctrl+C)"
            try:
                _checkpoint_per_case()
            except Exception as exc:
                checkpoint_failed = True
                print(
                    f"ERROR: graph checkpoint failed: {exc!r}",
                    file=sys.stderr,
                )
            break
        except Exception as e:
            res = build_exception_result(row, e)
        should_continue = summary.record(res)
        try:
            _checkpoint_per_case()
        except Exception as exc:
            checkpoint_failed = True
            summary.aborted_reason = f"checkpoint_write_failed:{type(exc).__name__}"
            print(
                f"ERROR: graph checkpoint failed; aborting before next case: {exc!r}",
                file=sys.stderr,
            )
            break
        if not should_continue:
            print(f"\n!!! {summary.aborted_reason}")
            break

    write_report(summary, args.output)

    # Plan 4 T1: per-case JSON dump for the failure-audit pipeline.
    # Captures the full structured agent envelope per instance,
    # including the per-iteration Iterations field (when LOCAGENT_ITERATIONS>=2).
    # Roundtable T2 (2026-05-06): also written as a checkpoint after every
    # instance via _checkpoint_per_case() above — see _build_per_case_dict.
    if args.per_case_json:
        try:
            write_graph_checkpoint(
                args.per_case_json,
                _build_per_case_dict(
                    summary,
                    checkpoint_contract=checkpoint_contract,
                ),
            )
        except Exception as exc:
            print(f"ERROR: final graph checkpoint failed: {exc!r}", file=sys.stderr)
            return 2
        print(f"\nPer-case JSON written: {args.per_case_json}")

    if summary.aborted_reason.startswith("invalid_experiment:"):
        return 2
    return 2 if checkpoint_failed else 0


if __name__ == "__main__":
    sys.exit(main())
