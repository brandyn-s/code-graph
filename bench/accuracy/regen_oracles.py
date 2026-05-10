"""Auto-regenerate fixture SHAs and oracle caches when source repos drift.

Eliminates the manual two-step (`_update_fixture_sha.py` per fixture, then
`python oracle_<lang>.py <fixture-id>` per fixture) by detecting drift,
updating fixtures.json in-place, and invoking the language-appropriate
oracle generator(s) to repopulate the cache at the new SHA.

Usage:
    python regen_oracles.py            # check all fixtures; regen drifted ones
    python regen_oracles.py --all      # force regen of every fixture, drift or not
    python regen_oracles.py --fixture mcp-servers   # one fixture
    python regen_oracles.py --check    # check-only, report drift without writing
    python regen_oracles.py --no-oracle  # bump SHAs in fixtures.json but skip oracle invocation

Language → oracle mapping:
    python:  oracle_pycg.py + oracle_ast_imports.py
    rust:    oracle_rust_syn.py
    go:      oracle_go_ast.py

Negative fixtures (negative_fixtures array) are skipped — they use
check_negative_fixtures.py instead of the syn-oracle path.

Plan: knowledge-base PR #489 plans/2026-05-10-reduce-recurring-plan-cycle-friction.md Phase B.
"""
from __future__ import annotations

import argparse
import json
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
FIXTURES_PATH = HERE / "fixtures.json"

# Language → list of oracle scripts (relative to HERE) that produce the
# JSON cache compare.py consumes. Each script accepts `<fixture-id>` as
# positional arg and writes to bench/accuracy/cache/.
LANGUAGE_ORACLES = {
    "python": ["oracle_pycg.py", "oracle_ast_imports.py"],
    "rust": ["oracle_rust_syn.py"],
    "go": ["oracle_go_ast.py"],
}


def get_head_sha(repo_path: str) -> str | None:
    """Return `git rev-parse HEAD` output, or None if it fails."""
    try:
        result = subprocess.run(
            ["git", "-C", repo_path, "rev-parse", "HEAD"],
            capture_output=True,
            timeout=10,
        )
    except (subprocess.SubprocessError, FileNotFoundError) as e:
        print(f"  WARN: git rev-parse failed for {repo_path}: {e}", file=sys.stderr)
        return None
    if result.returncode != 0:
        print(
            f"  WARN: git rev-parse exited {result.returncode}: "
            f"{result.stderr.decode('utf-8', errors='replace').strip()}",
            file=sys.stderr,
        )
        return None
    return result.stdout.decode("utf-8", errors="replace").strip()


def update_fixture_sha(data: dict, fixture_id: str, new_sha: str) -> bool:
    """Mutate `data` in-place: set fixture[fixture_id].sha + short_sha. Return True on success."""
    for fx in data.get("fixtures", []):
        if fx.get("id") == fixture_id:
            fx["sha"] = new_sha
            fx["short_sha"] = new_sha[:7]
            return True
    return False


def invoke_oracle(oracle_script: str, fixture_id: str, force: bool) -> bool:
    """Run a single oracle generator. Return True if exit code 0."""
    cmd = [sys.executable, str(HERE / oracle_script), fixture_id]
    if force:
        cmd.append("--force")
    print(f"  $ {' '.join(cmd)}")
    try:
        # Stream output live so progress is visible (compatible with the
        # bash-tail-buffering-guard hook: this writes directly to stdout
        # without piping).
        result = subprocess.run(cmd, timeout=600)
    except subprocess.TimeoutExpired:
        print(f"  TIMEOUT after 600s on {oracle_script}", file=sys.stderr)
        return False
    return result.returncode == 0


def process_fixture(fx: dict, args, data: dict) -> dict:
    """Process one fixture. Return a status dict for reporting."""
    fid = fx["id"]
    path = fx["path"]
    cited_sha = fx.get("sha")
    languages = fx.get("languages", [])

    status = {
        "id": fid,
        "drift": False,
        "regenerated": False,
        "oracle_ok": True,
        "note": "",
    }

    head_sha = get_head_sha(path)
    if head_sha is None:
        status["note"] = "git rev-parse failed; skipping"
        return status

    if head_sha != cited_sha:
        status["drift"] = True
        cited_short = (cited_sha or "(none)")[:7]
        status["note"] = f"{cited_short} → {head_sha[:7]}"
        if args.check:
            return status
        # Update fixtures.json in memory
        if not update_fixture_sha(data, fid, head_sha):
            status["note"] += " (fixture not found in fixtures.json — bug?)"
            return status
    elif not args.all:
        status["note"] = "in sync"
        return status

    if args.no_oracle:
        status["note"] += " (--no-oracle; skipped oracle invocation)"
        return status

    # Resolve oracles for this fixture's languages
    oracle_scripts: list[str] = []
    for lang in languages:
        oracle_scripts.extend(LANGUAGE_ORACLES.get(lang, []))
    if not oracle_scripts:
        status["note"] += f" (no oracle registered for languages={languages})"
        return status

    status["regenerated"] = True
    for script in oracle_scripts:
        ok = invoke_oracle(script, fid, force=args.all)
        if not ok:
            status["oracle_ok"] = False
            status["note"] += f" (oracle {script} failed)"
            break

    return status


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--fixture", help="process only this fixture id (default: all)")
    ap.add_argument("--all", action="store_true", help="force regen of every fixture (--force on each oracle)")
    ap.add_argument("--check", action="store_true", help="check-only; report drift, don't write")
    ap.add_argument("--no-oracle", action="store_true", help="bump SHAs but skip oracle invocation")
    args = ap.parse_args()

    if not FIXTURES_PATH.exists():
        print(f"FATAL: {FIXTURES_PATH} not found", file=sys.stderr)
        sys.exit(2)

    raw = FIXTURES_PATH.read_text(encoding="utf-8")
    data = json.loads(raw)

    fixtures = data.get("fixtures", [])
    if args.fixture:
        fixtures = [f for f in fixtures if f.get("id") == args.fixture]
        if not fixtures:
            print(f"FATAL: fixture {args.fixture!r} not found in fixtures.json", file=sys.stderr)
            sys.exit(2)

    statuses = []
    for fx in fixtures:
        print(f"\n=== {fx['id']} ===")
        st = process_fixture(fx, args, data)
        statuses.append(st)
        if st["drift"]:
            print(f"  drift: {st['note']}")
        else:
            print(f"  {st['note']}")

    # Persist fixtures.json updates (skip if --check or no drift)
    any_drift = any(s["drift"] for s in statuses)
    if any_drift and not args.check:
        FIXTURES_PATH.write_text(json.dumps(data, indent=2) + "\n", encoding="utf-8")
        print(f"\nfixtures.json updated.")

    # Summary
    print("\n=== Summary ===")
    for s in statuses:
        drift = "DRIFT" if s["drift"] else "ok"
        oracle = "regen" if s["regenerated"] else "-"
        result = "ok" if s["oracle_ok"] else "FAIL"
        print(f"  {s['id']:40s} {drift:6s} oracle={oracle:6s} {result} {s['note']}")

    failed = [s for s in statuses if not s["oracle_ok"]]
    if failed:
        sys.exit(1)


if __name__ == "__main__":
    main()
