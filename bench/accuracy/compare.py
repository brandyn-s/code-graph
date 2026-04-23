"""Compare oracle ground-truth edges vs code-graph extraction.

Computes TP/FP/FN/P/R/F1 per edge type for a fixture. Emits:
    baselines/<date>-<fixture>-report.md    — human readable
    baselines/<date>-<fixture>-report.json  — machine readable (for regression diffing)

Qualified-name normalization
----------------------------
code-graph stores QNs like `<project>.<service>.<file>.<func>` where `<project>` is
the file-system-derived project name (e.g. `c-Users-user-Documents-GitHub-mcp-servers`).
Oracle QNs (from PyCG, ast) are already project-relative — they start with a
service directory or a cross-service module (`airlock.foo`, `shared.bar`).

Alignment rule:
1. Strip code-graph's project prefix before comparison.
2. PyCG occasionally prefixes cross-service refs with the wrong service
   (see oracle_pycg.py note) — we handle this via a secondary "suffix-match"
   metric alongside the strict exact-match metric. If suffix >> exact, that's
   a known alignment artifact, not a code-graph bug.

Both metrics are reported so the QN-drift source is visible.
"""
from __future__ import annotations

import argparse
import datetime
import json
import subprocess
import sys
from collections import defaultdict
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from common import (  # noqa: E402
    BASELINES_DIR,
    CACHE_DIR,
    Edge,
    get_fixture,
    read_edges,
    verify_fixture_sha,
)


CODE_GRAPH_BINARY = (
    Path(__file__).resolve().parents[2] / "bin" / "codebase-memory-mcp.exe"
)


def project_name_for_fixture(fixture: dict) -> str:
    """code-graph derives project name from the indexed path. Mirror that."""
    path = fixture["path"]
    # Replace : / \ with - to match code-graph's escape logic.
    return (
        path.replace(":", "")
        .replace("/", "-")
        .replace("\\", "-")
        .lstrip("-")
        .replace("C-", "c-")
    )


def strip_project_prefix(qn: str, project: str) -> str:
    prefix = project + "."
    if qn.startswith(prefix):
        return qn[len(prefix):]
    return qn


def query_code_graph_edges(project: str, edge_type: str) -> list[Edge]:
    """Pull edges of `edge_type` from code-graph via CLI query_graph."""
    cypher = (
        f"MATCH (a)-[r:{edge_type}]->(b) "
        f"RETURN a.qualified_name AS f, b.qualified_name AS t, "
        f"a.file_path AS file, a.start_line AS line LIMIT 100000"
    )
    args_json = json.dumps({"project": project, "query": cypher})
    proc = subprocess.run(
        [str(CODE_GRAPH_BINARY), "cli", "--raw", "query_graph", args_json],
        capture_output=True,
        timeout=120,
    )
    if proc.returncode != 0:
        raise SystemExit(
            f"code-graph query_graph failed (rc={proc.returncode}): "
            f"{proc.stderr.decode('utf-8', errors='replace')[:500]}"
        )
    try:
        payload = json.loads(proc.stdout.decode("utf-8", errors="replace"))
    except json.JSONDecodeError as e:
        raise SystemExit(f"code-graph returned non-JSON: {e}")

    rows = payload.get("rows") or payload.get("data") or payload
    if not isinstance(rows, list):
        # Some responses wrap in {"rows": [...]}; try another unwrap.
        rows = payload.get("result", [])

    edges: list[Edge] = []
    for r in rows:
        if not isinstance(r, dict):
            continue
        f = r.get("f") or r.get("a.qualified_name") or ""
        t = r.get("t") or r.get("b.qualified_name") or ""
        if f and t:
            edges.append(
                Edge(
                    from_qn=strip_project_prefix(f, project),
                    to_qn=strip_project_prefix(t, project),
                    type=edge_type,
                    file=r.get("file", "") or "",
                    line=int(r.get("line", 0) or 0),
                    source="code-graph",
                )
            )
    return edges


def suffix_match_key(qn: str, min_segments: int = 3) -> str | None:
    """Return the last `min_segments` of a dotted QN, or None if too short.

    Used for permissive matching when QN prefixes drift (e.g., PyCG
    mislabels cross-service refs with the wrong service prefix).
    """
    parts = qn.split(".")
    if len(parts) < min_segments:
        return None
    return ".".join(parts[-min_segments:])


def compute_metrics(
    oracle: list[Edge], measured: list[Edge]
) -> dict:
    oracle_exact = {e.match_key() for e in oracle}
    measured_exact = {e.match_key() for e in measured}

    tp_exact = oracle_exact & measured_exact
    fp_exact = measured_exact - oracle_exact
    fn_exact = oracle_exact - measured_exact

    # Scope-aligned metric: restrict both sides to edges whose caller is in
    # the oracle's analyzed-caller set. The raw metric above includes
    # code-graph edges from callers PyCG never reached (e.g., test files
    # outside the service entry-point scope) as FPs, which isn't a fair
    # accuracy signal — it's a scope mismatch artifact. The scope-aligned
    # metric is apples-to-apples: on files PyCG actually analyzed, how
    # accurately does code-graph represent the edges?
    oracle_callers = {e.from_qn for e in oracle}
    oracle_scoped = {k for k in oracle_exact if k[0] in oracle_callers}
    measured_scoped = {k for k in measured_exact if k[0] in oracle_callers}
    tp_scoped = oracle_scoped & measured_scoped
    fp_scoped = measured_scoped - oracle_scoped
    fn_scoped = oracle_scoped - measured_scoped

    # Suffix match: (from_suffix, to_suffix, type)
    def suffix_key(k: tuple[str, str, str]) -> tuple[str, str, str] | None:
        fs = suffix_match_key(k[0])
        ts = suffix_match_key(k[1])
        if fs and ts:
            return (fs, ts, k[2])
        return None

    oracle_suffix = {s for s in (suffix_key(k) for k in oracle_exact) if s is not None}
    measured_suffix = {s for s in (suffix_key(k) for k in measured_exact) if s is not None}
    tp_suffix = oracle_suffix & measured_suffix
    fp_suffix = measured_suffix - oracle_suffix
    fn_suffix = oracle_suffix - measured_suffix

    def pr(tp: int, fp: int, fn: int) -> dict:
        precision = tp / (tp + fp) if (tp + fp) else 0.0
        recall = tp / (tp + fn) if (tp + fn) else 0.0
        f1 = 2 * precision * recall / (precision + recall) if (precision + recall) else 0.0
        return {
            "tp": tp,
            "fp": fp,
            "fn": fn,
            "precision": round(precision, 4),
            "recall": round(recall, 4),
            "f1": round(f1, 4),
        }

    return {
        "oracle_count": len(oracle),
        "measured_count": len(measured),
        "oracle_caller_count": len(oracle_callers),
        "exact": pr(len(tp_exact), len(fp_exact), len(fn_exact)),
        "suffix_3seg": pr(len(tp_suffix), len(fp_suffix), len(fn_suffix)),
        "scope_aligned": pr(len(tp_scoped), len(fp_scoped), len(fn_scoped)),
        "sample_fp_exact": sorted(fp_exact)[:10],
        "sample_fn_exact": sorted(fn_exact)[:10],
        "sample_fp_scoped": sorted(fp_scoped)[:10],
        "sample_fn_scoped": sorted(fn_scoped)[:10],
    }


def compare_fixture(fixture_id: str) -> tuple[dict, Path, Path]:
    fixture = get_fixture(fixture_id)
    verify_fixture_sha(fixture)

    project = project_name_for_fixture(fixture)
    today = datetime.date.today().isoformat()
    BASELINES_DIR.mkdir(parents=True, exist_ok=True)
    md_path = BASELINES_DIR / f"{today}-{fixture_id}-report.md"
    json_path = BASELINES_DIR / f"{today}-{fixture_id}-report.json"

    results: dict[str, dict] = {}

    # CALLS from PyCG oracle
    pycg_cache = CACHE_DIR / f"pycg-{fixture_id}-{fixture['short_sha']}.json"
    if pycg_cache.exists():
        oracle_calls = read_edges(pycg_cache)
        measured_calls = query_code_graph_edges(project, "CALLS")
        results["CALLS"] = {
            "oracle": "pycg",
            **compute_metrics(oracle_calls, measured_calls),
        }
    else:
        print(f"WARN: no PyCG cache at {pycg_cache}; run oracle_pycg.py first")

    # IMPORTS from AST oracle
    ast_cache = CACHE_DIR / f"ast-imports-{fixture_id}-{fixture['short_sha']}.json"
    if ast_cache.exists():
        oracle_imports = read_edges(ast_cache)
        measured_imports = query_code_graph_edges(project, "IMPORTS")
        results["IMPORTS"] = {
            "oracle": "ast",
            **compute_metrics(oracle_imports, measured_imports),
        }
    else:
        print(f"WARN: no AST cache at {ast_cache}; run oracle_ast_imports.py first")

    # HTTP_CALLS from LLM ensemble (future; stub for now)
    ensemble_cache = CACHE_DIR / f"ensemble-http-{fixture_id}-{fixture['short_sha']}.json"
    if ensemble_cache.exists():
        oracle_http = read_edges(ensemble_cache)
        measured_http = query_code_graph_edges(project, "HTTP_CALLS")
        results["HTTP_CALLS"] = {
            "oracle": "opus+sonnet",
            **compute_metrics(oracle_http, measured_http),
        }
    else:
        results["HTTP_CALLS"] = {
            "oracle": "opus+sonnet (not yet run)",
            "status": "pending",
        }

    # Write JSON
    report = {
        "schema_version": 1,
        "date": today,
        "fixture": fixture_id,
        "sha": fixture["sha"],
        "short_sha": fixture["short_sha"],
        "project_name": project,
        "results": results,
    }
    json_path.write_bytes(json.dumps(report, indent=2).encode("utf-8"))

    # Write markdown
    lines = [
        f"# code-graph accuracy baseline — {fixture_id}",
        "",
        f"- **Date**: {today}",
        f"- **Fixture SHA**: `{fixture['sha']}` (short: `{fixture['short_sha']}`)",
        f"- **Project name**: `{project}`",
        "",
        "## Summary",
        "",
        "Three metrics per edge type:",
        "- **Exact**: strict (from_qn, to_qn, type) equality between oracle and code-graph.",
        "- **Suffix-3**: permissive match on the last 3 QN segments — identifies QN-drift artifacts.",
        "- **Scope-aligned**: restricted to edges whose caller is in the oracle's analyzed-caller set. Filters out scope-mismatch artifacts (e.g., code-graph edges from test files PyCG never reached) to give an apples-to-apples accuracy reading.",
        "",
        "| Edge type | Oracle | Oracle / Measured | Exact P/R/F1 | Suffix-3 P/R/F1 | Scope-aligned P/R/F1 |",
        "|---|---|---|---|---|---|",
    ]
    for edge_type, res in results.items():
        if res.get("status") == "pending":
            lines.append(
                f"| {edge_type} | {res['oracle']} | — | — | — | — |"
            )
            continue
        e = res["exact"]
        s = res["suffix_3seg"]
        a = res["scope_aligned"]
        lines.append(
            f"| {edge_type} | {res['oracle']} | "
            f"{res['oracle_count']} / {res['measured_count']} | "
            f"{e['precision']:.3f} / {e['recall']:.3f} / {e['f1']:.3f} | "
            f"{s['precision']:.3f} / {s['recall']:.3f} / {s['f1']:.3f} | "
            f"{a['precision']:.3f} / {a['recall']:.3f} / {a['f1']:.3f} |"
        )
    lines.append("")
    lines.append("## Samples (first 10 per edge type)")
    for edge_type, res in results.items():
        if res.get("status") == "pending":
            continue
        lines.extend([
            "",
            f"### {edge_type}",
            "",
            f"Oracle analyzed callers: {res.get('oracle_caller_count', 0)}",
            "",
            "**Scope-aligned false positives** (code-graph edge from a PyCG-analyzed caller to a callee PyCG did not record):",
            "```",
            *[f"  {f} --> {t}" for f, t, _ in res.get("sample_fp_scoped", [])],
            "```",
            "",
            "**Scope-aligned false negatives** (oracle recorded, code-graph did NOT):",
            "```",
            *[f"  {f} --> {t}" for f, t, _ in res.get("sample_fn_scoped", [])],
            "```",
            "",
            "**Raw-exact false positives (may include out-of-scope callers)**:",
            "```",
            *[f"  {f} --> {t}" for f, t, _ in res["sample_fp_exact"]],
            "```",
            "",
            "**Raw-exact false negatives**:",
            "```",
            *[f"  {f} --> {t}" for f, t, _ in res["sample_fn_exact"]],
            "```",
        ])
    lines.append("")
    lines.append("## Targets")
    lines.append("")
    lines.append("- CALLS: target ≥85% recall, ≥60% precision (Sui 2020 baseline 0.884 recall adjusted for Python/Go vs Java).")
    lines.append("- IMPORTS: target ≥95% recall, ≥90% precision (imports are simple, high-trust).")
    lines.append("- HTTP_CALLS: target ≥70% recall, ≥60% precision (inherently noisier).")

    md_path.write_bytes("\n".join(lines).encode("utf-8"))
    return report, md_path, json_path


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("fixture", help="fixture id from fixtures.json")
    args = ap.parse_args()
    report, md_path, json_path = compare_fixture(args.fixture)
    print(f"\n[compare] wrote {md_path}")
    print(f"[compare] wrote {json_path}\n")
    # Print summary table inline
    for edge_type, res in report["results"].items():
        if res.get("status") == "pending":
            print(f"  {edge_type}: pending (run ensemble oracle)")
            continue
        e = res["exact"]
        print(
            f"  {edge_type}: exact P={e['precision']:.3f} R={e['recall']:.3f} "
            f"F1={e['f1']:.3f}  (oracle={res['oracle_count']}, measured={res['measured_count']})"
        )
    return 0


if __name__ == "__main__":
    sys.exit(main())
