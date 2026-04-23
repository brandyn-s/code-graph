"""CALLS + IMPORTS ground-truth oracle for Go fixtures.

CALLS: shells out to `golang.org/x/tools/cmd/callgraph -algo=rta`, parses the
  graphviz output, and keeps edges whose caller AND callee are inside the
  module's import prefix. RTA (Rapid Type Analysis) matches code-graph's
  gopls-informed extraction best — CHA is too imprecise (over-approximates
  interface calls), pointer analysis is overkill and slow.

IMPORTS: shells out to `go list -json ./...`, extracts the Imports field
  from each package. Internal-filter keeps imports whose target is inside
  the module.

QN format: code-graph's Go registry (internal/pipeline/go_dep_registry.go)
  stores Go function nodes as `importPath + "." + funcName` and methods as
  `importPath + "." + recvType + "." + funcName`. We emit the same form.
  Example: `github.com/DeusData/codebase-memory-mcp/internal/store.NewStore`

Scope filter: edges whose caller package path matches one of the fixture's
  subset entries (e.g., internal/store, internal/cypher).

Prerequisites:
  - `go` in PATH (Go 1.21+)
  - `go install golang.org/x/tools/cmd/callgraph@latest` (run on first use)
"""
from __future__ import annotations

import argparse
import json
import os
import re
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


# The callgraph command emits lines like:
#   "caller_sym" -> "callee_sym"
# where *_sym is a Go symbol form:
#   "package/path.FuncName"                            (package-level func)
#   "(*package/path.TypeName).MethodName"              (pointer-receiver method)
#   "(package/path.TypeName).MethodName"               (value-receiver method)
#   "(package/path.Interface).MethodName"              (interface method)
# We parse both sides with this:
_EDGE_RE = re.compile(r'^\s*"([^"]+)"\s*->\s*"([^"]+)"\s*$')


def ensure_callgraph_installed() -> Path:
    """Return path to the callgraph binary. Fails with actionable message."""
    # Try GOPATH/bin first, then PATH.
    gopath_bin = Path.home() / "go" / "bin" / (
        "callgraph.exe" if sys.platform == "win32" else "callgraph"
    )
    if gopath_bin.exists():
        return gopath_bin
    # Fallback to PATH lookup.
    from shutil import which
    p = which("callgraph")
    if p:
        return Path(p)
    raise SystemExit(
        "[go-callgraph] `callgraph` tool not found. Install with:\n"
        "    go install golang.org/x/tools/cmd/callgraph@latest"
    )


def parse_go_symbol(sym: str) -> tuple[str, str] | None:
    """Split a go callgraph symbol into (package_path, qualified_name).

    Input examples:
      "github.com/foo/bar.FuncName"              -> ("github.com/foo/bar", "github.com/foo/bar.FuncName")
      "(*github.com/foo/bar.Type).Method"        -> ("github.com/foo/bar", "github.com/foo/bar.Type.Method")
      "(github.com/foo/bar.Iface).Method"        -> ("github.com/foo/bar", "github.com/foo/bar.Iface.Method")
    """
    # Strip method-call parens and pointer star.
    s = sym
    if s.startswith("(*"):
        # Pointer method: (*pkg.Type).Method
        m = re.match(r"^\(\*([^)]+)\)\.(.+)$", s)
        if not m:
            return None
        qual_type = m.group(1)  # "github.com/foo/bar.Type"
        method = m.group(2)
        dot = qual_type.rfind(".")
        if dot < 0:
            return None
        pkg = qual_type[:dot]
        type_name = qual_type[dot + 1:]
        return pkg, f"{pkg}.{type_name}.{method}"
    if s.startswith("("):
        # Value or interface method: (pkg.Type).Method
        m = re.match(r"^\(([^)]+)\)\.(.+)$", s)
        if not m:
            return None
        qual_type = m.group(1)
        method = m.group(2)
        dot = qual_type.rfind(".")
        if dot < 0:
            return None
        pkg = qual_type[:dot]
        type_name = qual_type[dot + 1:]
        return pkg, f"{pkg}.{type_name}.{method}"
    # Plain "pkg/path.FuncName"
    dot = s.rfind(".")
    if dot < 0:
        return None
    pkg = s[:dot]
    # Sanity: pkg should contain "/" or be a stdlib package name.
    # We return it as-is; internal-filter decides.
    return pkg, s


def run_go_callgraph(module_root: Path, timeout: int = 300) -> list[tuple[str, str]]:
    """Run callgraph -algo=rta ./... from module_root. Returns [(caller, callee)]."""
    callgraph_bin = ensure_callgraph_installed()
    argv = [str(callgraph_bin), "-algo=rta", "-format=graphviz", "./..."]
    env = os.environ.copy()
    env["PYTHONIOENCODING"] = "utf-8"
    env["GO111MODULE"] = "on"
    import subprocess
    proc = subprocess.run(
        argv,
        cwd=str(module_root),
        capture_output=True,
        env=env,
        timeout=timeout,
    )
    if proc.returncode != 0:
        err = proc.stderr.decode("utf-8", errors="replace")[:1000]
        raise SystemExit(f"[go-callgraph] rc={proc.returncode}: {err}")
    text = proc.stdout.decode("utf-8", errors="replace")
    edges: list[tuple[str, str]] = []
    for line in text.splitlines():
        m = _EDGE_RE.match(line)
        if m:
            edges.append((m.group(1), m.group(2)))
    return edges


def run_go_list(module_root: Path, timeout: int = 120) -> list[dict]:
    """Run `go list -json ./...`. Returns list of package metadata dicts."""
    argv = ["go", "list", "-json", "./..."]
    env = os.environ.copy()
    env["PYTHONIOENCODING"] = "utf-8"
    env["GO111MODULE"] = "on"
    import subprocess
    proc = subprocess.run(
        argv,
        cwd=str(module_root),
        capture_output=True,
        env=env,
        timeout=timeout,
    )
    if proc.returncode != 0:
        err = proc.stderr.decode("utf-8", errors="replace")[:1000]
        raise SystemExit(f"[go-list] rc={proc.returncode}: {err}")
    # go list -json emits concatenated JSON objects (one per package), not an array.
    text = proc.stdout.decode("utf-8", errors="replace")
    packages: list[dict] = []
    decoder = json.JSONDecoder()
    idx = 0
    while idx < len(text):
        # Skip whitespace
        while idx < len(text) and text[idx].isspace():
            idx += 1
        if idx >= len(text):
            break
        try:
            obj, end = decoder.raw_decode(text, idx)
            packages.append(obj)
            idx = end
        except json.JSONDecodeError:
            break
    return packages


def module_path(module_root: Path) -> str:
    """Read `module <path>` from go.mod."""
    go_mod = module_root / "go.mod"
    for line in go_mod.read_text(encoding="utf-8", errors="replace").splitlines():
        line = line.strip()
        if line.startswith("module "):
            return line.split(None, 1)[1].strip()
    raise SystemExit(f"[go-callgraph] no module declaration in {go_mod}")


def scope_filter(package_path: str, module_prefix: str, subset_dirs: set[str]) -> bool:
    """Is the given package part of the fixture's in-scope subset?"""
    if not package_path.startswith(module_prefix):
        return False
    rel = package_path[len(module_prefix):].lstrip("/")
    # Keep if the package matches any subset prefix.
    for sd in subset_dirs:
        sd_norm = sd.replace("\\", "/")
        if rel == sd_norm or rel.startswith(sd_norm + "/"):
            return True
    return False


def build_ground_truth(fixture_id: str, force: bool = False) -> Path:
    fixture = get_fixture(fixture_id)
    verify_fixture_sha(fixture)

    cache_path = CACHE_DIR / f"go-callgraph-{fixture_id}-{fixture['short_sha']}.json"
    if cache_path.exists() and not force:
        print(f"[go-callgraph] cache hit: {cache_path}")
        return cache_path

    module_root = Path(fixture["path"])
    module_prefix = module_path(module_root)
    subsets = set(fixture.get("subset") or [])
    if not subsets:
        raise SystemExit(f"[go-callgraph] fixture {fixture_id}: no 'subset' configured")

    print(f"[go-callgraph] module: {module_prefix}")
    print(f"[go-callgraph] subset: {sorted(subsets)}")

    # --- Phase 1: CALLS via callgraph ---
    t0 = time.time()
    print(f"[go-callgraph] running callgraph -algo=rta ./... (large output, may take ~1min)")
    raw_edges = run_go_callgraph(module_root, timeout=600)
    print(f"[go-callgraph] raw callgraph edges: {len(raw_edges)}")

    calls_edges: list[Edge] = []
    kept_calls = 0
    dropped_external = 0
    dropped_out_of_subset = 0
    dropped_init_chain = 0
    for caller_sym, callee_sym in raw_edges:
        caller_parsed = parse_go_symbol(caller_sym)
        callee_parsed = parse_go_symbol(callee_sym)
        if not caller_parsed or not callee_parsed:
            continue
        caller_pkg, caller_qn = caller_parsed
        callee_pkg, callee_qn = callee_parsed
        # Drop the implicit init-chain: when package A imports package B,
        # Go's runtime synthesizes `A.init -> B.init` edges. These aren't
        # visible in source and code-graph (tree-sitter) doesn't emit them,
        # so dropping preserves apples-to-apples. Explicit user init bodies
        # (e.g., `func init() { doSetup() }`) are kept because their callees
        # are named differently than `init`.
        if callee_qn.endswith(".init") and caller_qn.endswith(".init"):
            dropped_init_chain += 1
            continue
        # Scope alignment: caller must be inside our subset. Callee must be
        # inside the module (internal-only), but may be outside the subset —
        # we keep cross-subset internal calls because code-graph does too.
        caller_in_scope = scope_filter(caller_pkg, module_prefix, subsets)
        callee_internal = callee_pkg.startswith(module_prefix) or callee_pkg == module_prefix
        if not caller_in_scope:
            dropped_out_of_subset += 1
            continue
        if not callee_internal:
            dropped_external += 1
            continue
        calls_edges.append(
            Edge(
                from_qn=caller_qn,
                to_qn=callee_qn,
                type="CALLS",
                source="go-callgraph-rta",
            )
        )
        kept_calls += 1

    print(
        f"[go-callgraph] CALLS: kept={kept_calls} "
        f"dropped_external={dropped_external} dropped_out_of_subset={dropped_out_of_subset} "
        f"dropped_init_chain={dropped_init_chain}"
    )

    # --- Phase 2: IMPORTS via go list -json ---
    print(f"[go-callgraph] running go list -json ./... for imports")
    packages = run_go_list(module_root, timeout=120)
    imports_edges: list[Edge] = []
    imp_kept = 0
    imp_ext = 0
    imp_out_of_subset = 0
    for pkg in packages:
        importer_path = pkg.get("ImportPath", "")
        if not scope_filter(importer_path, module_prefix, subsets):
            imp_out_of_subset += 1
            continue
        for dep in pkg.get("Imports", []) or []:
            if not dep.startswith(module_prefix):
                imp_ext += 1
                continue
            imports_edges.append(
                Edge(
                    from_qn=importer_path,
                    to_qn=dep,
                    type="IMPORTS",
                    source="go-list",
                )
            )
            imp_kept += 1

    print(
        f"[go-callgraph] IMPORTS: kept={imp_kept} "
        f"dropped_external={imp_ext} dropped_out_of_subset packages={imp_out_of_subset}"
    )

    all_edges = calls_edges + imports_edges

    # Dedup (go callgraph can emit duplicate edges for multi-call-site).
    seen: set[tuple[str, str, str]] = set()
    deduped: list[Edge] = []
    for e in all_edges:
        if e.match_key() not in seen:
            seen.add(e.match_key())
            deduped.append(e)

    elapsed = time.time() - t0
    print(
        f"[go-callgraph] total: {len(deduped)} unique edges "
        f"({len(all_edges) - len(deduped)} dups) in {elapsed:.1f}s"
    )

    write_edges(deduped, cache_path)
    sidecar = cache_path.with_suffix(".meta.json")
    sidecar.write_bytes(
        json.dumps(
            {
                "fixture": fixture_id,
                "sha": fixture["sha"],
                "elapsed_seconds": round(elapsed, 1),
                "module_path": module_prefix,
                "subsets": sorted(subsets),
                "raw_callgraph_edges": len(raw_edges),
                "kept_calls": kept_calls,
                "kept_imports": imp_kept,
                "dropped_external_calls": dropped_external,
                "dropped_external_imports": imp_ext,
                "dropped_out_of_subset_calls": dropped_out_of_subset,
                "dropped_init_chain": dropped_init_chain,
                "unique_edges": len(deduped),
            },
            indent=2,
        ).encode("utf-8")
    )
    print(f"[go-callgraph] wrote {cache_path} + sidecar")
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
