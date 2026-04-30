"""Second Rust oracle: tree-sitter-based call/def extractor.

Replaces the crude regex-based oracle_rust_regex.py. Uses `tree-sitter-rust`
(same parser used by code-graph internally — but from Python, not C+CGO).
This is genuinely independent of oracle_rust_syn.py (which uses `syn`, a
completely different AST library written in Rust).

Purpose: measure oracle-class uncertainty for Rust. If syn and tree-sitter
agree on X% of edges, (100-X)% is the floor on "how much does the headline
F1 depend on which parser we chose?"

Note: tree-sitter's Rust grammar and `syn` are independent implementations
but target the same language. Disagreement here is smaller than the Python
Jedi-vs-PyCG gap because both are syntactic rather than semantic.
"""
from __future__ import annotations

import argparse
import json
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from common import CACHE_DIR, get_fixture, verify_fixture_sha  # noqa: E402

try:
    import tree_sitter_rust as tsrust  # type: ignore
    from tree_sitter import Language, Parser  # type: ignore
except ImportError:
    raise SystemExit("pip install tree-sitter tree-sitter-rust")

RUST = Language(tsrust.language())
parser = Parser(RUST)


def walk_fn_defs_and_calls(source: bytes):
    """Yield (fn_name, [callees]) pairs using tree-sitter traversal."""
    tree = parser.parse(source)
    root = tree.root_node

    def node_text(n):
        return source[n.start_byte:n.end_byte].decode("utf-8", errors="replace")

    def find_fn_name(fn_node):
        # function_item has a name field
        name_node = fn_node.child_by_field_name("name")
        return node_text(name_node) if name_node else ""

    def extract_callee(call_node):
        """call_expression has a function field containing the callee expr."""
        fn = call_node.child_by_field_name("function")
        if fn is None:
            return ""
        if fn.type == "identifier":
            return node_text(fn)
        if fn.type == "field_expression":
            field = fn.child_by_field_name("field")
            if field:
                return node_text(field)
        if fn.type == "scoped_identifier":
            # foo::bar -> use just `bar` (bare form for comparison with syn)
            name_node = fn.child_by_field_name("name")
            if name_node:
                return node_text(name_node)
        return ""

    def walk(n, current_fn):
        if n.type == "function_item":
            nm = find_fn_name(n)
            if nm:
                for child in n.children:
                    yield from walk(child, nm)
                return
        if n.type == "call_expression" and current_fn:
            callee = extract_callee(n)
            if callee:
                yield (current_fn, callee)
        for child in n.children:
            yield from walk(child, current_fn)

    yield from walk(root, "")


def extract_edges(fixture_path: Path, subset: str) -> set[tuple[str, str]]:
    sub_dir = fixture_path / subset
    edges: set[tuple[str, str]] = set()
    # Collect all fn defs first so we can filter callees to internal only
    # (matches the comparison model used by oracle_rust_regex).
    defs: set[str] = set()
    files = []
    for rs in sub_dir.rglob("*.rs"):
        if "/target/" in rs.as_posix():
            continue
        try:
            src = rs.read_bytes()
        except OSError:
            continue
        files.append((rs, src))
        tree = parser.parse(src)
        def find_defs(n):
            if n.type == "function_item":
                name_node = n.child_by_field_name("name")
                if name_node:
                    defs.add(src[name_node.start_byte:name_node.end_byte].decode("utf-8", errors="replace"))
            for c in n.children:
                find_defs(c)
        find_defs(tree.root_node)

    for rs, src in files:
        for caller, callee in walk_fn_defs_and_calls(src):
            if callee in defs and callee != caller:
                edges.add((caller, callee))
    return edges


def compare_to_syn(fixture_id: str) -> None:
    fixture = get_fixture(fixture_id)
    verify_fixture_sha(fixture)
    sha = fixture["short_sha"]
    syn_cache = CACHE_DIR / f"rust-syn-{fixture_id}-{sha}.json"
    if not syn_cache.exists():
        raise SystemExit(f"run oracle_rust_syn.py {fixture_id} first")
    from common import read_edges
    syn_edges = read_edges(syn_cache)
    syn_pairs = {
        (e.from_qn.rsplit(".", 1)[-1], e.to_qn.rsplit(".", 1)[-1])
        for e in syn_edges if e.type == "CALLS"
    }

    fixture_path = Path(fixture["path"])
    subsets = fixture.get("subset") or []
    t0 = time.time()
    ts_pairs: set[tuple[str, str]] = set()
    for sub in subsets:
        ts_pairs |= extract_edges(fixture_path, sub)
    print(f"[rust-ts] extracted {len(ts_pairs)} pairs in {time.time()-t0:.1f}s")

    agree = ts_pairs & syn_pairs
    ts_only = ts_pairs - syn_pairs
    syn_only = syn_pairs - ts_pairs
    union = ts_pairs | syn_pairs
    print()
    print(f"=== Rust oracle-class uncertainty on {fixture_id} (tree-sitter vs syn) ===")
    print(f"syn (AST):     {len(syn_pairs)} bare-name pairs")
    print(f"tree-sitter:   {len(ts_pairs)} bare-name pairs")
    print(f"Agree:         {len(agree)} ({100*len(agree)/max(len(union),1):.1f}% of union)")
    print(f"tree-sitter-only: {len(ts_only)}")
    print(f"syn-only:         {len(syn_only)}")
    print()
    print(f"Jaccard similarity: {len(agree)/max(len(union),1):.4f}")

    out_path = CACHE_DIR / f"rust-ts-vs-syn-{fixture_id}-{sha}.json"
    out_path.write_bytes(json.dumps({
        "fixture": fixture_id,
        "syn_edges": len(syn_pairs),
        "treesitter_edges": len(ts_pairs),
        "agree": len(agree),
        "syn_only": len(syn_only),
        "treesitter_only": len(ts_only),
        "jaccard": round(len(agree)/max(len(union),1), 4),
    }, indent=2).encode("utf-8"))
    print(f"[rust-ts] wrote {out_path}")


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("fixture", help="Rust fixture id")
    args = ap.parse_args()
    compare_to_syn(args.fixture)
    return 0


if __name__ == "__main__":
    sys.exit(main())
