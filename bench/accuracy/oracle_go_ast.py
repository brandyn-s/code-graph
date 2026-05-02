"""CALLS + IMPORTS ground-truth oracle for Go fixtures via go/ast.

Go analog of oracle_rust_syn.py. Shells out to a compiled `oracle-go-ast`
binary (built from bench/accuracy/tools/oracle-go-ast/), which uses Go's
standard library parser/ast to walk every .go file and emit edges + def
QNs in code-graph's storage form.

Replaces the earlier oracle_go_callgraph.py, which emitted go-native
symbols (github.com/foo/pkg.Func) that didn't align with code-graph's
sanitized-path QNs (c-Users-...pkg.file.Func). Keeping go_callgraph as
a reference but not wiring it into compare.py for Go fixtures.

Per-subset invocation with sanitized-path project name — same pattern as
the Rust oracle, so compare.py's query_edges_multi_project works unchanged.
"""
from __future__ import annotations

import argparse
import json
import subprocess
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from common import (  # noqa: E402
    ACCURACY_DIR,
    CACHE_DIR,
    Edge,
    get_fixture,
    run_captured,
    verify_fixture_sha,
    write_edges,
)

ORACLE_BIN = (
    ACCURACY_DIR
    / "tools"
    / "oracle-go-ast"
    / ("oracle-go-ast.exe" if sys.platform == "win32" else "oracle-go-ast")
)


def ensure_oracle_built() -> Path:
    """Build the Go oracle binary if missing. Idempotent."""
    if ORACLE_BIN.exists():
        return ORACLE_BIN
    print(f"[go-ast] building oracle binary at {ORACLE_BIN.parent}")
    rc = subprocess.run(
        ["go", "build", "-o", str(ORACLE_BIN), "."],
        cwd=ORACLE_BIN.parent,
        capture_output=True,
    ).returncode
    if rc != 0 or not ORACLE_BIN.exists():
        raise SystemExit("[go-ast] go build failed; see stderr above")
    return ORACLE_BIN


def project_name_from_path(abs_path: Path) -> str:
    """Mirror code-graph's `pipeline.ProjectNameFromPath` sanitization."""
    s = str(abs_path).replace("\\", "/")
    if len(s) >= 2 and s[1] == ":":
        s = s[0].lower() + s[1:]
    s = s.replace("/", "-").replace(":", "-")
    while "--" in s:
        s = s.replace("--", "-")
    return s.lstrip("-") or "root"


def run_oracle_on_subset(project: str, subset_dir: Path, timeout: int = 300) -> tuple[list[dict], list[str]]:
    """Shell out to oracle-go-ast. Returns (raw_edges, def_qns)."""
    argv = [str(ORACLE_BIN), str(subset_dir), project]
    rc, stdout, stderr = run_captured(argv, timeout=timeout)
    if rc != 0:
        err = stderr.decode("utf-8", errors="replace")[:500]
        print(f"  WARN: oracle-go-ast rc={rc} on {subset_dir}: {err}")
        return [], []
    stderr_text = stderr.decode("utf-8", errors="replace")
    for line in stderr_text.splitlines():
        if line.startswith("oracle-go-ast:"):
            print(f"  {line}")
    try:
        payload = json.loads(stdout.decode("utf-8", errors="replace"))
    except json.JSONDecodeError as e:
        print(f"  WARN: oracle-go-ast returned non-JSON: {e}")
        return [], []
    if isinstance(payload, dict):
        return payload.get("edges") or [], payload.get("defs") or []
    return [], []


def build_fn_def_map_from_binary(defs: list[str]) -> dict[str, str]:
    """Map bare-name -> full def QN using last segment as key.

    Same strategy as the Rust oracle. For Go, this captures both free
    functions and methods since code-graph's Go QN form is
    `<project>.<file>.<name>` uniformly (no receiver type segment).
    Ambiguity (multiple methods with the same name) resolves to first-seen.

    Note (follow-up #5, 2026-05-02): callers should prefer
    `_build_def_indexes` for ambiguity-aware bare-name resolution and
    receiver-type-resolved lookups. This single-value first-write-wins
    map is preserved for backward compatibility but is the source of
    the `Store.Close -> ConfigStore.Close` hallucination fixed in #5.
    """
    fn_to_qn: dict[str, str] = {}
    for def_qn in defs:
        last = def_qn.rsplit(".", 1)[-1]
        if last and last not in fn_to_qn:
            fn_to_qn[last] = def_qn
    return fn_to_qn


def _build_def_indexes(
    defs: list[str], project: str
) -> tuple[dict[str, list[str]], dict[str, list[str]]]:
    """Build two ambiguity-preserving def indexes for follow-up #5.

    Returns:
      bare_to_qns: simple_name -> list of QNs.
        Used for bare-name callee resolution. Only resolves when the list
        has exactly one entry — multi-candidate bare-names are DROPPED
        instead of arbitrarily picking the first (which produced the
        `Store.Close -> ConfigStore.Close` hallucination in PR #139).

      recv_method_to_qns: '<RecvType>.<method>' -> list of QNs.
        Built from method defs (those with >= 3 dot-segments after the
        project prefix: `<file>.<RecvType>.<method>`). Used to resolve
        the `<RecvType>.<method>` callee form emitted by the oracle
        binary's Y.5 self-receiver substitution.

    Project name is treated as opaque — projects like
    `c-Users-...-internal-tools` use hyphens, not dots, in code-graph's
    sanitized form, so a single project-prefix-strip is sufficient.
    """
    project_prefix = project + "."
    bare: dict[str, list[str]] = {}
    recv_method: dict[str, list[str]] = {}
    for qn in defs:
        if not qn.startswith(project_prefix):
            continue
        rest = qn[len(project_prefix):]
        rest_parts = rest.split(".")
        if not rest_parts:
            continue
        simple = rest_parts[-1]
        if simple:
            bare.setdefault(simple, []).append(qn)
        # Method def shape: <file>.<RecvType>.<method> (>= 3 segments
        # after project prefix). Free functions are 2 segments and skip
        # this branch.
        if len(rest_parts) >= 3:
            key = rest_parts[-2] + "." + rest_parts[-1]
            recv_method.setdefault(key, []).append(qn)
    return bare, recv_method


def resolve_and_filter(
    raw_edges: list[dict],
    fn_def_map: dict[str, str],
    project: str,
    defs: list[str] | None = None,
) -> tuple[list[Edge], dict[str, int]]:
    """Filter to internal-only edges with resolved QNs.

    CALLS rules:
      - bare ident (single segment): look up in `bare_to_qns`. Resolve only
        if exactly one candidate. Multi-candidate bare-names are DROPPED
        (rather than arbitrarily picked) — fixes the
        `Store.Close -> ConfigStore.Close` hallucination from PR #139.
      - `<RecvType>.<method>` (2 segments, RecvType is a known struct):
        resolve via `recv_method_to_qns`. Emitted by the oracle binary's
        Y.5 self-receiver substitution (PR following #137). When inside a
        method `func (p *Pipeline) X() { p.Y() }`, the oracle emits
        callee `Pipeline.Y` instead of the unresolvable `p.Y`.
      - `<file>.<fn>` (2 segments, first is a known file segment): existing
        package-local resolution path.
      - All else: drop.

    IMPORTS rules:
      - Keep only paths starting with the module prefix. We detect module
        prefix by looking for imports that contain the project-short name
        (e.g., "codebase-memory-mcp" in the module path). External imports
        (std, third-party) are dropped.

    For IMPORTS, the oracle initially emits the raw import path from `import
    "github.com/foo/bar"`. To match code-graph (which stores IMPORTS edges
    between file modules), we'd need to resolve import paths to internal
    file QNs — deferred. IMPORTS are dropped for now; the `.meta.json`
    sidecar records counts.

    Backward compatibility: `fn_def_map` argument retained for callers that
    haven't switched to passing `defs`. When `defs` is None, behavior
    falls back to the legacy first-write-wins resolution path (with the
    Store.Close hallucination still present).
    """
    stats = {
        "calls_bare_resolved": 0,
        "calls_bare_unresolved": 0,
        "calls_bare_ambiguous_dropped": 0,
        "calls_recv_method_resolved": 0,
        "calls_recv_method_ambiguous_dropped": 0,
        "calls_path_resolved": 0,
        "calls_path_dropped": 0,
        "imports_dropped": 0,
    }
    # Build a set of known file segments so we can recognize package-local refs.
    # Def QNs look like `<project>.<file>.<name>` — so the penultimate segment
    # is the "file" identifier for free fns. Collect all unique file segments.
    file_segments: set[str] = set()
    project_prefix = project + "."
    for qn in fn_def_map.values():
        if not qn.startswith(project_prefix):
            continue
        rest = qn[len(project_prefix):]
        parts = rest.split(".")
        if len(parts) >= 2:
            # For `<project>.<file>.<fn>`, parts[0] is the file
            file_segments.add(parts[0])

    # Build the ambiguity-aware indexes (follow-up #5). When `defs` is not
    # provided, fall back to single-value fn_def_map for backward compat.
    if defs is not None:
        bare_to_qns, recv_method_to_qns = _build_def_indexes(defs, project)
    else:
        bare_to_qns = {k: [v] for k, v in fn_def_map.items()}
        recv_method_to_qns = {}

    kept: list[Edge] = []

    for e in raw_edges:
        t = e["type"]
        to = e["to_qn"]
        if t == "IMPORTS":
            stats["imports_dropped"] += 1
            continue
        if t != "CALLS":
            continue
        segs = to.split(".")
        if len(segs) == 1:
            # Bare-name resolution: only emit when uniquely resolvable.
            # Multi-candidate bare names produce the Store.Close
            # hallucination if we pick first-seen.
            candidates = bare_to_qns.get(to, [])
            if len(candidates) == 1:
                kept.append(Edge(
                    from_qn=e["from_qn"],
                    to_qn=candidates[0],
                    type="CALLS",
                    file=e.get("file", ""),
                    line=int(e.get("line", 0) or 0),
                    source="go-ast",
                ))
                stats["calls_bare_resolved"] += 1
            elif len(candidates) > 1:
                stats["calls_bare_ambiguous_dropped"] += 1
            else:
                stats["calls_bare_unresolved"] += 1
        elif len(segs) == 2:
            # Try Y.5 receiver-type resolution FIRST. The oracle binary's
            # self-receiver substitution emits `<RecvType>.<method>` for
            # calls like `p.Method()` inside a `func (p *Pipeline) ...`.
            rm_candidates = recv_method_to_qns.get(to, [])
            if len(rm_candidates) == 1:
                kept.append(Edge(
                    from_qn=e["from_qn"],
                    to_qn=rm_candidates[0],
                    type="CALLS",
                    file=e.get("file", ""),
                    line=int(e.get("line", 0) or 0),
                    source="go-ast",
                ))
                stats["calls_recv_method_resolved"] += 1
                continue
            elif len(rm_candidates) > 1:
                # Multiple `<RecvType>.<method>` defs across files (e.g.
                # split-file methods in the same package). Pick the first
                # — these are still semantically the same method,
                # disambiguated only by file. Code-graph stores each in
                # its own file's QN, so the resolved QN we pick here may
                # not match exactly. Conservative choice: drop. If this
                # bucket gets large, we can re-evaluate.
                stats["calls_recv_method_ambiguous_dropped"] += 1
                continue

            # Fall back to file-segment-based package-local resolution.
            pkg, fn = segs
            # Package-local call: `router.ForProject()` where router.go is in
            # the same indexed subset. Resolve via bare_to_qns by fn name.
            if pkg in file_segments:
                resolved_candidates = bare_to_qns.get(fn, [])
                if len(resolved_candidates) == 1:
                    kept.append(Edge(
                        from_qn=e["from_qn"],
                        to_qn=resolved_candidates[0],
                        type="CALLS",
                        file=e.get("file", ""),
                        line=int(e.get("line", 0) or 0),
                        source="go-ast",
                    ))
                    stats["calls_path_resolved"] += 1
                else:
                    stats["calls_path_dropped"] += 1
            else:
                # External package (fmt, time, etc.). Code-graph DOES emit
                # CALLS edges targeting stdlib Functions when its Go LSP
                # resolver finds them, but not uniformly — only for imports
                # the package actually uses and only to symbols the resolver
                # tracks. Emitting all syntactic 2-seg external calls would
                # blow up FNs (oracle has, code-graph doesn't). Drop to
                # preserve recall symmetry. Empirically emitting them
                # dropped F1 from 0.679 to 0.494.
                stats["calls_path_dropped"] += 1
        else:
            stats["calls_path_dropped"] += 1
    return kept, stats


def build_ground_truth(fixture_id: str, force: bool = False) -> Path:
    fixture = get_fixture(fixture_id)
    verify_fixture_sha(fixture)

    cache_path = CACHE_DIR / f"go-ast-{fixture_id}-{fixture['short_sha']}.json"
    if cache_path.exists() and not force:
        print(f"[go-ast] cache hit: {cache_path}")
        return cache_path

    ensure_oracle_built()

    fixture_path = Path(fixture["path"])
    subsets: list[str] = fixture.get("subset") or []
    if not subsets:
        raise SystemExit(f"fixture {fixture_id}: no 'subset' key; Go fixtures must list subset dirs")

    t0 = time.time()
    all_edges: list[Edge] = []
    per_subset_stats: dict[str, dict] = {}
    for sub in subsets:
        sub_dir = fixture_path / sub
        if not sub_dir.exists():
            print(f"  WARN: subset missing: {sub_dir}")
            continue
        project = project_name_from_path(sub_dir.resolve())
        print(f"[go-ast] subset={sub} project={project}")
        raw, defs = run_oracle_on_subset(project, sub_dir)
        fn_def_map = build_fn_def_map_from_binary(defs)
        print(f"  fn defs (binary-sourced): {len(fn_def_map)}")
        kept, stats = resolve_and_filter(raw, fn_def_map, project, defs=defs)
        per_subset_stats[sub] = {"raw_edges": len(raw), "kept": len(kept), **stats}
        print(
            f"  raw={len(raw)} kept={len(kept)} "
            f"bare={stats['calls_bare_resolved']} "
            f"recv_method={stats.get('calls_recv_method_resolved', 0)} "
            f"path={stats['calls_path_resolved']} "
            f"dropped={stats['calls_bare_unresolved'] + stats['calls_path_dropped'] + stats.get('calls_bare_ambiguous_dropped', 0) + stats.get('calls_recv_method_ambiguous_dropped', 0)}"
        )
        all_edges.extend(kept)

    # Dedup by match_key.
    seen: set[tuple[str, str, str]] = set()
    deduped: list[Edge] = []
    for e in all_edges:
        if e.match_key() not in seen:
            seen.add(e.match_key())
            deduped.append(e)

    elapsed = time.time() - t0
    print(f"[go-ast] total: {len(deduped)} unique edges ({len(all_edges) - len(deduped)} dups) in {elapsed:.1f}s")

    write_edges(deduped, cache_path)
    sidecar = cache_path.with_suffix(".meta.json")
    sidecar.write_bytes(
        json.dumps(
            {
                "fixture": fixture_id,
                "sha": fixture["sha"],
                "elapsed_seconds": round(elapsed, 1),
                "subsets": subsets,
                "per_subset": per_subset_stats,
                "unique_edges": len(deduped),
            },
            indent=2,
        ).encode("utf-8")
    )
    print(f"[go-ast] wrote {cache_path} + sidecar")
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
