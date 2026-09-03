"""
Phase 0 benchmark harness for code-graph.

Runs the 20-question suite from questions.json against the 4 fixture repos in
fixtures.json, producing a JSON baseline file. Subsequent feature PRs re-run
this harness and use compare.py to diff against the baseline.

Why Python: shells out to the Go binary via subprocess, no CGO dependencies,
fast to iterate. The baseline output is language-agnostic JSON.

Usage:
    python bench/harness.py --output bench/baseline_2026-04-22.json
    python bench/harness.py --output bench/after_pr1.json --repo mcp-servers
    python bench/harness.py --output bench/smoke.json --smoke

Invariants (from ~/.claude/rules/platform-constraints.md):
- All I/O is UTF-8. subprocess uses bytes + .decode('utf-8', errors='replace').
- Never text=True on subprocess.run for external tool output.
- Paths expanded via os.path.expanduser — works across Windows/Linux.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import subprocess
import sys
import time
from datetime import datetime, timezone
from pathlib import Path


BENCH_DIR = Path(__file__).resolve().parent
REPO_ROOT = BENCH_DIR.parent


def load_json(path: Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def expand(p: str) -> Path:
    """Expand ~ AND environment variables. Path strings in fixtures.json use
    $HOME (or %USERPROFILE% via expandvars) rather than hard-coded absolute
    paths, so the same fixture file works on any machine."""
    return Path(os.path.expandvars(os.path.expanduser(p))).resolve()


def repo_sha(repo_path: Path) -> str:
    """Return short SHA of repo HEAD. Empty string if not a git repo."""
    result = subprocess.run(
        ["git", "-C", str(repo_path), "rev-parse", "--short", "HEAD"],
        capture_output=True, timeout=10,
    )
    if result.returncode != 0:
        return ""
    return result.stdout.decode("utf-8", errors="replace").strip()


def repo_is_dirty(repo_path: Path) -> bool:
    """True if repo has uncommitted changes."""
    result = subprocess.run(
        ["git", "-C", str(repo_path), "status", "--porcelain"],
        capture_output=True, timeout=10,
    )
    return bool(result.stdout.strip())


class Harness:
    """Wrapper around `code-graph cli <tool> <json_args>`."""

    def __init__(self, binary: str, with_embeddings: bool = False):
        self.binary = str(expand(binary))
        if not Path(self.binary).exists():
            sys.exit(f"binary not found: {self.binary}")
        # By default, the harness unsets VOYAGE_API_KEY so the embeddings pass
        # is skipped during indexing. This is a workaround for a known stall in
        # the embeddings HTTP loop (2026-04-22: observed 8+ minute stall with
        # no progress log after "generating embeddings pct=97"). Pass
        # with_embeddings=True only when semantic_search data is needed.
        self.env = os.environ.copy()
        if not with_embeddings:
            self.env["VOYAGE_API_KEY"] = ""
        self.with_embeddings = with_embeddings

    def call_tool(self, tool: str, args: dict, timeout: int = 120) -> tuple[dict | str, int]:
        """Run the binary's cli mode. Returns (parsed_result, latency_ms).

        Parsed result: dict if the result was JSON, otherwise raw string.
        On error, returns {"error": "..."} and still records latency.

        Passes --raw for deterministic JSON output. Older binaries that lack
        --raw emit a {content, isError} envelope on stdout instead, which we
        also detect and unwrap for back-compat.
        """
        args_json = json.dumps(args)
        cmd = [self.binary, "cli", "--raw", tool, args_json]
        start = time.perf_counter()
        try:
            result = subprocess.run(
                cmd,
                capture_output=True,
                timeout=timeout,
                env=self.env,
            )
        except subprocess.TimeoutExpired as e:
            # Stall watchdog: capture partial output and surface the error
            # cleanly so the harness doesn't leave behind an orphan process.
            latency_ms = int((time.perf_counter() - start) * 1000)
            stdout = (e.stdout or b"").decode("utf-8", errors="replace")
            stderr = (e.stderr or b"").decode("utf-8", errors="replace")
            return {
                "error": f"timeout after {timeout}s ({tool})",
                "stdout_tail": stdout[-500:],
                "stderr_tail": stderr[-500:],
            }, latency_ms
        latency_ms = int((time.perf_counter() - start) * 1000)
        stdout = result.stdout.decode("utf-8", errors="replace")
        stderr = result.stderr.decode("utf-8", errors="replace")
        rc = result.returncode

        # Try JSON parse of stdout
        try:
            parsed = json.loads(stdout)
        except json.JSONDecodeError:
            # Non-JSON — may be human-readable summary (old binary lacking --raw)
            # or plain text. Surface as error if rc != 0, else return as string.
            if rc != 0:
                return {"error": "non-JSON output", "stdout": stdout[:500], "stderr": stderr[:500]}, latency_ms
            return stdout, latency_ms

        # Old-binary envelope: {"content": [{"type": "text", "text": "..."}], "isError": bool}
        # When --raw works, stdout is the raw tool result (no envelope).
        if isinstance(parsed, dict) and "content" in parsed and "isError" in parsed:
            is_err = parsed.get("isError", False)
            text = ""
            for c in parsed.get("content", []):
                if c.get("type") == "text":
                    text = c.get("text", "")
                    break
            if is_err:
                return {"error": text}, latency_ms
            try:
                return json.loads(text), latency_ms
            except json.JSONDecodeError:
                return text, latency_ms

        # Current binary with --raw: stdout is the raw tool result.
        # If rc != 0, surface as error.
        if rc != 0:
            err_text = stderr.strip() or (json.dumps(parsed) if not isinstance(parsed, str) else parsed)[:500]
            return {"error": err_text}, latency_ms

        return parsed, latency_ms

    def ensure_indexed(self, repo_path: Path, timeout: int = 600) -> dict:
        """Re-index the repo for a fresh baseline. Returns index result.

        Default VOYAGE_API_KEY="" so the embeddings pass is skipped —
        prevents the known indefinite stall during the embeddings HTTP
        loop. Pass with_embeddings=True on Harness() to opt in.
        """
        result, latency_ms = self.call_tool(
            "index_repository",
            {"repo_path": str(repo_path)},
            timeout=timeout,
        )
        if isinstance(result, dict):
            result["_index_latency_ms"] = latency_ms
            result["_embeddings_populated"] = self.with_embeddings
        return result if isinstance(result, dict) else {"error": "non-dict index result", "_index_latency_ms": latency_ms}

    def resolve_project_name(self, repo_path: Path) -> str | None:
        """Find the project name the binary uses for a given repo path.

        The binary normalizes paths to names like
        'c-Users-user-Documents-GitHub-mcp-servers'. Match by
        root_path (case-insensitive on Windows) rather than guessing.

        Shape varies by binary version: new --raw returns a top-level array;
        old envelope wraps it as {"projects": [...]}. Both handled.
        """
        result, _ = self.call_tool("list_projects", {})
        if isinstance(result, list):
            projects = result
        elif isinstance(result, dict):
            projects = result.get("projects", [])
        else:
            return None
        target = str(repo_path).replace("\\", "/").lower()
        for p in projects:
            if not isinstance(p, dict):
                continue
            root = (p.get("root_path") or "").replace("\\", "/").lower()
            if root == target:
                return p.get("name")
        return None


def hash_result(result) -> str:
    """Stable SHA256 of a result for change detection."""
    if isinstance(result, (dict, list)):
        canonical = json.dumps(result, sort_keys=True, default=str)
    else:
        canonical = str(result)
    return hashlib.sha256(canonical.encode("utf-8")).hexdigest()[:12]


def preview(result, limit: int = 200) -> str:
    """Short preview of result for the baseline file."""
    if isinstance(result, (dict, list)):
        s = json.dumps(result, default=str)
    else:
        s = str(result)
    return s[:limit] + ("..." if len(s) > limit else "")


def resolve_qualified_name(harness: Harness, project: str, hint: str) -> str | None:
    """For Q5: given a short name hint, resolve a qualified name via search_graph."""
    result, _ = harness.call_tool(
        "search_graph",
        {"project": project, "label": "Function", "name_pattern": hint, "limit": 1},
    )
    if isinstance(result, dict):
        items = result.get("results") or result.get("nodes") or []
        if items and isinstance(items, list):
            first = items[0]
            if isinstance(first, dict):
                return first.get("qualified_name") or first.get("name")
    return None


def grade_question(q: dict, result) -> tuple[str, str | None]:
    """Grade PASS / PARTIAL / FAIL / N/A / ERROR against the question's expectations.

    Returns (grade, note).
    """
    # Error from tool
    if isinstance(result, dict) and "error" in result:
        err = str(result.get("error"))
        feature_pr = q.get("feature_pr")
        if feature_pr and q.get("pre_merge_result", "").startswith("expected_error"):
            return "N/A", f"feature {feature_pr} not merged (expected)"
        # "unknown tool" → binary predates the tool. Treat as N/A with a note
        # so baselines from outdated binaries don't misleadingly show ERRORs.
        if "unknown tool" in err.lower():
            return "N/A", f"tool not present in binary ({err[:80]})"
        return "ERROR", err[:120]

    # expect_contains check
    if "expect_contains" in q:
        serialized = json.dumps(result, default=str) if isinstance(result, (dict, list)) else str(result)
        missing = [k for k in q["expect_contains"] if k not in serialized]
        if missing:
            return "FAIL", f"missing keys: {missing}"
        return "PASS", None

    # expect_min_results check
    if "expect_min_results" in q:
        if q["expect_min_results"] is None:
            return "N/A", "criterion is null (post-merge only)"
        count = 0
        if isinstance(result, list):
            count = len(result)
        elif isinstance(result, dict):
            for key in ("results", "nodes", "edges", "rows", "items", "paths", "callees", "callers", "matches"):
                if key in result and isinstance(result[key], list):
                    count = len(result[key])
                    break
        if count < q["expect_min_results"]:
            return "FAIL", f"got {count} results, wanted >={q['expect_min_results']}"
        return "PASS", f"{count} results"

    # expect_non_empty
    if q.get("expect_non_empty"):
        if not result or (isinstance(result, (dict, list)) and len(result) == 0):
            return "FAIL", "empty result"
        return "PASS", None

    # No explicit criterion — if result is non-error, call it PASS
    return "PASS", None


def run_question(harness: Harness, q: dict, project: str, fixture_id: str) -> dict:
    """Run one question against one repo. Returns result record."""
    args = q.get("args_per_repo", {}).get(fixture_id) or q.get("args") or {}
    args = dict(args)  # copy so we can mutate
    if "project" not in args and q["tool"] != "_file_check":
        args["project"] = project

    # Special-case Q5: resolve qualified_name from hint
    if q["id"] == "Q5" and "qualified_name_hint" in args:
        hint = args.pop("qualified_name_hint")
        qn = resolve_qualified_name(harness, project, hint)
        if qn is None:
            return {
                "latency_ms": 0,
                "grade": "FAIL",
                "note": f"could not resolve qualified name for hint '{hint}'",
                "result_hash": None,
                "preview": None,
            }
        args["qualified_name"] = qn

    # Special-case Q15: file check only, no tool call
    if q["tool"] == "_file_check":
        path = expand(args["path"])
        exists = path.exists()
        grade = "PASS" if exists else "N/A"
        note = "file exists" if exists else f"feature {q.get('feature_pr')} not merged (expected)"
        return {
            "latency_ms": 0,
            "grade": grade,
            "note": note,
            "result_hash": None,
            "preview": str(path),
        }

    result, latency_ms = harness.call_tool(q["tool"], args)
    grade, note = grade_question(q, result)
    return {
        "latency_ms": latency_ms,
        "grade": grade,
        "note": note,
        "result_hash": hash_result(result),
        "preview": preview(result),
    }


def run_repo(harness: Harness, fixture: dict, questions: list[dict], skip_index: bool = False, index_timeout: int = 600) -> dict:
    """Run the full question suite against one repo. Returns repo record."""
    repo_path = expand(fixture["path"])
    fixture_id = fixture["id"]
    print(f"\n=== {fixture_id} ({repo_path}) ===", file=sys.stderr)

    actual_sha = repo_sha(repo_path)
    dirty = repo_is_dirty(repo_path)
    if actual_sha != fixture["sha"]:
        print(f"  WARN: fixture SHA drift — expected {fixture['sha']}, got {actual_sha}", file=sys.stderr)
    if dirty:
        print(f"  WARN: repo is dirty — baseline reproducibility compromised", file=sys.stderr)

    record = {
        "path": str(repo_path),
        "expected_sha": fixture["sha"],
        "actual_sha": actual_sha,
        "dirty": dirty,
        "index": None,
        "questions": {},
    }

    # Index (unless --skip-index)
    if not skip_index:
        tag = "with embeddings" if harness.with_embeddings else "embeddings off"
        print(f"  indexing ({tag})...", file=sys.stderr)
        idx = harness.ensure_indexed(repo_path, timeout=index_timeout)
        record["index"] = idx
        if "error" in idx:
            print(f"    FAILED: {idx['error']}", file=sys.stderr)
        else:
            print(f"    {idx.get('nodes', '?')} nodes, {idx.get('edges', '?')} edges, {idx.get('_index_latency_ms', '?')}ms", file=sys.stderr)

    # Determine project name — code-graph normalizes paths (e.g.
    # 'c-Users-user-Documents-GitHub-mcp-servers'). Prefer the
    # index result when we just indexed; otherwise resolve via list_projects.
    project = None
    if record["index"] and isinstance(record["index"], dict):
        project = record["index"].get("project")
    if not project:
        project = harness.resolve_project_name(repo_path)
    if not project:
        print(f"  ERROR: could not resolve project name for {repo_path}", file=sys.stderr)
        project = fixture_id  # fallback; queries will likely fail
    record["project_name"] = project

    # Run questions
    for q in questions:
        print(f"  {q['id']} ({q['tool']})...", file=sys.stderr, end=" ")
        r = run_question(harness, q, project, fixture_id)
        record["questions"][q["id"]] = r
        print(f"{r['grade']} ({r['latency_ms']}ms)", file=sys.stderr)

    return record


def main() -> int:
    ap = argparse.ArgumentParser(description="code-graph Phase 0 baseline harness")
    ap.add_argument("--output", required=True, help="output JSON path")
    ap.add_argument("--fixtures", default=str(BENCH_DIR / "fixtures.json"))
    ap.add_argument("--questions", default=str(BENCH_DIR / "questions.json"))
    ap.add_argument("--repo", help="run only this fixture id (by default: all)")
    ap.add_argument("--smoke", action="store_true", help="smoke mode: only Q1+Q2 per repo")
    ap.add_argument("--skip-index", action="store_true", help="assume repos already indexed; skip re-index")
    ap.add_argument("--binary", help="override binary path (default: fixtures.binary)")
    ap.add_argument("--with-embeddings", action="store_true",
                    help="populate Voyage embeddings during indexing (off by default — "
                         "a known stall in the embeddings HTTP loop can hang the indexer "
                         "for many minutes; opt in only when semantic_search data is needed)")
    ap.add_argument("--index-timeout", type=int, default=600,
                    help="seconds to wait for index_repository before declaring a stall (default: 600)")
    args = ap.parse_args()

    fixtures = load_json(Path(args.fixtures))
    questions = load_json(Path(args.questions))["questions"]
    if args.smoke:
        questions = [q for q in questions if q["id"] in ("Q1", "Q2")]

    harness = Harness(
        args.binary or fixtures["binary"],
        with_embeddings=args.with_embeddings,
    )

    fixture_list = fixtures["fixtures"]
    if args.repo:
        fixture_list = [f for f in fixture_list if f["id"] == args.repo]
        if not fixture_list:
            sys.exit(f"no fixture with id {args.repo!r}")

    # Binary SHA (from code-graph repo HEAD at time of run)
    binary_sha = repo_sha(REPO_ROOT)

    output = {
        "schema_version": 1,
        "captured_at": datetime.now(timezone.utc).isoformat(),
        "binary_sha": binary_sha,
        "binary_path": harness.binary,
        "fixtures_pinned_at": fixtures["pinned_at"],
        "smoke_mode": args.smoke,
        "skip_index": args.skip_index,
        "with_embeddings": args.with_embeddings,
        "index_timeout_s": args.index_timeout,
        "repos": {},
    }

    for fixture in fixture_list:
        output["repos"][fixture["id"]] = run_repo(
            harness, fixture, questions,
            skip_index=args.skip_index,
            index_timeout=args.index_timeout,
        )

    out_path = Path(args.output).resolve()
    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_text(json.dumps(output, indent=2, default=str), encoding="utf-8")
    print(f"\nwrote {out_path}", file=sys.stderr)

    # Quick summary
    for fid, repo in output["repos"].items():
        grades = [r["grade"] for r in repo["questions"].values()]
        from collections import Counter
        counts = Counter(grades)
        print(f"  {fid}: {dict(counts)}", file=sys.stderr)

    return 0


if __name__ == "__main__":
    sys.exit(main())
