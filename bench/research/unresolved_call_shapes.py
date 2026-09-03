"""Phase C1: Unresolved-call shape histogram per language.

The 2026-05-05 roundtable's top-leverage investment is "what shapes are
the unresolved calls?" — the answer determines which extractor work
matters most. Currently the graph just records `unresolved_call_count`
as an integer; to know whether that integer is dominated by `getattr`
patterns or by Go interface dispatch or by Rust trait objects, we need
to bin them.

This script walks the indexed projects (using their root_paths from
the SQLite store) and pattern-matches the canonical dispatch shapes
per language. The output is a histogram of "what FRACTION of unresolved-
call-shaped patterns in each language are which shape."

The pattern matchers are heuristic — they over-count (a `getattr` in a
test file is counted) and under-count (some legitimate dispatch shapes
are language-specific edge cases not covered). For the purposes of
"which shape category dominates," they're directionally accurate.

Usage:
    python bench/research/unresolved_call_shapes.py
        Walk all indexed projects; produce the histogram.

    python bench/research/unresolved_call_shapes.py --project NAME
        Walk one project only (faster iteration).

    python bench/research/unresolved_call_shapes.py --json
        Output structured JSON instead of human-readable table.

Decision rule (per Plan 1 Phase C1):
    >60% of unresolved shapes in (a) wrong-function despite correct graph
        → Func Acc@10 work is #1 priority
    >60% in (b) missing dynamic-dispatch edge
        → cross-language indirect-call coverage is #1
    >60% in (c) scorer artifact (resolved by iter=2/MRR aggregation)
        → scorer/protocol ablation is #1

This script answers half of the decision: it produces the (b) bucket
data per language. The (a) and (c) buckets require the Loc-Bench
failure-audit harness (see locbench_failure_audit.py).
"""
from __future__ import annotations

import argparse
import json
import pathlib
import re
import sqlite3
import sys
from collections import Counter, defaultdict
from typing import Dict, List, Tuple

CACHE_DIR = pathlib.Path.home() / ".cache" / "code-graph"

# Canonical dispatch-shape patterns per language. Each pattern is a
# regex compiled with the canonical name (used as the bucket label).
# Patterns are SHAPES of dispatch sites — counting these gives the
# distribution of dispatch SHAPES in source code, which is a lower
# bound on the distribution of unresolved-call shapes.
PYTHON_PATTERNS: List[Tuple[str, re.Pattern]] = [
    ("executor.submit", re.compile(r"\.submit\s*\(\s*[a-zA-Z_]")),
    ("executor.map", re.compile(r"\.map\s*\(\s*[a-zA-Z_]")),
    ("getattr_string_literal", re.compile(r"getattr\s*\([^,]+,\s*['\"][a-zA-Z_]")),
    ("getattr_variable", re.compile(r"getattr\s*\([^,]+,\s*[a-zA-Z_][a-zA-Z_0-9]*\s*[,\)]")),
    ("decorator_call", re.compile(r"^\s*@[a-zA-Z_][\w.]*\s*\(", re.MULTILINE)),
    ("kwargs_call", re.compile(r"\(\s*\*\*[a-zA-Z_]")),
    ("partial_call", re.compile(r"functools\.partial\s*\(")),
    ("dispatch_dict", re.compile(r"\b[a-zA-Z_]\w*\s*\[\s*['\"][^'\"]+['\"]\s*\]\s*\(")),
]

GO_PATTERNS: List[Tuple[str, re.Pattern]] = [
    ("interface_method_call", re.compile(r"\b[a-zA-Z_]\w*\.[A-Z][a-zA-Z_]*\s*\(")),
    ("type_assertion", re.compile(r"\.\(\s*\*?[a-zA-Z_][\w.]*\s*\)")),
    ("type_switch", re.compile(r"switch\s+[a-zA-Z_]\w*\s*:?=\s*[a-zA-Z_]\w*\.\(type\)")),
    ("function_value", re.compile(r"\bfunc\s*\(")),
    ("reflect_call", re.compile(r"reflect\.(Value)?\s*\(?\s*\)?\.\s*Call")),
    ("goroutine_dispatch", re.compile(r"go\s+[a-zA-Z_]\w*\s*\(")),
]

RUST_PATTERNS: List[Tuple[str, re.Pattern]] = [
    ("trait_object", re.compile(r"&\s*(?:dyn\s+|Box<\s*dyn\s+)")),
    ("trait_method_call", re.compile(r"\b[a-zA-Z_]\w*\s*\.\s*[a-z_][\w]*\s*\(")),  # heuristic: lowercase method
    ("closure_invocation", re.compile(r"\|[^|]*\|\s*\{")),
    ("function_pointer_arg", re.compile(r"fn\s*\([^)]*fn\s*\(")),
    ("dynamic_dispatch_via_box", re.compile(r"Box<\s*dyn\s+")),
]

TYPESCRIPT_PATTERNS: List[Tuple[str, re.Pattern]] = [
    ("dynamic_property_access", re.compile(r"\b[a-zA-Z_]\w*\s*\[\s*['\"][^'\"]+['\"]\s*\]\s*\(")),
    ("function_value", re.compile(r"\b(const|let|var)\s+[a-zA-Z_]\w*\s*=\s*\(")),
    ("decorator", re.compile(r"^\s*@[a-zA-Z_][\w.]*", re.MULTILINE)),
    ("method_dispatch", re.compile(r"\.\s*[a-z_][\w]*\s*\(")),
    ("call_apply", re.compile(r"\.\s*(call|apply|bind)\s*\(")),
]

JAVASCRIPT_PATTERNS: List[Tuple[str, re.Pattern]] = TYPESCRIPT_PATTERNS  # similar shapes

LANGUAGE_PATTERNS: Dict[str, Tuple[List[Tuple[str, re.Pattern]], List[str]]] = {
    "python": (PYTHON_PATTERNS, [".py"]),
    "go": (GO_PATTERNS, [".go"]),
    "rust": (RUST_PATTERNS, [".rs"]),
    "typescript": (TYPESCRIPT_PATTERNS, [".ts", ".tsx"]),
    "javascript": (JAVASCRIPT_PATTERNS, [".js", ".jsx", ".mjs"]),
}


def list_indexed_projects() -> List[Tuple[str, pathlib.Path]]:
    """Return [(project_name, root_path), ...] for every indexed project
    that has a recorded root_path."""
    if not CACHE_DIR.exists():
        return []
    out: List[Tuple[str, pathlib.Path]] = []
    for db in sorted(CACHE_DIR.glob("*.db")):
        if db.name.endswith("-shm.db") or db.name.endswith("-wal.db"):
            continue
        try:
            con = sqlite3.connect(str(db))
            con.execute("PRAGMA query_only = ON")
            rows = con.execute(
                "SELECT name, root_path FROM projects WHERE root_path != '' AND root_path IS NOT NULL"
            ).fetchall()
            con.close()
        except sqlite3.Error:
            continue
        for name, root_path in rows:
            p = pathlib.Path(root_path)
            if p.is_dir():
                out.append((name, p))
    return out


def walk_files(root: pathlib.Path, extensions: List[str], max_files: int = 5000) -> List[pathlib.Path]:
    """Walk root, return files matching extensions (skipping common
    unwanted dirs). Cap at max_files to bound runtime."""
    skip_dirs = {".git", "node_modules", "target", "dist", "build", "__pycache__", ".venv", "venv", ".cache"}
    out: List[pathlib.Path] = []
    for path in root.rglob("*"):
        if len(out) >= max_files:
            break
        if not path.is_file():
            continue
        if any(part in skip_dirs for part in path.parts):
            continue
        if path.suffix in extensions:
            out.append(path)
    return out


def count_shapes_in_file(content: str, patterns: List[Tuple[str, re.Pattern]]) -> Counter:
    """Count pattern matches in content. Returns Counter of shape -> count."""
    counts: Counter = Counter()
    for shape, pattern in patterns:
        matches = pattern.findall(content)
        if matches:
            counts[shape] = len(matches)
    return counts


def aggregate_project(root: pathlib.Path) -> Dict[str, Counter]:
    """Aggregate shape counts for a single project, broken down by language."""
    by_lang: Dict[str, Counter] = defaultdict(Counter)
    for lang, (patterns, exts) in LANGUAGE_PATTERNS.items():
        files = walk_files(root, exts)
        for fpath in files:
            try:
                content = fpath.read_text(encoding="utf-8", errors="replace")
            except OSError:
                continue
            file_counts = count_shapes_in_file(content, patterns)
            for shape, count in file_counts.items():
                by_lang[lang][shape] += count
    return by_lang


def main() -> int:
    ap = argparse.ArgumentParser(description=(__doc__ or "").split("\n\n")[0])
    ap.add_argument("--project", help="Single project name to analyze")
    ap.add_argument("--json", action="store_true", help="Output JSON instead of table")
    args = ap.parse_args()

    projects = list_indexed_projects()
    if args.project:
        projects = [(name, root) for name, root in projects if name == args.project]
    if not projects:
        print("No indexed projects found at " + str(CACHE_DIR))
        return 1

    print(f"# Phase C1: unresolved-call shape histogram", file=sys.stderr)
    print(f"# {len(projects)} projects to analyze", file=sys.stderr)

    aggregate: Dict[str, Counter] = defaultdict(Counter)
    per_project: Dict[str, Dict[str, Counter]] = {}
    for name, root in projects:
        print(f"  {name}: walking {root}...", file=sys.stderr)
        by_lang = aggregate_project(root)
        per_project[name] = by_lang
        for lang, counts in by_lang.items():
            for shape, count in counts.items():
                aggregate[lang][shape] += count

    if args.json:
        out = {
            "aggregate": {
                lang: dict(counts) for lang, counts in aggregate.items()
            },
            "per_project": {
                name: {lang: dict(counts) for lang, counts in by_lang.items()}
                for name, by_lang in per_project.items()
            },
        }
        print(json.dumps(out, indent=2, sort_keys=True))
        return 0

    print(f"\n=== Aggregate dispatch-shape histogram across {len(projects)} projects ===\n")
    for lang, counts in sorted(aggregate.items()):
        if not counts:
            continue
        total = sum(counts.values())
        print(f"## {lang} (total dispatch sites: {total})")
        for shape, count in counts.most_common():
            pct = 100.0 * count / total
            bar = "#" * int(40 * count / max(counts.values()))
            print(f"  {shape:30s} {count:>7} ({pct:>5.1f}%) {bar}")
        print()

    # Decision-rule output
    print("=== Decision rule per Plan 1 Phase C1 ===")
    print("  Per-language: which shape dominates?")
    for lang, counts in sorted(aggregate.items()):
        if not counts:
            continue
        total = sum(counts.values())
        top_shape, top_count = counts.most_common(1)[0]
        top_pct = 100.0 * top_count / total
        print(f"    {lang:12s} top shape: {top_shape} ({top_pct:.1f}%)")
    print()
    print("  This data feeds Plan 1 Phase D (investigations gated by C):")
    print("  - If Python's `getattr_variable` or `kwargs_call` dominate,")
    print("    INDIRECT_CALLS v0.4 (fn-pointer-as-arg) and v0.5 (kwargs propagation)")
    print("    are the highest-leverage Python work.")
    print("  - If Go's `interface_method_call` dominates, cross-language")
    print("    indirect-call coverage (Go interface dispatch) is the priority.")
    print("  - If Rust's `trait_object` / `dynamic_dispatch_via_box` dominate,")
    print("    Rust trait-object dispatch coverage is the priority.")

    return 0


if __name__ == "__main__":
    sys.exit(main())
