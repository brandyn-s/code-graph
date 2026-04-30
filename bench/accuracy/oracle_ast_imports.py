"""IMPORTS ground-truth oracle via Python's stdlib `ast` module.

Pure deterministic: walks every .py file under the fixture, parses with
`ast.parse()`, extracts `Import` and `ImportFrom` nodes. No third-party
dependencies, runs on the system Python. Fast (seconds) — no caching
needed.

Edge format:
    from_qn = "<service>.<relative_path_without_ext>" (caller module)
    to_qn   = imported module name (absolute per Python resolution)
    type    = "IMPORTS"

`from X import Y` → single IMPORTS edge to `X` (the package). Individual
symbols Y are not tracked as separate edges; that's not how code-graph
represents them either.

`import X.Y.Z as alias` → IMPORTS edge to `X.Y.Z`.

Relative imports (`from . import foo`, `from ..bar import baz`) are
resolved to their absolute form using the importing file's package path.
"""
from __future__ import annotations

import argparse
import ast
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from common import (  # noqa: E402
    CACHE_DIR,
    Edge,
    get_fixture,
    verify_fixture_sha,
    write_edges,
)

def discover_services(fixture_path: Path) -> list[str]:
    """Every top-level directory with at least one .py file is a service
    for IMPORTS scope purposes. This matches code-graph's indexing which
    walks the whole repo, not just entry-point services.
    """
    services: list[str] = []
    for d in sorted(fixture_path.iterdir()):
        if not d.is_dir():
            continue
        if d.name.startswith(".") or d.name == "__pycache__" or d.name == "docs":
            continue
        if any(d.rglob("*.py")):
            services.append(d.name)
    return services


def module_qn_for_file(service_prefix: str, service_root: Path, file_path: Path) -> str:
    """Derive the module's qualified name relative to the service root.

    service_root/foo/bar.py       → <prefix>.foo.bar
    service_root/foo/__init__.py  → <prefix>.foo
    service_root/main.py          → <prefix>.main

    Preserves hyphens in service names (e.g., `claude-compliance`) because
    code-graph's QN format keeps the directory name verbatim. Earlier versions
    replaced `-` with `_` for Python-import-path compatibility, but that
    created a normalization mismatch with code-graph producing FP/FN noise.
    """
    rel = file_path.relative_to(service_root)
    parts = list(rel.with_suffix("").parts)
    if parts and parts[-1] == "__init__":
        parts = parts[:-1]
    return ".".join([service_prefix] + parts) if parts else service_prefix


def resolve_relative(base_qn: str, level: int, module: str | None) -> str:
    """Resolve `from . import X` / `from ..foo import Y` to absolute QN.

    Python spec: level counts dots. level=1 means current package; level=2
    means parent package; etc. Strip `level` trailing components from the
    base, then append module if provided.
    """
    parts = base_qn.split(".")
    if level >= len(parts):
        # Would walk above service root; clamp to service root.
        resolved = parts[:1]
    else:
        resolved = parts[:-level]
    if module:
        resolved.extend(module.split("."))
    return ".".join(resolved)


def _is_internal_to_fixture(module_qn: str, fixture_path: Path) -> bool:
    """Does `module_qn` resolve to a file inside the fixture?

    code-graph only creates IMPORTS edges for internal imports (between modules
    that both exist in the indexed repo). External imports (stdlib, pip packages)
    don't appear in code-graph's IMPORTS graph. To make the oracle comparable,
    we filter AST imports the same way: only include edges whose target
    resolves to a Python file inside the fixture.
    """
    parts = module_qn.split(".")
    # Try progressively shorter prefixes: `a.b.c` → `a/b/c.py` | `a/b.py` | `a.py`
    # plus the __init__.py variants.
    for i in range(len(parts), 0, -1):
        candidate = fixture_path.joinpath(*parts[:i])
        if candidate.with_suffix(".py").exists() or (candidate / "__init__.py").exists():
            return True
    return False


def _submod_exists(root: Path, parts: list[str], name: str) -> bool:
    """True if `root/<parts>/<name>.py` or `root/<parts>/<name>/__init__.py` exists."""
    candidate = root
    for seg in parts:
        candidate = candidate / seg
    return (candidate / f"{name}.py").exists() or (candidate / name / "__init__.py").exists()


def extract_imports_from_file(service_prefix: str, service_root: Path, file_path: Path, fixture_path: Path) -> list[Edge]:
    """Parse one .py file and return its IMPORTS edges."""
    try:
        source = file_path.read_bytes().decode("utf-8", errors="replace")
        tree = ast.parse(source, filename=str(file_path))
    except (SyntaxError, UnicodeDecodeError) as e:
        print(f"  WARN: parse failed on {file_path}: {e}")
        return []

    caller_qn = module_qn_for_file(service_prefix, service_root, file_path)
    edges: list[Edge] = []

    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            for alias in node.names:
                edges.append(
                    Edge(
                        from_qn=caller_qn,
                        to_qn=alias.name,
                        type="IMPORTS",
                        file=str(file_path.relative_to(service_root.parent)),
                        line=node.lineno,
                        source="ast",
                    )
                )
        elif isinstance(node, ast.ImportFrom):
            if node.level and node.level > 0:
                to_qn = resolve_relative(caller_qn, node.level, node.module)
            else:
                to_qn = node.module or ""
            if to_qn:
                edges.append(
                    Edge(
                        from_qn=caller_qn,
                        to_qn=to_qn,
                        type="IMPORTS",
                        file=str(file_path.relative_to(service_root.parent)),
                        line=node.lineno,
                        source="ast",
                    )
                )
                # ALSO emit per-imported-name edges when that name resolves to
                # a module (submodule import: `from pkg import submod`). Two
                # forms emitted so either code-graph granularity matches:
                #
                #   - `<to_qn>.<name>`              — for cross-service imports
                #                                     that match code-graph's
                #                                     package-level form (when
                #                                     <to_qn> is itself a
                #                                     top-level pkg like `shared`)
                #   - `<service>.<to_qn>.<name>`    — for intra-service imports
                #                                     where code-graph's
                #                                     sanitized-path QN includes
                #                                     the service segment (e.g.,
                #                                     `workspace-provisioner.clients.confluence`)
                #
                # Detected by checking WHICH root the submodule file lives
                # under. If under a service subdir, prefix with that service.
                for alias in node.names:
                    if alias.name == "*":
                        continue
                    candidate_parts = to_qn.split(".")
                    # Try fixture root first (cross-service imports like
                    # `from shared import errors` where `shared` is a top-level
                    # dir). Then each service root (intra-service imports).
                    # Stop at first hit.
                    fixture_root = fixture_path
                    if _submod_exists(fixture_root, candidate_parts, alias.name):
                        edges.append(Edge(
                            from_qn=caller_qn,
                            to_qn=f"{to_qn}.{alias.name}",
                            type="IMPORTS",
                            file=str(file_path.relative_to(service_root.parent)),
                            line=node.lineno,
                            source="ast",
                        ))
                        continue
                    for svc_root in fixture_path.iterdir():
                        if not svc_root.is_dir():
                            continue
                        if _submod_exists(svc_root, candidate_parts, alias.name):
                            edges.append(Edge(
                                from_qn=caller_qn,
                                to_qn=f"{svc_root.name}.{to_qn}.{alias.name}",
                                type="IMPORTS",
                                file=str(file_path.relative_to(service_root.parent)),
                                line=node.lineno,
                                source="ast",
                            ))
                            break

    return edges


def build_ground_truth(fixture_id: str, force: bool = False) -> Path:
    fixture = get_fixture(fixture_id)
    verify_fixture_sha(fixture)

    cache_path = CACHE_DIR / f"ast-imports-{fixture_id}-{fixture['short_sha']}.json"
    if cache_path.exists() and not force:
        print(f"[ast-imports] cache hit: {cache_path}")
        return cache_path

    fixture_path = Path(fixture["path"])
    services = discover_services(fixture_path)
    print(f"[ast-imports] discovered {len(services)} services: {services}")

    all_edges: list[Edge] = []
    external_count = 0
    for service_dir in services:
        service_root = fixture_path / service_dir
        if not service_root.exists():
            print(f"  WARN: service root missing: {service_root}")
            continue
        py_files = list(service_root.rglob("*.py"))
        py_files = [
            f
            for f in py_files
            if "/__pycache__/" not in str(f).replace("\\", "/")
            and not any(p.name.startswith(".") for p in f.parents)
        ]
        svc_raw = 0
        svc_kept = 0
        for py in py_files:
            for edge in extract_imports_from_file(service_dir, service_root, py, fixture_path):
                svc_raw += 1
                # Filter to internal imports only to match code-graph's scope.
                if _is_internal_to_fixture(edge.to_qn, fixture_path):
                    all_edges.append(edge)
                    svc_kept += 1
                else:
                    external_count += 1
        print(f"[ast-imports] {service_dir}: {svc_kept} internal edges ({svc_raw - svc_kept} external filtered) from {len(py_files)} files")
    print(f"[ast-imports] total filtered as external (stdlib/pip): {external_count}")

    # Dedup: the same (from, to, type) can appear multiple times in a file
    # (e.g., imported twice). Keep one.
    seen: set[tuple[str, str, str]] = set()
    deduped: list[Edge] = []
    for e in all_edges:
        if e.match_key() not in seen:
            seen.add(e.match_key())
            deduped.append(e)

    print(f"[ast-imports] total: {len(deduped)} unique edges ({len(all_edges) - len(deduped)} dups)")
    write_edges(deduped, cache_path)
    print(f"[ast-imports] wrote {cache_path}")
    return cache_path


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("fixture", help="fixture id from fixtures.json")
    ap.add_argument("--force", action="store_true", help="ignore cache")
    args = ap.parse_args()
    build_ground_truth(args.fixture, force=args.force)
    return 0


if __name__ == "__main__":
    sys.exit(main())
