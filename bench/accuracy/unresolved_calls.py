"""Unresolved-call measurement for the resolver tiers.

Indexes a repository into a throwaway cache directory and reports, per source
language (file extension), how many Function/Method nodes exist, how many
CALLS edges were emitted, how many call sites stayed unresolved
(sum of the `unresolved_call_count` node property written by
passWriteUnresolvedCounts), and the resolver_rule distribution of the emitted
edges. Used to measure the hybrid LSP tier (CODE_GRAPH_RESOLVER_TIER) before
and after: run once with the default tier and once with
CODE_GRAPH_RESOLVER_TIER=lsp_local and diff the numbers.

Usage:
  python bench/accuracy/unresolved_calls.py <repo-path> [--ext .py --ext .rs] [--json out.json]
  CODE_GRAPH_RESOLVER_TIER=lsp_local python bench/accuracy/unresolved_calls.py <repo-path>
"""
from __future__ import annotations

import argparse
import json
import os
import pathlib
import re
import sqlite3
import subprocess
import sys
import tempfile

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
BINARY_CANDIDATES = [REPO_ROOT / "bin" / "code-graph", REPO_ROOT / "bin" / "code-graph.exe"]


def find_binary() -> pathlib.Path:
    env = os.environ.get("CODE_GRAPH_BIN")
    if env:
        return pathlib.Path(env)
    for c in BINARY_CANDIDATES:
        if c.exists():
            return c
    raise SystemExit("code-graph binary not found; run `make build` first")


UNRESOLVED_RE = re.compile(r'resolver\.unresolved (.*)$')
KV_RE = re.compile(r'(\w+)=("(?:[^"\\]|\\.)*"|\S+)')


def parse_unresolved_records(stderr: str) -> list[dict[str, str]]:
    """Parse the RESOLVER_TIER2_DEBUG=1 `resolver.unresolved` records (slog
    text or JSON handler) into dicts."""
    records: list[dict[str, str]] = []
    for line in stderr.splitlines():
        if "resolver.unresolved" not in line:
            continue
        if line.lstrip().startswith("{"):
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                continue
            if obj.get("msg") == "resolver.unresolved":
                records.append({k: str(v) for k, v in obj.items()})
            continue
        m = UNRESOLVED_RE.search(line)
        if not m:
            continue
        records.append({k: v.strip('"') for k, v in KV_RE.findall(m.group(1))})
    return records


def index_into(binary: pathlib.Path, repo: pathlib.Path, cache_dir: pathlib.Path, debug: bool = False) -> tuple[pathlib.Path, list[dict[str, str]]]:
    env = dict(os.environ, CODE_GRAPH_CACHE_DIR=str(cache_dir))
    if debug:
        env["RESOLVER_TIER2_DEBUG"] = "1"
    args = json.dumps({"repo_path": str(repo), "mode": "full", "force": True})
    proc = subprocess.run([str(binary), "cli", "index_repository", args], capture_output=True, env=env, timeout=1800, check=False)
    stderr = proc.stderr.decode("utf-8", "replace")
    if proc.returncode != 0:
        sys.stderr.write(stderr[-2000:])
        raise SystemExit(f"index_repository failed rc={proc.returncode}")
    records = parse_unresolved_records(stderr) if debug else []
    dbs = sorted(d for d in cache_dir.glob("*.db") if not d.name.startswith("_"))
    if len(dbs) != 1:
        raise SystemExit(f"expected exactly one database in {cache_dir}, found {[d.name for d in dbs]}")
    return dbs[0], records


def summarize_debug(records: list[dict[str, str]], exts: list[str] | None, top: int = 25) -> dict:
    """Aggregate unresolved call sites: external (no project symbol with that
    short name) versus in-registry (a candidate existed), by dispatch kind,
    plus the most frequent in-registry callee names."""
    by_lang: dict[str, dict] = {}
    for r in records:
        lang = r.get("lang", "?")
        bucket = by_lang.setdefault(lang, {"total": 0, "external": 0, "in_registry": 0, "locally_bound": 0, "by_dispatch": {}, "top_in_registry": {}})
        bucket["total"] += 1
        if r.get("locally_bound") == "true":
            bucket["locally_bound"] += 1
        if r.get("in_registry") == "true":
            bucket["in_registry"] += 1
            name = r.get("callee", "")
            bucket["top_in_registry"][name] = bucket["top_in_registry"].get(name, 0) + 1
        else:
            bucket["external"] += 1
        dk = r.get("dispatch", "") or "direct"
        bucket["by_dispatch"][dk] = bucket["by_dispatch"].get(dk, 0) + 1
    for bucket in by_lang.values():
        bucket["top_in_registry"] = dict(sorted(bucket["top_in_registry"].items(), key=lambda kv: -kv[1])[:top])
    return by_lang


def ext_expr(column: str) -> str:
    # lower(substr(path, last '.')) — SQLite has no rsplit, so use replace trick
    return f"lower(CASE WHEN instr({column}, '.') > 0 THEN replace({column}, rtrim({column}, replace({column}, '.', '')), '') ELSE '' END)"


def measure(db: pathlib.Path, exts: list[str] | None) -> dict:
    con = sqlite3.connect(str(db))
    con.row_factory = sqlite3.Row
    ext_nodes = ext_expr("file_path")
    rows = con.execute(
        f"""
        SELECT '.' || {ext_nodes} AS ext,
               COUNT(*) AS callables,
               COALESCE(SUM(CAST(json_extract(properties, '$.unresolved_call_count') AS INTEGER)), 0) AS unresolved
        FROM nodes
        WHERE label IN ('Function', 'Method') AND file_path != ''
        GROUP BY ext
        """
    ).fetchall()
    per_ext: dict[str, dict] = {}
    for r in rows:
        if r["ext"] == "." or (exts and r["ext"] not in exts):
            continue
        per_ext[r["ext"]] = {"callables": r["callables"], "unresolved_calls": r["unresolved"], "calls_edges": 0, "resolver_rules": {}, "lsp_local_edges": 0}
    edge_rows = con.execute(
        f"""
        SELECT '.' || {ext_expr('n.file_path')} AS ext,
               COALESCE(json_extract(e.properties, '$.resolver_rule'), json_extract(e.properties, '$.strategy'), 'unlabelled') AS rule,
               COUNT(*) AS n
        FROM edges e JOIN nodes n ON n.id = e.source_id
        WHERE e.type = 'CALLS' AND n.file_path != ''
        GROUP BY ext, rule
        """
    ).fetchall()
    for r in edge_rows:
        if r["ext"] not in per_ext:
            continue
        per_ext[r["ext"]]["calls_edges"] += r["n"]
        per_ext[r["ext"]]["resolver_rules"][r["rule"]] = r["n"]
    tier_rows = con.execute(
        f"""
        SELECT '.' || {ext_expr('n.file_path')} AS ext, COUNT(*) AS n
        FROM edges e JOIN nodes n ON n.id = e.source_id
        WHERE e.type = 'CALLS' AND json_extract(e.properties, '$.resolver_tier') = 'lsp_local'
        GROUP BY ext
        """
    ).fetchall()
    for r in tier_rows:
        if r["ext"] in per_ext:
            per_ext[r["ext"]]["lsp_local_edges"] = r["n"]
    for stats in per_ext.values():
        total = stats["calls_edges"] + stats["unresolved_calls"]
        stats["call_sites"] = total
        stats["unresolved_ratio"] = round(stats["unresolved_calls"] / total, 4) if total else 0.0
    con.close()
    return per_ext


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("repo", type=pathlib.Path)
    ap.add_argument("--ext", action="append", help="restrict to these extensions (repeatable)")
    ap.add_argument("--json", type=pathlib.Path, help="write the per-extension stats to this file")
    ap.add_argument("--debug", action="store_true", help="index with RESOLVER_TIER2_DEBUG=1 and classify each unresolved call site (external vs in-registry)")
    args = ap.parse_args()
    repo = args.repo.resolve()
    if not repo.is_dir():
        raise SystemExit(f"not a directory: {repo}")
    binary = find_binary()
    with tempfile.TemporaryDirectory(prefix="cg-unresolved-") as tmp:
        db, records = index_into(binary, repo, pathlib.Path(tmp), debug=args.debug)
        stats = measure(db, args.ext)
    debug = summarize_debug(records, args.ext) if args.debug else {}
    tier = os.environ.get("CODE_GRAPH_RESOLVER_TIER", "default")
    print(f"repo={repo} resolver_tier={tier}")
    print(f"{'ext':<8}{'callables':>10}{'call_sites':>11}{'calls_edges':>12}{'unresolved':>11}{'ratio':>8}{'lsp_local':>10}")
    for ext in sorted(stats):
        s = stats[ext]
        print(f"{ext:<8}{s['callables']:>10}{s['call_sites']:>11}{s['calls_edges']:>12}{s['unresolved_calls']:>11}{s['unresolved_ratio']:>8.3f}{s['lsp_local_edges']:>10}")
        rules = ", ".join(f"{k}={v}" for k, v in sorted(s["resolver_rules"].items(), key=lambda kv: -kv[1]))
        if rules:
            print(f"        rules: {rules}")
    for lang, b in sorted(debug.items()):
        print(f"unresolved[{lang}]: total={b['total']} external={b['external']} in_registry={b['in_registry']} locally_bound={b['locally_bound']}")
        print(f"        by_dispatch: {b['by_dispatch']}")
        print(f"        top in-registry callees: {b['top_in_registry']}")
    if args.json:
        args.json.write_text(json.dumps({"repo": str(repo), "resolver_tier": tier, "stats": stats, "unresolved_debug": debug}, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
