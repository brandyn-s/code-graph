"""CALLS ground-truth oracle for Python fixtures via PyCG.

PyCG (https://github.com/vitsalis/PyCG, Salis 2021) is a peer-reviewed
flow- and context-insensitive call-graph generator for Python. It's the
standard micro-benchmark ground-truth tool cited in the literature
(PyCG test suite + HeaderGen additions + Jarvis).

Usage:
    python oracle_pycg.py mcp-servers
        --> bench/accuracy/cache/pycg-mcp-servers-81fa7d5.json

Per-service invocation:
    mcp-servers is a multi-service repo (claude-proxy, crowdstrike, ...).
    We run PyCG once per service directory with its entry point, then
    union all edge outputs. Each service's QN namespace is prefixed with
    the service directory name to avoid collisions.

Qualified-name normalization:
    PyCG emits QNs relative to its `--package` root, like `foo.bar.baz`.
    To match code-graph's QN format (`<project>.<path_parts>.<name>`),
    we prefix with the service directory name and record the mapping
    so compare.py can reconcile.
"""
from __future__ import annotations

import argparse
import json
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from common import (  # noqa: E402
    CACHE_DIR,
    Edge,
    get_fixture,
    run_captured,
    verify_fixture_sha,
    write_edges,
)
from _env import ensure_bench_venv  # noqa: E402

# Services in mcp-servers that have clear Python entry points.
# Format: (service_dir, entry_point_relative_to_service)
MCP_SERVERS_SERVICES = [
    ("airlock", "airlock_mcp_server.py"),
    ("claude-proxy", "claude_proxy.py"),
    ("crowdstrike", "proxy.py"),
    ("security-remix", "security_remix_server.py"),
    ("slack-connect", "slack_connect_app.py"),
]


def run_pycg_for_service(
    fixture_path: Path, service_dir: str, entry_point: str, timeout: int = 120
) -> tuple[dict, str | None]:
    """Run PyCG on one service; return (edges_dict, error_or_none).

    PyCG exit 0 + empty stdout happens when the service has no callable
    graph (e.g., pure config dir). That's not an error, just no edges.
    """
    service_path = fixture_path / service_dir
    entry_abs = service_path / entry_point
    if not entry_abs.exists():
        return {}, f"entry point missing: {entry_abs}"

    # PyCG emits JSON to stdout when --output is not set.
    # Use the service directory as --package so qualified names are
    # relative to the service, which matches code-graph's per-service
    # namespacing.
    # PyCG requires Python 3.11 + three local patches (see bench/accuracy/_env.py).
    # ensure_bench_venv() provisions / repairs / returns the cached interpreter,
    # idempotent across runs. The bench venv is uv-managed at
    # ~/.cache/code-graph-bench/py311.
    pycg_py = str(ensure_bench_venv())
    argv = [
        pycg_py,
        "-m",
        "pycg",
        "--package",
        str(service_path),
        "--max-iter",
        "2",
        str(entry_abs),
    ]
    try:
        rc, stdout, stderr = run_captured(argv, timeout=timeout)
    except Exception as e:  # subprocess.TimeoutExpired, etc.
        return {}, f"pycg crashed on {service_dir}: {e}"

    if rc != 0:
        return {}, (
            f"pycg rc={rc} on {service_dir}; "
            f"stderr={stderr.decode('utf-8', errors='replace')[:500]}"
        )

    try:
        data = json.loads(stdout.decode("utf-8", errors="replace"))
    except json.JSONDecodeError as e:
        return {}, f"pycg returned non-JSON on {service_dir}: {e}"

    return data, None


def normalize_to_edges(
    pycg_output: dict[str, list[str]],
    service_prefix: str,
    service_root: Path,
    fixture_path: Path,
) -> list[Edge]:
    """Convert PyCG's {caller: [callees]} into Edge list, filtered + namespaced.

    To align with code-graph's scope (which only tracks calls between functions
    defined in indexed files), we:

    1. Compute `service_modules` = Python-file stems under the service dir.
       PyCG emits these as its caller-key prefix.
    2. Compute `fixture_top_dirs` = top-level dirs in the fixture that contain
       __init__.py or any .py (e.g., `shared`, `airlock`, `claude-proxy`).
       These are cross-service internal namespaces.
    3. For each (caller, callee) pair:
       - Caller is always internal (PyCG analyzed it) → prefix with service.
       - Callee: if first segment ∈ service_modules → intra-service internal,
         prefix with service. If first segment ∈ fixture_top_dirs → cross-service
         internal, emit verbatim (matches code-graph's top-level). Else
         external (stdlib/pip) → filter out (outside code-graph's scope).
    """
    edges: list[Edge] = []
    # Convert path-like service prefixes (e.g., "src/flask" or "src\flask") to
    # dotted form so emitted QNs match code-graph's fqn.Compute output
    # (which also dot-joins path segments). mcp-servers' single-segment
    # service names pass through unchanged.
    prefix = service_prefix.replace("\\", "/").replace("/", ".")

    service_modules = {f.stem for f in service_root.rglob("*.py")}
    # fixture_top_dirs: directories directly under the fixture root that contain
    # Python code. These are potential cross-service namespaces.
    fixture_top_dirs = {
        d.name
        for d in fixture_path.iterdir()
        if d.is_dir() and any(d.rglob("*.py"))
    }

    filtered_external = 0
    for caller, callees in pycg_output.items():
        if not isinstance(callees, list):
            continue
        from_qn = f"{prefix}.{caller}" if caller else prefix
        for callee in callees:
            if callee.startswith("<"):
                continue
            first_seg = callee.split(".", 1)[0]
            if first_seg in service_modules:
                to_qn = f"{prefix}.{callee}"
            elif first_seg in fixture_top_dirs:
                to_qn = callee
            else:
                # External (stdlib / pip package); code-graph doesn't track these.
                filtered_external += 1
                continue
            edges.append(
                Edge(
                    from_qn=from_qn,
                    to_qn=to_qn,
                    type="CALLS",
                    source="pycg",
                )
            )
    if filtered_external:
        print(f"    filtered {filtered_external} external calls (stdlib/pip)")
    return edges


def build_ground_truth(fixture_id: str, force: bool = False) -> Path:
    fixture = get_fixture(fixture_id)
    verify_fixture_sha(fixture)

    cache_path = CACHE_DIR / f"pycg-{fixture_id}-{fixture['short_sha']}.json"
    if cache_path.exists() and not force:
        print(f"[pycg] cache hit: {cache_path}")
        return cache_path

    fixture_path = Path(fixture["path"])
    # Derive per-service (service_dir, entry_point) tuples. mcp-servers has
    # its hardcoded list of services; other fixtures use the `entry_points`
    # field from fixtures.json (relative paths).
    if fixture_id == "mcp-servers":
        services = list(MCP_SERVERS_SERVICES)
    else:
        entry_points = fixture.get("entry_points") or []
        if not entry_points:
            raise SystemExit(
                f"oracle_pycg.py: fixture {fixture_id} has no entry_points. "
                f"Add entry_points: [\"path/to/main.py\", ...] to fixtures.json."
            )
        services = []
        for ep in entry_points:
            # ep is a path relative to fixture_path. Split into dir + basename.
            # PyCG wants a directory as --package and a file as the entry point.
            ep_path = Path(ep)
            services.append((str(ep_path.parent), ep_path.name))

    all_edges: list[Edge] = []
    errors: list[str] = []
    t0 = time.time()
    for service_dir, entry_point in services:
        print(f"[pycg] running on {service_dir}/{entry_point} ...")
        data, err = run_pycg_for_service(fixture_path, service_dir, entry_point)
        if err:
            print(f"  WARN: {err}")
            errors.append(err)
            continue
        svc_edges = normalize_to_edges(data, service_dir, fixture_path / service_dir, fixture_path)
        print(f"  {len(svc_edges)} edges")
        all_edges.extend(svc_edges)

    # Dedup — multiple services may share util modules, producing duplicate
    # edges. Match key ignores file/line/source.
    seen: set[tuple[str, str, str]] = set()
    deduped: list[Edge] = []
    for e in all_edges:
        if e.match_key() not in seen:
            seen.add(e.match_key())
            deduped.append(e)

    elapsed = time.time() - t0
    print(
        f"[pycg] total: {len(deduped)} unique edges "
        f"({len(all_edges) - len(deduped)} dups) in {elapsed:.1f}s, "
        f"{len(errors)} errors"
    )

    write_edges(deduped, cache_path)
    # Also persist a sidecar with the raw per-service data + errors
    # so gap analysis can debug specific services.
    sidecar = cache_path.with_suffix(".meta.json")
    sidecar.write_bytes(
        json.dumps(
            {
                "fixture": fixture_id,
                "sha": fixture["sha"],
                "elapsed_seconds": round(elapsed, 1),
                "services_run": [s[0] for s in MCP_SERVERS_SERVICES],
                "errors": errors,
                "edge_count": len(deduped),
            },
            indent=2,
        ).encode("utf-8")
    )
    print(f"[pycg] wrote {cache_path} + sidecar")
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
