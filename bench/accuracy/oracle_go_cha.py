"""Second Go oracle: `go callgraph -algo=cha` for inter-oracle comparison.

Companion to oracle_go_ast.py (syntactic). Uses a DIFFERENT algorithm
(Class Hierarchy Analysis, `go callgraph -algo=cha`) on the same fixture.

CHA over-approximates interface calls (treats any method call through
an interface as potentially calling any implementation of that interface),
while RTA narrows via reachability. Both are semantic analyses — neither
pure-syntactic like oracle_go_ast.

The goal here is the same as oracle_jedi.py vs oracle_pycg.py: measure
oracle-class uncertainty. If CHA and RTA agree on ~X% of edges, we know
F1 numbers vs either one carry (100-X)% uncertainty from algorithm choice.

Note: we already had a go-callgraph RTA oracle (oracle_go_callgraph.py)
but it emits go-native symbols that don't match code-graph's QN form.
This file runs BOTH algorithms under the hood and compares THEM against
each other, not against code-graph. Independent of code-graph accuracy.
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from common import CACHE_DIR, get_fixture, verify_fixture_sha  # noqa: E402

_EDGE_RE = re.compile(r'^\s*"([^"]+)"\s*->\s*"([^"]+)"\s*$')


def ensure_callgraph_installed() -> Path:
    gopath_bin = Path.home() / "go" / "bin" / (
        "callgraph.exe" if sys.platform == "win32" else "callgraph"
    )
    if gopath_bin.exists():
        return gopath_bin
    from shutil import which
    p = which("callgraph")
    if p:
        return Path(p)
    raise SystemExit("install with: go install golang.org/x/tools/cmd/callgraph@latest")


def run_callgraph(module_root: Path, algo: str, timeout: int = 600) -> set[tuple[str, str]]:
    """Run callgraph with the given algo and return a set of (caller, callee) pairs."""
    bin_path = ensure_callgraph_installed()
    argv = [str(bin_path), f"-algo={algo}", "-format=graphviz", "./..."]
    proc = subprocess.run(argv, cwd=str(module_root), capture_output=True, timeout=timeout)
    if proc.returncode != 0:
        err = proc.stderr.decode("utf-8", errors="replace")[:500]
        raise SystemExit(f"go callgraph -algo={algo} rc={proc.returncode}: {err}")
    text = proc.stdout.decode("utf-8", errors="replace")
    edges: set[tuple[str, str]] = set()
    for line in text.splitlines():
        m = _EDGE_RE.match(line)
        if m:
            edges.add((m.group(1), m.group(2)))
    return edges


def module_path(module_root: Path) -> str:
    go_mod = module_root / "go.mod"
    for line in go_mod.read_text(encoding="utf-8", errors="replace").splitlines():
        line = line.strip()
        if line.startswith("module "):
            return line.split(None, 1)[1].strip()
    raise SystemExit(f"no module in {go_mod}")


def filter_internal(
    edges: set[tuple[str, str]], module_prefix: str
) -> set[tuple[str, str]]:
    """Keep only edges where both caller and callee are in-module."""
    out: set[tuple[str, str]] = set()
    for a, b in edges:
        if module_prefix in a and module_prefix in b:
            out.add((a, b))
    return out


def compare_algos(fixture_id: str) -> None:
    fixture = get_fixture(fixture_id)
    verify_fixture_sha(fixture)
    module_root = Path(fixture["path"]).resolve()
    mp = module_path(module_root)
    print(f"[go-cha] comparing algos on {fixture_id} (module={mp})")
    t0 = time.time()
    rta = filter_internal(run_callgraph(module_root, "rta"), mp)
    print(f"[go-cha] rta: {len(rta)} edges in {time.time()-t0:.1f}s")
    t0 = time.time()
    cha = filter_internal(run_callgraph(module_root, "cha"), mp)
    print(f"[go-cha] cha: {len(cha)} edges in {time.time()-t0:.1f}s")

    agree = rta & cha
    rta_only = rta - cha
    cha_only = cha - rta
    union = rta | cha
    print()
    print(f"=== Go oracle-class uncertainty on {fixture_id} ===")
    print(f"RTA:      {len(rta)} edges")
    print(f"CHA:      {len(cha)} edges")
    print(f"Agree:    {len(agree)} edges ({100*len(agree)/max(len(union),1):.1f}% of union)")
    print(f"RTA-only: {len(rta_only)} (CHA missed)")
    print(f"CHA-only: {len(cha_only)} (RTA missed, often interface over-approximation)")
    print()
    print(f"Jaccard similarity: {len(agree)/max(len(union),1):.4f}")
    print(f"Oracle-class uncertainty: +/- {(len(rta_only) + len(cha_only))/max(len(union),1):.4f} of union")
    # Save to a side-by-side JSON for the report.
    out_path = CACHE_DIR / f"go-algo-uncertainty-{fixture_id}-{fixture['short_sha']}.json"
    out_path.write_bytes(json.dumps({
        "fixture": fixture_id,
        "module_path": mp,
        "rta_edges": len(rta),
        "cha_edges": len(cha),
        "agree": len(agree),
        "rta_only": len(rta_only),
        "cha_only": len(cha_only),
        "jaccard": round(len(agree)/max(len(union),1), 4),
        "algorithm_class_uncertainty": round((len(rta_only) + len(cha_only))/max(len(union),1), 4),
    }, indent=2).encode("utf-8"))
    print(f"[go-cha] wrote {out_path}")


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("fixture", help="fixture id (Go, with go.mod at path)")
    args = ap.parse_args()
    compare_algos(args.fixture)
    return 0


if __name__ == "__main__":
    sys.exit(main())
