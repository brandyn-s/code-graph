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

Multi-crate handling:
  Each subset entry in fixtures.json (e.g., "canstatd") may contain multiple
  Cargo.toml files (workspace crates). We find every Cargo.toml under each
  subset path, read the package.name, and run the oracle once per crate with
  project_name = the Cargo.toml's package.name.

Internal vs external filtering:
  Mirror oracle_pycg.py's approach. Collect all crate names seen across the
  fixture. An IMPORTS edge is internal iff the first segment of to_qn matches
  a crate name (with `-` -> `_` normalization since Rust `use` paths
  substitute hyphens with underscores). A CALLS edge is internal iff the
  first segment of to_qn matches a crate name OR the callee is a free-function
  call whose name matches a function defined somewhere in the fixture.
  Everything else (stdlib, pip-equivalent crates.io deps, unresolved methods)
  is dropped, matching code-graph's scope.
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


_PACKAGE_NAME_RE = re.compile(r'^\s*name\s*=\s*"([^"]+)"', re.MULTILINE)


def read_crate_name(cargo_toml: Path) -> str | None:
    """Parse `[package] name = "..."` from a Cargo.toml. Skip workspaces
    without a [package] section."""
    try:
        txt = cargo_toml.read_text(encoding="utf-8", errors="replace")
    except OSError:
        return None
    # Only match names in [package] section; if the file is a workspace root,
    # there's no [package] and first name= might be in [workspace.package].
    # We use a simple heuristic: first [package] section block.
    if "[package]" not in txt:
        return None
    pkg_idx = txt.index("[package]")
    next_section = re.search(r"^\[", txt[pkg_idx + len("[package]"):], re.MULTILINE)
    section_end = pkg_idx + len("[package]") + (next_section.start() if next_section else len(txt))
    pkg_block = txt[pkg_idx:section_end]
    m = _PACKAGE_NAME_RE.search(pkg_block)
    return m.group(1) if m else None


def find_crates(root: Path) -> list[tuple[str, Path]]:
    """Return (crate_name, crate_dir) for every Cargo.toml under root.

    crate_dir is the directory CONTAINING Cargo.toml. Skips target/ dirs.
    """
    out: list[tuple[str, Path]] = []
    for cargo in root.rglob("Cargo.toml"):
        if "/target/" in cargo.as_posix() or "\\target\\" in str(cargo):
            continue
        name = read_crate_name(cargo)
        if not name:
            continue
        out.append((name, cargo.parent))
    return out


def run_oracle_on_crate(crate_name: str, crate_dir: Path, timeout: int = 120) -> list[dict]:
    """Shell out to oracle-rust-syn; return list of edge dicts (raw, unfiltered)."""
    argv = [str(ORACLE_BIN), str(crate_dir), crate_name]
    rc, stdout, stderr = run_captured(argv, timeout=timeout)
    if rc != 0:
        err = stderr.decode("utf-8", errors="replace")[:500]
        print(f"  WARN: oracle-rust-syn rc={rc} on {crate_name}: {err}")
        return []
    # oracle writes progress to stderr (visible for debugging) and edges JSON
    # to stdout. stderr is informational, don't error on it.
    stderr_text = stderr.decode("utf-8", errors="replace")
    if stderr_text.strip():
        # Just print the summary line, not per-error noise here.
        for line in stderr_text.splitlines():
            if line.startswith("oracle-rust-syn:"):
                print(f"  {line}")
    try:
        return json.loads(stdout.decode("utf-8", errors="replace"))
    except json.JSONDecodeError as e:
        print(f"  WARN: oracle-rust-syn returned non-JSON on {crate_name}: {e}")
        return []


def normalize_crate_name(name: str) -> str:
    """Rust `use` paths substitute `-` with `_`. Normalize so that crate
    name `canstatd-types` matches `use canstatd_types::...` edges."""
    return name.replace("-", "_")


def filter_internal(
    edges: list[dict],
    internal_crate_names: set[str],
    internal_fn_defs: set[str],
) -> tuple[list[Edge], int, int]:
    """Filter to internal-only edges.

    Rules (mirroring oracle_pycg + oracle_ast_imports patterns):
      - IMPORTS: keep iff to_qn's first segment ∈ internal_crate_names.
      - CALLS: keep iff
          (a) to_qn's first segment ∈ internal_crate_names (path-based call), OR
          (b) to_qn has a single segment AND matches a known internal fn def
              (bare local call resolved within the crate).
        Bare method calls (single segment that's a method name, not a known
        internal fn def) are dropped — same as code-graph drops them when it
        can't resolve the receiver type.
    """
    internal_norm = {normalize_crate_name(n) for n in internal_crate_names}
    kept: list[Edge] = []
    ext_imports = 0
    ext_calls = 0
    for e in edges:
        first_seg = e["to_qn"].split(".", 1)[0]
        segs_count = e["to_qn"].count(".") + 1
        is_internal_prefix = normalize_crate_name(first_seg) in internal_norm
        if e["type"] == "IMPORTS":
            if is_internal_prefix:
                kept.append(Edge(**e))
            else:
                ext_imports += 1
        elif e["type"] == "CALLS":
            if is_internal_prefix:
                kept.append(Edge(**e))
            elif segs_count == 1 and e["to_qn"] in internal_fn_defs:
                # Bare free-function call resolved by name.
                kept.append(Edge(**e))
            else:
                ext_calls += 1
    return kept, ext_imports, ext_calls


def collect_internal_fn_defs(fixture_path: Path, subsets: list[str]) -> set[str]:
    """Collect function names defined anywhere in the subset crates, so we can
    recognize bare free-function calls as internal.

    Very coarse: just grep for `fn <name>` and `pub fn <name>`. Shared
    suffix with method names is unavoidable, but that's symmetric with
    code-graph's resolver.
    """
    pattern = re.compile(r"\bfn\s+([a-zA-Z_][a-zA-Z0-9_]*)\s*[<(]")
    names: set[str] = set()
    for sub in subsets:
        sub_dir = fixture_path / sub
        for rs in sub_dir.rglob("*.rs"):
            if "/target/" in rs.as_posix():
                continue
            try:
                txt = rs.read_text(encoding="utf-8", errors="replace")
            except OSError:
                continue
            for m in pattern.finditer(txt):
                names.add(m.group(1))
    return names


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
        raise SystemExit(f"fixture {fixture_id}: no 'subset' key; Rust fixtures must list crate dirs")

    # Phase 1: discover all crates across subsets.
    all_crates: list[tuple[str, Path]] = []
    for sub in subsets:
        sub_dir = fixture_path / sub
        if not sub_dir.exists():
            print(f"  WARN: subset missing: {sub_dir}")
            continue
        crates = find_crates(sub_dir)
        print(f"[rust-syn] {sub}: {len(crates)} crate(s) -> {[c[0] for c in crates]}")
        all_crates.extend(crates)

    internal_crate_names = {n for n, _ in all_crates}
    internal_fn_defs = collect_internal_fn_defs(fixture_path, subsets)
    print(f"[rust-syn] internal: {len(internal_crate_names)} crates, {len(internal_fn_defs)} fn defs")

    # Phase 2: run oracle per crate.
    t0 = time.time()
    all_raw: list[dict] = []
    for name, crate_dir in all_crates:
        print(f"[rust-syn] running on {name} at {crate_dir.relative_to(fixture_path)} ...")
        raw = run_oracle_on_crate(name, crate_dir)
        all_raw.extend(raw)

    # Phase 3: filter internal.
    kept, ext_imp, ext_calls = filter_internal(all_raw, internal_crate_names, internal_fn_defs)
    print(
        f"[rust-syn] raw edges: {len(all_raw)} | kept internal: {len(kept)} "
        f"| filtered external: imports={ext_imp} calls={ext_calls}"
    )

    # Dedup by match_key.
    seen: set[tuple[str, str, str]] = set()
    deduped: list[Edge] = []
    for e in kept:
        if e.match_key() not in seen:
            seen.add(e.match_key())
            deduped.append(e)

    elapsed = time.time() - t0
    print(f"[rust-syn] total: {len(deduped)} unique edges ({len(kept) - len(deduped)} dups) in {elapsed:.1f}s")

    write_edges(deduped, cache_path)
    sidecar = cache_path.with_suffix(".meta.json")
    sidecar.write_bytes(
        json.dumps(
            {
                "fixture": fixture_id,
                "sha": fixture["sha"],
                "elapsed_seconds": round(elapsed, 1),
                "crates_analyzed": [n for n, _ in all_crates],
                "internal_fn_defs": len(internal_fn_defs),
                "raw_edges": len(all_raw),
                "kept_edges": len(kept),
                "unique_edges": len(deduped),
                "filtered_external_imports": ext_imp,
                "filtered_external_calls": ext_calls,
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
