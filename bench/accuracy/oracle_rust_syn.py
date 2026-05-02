"""CALLS + IMPORTS ground-truth oracle for Rust fixtures via syn-based helper.

This is the Rust analog of oracle_pycg.py (Python) and oracle_ast_imports.py.
It shells out to a compiled `oracle-rust-syn` binary (built from
bench/accuracy/tools/oracle-rust-syn/), which uses `syn` 2.x to parse every
.rs file and emit edges from ExprCall / ExprMethodCall / ItemUse.

Why syn instead of rust-analyzer or cargo-call-stack:
  - syn parses unexpanded source (same level as code-graph's tree-sitter).
    Apples-to-apples on macro-invisible calls.
  - Runs per-crate in seconds. rust-analyzer requires full type resolution
    across the workspace; at 260 crates in psm that's
    prohibitive and introduces type-resolution semantics code-graph doesn't
    have.
  - Deterministic and cacheable by fixture SHA.

Project-name alignment (critical for compare.py):
  code-graph derives the project name for an indexed path by sanitizing the
  absolute path: `C:/Users/...canstatd` -> `c-Users-...canstatd`. Node QNs
  are stored as `<project>.<rel_path>.<name>`.

  To match, the oracle runs once per fixture SUBSET (not per Cargo.toml) and
  passes the sanitized path as the project name to the Rust binary. Edges
  emit with the same long project prefix, so compare.py's
  `strip_project_prefix` works identically on both sides.

Bare-call resolution:
  syn can only report the syntactic callee: `foo()` -> to_qn="foo",
  `Duration::from_secs(1)` -> to_qn="Duration.from_secs". Code-graph's
  resolver does the same lookup we do here: match bare calls against
  function definitions in indexed files, upgrade to full QN. We mirror it.
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
    / "oracle-rust-syn"
    / "target"
    / "release"
    / ("oracle-rust-syn.exe" if sys.platform == "win32" else "oracle-rust-syn")
)


def ensure_oracle_built() -> Path:
    """Build the Rust oracle binary if missing. Idempotent."""
    if ORACLE_BIN.exists():
        return ORACLE_BIN
    print(f"[rust-syn] building oracle binary at {ORACLE_BIN.parent.parent}")
    cargo_dir = ACCURACY_DIR / "tools" / "oracle-rust-syn"
    rc = subprocess.run(
        ["cargo", "build", "--release"],
        cwd=cargo_dir,
        capture_output=True,
    ).returncode
    if rc != 0 or not ORACLE_BIN.exists():
        raise SystemExit("[rust-syn] cargo build failed; see stderr above")
    return ORACLE_BIN


def project_name_from_path(abs_path: Path) -> str:
    """Mirror code-graph's `pipeline.ProjectNameFromPath` sanitization.

    Rules: ToSlash, lowercase the drive letter, replace `/` and `:` with `-`,
    collapse consecutive dashes, trim leading dash.
    """
    s = str(abs_path).replace("\\", "/")
    # Lowercase drive letter: "C:/..." -> "c:/..."
    if len(s) >= 2 and s[1] == ":":
        s = s[0].lower() + s[1:]
    s = s.replace("/", "-").replace(":", "-")
    while "--" in s:
        s = s.replace("--", "-")
    return s.lstrip("-") or "root"


def run_oracle_on_subset(project: str, subset_dir: Path, timeout: int = 300) -> tuple[list[dict], list[str]]:
    """Shell out to oracle-rust-syn once per subset dir.

    Returns (raw_edges, def_qns). The binary emits JSON
    {"edges": [...], "defs": ["<project>.<path>.<fn>", ...]}. The def QNs
    have full impl/mod scope because the Rust visitor tracks it — simpler
    and more correct than a Python-side regex scan.
    """
    argv = [str(ORACLE_BIN), str(subset_dir), project]
    rc, stdout, stderr = run_captured(argv, timeout=timeout)
    if rc != 0:
        err = stderr.decode("utf-8", errors="replace")[:500]
        print(f"  WARN: oracle-rust-syn rc={rc} on {subset_dir}: {err}")
        return [], []
    stderr_text = stderr.decode("utf-8", errors="replace")
    for line in stderr_text.splitlines():
        if line.startswith("oracle-rust-syn:"):
            print(f"  {line}")
    try:
        payload = json.loads(stdout.decode("utf-8", errors="replace"))
    except json.JSONDecodeError as e:
        print(f"  WARN: oracle-rust-syn returned non-JSON on {subset_dir}: {e}")
        return [], []
    if isinstance(payload, dict):
        return payload.get("edges", []) or [], payload.get("defs", []) or []
    # Backward compat: old binary returned just the edges array.
    if isinstance(payload, list):
        return payload, []
    return [], []


def build_fn_def_map_from_binary(defs: list[str]) -> dict[str, list[str]]:
    """Map bare-name -> [full QNs] using def QNs from the Rust binary.

    Def QNs look like:
      "c-Users-...canstatd.src.main.main"                    (free fn)
      "c-Users-...canstatd.src.main.AdsbDecoder.process_message"  (method)
      "c-Users-...canstatd.src.main.tests.altitude_defaults_to_zero"  (mod tests)

    For bare-call resolution we key by the LAST segment (the fn ident). Returns
    ALL defs sharing each bare name; resolve_and_filter drops ambiguous names
    (count > 1) to avoid the bare-name conflation pattern that produced 5/5
    instrument-artifact FNs in the 2026-05-02 plateau-diagnose Step 6 sample
    (e.g., `service.call(req)` resolved to `TailscaleAuthService.call` because
    `call` had multiple defs and the first-encountered policy picked one).
    """
    fn_to_qns: dict[str, list[str]] = {}
    for def_qn in defs:
        last = def_qn.rsplit(".", 1)[-1]
        if last:
            fn_to_qns.setdefault(last, []).append(def_qn)
    return fn_to_qns


def resolve_and_filter(
    raw_edges: list[dict],
    fn_def_map: dict[str, list[str]],
) -> tuple[list[Edge], dict[str, int]]:
    """Resolve bare calls, drop external/unresolvable/ambiguous edges.

    Rules:
      IMPORTS: drop — code-graph's Rust IMPORTS resolver only emits edges for
        a narrow set of cases (confirmed empirically: 0 edges for a single
        canstatd index, 8 total across the full 260-crate repo). To avoid
        false-negative noise from extractor limitations, we drop IMPORTS from
        the oracle output too. They can be re-enabled when code-graph's
        Rust IMPORTS resolver is completed.
      CALLS path-form (`a.b.c`): drop — these are external references
        (`std.fs.read_to_string`, `Duration.from_secs`) that code-graph
        can't resolve without type info. Symmetric drop.
      CALLS bare (`foo`):
        - 0 defs: drop (external or test-only).
        - 1 def: emit upgraded to full QN.
        - 2+ defs: drop (ambiguous). syn has no type info, so any pick among
          multiple same-named defs is a guess. Mirrors code-graph's
          discrimination ladder (Phase 3a-3d) which drops the same shape.
          2026-05-02 plateau-diagnose Step 6 verified 5/5 of these were
          oracle over-emissions on assetman: e.g., `service.call(req)` was
          resolving to TailscaleAuthService.call (one of multiple `call`
          defs) and emerging as a phantom FN against code-graph.
    """
    stats = {
        "imports_dropped_always": 0,
        "calls_bare_resolved": 0,
        "calls_bare_unresolved": 0,
        "calls_bare_ambiguous_dropped": 0,
        "calls_path_dropped": 0,
    }
    kept: list[Edge] = []
    for e in raw_edges:
        if e["type"] == "IMPORTS":
            stats["imports_dropped_always"] += 1
            continue
        if e["type"] != "CALLS":
            continue
        to = e["to_qn"]
        if "." in to:
            stats["calls_path_dropped"] += 1
            continue
        # Bare call: try resolve via fn_def_map.
        candidates = fn_def_map.get(to) or []
        if not candidates:
            stats["calls_bare_unresolved"] += 1
            continue
        if len(candidates) > 1:
            stats["calls_bare_ambiguous_dropped"] += 1
            continue
        stats["calls_bare_resolved"] += 1
        kept.append(Edge(
            from_qn=e["from_qn"],
            to_qn=candidates[0],
            type="CALLS",
            file=e.get("file", ""),
            line=int(e.get("line", 0) or 0),
            source="syn",
        ))
    return kept, stats


def build_ground_truth(fixture_id: str, force: bool = False) -> Path:
    fixture = get_fixture(fixture_id)
    verify_fixture_sha(fixture)

    cache_path = CACHE_DIR / f"rust-syn-{fixture_id}-{fixture['short_sha']}.json"
    if cache_path.exists() and not force:
        print(f"[rust-syn] cache hit: {cache_path}")
        return cache_path

    ensure_oracle_built()

    fixture_path = Path(fixture["path"])
    subsets: list[str] = fixture.get("subset") or []
    if not subsets:
        raise SystemExit(f"fixture {fixture_id}: no 'subset' key; Rust fixtures must list subset dirs")

    t0 = time.time()
    all_edges: list[Edge] = []
    per_subset_stats: dict[str, dict] = {}
    for sub in subsets:
        sub_dir = fixture_path / sub
        if not sub_dir.exists():
            print(f"  WARN: subset missing: {sub_dir}")
            continue
        project = project_name_from_path(sub_dir.resolve())
        print(f"[rust-syn] subset={sub} project={project}")
        raw, defs = run_oracle_on_subset(project, sub_dir)
        fn_def_map = build_fn_def_map_from_binary(defs)
        print(f"  fn defs (binary-sourced): {len(fn_def_map)}")
        kept, stats = resolve_and_filter(raw, fn_def_map)
        per_subset_stats[sub] = {"raw_edges": len(raw), "kept": len(kept), **stats}
        print(
            f"  raw={len(raw)} kept={len(kept)} "
            f"bare_resolved={stats['calls_bare_resolved']} "
            f"bare_unresolved={stats['calls_bare_unresolved']} "
            f"path_dropped={stats['calls_path_dropped']}"
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
    print(f"[rust-syn] total: {len(deduped)} unique edges ({len(all_edges) - len(deduped)} dups) in {elapsed:.1f}s")

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
    print(f"[rust-syn] wrote {cache_path} + sidecar")
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
