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


def get_fixture(fixture_id: str) -> dict[str, Any]:
    for f in load_fixtures():
        if f["id"] == fixture_id:
            return f
    raise KeyError(f"fixture {fixture_id!r} not found in {FIXTURES_PATH}")


def verify_fixture_sha(fixture: dict[str, Any]) -> None:
    """Exit 2 on SHA drift. This prevents silently measuring against the wrong commit."""
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
            f"  Reset the fixture or update fixtures.json."
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
