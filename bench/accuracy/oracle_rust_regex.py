"""Second Rust oracle: regex-based call/def extractor for oracle-class comparison.

Companion to oracle_rust_syn.py. Uses text-level regex (no AST) to find
function definitions and call sites. Different approach than syn, so
disagreement between the two is a measure of method-class uncertainty.

Syn's advantages: handles multi-line fn signatures, nested expressions,
macro-wrapped calls. Regex misses those. But regex has its own quirks
and catches things syn might fold away. The DELTA is what we care about.

Not intended as a production oracle — this is ONLY for the inter-oracle
comparison (measuring how much the F1 number depends on which oracle
you pick).
"""
from __future__ import annotations

import argparse
import json
import re
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from common import CACHE_DIR, get_fixture, verify_fixture_sha  # noqa: E402

# Match `fn name` or `pub fn name` or `pub(crate) fn name`, up to the first `(` or `<`.
_FN_DEF_RE = re.compile(r"\bfn\s+([a-zA-Z_][a-zA-Z0-9_]*)\s*[<(]")

# Match a call-like form: an identifier followed by `(`. Misses complex
# call chains; catches the common cases. Excludes obvious definition
# contexts by a lookbehind for `fn `.
_CALL_RE = re.compile(r"(?<!fn )(?<![.\w])([a-zA-Z_][a-zA-Z0-9_]*)\s*\(")


def project_name_from_path(abs_path: Path) -> str:
    s = str(abs_path).replace("\\", "/")
    if len(s) >= 2 and s[1] == ":":
        s = s[0].lower() + s[1:]
    s = s.replace("/", "-").replace(":", "-")
    while "--" in s:
        s = s.replace("--", "-")
    return s.lstrip("-") or "root"


def extract_edges(fixture_path: Path, subset: str) -> set[tuple[str, str]]:
    """Regex-based CALLS extraction. Returns (caller_fn, callee_fn) bare-name pairs."""
    sub_dir = fixture_path / subset
    edges: set[tuple[str, str]] = set()
    # Pre-compute all fn def names so we can filter callees that aren't
    # defined anywhere (the common CALLS-to-Variable / external drop).
    all_defs: set[str] = set()
    files = []
    for rs in sub_dir.rglob("*.rs"):
        if "/target/" in rs.as_posix():
            continue
        try:
            txt = rs.read_text(encoding="utf-8", errors="replace")
        except OSError:
            continue
        for m in _FN_DEF_RE.finditer(txt):
            all_defs.add(m.group(1))
        files.append((rs, txt))

    # For each file, find every fn definition span and collect callee
    # names inside that span. Approximate: we use line-level spans
    # (function body = text between the fn signature and the next fn/trait/impl
    # at the same indent). Not perfect but consistent.
    for rs, txt in files:
        lines = txt.splitlines()
        i = 0
        while i < len(lines):
            line = lines[i]
            m = _FN_DEF_RE.search(line)
            if not m:
                i += 1
                continue
            fn_name = m.group(1)
            # Find body end: the next line at or below column 0 that starts
            # with `fn ` or `pub fn ` or ends the file.
            end = len(lines)
            brace_depth = 0
            found_brace = False
            for j in range(i, len(lines)):
                for ch in lines[j]:
                    if ch == "{":
                        brace_depth += 1
                        found_brace = True
                    elif ch == "}":
                        brace_depth -= 1
                        if found_brace and brace_depth == 0:
                            end = j + 1
                            break
                if end != len(lines):
                    break
            # Extract calls within the body.
            body = "\n".join(lines[i:end])
            for cm in _CALL_RE.finditer(body):
                callee = cm.group(1)
                if callee == fn_name:
                    continue  # recursion, keep
                if callee in all_defs:
                    edges.add((fn_name, callee))
            i = end if end > i else i + 1
    return edges


def compare_to_syn(fixture_id: str) -> None:
    fixture = get_fixture(fixture_id)
    verify_fixture_sha(fixture)
    sha = fixture["short_sha"]
    syn_cache = CACHE_DIR / f"rust-syn-{fixture_id}-{sha}.json"
    if not syn_cache.exists():
        raise SystemExit(f"run oracle_rust_syn.py {fixture_id} first")
    from common import read_edges
    syn_edges_raw = read_edges(syn_cache)
    # Extract just (caller_bare, callee_bare) pairs from syn's QNs.
    def bare(qn: str) -> str:
        return qn.rsplit(".", 1)[-1]
    syn_pairs = {(bare(e.from_qn), bare(e.to_qn)) for e in syn_edges_raw if e.type == "CALLS"}

    fixture_path = Path(fixture["path"])
    subsets = fixture.get("subset") or []
    t0 = time.time()
    regex_pairs: set[tuple[str, str]] = set()
    for sub in subsets:
        edges = extract_edges(fixture_path, sub)
        regex_pairs |= edges
    print(f"[rust-regex] extracted {len(regex_pairs)} pairs in {time.time()-t0:.1f}s")

    agree = regex_pairs & syn_pairs
    regex_only = regex_pairs - syn_pairs
    syn_only = syn_pairs - regex_pairs
    union = regex_pairs | syn_pairs
    print()
    print(f"=== Rust oracle-class uncertainty on {fixture_id} ===")
    print(f"syn (AST):   {len(syn_pairs)} edges (bare-name pairs)")
    print(f"regex:       {len(regex_pairs)} edges")
    print(f"Agree:       {len(agree)} ({100*len(agree)/max(len(union),1):.1f}% of union)")
    print(f"Regex-only:  {len(regex_only)} (syn missed)")
    print(f"Syn-only:    {len(syn_only)} (regex missed)")
    print()
    print(f"Jaccard similarity: {len(agree)/max(len(union),1):.4f}")
    print(f"Oracle-class uncertainty: +/- {(len(regex_only)+len(syn_only))/max(len(union),1):.4f} of union")

    out_path = CACHE_DIR / f"rust-oracle-uncertainty-{fixture_id}-{sha}.json"
    out_path.write_bytes(json.dumps({
        "fixture": fixture_id,
        "syn_edges": len(syn_pairs),
        "regex_edges": len(regex_pairs),
        "agree": len(agree),
        "syn_only": len(syn_only),
        "regex_only": len(regex_only),
        "jaccard": round(len(agree)/max(len(union),1), 4),
    }, indent=2).encode("utf-8"))
    print(f"[rust-regex] wrote {out_path}")


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("fixture", help="Rust fixture id")
    args = ap.parse_args()
    compare_to_syn(args.fixture)
    return 0


if __name__ == "__main__":
    sys.exit(main())
