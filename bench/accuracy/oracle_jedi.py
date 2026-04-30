"""CALLS ground-truth oracle via Jedi (static inference).

Companion oracle to oracle_pycg.py. Used to measure oracle-class
uncertainty: if Jedi and PyCG disagree on edges for the same fixture,
the disagreement is a floor on "how much of our F1 number is oracle
arbitration noise vs real code-graph accuracy."

How it works:
- For each .py file under each fixture service, walk with ast to find
  every `Call` node.
- Use Jedi to resolve the callee at that call site — `infer()` on the
  call target returns definition(s). Keep the definition's module_path
  and qualname if it's inside the project.
- Emit CALLS edges with QNs in the same form oracle_pycg emits
  (<service>.<module_dotted>.<fn>). For flask-style fixtures
  (src/flask nested), the same service_prefix normalization applies.

Jedi is PyCG-independent — it uses a different algorithm (type
inference rather than flow analysis). Edges they both emit are high-
confidence. Edges only one emits are oracle-class uncertainty.
"""
from __future__ import annotations

import argparse
import ast
import json
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from common import (  # noqa: E402
    CACHE_DIR,
    Edge,
    get_fixture,
    verify_fixture_sha,
    write_edges,
)

try:
    import jedi  # type: ignore
except ImportError:  # pragma: no cover
    raise SystemExit("pip install jedi")


def service_tuples_for_fixture(fixture: dict) -> list[tuple[str, str]]:
    """Mirror oracle_pycg's service discovery (mcp-servers hardcoded list,
    other fixtures use entry_points)."""
    if fixture["id"] == "mcp-servers":
        return [
            ("airlock", "airlock_mcp_server.py"),
            ("claude-proxy", "claude_proxy.py"),
            ("crowdstrike", "proxy.py"),
            ("security-remix", "security_remix_server.py"),
            ("slack-connect", "slack_connect_app.py"),
        ]
    entry_points = fixture.get("entry_points") or []
    tuples: list[tuple[str, str]] = []
    for ep in entry_points:
        ep_path = Path(ep)
        tuples.append((str(ep_path.parent), ep_path.name))
    return tuples


def normalize_prefix(service_dir: str) -> str:
    return service_dir.replace("\\", "/").replace("/", ".")


def extract_calls_from_file(
    py_file: Path,
    service_root: Path,
    project: jedi.Project,
    prefix: str,
) -> list[Edge]:
    """Walk a .py file, emit (caller_qn, callee_qn) edges using Jedi."""
    try:
        source = py_file.read_text(encoding="utf-8", errors="replace")
    except OSError:
        return []
    try:
        tree = ast.parse(source, filename=str(py_file))
    except SyntaxError:
        return []

    rel = py_file.relative_to(service_root).with_suffix("")
    parts = list(rel.parts)
    if parts and parts[-1] == "__init__":
        parts = parts[:-1]
    module_qn = ".".join([prefix] + parts) if parts else prefix

    # Build a caller-stack via AST walk (function/method scope).
    edges: list[Edge] = []

    # jedi.Script for this file — reused across call-sites.
    script = jedi.Script(code=source, path=str(py_file), project=project)

    # Walk AST to find function definitions and calls inside them.
    def walk(node: ast.AST, caller_qn: str):
        for child in ast.iter_child_nodes(node):
            if isinstance(child, (ast.FunctionDef, ast.AsyncFunctionDef)):
                new_caller = caller_qn + "." + child.name if caller_qn else child.name
                walk(child, new_caller)
            elif isinstance(child, ast.ClassDef):
                new_class = caller_qn + "." + child.name if caller_qn else child.name
                walk(child, new_class)
            elif isinstance(child, ast.Call):
                func = child.func
                line = getattr(func, "lineno", 0)
                col = getattr(func, "col_offset", 0)
                # Jedi infer at the callee position. For `foo.bar(x)` we want
                # the position of `bar`, i.e., the end of the dotted expr.
                try:
                    infs = script.infer(line=line, column=col + 1)
                except Exception:
                    infs = []
                for inf in infs:
                    # Only keep definitions inside the project.
                    mp = inf.module_path
                    if not mp:
                        continue
                    try:
                        mp_rel = Path(mp).relative_to(service_root.parent.resolve())
                    except ValueError:
                        continue  # outside the project
                    # Build callee QN: <fixture-relative-module>.<inf.full_name_tail>
                    mod_parts = list(mp_rel.with_suffix("").parts)
                    if mod_parts and mod_parts[-1] == "__init__":
                        mod_parts = mod_parts[:-1]
                    # Strip leading service_root component since caller QN uses
                    # prefix form.
                    callee_module_qn = ".".join(mod_parts)
                    # inf.full_name is the qualified name WITHIN its module.
                    # We can't always map it cleanly, so use inf.name as a
                    # fallback.
                    ident = inf.name
                    if not ident:
                        continue
                    to_qn = callee_module_qn + "." + ident if callee_module_qn else ident
                    edges.append(Edge(
                        from_qn=caller_qn if caller_qn else module_qn,
                        to_qn=to_qn,
                        type="CALLS",
                        file=str(py_file),
                        line=line,
                        source="jedi",
                    ))
                walk(child, caller_qn)
            else:
                walk(child, caller_qn)

    walk(tree, module_qn)
    return edges


def build_ground_truth(fixture_id: str, force: bool = False) -> Path:
    fixture = get_fixture(fixture_id)
    verify_fixture_sha(fixture)

    cache_path = CACHE_DIR / f"jedi-{fixture_id}-{fixture['short_sha']}.json"
    if cache_path.exists() and not force:
        print(f"[jedi] cache hit: {cache_path}")
        return cache_path

    fixture_path = Path(fixture["path"]).resolve()
    project = jedi.Project(path=str(fixture_path))

    services = service_tuples_for_fixture(fixture)
    all_edges: list[Edge] = []
    t0 = time.time()
    for service_dir, _entry in services:
        service_root = fixture_path / service_dir
        if not service_root.exists():
            continue
        prefix = normalize_prefix(service_dir)
        print(f"[jedi] walking {service_dir} ...")
        py_files = list(service_root.rglob("*.py"))
        py_files = [f for f in py_files if "/__pycache__/" not in f.as_posix() and "\\__pycache__\\" not in str(f)]
        file_count = 0
        edge_count_before = len(all_edges)
        for py in py_files:
            edges = extract_calls_from_file(py, service_root, project, prefix)
            all_edges.extend(edges)
            file_count += 1
            if file_count % 20 == 0:
                print(f"  ... {file_count}/{len(py_files)} files")
        print(f"  {service_dir}: {len(all_edges) - edge_count_before} edges from {len(py_files)} files")

    # Dedup
    seen: set[tuple[str, str, str]] = set()
    deduped: list[Edge] = []
    for e in all_edges:
        if e.match_key() not in seen:
            seen.add(e.match_key())
            deduped.append(e)

    elapsed = time.time() - t0
    print(f"[jedi] total: {len(deduped)} unique edges ({len(all_edges) - len(deduped)} dups) in {elapsed:.1f}s")
    write_edges(deduped, cache_path)
    sidecar = cache_path.with_suffix(".meta.json")
    sidecar.write_bytes(
        json.dumps({
            "fixture": fixture_id,
            "sha": fixture["sha"],
            "elapsed_seconds": round(elapsed, 1),
            "unique_edges": len(deduped),
        }, indent=2).encode("utf-8")
    )
    print(f"[jedi] wrote {cache_path}")
    return cache_path


def compare_to_pycg(fixture_id: str) -> None:
    """Compare Jedi and PyCG edge sets for the same fixture."""
    fixture = get_fixture(fixture_id)
    sha = fixture["short_sha"]
    jedi_cache = CACHE_DIR / f"jedi-{fixture_id}-{sha}.json"
    pycg_cache = CACHE_DIR / f"pycg-{fixture_id}-{sha}.json"
    if not (jedi_cache.exists() and pycg_cache.exists()):
        raise SystemExit("Need both jedi and pycg caches — run both oracles first.")
    from common import read_edges
    jedi_edges = read_edges(jedi_cache)
    pycg_edges = read_edges(pycg_cache)
    jedi_keys = {(e.from_qn, e.to_qn) for e in jedi_edges}
    pycg_keys = {(e.from_qn, e.to_qn) for e in pycg_edges}
    agree = jedi_keys & pycg_keys
    jedi_only = jedi_keys - pycg_keys
    pycg_only = pycg_keys - jedi_keys
    union = jedi_keys | pycg_keys
    print(f"\n=== Jedi vs PyCG on {fixture_id} ===")
    print(f"Jedi:     {len(jedi_keys)} edges")
    print(f"PyCG:     {len(pycg_keys)} edges")
    print(f"Agree:    {len(agree)} edges ({100 * len(agree) / max(len(union), 1):.1f}% of union)")
    print(f"Jedi-only:{len(jedi_only)} edges (PyCG missed)")
    print(f"PyCG-only:{len(pycg_only)} edges (Jedi missed)")
    print()
    print(f"Jaccard similarity: {len(agree) / max(len(union), 1):.4f}")
    print(f"Oracle-class uncertainty: +/- {(len(jedi_only) + len(pycg_only)) / max(len(union), 1):.4f} of union")


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("fixture", help="fixture id")
    ap.add_argument("--force", action="store_true")
    ap.add_argument("--compare", action="store_true", help="compare against pycg cache")
    args = ap.parse_args()
    build_ground_truth(args.fixture, force=args.force)
    if args.compare:
        compare_to_pycg(args.fixture)
    return 0


if __name__ == "__main__":
    sys.exit(main())
