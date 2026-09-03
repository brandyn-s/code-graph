"""Adversarial-F1 regression gate for accuracy-regression.yml.

Indexes one adversarial fixture (`flask-adversarial` or `requests-adversarial`),
runs the standard compare.py harness against it, and asserts the
scope-aligned CALLS F1 is at least the floor declared on the command
line. Exits non-zero (fails the CI job) when F1 drops below the floor.

Floors are pinned 2-3pp below the 2026-05-14 measurement to absorb
runner/build noise without masking real regressions:

  flask-adversarial:    measured 0.573, floor 0.55
  requests-adversarial: measured 0.619, floor 0.60

The floor values live in accuracy-regression.yml as the source of truth
so a single PR can raise both. This script is the mechanical assertion;
the workflow file is the policy.

Phase C of the 2026-05-14 grade-lift roadmap.

Usage:
  # CI: actions/checkout has already placed the fixture at <path>;
  # set the env var so compare.py picks it up.
  export CODE_GRAPH_FIXTURE_PATH_FLASK_ADVERSARIAL=/runner/_work/flask
  python bench/accuracy/check_adversarial_f1.py --fixture flask-adversarial --min-f1 0.55

  # Local sanity (using fixtures.json path):
  python bench/accuracy/check_adversarial_f1.py --fixture requests-adversarial --min-f1 0.60
"""
from __future__ import annotations

import argparse
import json
import os
import pathlib
import subprocess
import sys


REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
CODE_GRAPH_BINARY_CANDIDATES = [
    REPO_ROOT / "bin" / "code-graph",
    REPO_ROOT / "bin" / "code-graph.exe",
]


def find_binary() -> pathlib.Path:
    for c in CODE_GRAPH_BINARY_CANDIDATES:
        if c.exists():
            return c
    raise SystemExit(
        f"code-graph binary not found in {[str(c) for c in CODE_GRAPH_BINARY_CANDIDATES]}; "
        "run `go build -o bin/code-graph ./cmd/code-graph/` first."
    )


def index_fixture(binary: pathlib.Path, fixture_path: pathlib.Path) -> None:
    """Run `cli index_repository` on the fixture path. Force-reindex so the
    test isn't sensitive to a stale cache from a prior CI run.
    """
    args_json = json.dumps({
        "repo_path": str(fixture_path),
        "force": True,
        "skip_report": True,
    })
    proc = subprocess.run(
        [str(binary), "cli", "--raw", "index_repository", args_json],
        capture_output=True,
        timeout=600,
    )
    if proc.returncode != 0:
        raise SystemExit(
            f"index_repository failed (rc={proc.returncode}): "
            f"{proc.stderr.decode('utf-8', errors='replace')[:1000]}"
        )


def run_compare(fixture_id: str) -> dict:
    """Invoke compare.py for the named fixture and return the resulting
    JSON report. compare.py writes the report to baselines/ and prints
    a summary; we read the JSON directly.
    """
    proc = subprocess.run(
        [sys.executable, "bench/accuracy/compare.py", fixture_id],
        cwd=REPO_ROOT,
        capture_output=True,
        timeout=600,
    )
    if proc.returncode != 0:
        raise SystemExit(
            f"compare.py {fixture_id} failed (rc={proc.returncode}):\n"
            f"STDOUT:\n{proc.stdout.decode('utf-8', errors='replace')[:1500]}\n"
            f"STDERR:\n{proc.stderr.decode('utf-8', errors='replace')[:1500]}"
        )
    # The compare.py output prints the JSON report path. Find the newest
    # 2026-*-<fixture-id>-report.json under baselines/.
    baselines = REPO_ROOT / "bench" / "accuracy" / "baselines"
    candidates = sorted(
        baselines.glob(f"*-{fixture_id}-report.json"),
        key=lambda p: p.stat().st_mtime,
        reverse=True,
    )
    if not candidates:
        raise SystemExit(f"compare.py {fixture_id}: no report JSON found in {baselines}")
    return json.loads(candidates[0].read_text(encoding="utf-8"))


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument(
        "--fixture",
        required=True,
        choices=["flask-adversarial", "requests-adversarial"],
        help="fixture id from fixtures.json",
    )
    ap.add_argument(
        "--min-f1",
        type=float,
        required=True,
        help="minimum scope-aligned CALLS F1 floor (e.g. 0.55)",
    )
    ap.add_argument(
        "--metric",
        default="scope_aligned",
        choices=["scope_aligned", "scope_aligned_high_confidence", "exact"],
        help="which F1 to check (default: scope_aligned)",
    )
    args = ap.parse_args()

    env_var = "CODE_GRAPH_FIXTURE_PATH_" + args.fixture.upper().replace("-", "_")
    fixture_path = os.environ.get(env_var)
    if not fixture_path:
        # Fall back to fixtures.json path (local dev mode). Mirror compare.py.
        sys.path.insert(0, str(REPO_ROOT / "bench" / "accuracy"))
        from common import get_fixture  # type: ignore[import-not-found]
        fixture_path = get_fixture(args.fixture)["path"]
    fixture_path_p = pathlib.Path(fixture_path)
    if not fixture_path_p.exists():
        raise SystemExit(
            f"fixture path does not exist: {fixture_path}\n"
            f"  set {env_var} or fixtures.json path correctly"
        )

    binary = find_binary()
    print(f"[adversarial-f1-gate] fixture={args.fixture}")
    print(f"[adversarial-f1-gate] path={fixture_path_p}")
    print(f"[adversarial-f1-gate] binary={binary}")
    print(f"[adversarial-f1-gate] indexing...")
    index_fixture(binary, fixture_path_p)

    print(f"[adversarial-f1-gate] comparing...")
    report = run_compare(args.fixture)

    calls = report["results"]["CALLS"]
    metric = calls[args.metric]
    f1 = metric["f1"]
    p = metric["precision"]
    r = metric["recall"]
    tp = metric["tp"]
    fp = metric["fp"]
    fn = metric["fn"]

    print(
        f"[adversarial-f1-gate] {args.fixture} {args.metric}: "
        f"F1={f1:.3f} P={p:.3f} R={r:.3f} TP={tp} FP={fp} FN={fn}"
    )
    print(f"[adversarial-f1-gate] floor: {args.min_f1:.3f}")

    if f1 < args.min_f1:
        print(
            f"[adversarial-f1-gate] FAIL: F1 {f1:.3f} < floor {args.min_f1:.3f}",
            file=sys.stderr,
        )
        return 1
    print(f"[adversarial-f1-gate] PASS: F1 {f1:.3f} >= floor {args.min_f1:.3f}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
