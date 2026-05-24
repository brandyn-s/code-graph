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
from cargo_metadata import CargoMetadataResult, run_cargo_metadata  # noqa: E402

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
    cargo: CargoMetadataResult | None = None,
) -> tuple[list[Edge], dict[str, int]]:
    """Resolve bare and scoped-form calls, drop external/unresolvable/ambiguous.

    Rules:
      IMPORTS: drop — code-graph's Rust IMPORTS resolver only emits edges for
        a narrow set of cases (confirmed empirically: 0 edges for a single
        canstatd index, 8 total across the full 260-crate repo). Symmetric
        drop until the resolver is completed.

      External-chain drop (NEW 2026-05-24): when `cargo` is provided and
      the edge's chain_root_path (set by the Rust binary on ExprMethodCall)
      classifies as an external crate per cargo metadata, drop the edge
      regardless of whether the bare name has an in-graph def. This closes
      the bug where `diesel::insert_into(...).get_result(conn)` phantom-
      resolved to a coincidentally-named in-graph `get_result` (see audit
      `plans/2026-05-24-syn-oracle-barename-audit.md`).

      CALLS bare (`foo`):
        - 0 defs: drop (external or test-only).
        - 1 def: emit upgraded to full QN.
        - 2+ defs: drop (ambiguous). syn has no type info; any pick among
          multiple same-named defs is a guess.

      CALLS scoped (`Type.method`, originally `Type::method`) — ACC-007 fix
      (2026-05-02): mirrors code-graph's resolveViaTypeStaticDispatch
      symmetrically.
        - The binary emits scoped-form `to_qn` ONLY from `ExprCall` (path
          expressions like `Foo::new` or `std::fs::read_to_string`). Method
          calls always emit bare names. So a `.` in `to_qn` is a strong
          signal: scoped path.
        - Split on `.` -> (typeName, remainder).
        - candidates = fn_def_map[last(remainder)] filtered to those whose
          parent QN's last segment == typeName (i.e., the class name match).
        - 0 internal candidates: drop (external — Vec::new, tracing::info).
        - 1 candidate: emit upgraded to <class_qn>.<remainder>.
        - 2+ candidates: drop (ambiguous — same class name in multiple
          modules, can't pick without type info).

      CALLS multi-segment scoped (`a.b.c.method`): same as scoped but with
      remainder containing nested segments. Match on last suffix segment +
      class prefix; drop if no match.
    """
    stats = {
        "imports_dropped_always": 0,
        "calls_bare_resolved": 0,
        "calls_bare_unresolved": 0,
        "calls_bare_ambiguous_dropped": 0,
        "calls_method_external_chain_dropped": 0,  # 2026-05-24 external-chain fix
        "calls_scoped_resolved": 0,
        "calls_scoped_external_dropped": 0,
        "calls_scoped_ambiguous_dropped": 0,
    }
    external_crates = cargo.external_crates if cargo else set()
    workspace_members = cargo.workspace_members if cargo else set()

    def is_external_chain_root(chain_root: str | None) -> bool:
        """Classify the chain's syntactic root against cargo metadata.

        The root is the first segment of the chain (e.g., 'diesel',
        'tokio', 'OpenOptions', 'self', 'x'). Normalize per Rust
        identifier convention (already kebab-stripped at parse time
        for cargo names) and check membership in the external set
        but NOT the workspace set.
        """
        if not chain_root:
            return False
        # Normalize the same way cargo metadata names are normalized so
        # `tokio_util` vs `tokio-util` both classify correctly.
        root_norm = chain_root.replace("-", "_")
        if root_norm in workspace_members:
            return False  # workspace member overrides
        return root_norm in external_crates

    kept: list[Edge] = []
    for e in raw_edges:
        if e["type"] == "IMPORTS":
            stats["imports_dropped_always"] += 1
            continue
        if e["type"] != "CALLS":
            continue
        to = e["to_qn"]
        if "." not in to:
            # Bare call (ExprMethodCall): check chain root against cargo metadata
            # BEFORE the fn_def_map lookup. An external chain root means the
            # syntactic receiver is an external crate (diesel, tokio, std);
            # the bare callee's in-graph "candidate" is a coincidental name
            # match, not a real edge.
            if is_external_chain_root(e.get("chain_root_path")):
                stats["calls_method_external_chain_dropped"] += 1
                continue
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
            continue

        # Scoped call (`Type.method` or `a.b.c.method` from `Type::method`
        # or `a::b::c::method`). The binary emits this shape ONLY from
        # ExprCall path expressions; never from ExprMethodCall.
        type_name, _, remainder = to.partition(".")
        method_name = remainder.rsplit(".", 1)[-1]  # last segment

        # Candidates: defs whose simple name matches the method, AND whose
        # immediate parent's last segment matches typeName.
        method_candidates = fn_def_map.get(method_name) or []
        internal_matches = []
        for cand_qn in method_candidates:
            # cand_qn shape: <project>...<class>.<method>
            parts = cand_qn.rsplit(".", 2)
            if len(parts) < 2:
                continue
            parent_last = parts[-2] if len(parts) >= 2 else ""
            if parent_last == type_name:
                internal_matches.append(cand_qn)

        # Dedup identical QNs before counting. The oracle binary's
        # `visit_impl_item_fn` emits one def-QN per impl block, so a type
        # with multiple `impl Trait<X> for T { fn method }` blocks (e.g.
        # `HostType` with `From<i16>`, `From<&str>`, `From<String>`) yields
        # N literally-identical entries in fn_def_map[method] — all of
        # shape `<project>.<file>.T.method`. Without this dedup, the
        # `len(internal_matches) > 1` check below fires on the duplicates
        # and drops the call as `calls_scoped_ambiguous_dropped` even
        # though every candidate IS the same target. Confirmed via
        # 2026-05-24 HostType.from / NixOSImage.from audit: 9 of the
        # 74 assetman FPs trace to this missing dedup.
        #
        # set() is safe here — internal_matches is a list of strings; we
        # only need uniqueness, not ordering (downstream we either emit
        # the single element or drop on len>1).
        internal_matches = list(set(internal_matches))

        if not internal_matches:
            # External (Vec::new, tracing::info, std::fs::read_to_string)
            # OR module-routed call we can't resolve (`crate::foo::bar`
            # where `foo` is a module not a class).
            stats["calls_scoped_external_dropped"] += 1
            continue
        if len(internal_matches) > 1:
            # Same class name in multiple modules — ambiguous.
            stats["calls_scoped_ambiguous_dropped"] += 1
            continue
        stats["calls_scoped_resolved"] += 1
        kept.append(Edge(
            from_qn=e["from_qn"],
            to_qn=internal_matches[0],
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
        # 2026-05-24 external-chain fix: load cargo metadata per subset so
        # the resolver can drop external-rooted method chains via cargo's
        # authoritative crate classification. Graceful degradation: empty
        # result on missing Cargo.toml / cargo binary / parse error means
        # external-chain drop simply doesn't fire (pre-fix behavior).
        cargo = run_cargo_metadata(sub_dir)
        print(
            f"  fn defs (binary-sourced): {len(fn_def_map)} | "
            f"cargo external_crates={len(cargo.external_crates)} "
            f"workspace_members={len(cargo.workspace_members)}"
        )
        kept, stats = resolve_and_filter(raw, fn_def_map, cargo=cargo)
        per_subset_stats[sub] = {"raw_edges": len(raw), "kept": len(kept), **stats}
        print(
            f"  raw={len(raw)} kept={len(kept)} "
            f"bare_resolved={stats['calls_bare_resolved']} "
            f"bare_unresolved={stats['calls_bare_unresolved']} "
            f"method_external_dropped={stats['calls_method_external_chain_dropped']} "
            f"scoped_resolved={stats['calls_scoped_resolved']} "
            f"scoped_external_dropped={stats['calls_scoped_external_dropped']}"
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
