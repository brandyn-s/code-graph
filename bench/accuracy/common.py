"""Shared types + utilities for bench/accuracy/.

Edge schema is the normalized format every oracle (and code-graph itself)
emits to so compare.py can match apples-to-apples.
"""
from __future__ import annotations

import hashlib
import json
import os
import subprocess
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any, Iterable

EDGE_SCHEMA_VERSION = 1

REPO_ROOT = Path(__file__).resolve().parents[2]
ACCURACY_DIR = Path(__file__).resolve().parent
CACHE_DIR = ACCURACY_DIR / "cache"
BASELINES_DIR = ACCURACY_DIR / "baselines"
FIXTURES_PATH = ACCURACY_DIR / "fixtures.json"


@dataclass(frozen=True)
class Edge:
    """One structural edge extracted from source by some oracle.

    Matching rules (used by compare.py):
      CALLS, IMPORTS, INHERITS: equality of (from_qn, to_qn, type)
      HTTP_CALLS: equality of (from_qn, to_qn) where to_qn is a URL pattern;
                  file/line are informational only.
    """
    from_qn: str
    to_qn: str
    type: str
    file: str = ""
    line: int = 0
    source: str = ""  # which oracle produced this: "pycg" | "ast" | "opus+sonnet" | "code-graph"

    def match_key(self) -> tuple[str, str, str]:
        return (self.from_qn, self.to_qn, self.type)


def load_fixtures() -> list[dict[str, Any]]:
    with FIXTURES_PATH.open(encoding="utf-8") as f:
        return json.load(f)["fixtures"]


def _fixture_path_env_var(fixture_id: str) -> str:
    """Map fixture id to its CI path-override env var name.

    Example: `flask-adversarial` -> `CODE_GRAPH_FIXTURE_PATH_FLASK_ADVERSARIAL`.
    Used by accuracy-regression.yml to point compare.py at CI-checked-out
    fixture directories without committing CI-specific paths to fixtures.json.
    """
    return "CODE_GRAPH_FIXTURE_PATH_" + fixture_id.upper().replace("-", "_")


def get_fixture(fixture_id: str) -> dict[str, Any]:
    for f in load_fixtures():
        if f["id"] == fixture_id:
            # CI override: when CODE_GRAPH_FIXTURE_PATH_<id> is set, the
            # local-developer path baked into fixtures.json is replaced.
            # actions/checkout in accuracy-regression.yml clones the
            # upstream fixture at the pinned SHA into a CI-local path,
            # then sets this env var so compare.py uses it. Local
            # developers don't set the env var and continue to use the
            # fixtures.json path as before. The SHA-verification gate
            # still runs against the env-pointed dir.
            override = os.environ.get(_fixture_path_env_var(fixture_id))
            if override:
                f = dict(f)
                f["path"] = override
            return f
    raise KeyError(f"fixture {fixture_id!r} not found in {FIXTURES_PATH}")


def verify_fixture_sha(fixture: dict[str, Any]) -> None:
    """Exit 2 on SHA drift OR uncommitted changes inside subset paths.

    The SHA check alone isn't enough: `git rev-parse HEAD` returns the same
    commit even when the working tree has uncommitted .rs / .go / .py changes
    that would alter measurement output. For fixtures with a `subset` key,
    we also run `git status --porcelain` and fail if any listed subset has
    modified or untracked files.

    Uncommitted files OUTSIDE listed subsets produce a warning (common for
    environments like psm where auth-gateway/go.mod is
    routinely modified but we're measuring Rust crates).
    """
    path = fixture["path"]
    expected = fixture["sha"]
    try:
        result = subprocess.run(
            ["git", "-C", path, "rev-parse", "HEAD"],
            capture_output=True,
            timeout=10,
        )
    except (subprocess.SubprocessError, FileNotFoundError) as e:
        raise SystemExit(f"fixture {fixture['id']}: git rev-parse failed: {e}")
    actual = result.stdout.decode("utf-8", errors="replace").strip()
    if actual != expected:
        raise SystemExit(
            f"fixture {fixture['id']}: SHA drift.\n"
            f"  expected: {expected}\n"
            f"  actual:   {actual}\n"
            f"  Reset the fixture, OR run `python bench/accuracy/regen_oracles.py "
            f"--fixture {fixture['id']}` to bump fixtures.json + regenerate the oracle cache."
        )

    # Working-tree drift check. Only applies to fixtures with a subset list —
    # for single-project fixtures like mcp-servers we could add it later but
    # the blast radius of uncommitted changes there isn't obviously worse
    # than SHA drift alone.
    subsets = fixture.get("subset") or []
    if not subsets:
        return
    try:
        status = subprocess.run(
            ["git", "-C", path, "status", "--porcelain"],
            capture_output=True,
            timeout=10,
        )
    except (subprocess.SubprocessError, FileNotFoundError) as e:
        print(f"fixture {fixture['id']}: git status failed (non-fatal): {e}")
        return
    status_text = status.stdout.decode("utf-8", errors="replace")
    if not status_text.strip():
        return
    subset_drift: list[str] = []
    other_drift: list[str] = []
    for line in status_text.splitlines():
        # Porcelain format: `XY path` where X/Y are status codes (e.g. " M",
        # "??"). path is from index 3 onward. Renames use "R  old -> new" —
        # for simplicity we keep the whole post-code string.
        if len(line) < 3:
            continue
        rel = line[3:].strip()
        # Normalize: git always reports forward slashes.
        rel_posix = rel.replace("\\", "/")
        in_subset = any(rel_posix == s or rel_posix.startswith(s.rstrip("/") + "/") for s in subsets)
        if in_subset:
            subset_drift.append(line)
        else:
            other_drift.append(line)
    if subset_drift:
        detail = "\n  ".join(subset_drift[:10])
        raise SystemExit(
            f"fixture {fixture['id']}: uncommitted changes inside subset paths.\n"
            f"  subsets: {subsets}\n"
            f"  drift ({len(subset_drift)} paths; first 10 shown):\n  {detail}\n"
            f"  Commit, stash, or revert before running the harness."
        )
    if other_drift:
        print(
            f"fixture {fixture['id']}: {len(other_drift)} uncommitted path(s) "
            f"outside subsets (not blocking). Sample: {other_drift[0][:80]}"
        )


def file_sha(path: Path) -> str:
    """Content hash used for per-file LLM cache keys."""
    h = hashlib.sha256()
    h.update(path.read_bytes())
    return h.hexdigest()[:16]


def write_edges(edges: Iterable[Edge], out_path: Path) -> None:
    out_path.parent.mkdir(parents=True, exist_ok=True)
    payload = {
        "schema_version": EDGE_SCHEMA_VERSION,
        "edges": [asdict(e) for e in edges],
    }
    out_path.write_bytes(json.dumps(payload, indent=2).encode("utf-8"))


def read_edges(in_path: Path) -> list[Edge]:
    with in_path.open(encoding="utf-8") as f:
        payload = json.load(f)
    if payload.get("schema_version") != EDGE_SCHEMA_VERSION:
        raise SystemExit(f"{in_path}: schema_version mismatch")
    return [Edge(**e) for e in payload["edges"]]


def run_captured(argv: list[str], *, cwd: str | None = None, timeout: int = 300) -> tuple[int, bytes, bytes]:
    """subprocess.run with bytes output (utf-8 decode at caller) and utf-8 env."""
    env = os.environ.copy()
    env["PYTHONIOENCODING"] = "utf-8"
    env["PYTHONUTF8"] = "1"
    proc = subprocess.run(
        argv,
        capture_output=True,
        cwd=cwd,
        env=env,
        timeout=timeout,
    )
    return proc.returncode, proc.stdout, proc.stderr
