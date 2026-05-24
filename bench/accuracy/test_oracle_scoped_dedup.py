"""Regression test for the 2026-05-24 oracle scoped-form dedup fix.

The Rust binary's `visit_impl_item_fn` pushes one def-QN per `impl Trait<X>
for T { fn method }` block. A type with multiple `From<X>` impls produces
N literally-identical entries in fn_def_map[method]. Pre-fix, the scoped
resolver's `len(internal_matches) > 1` check fired on these duplicates and
dropped the call as `calls_scoped_ambiguous_dropped`.

Audit evidence (2026-05-24):
  - 6 assetman FPs to HostType.from (4 impl From<X> for HostType blocks)
  - 3 assetman FPs to NixOSImage.from (3 impl From<X> for NixOSImage blocks)
  All 9 were oracle-side ambiguous-drops on duplicate def-QNs.

This test pins the dedup behavior so the fix doesn't silently regress.
"""
from __future__ import annotations

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from oracle_rust_syn import resolve_and_filter  # noqa: E402


def _make_raw_call(from_qn: str, to_qn: str):
    """Construct a CALLS edge dict matching what the Rust binary emits."""
    return {
        "from_qn": from_qn,
        "to_qn": to_qn,
        "type": "CALLS",
        "file": "src/lib.rs",
        "line": 1,
    }


def test_scoped_call_dedupes_duplicate_def_qns():
    """4 impl blocks of From for HostType produce 4 identical def-QNs.
    The resolver must dedup and emit a single resolved edge.
    """
    raw_edges = [
        _make_raw_call(
            from_qn="assetman.src.routes.business.orm.OrmContext.get_subassets",
            to_qn="HostType.from",
        ),
    ]
    # Simulate fn_def_map with 4 duplicate entries (one per impl block).
    fn_def_map = {
        "from": [
            "assetman.src.models.subasset.HostType.from",
            "assetman.src.models.subasset.HostType.from",
            "assetman.src.models.subasset.HostType.from",
            "assetman.src.models.subasset.HostType.from",
        ],
    }

    kept, stats = resolve_and_filter(raw_edges, fn_def_map)

    assert len(kept) == 1, (
        f"expected 1 resolved edge (dedup collapses 4 duplicates), "
        f"got {len(kept)}; stats={stats}"
    )
    assert kept[0].to_qn == "assetman.src.models.subasset.HostType.from"
    assert stats["calls_scoped_resolved"] == 1, (
        f"expected calls_scoped_resolved=1, got {stats}"
    )
    assert stats["calls_scoped_ambiguous_dropped"] == 0, (
        f"expected 0 ambiguous-drops (dedup should prevent), got {stats}"
    )


def test_scoped_call_drops_when_two_distinct_classes_share_method():
    """Different class.method pairs ARE genuinely ambiguous — must still drop.

    Pin that the dedup doesn't accidentally collapse two distinct-class
    matches into a single resolved edge.
    """
    raw_edges = [
        _make_raw_call(
            from_qn="caller.fn",
            to_qn="MyType.method",
        ),
    ]
    # Two DIFFERENT classes happen to both be named MyType in different modules
    # (e.g. crate has MyType in two scopes). These are genuinely ambiguous;
    # dedup should NOT collapse them.
    fn_def_map = {
        "method": [
            "crate.modA.MyType.method",
            "crate.modB.MyType.method",
        ],
    }

    kept, stats = resolve_and_filter(raw_edges, fn_def_map)

    assert len(kept) == 0, (
        f"expected drop on genuinely-distinct-class ambiguity, "
        f"got {len(kept)} kept; stats={stats}"
    )
    assert stats["calls_scoped_ambiguous_dropped"] == 1, (
        f"expected calls_scoped_ambiguous_dropped=1, got {stats}"
    )


def test_scoped_call_drops_when_no_internal_match():
    """No matching class → external — must still drop (no behavior change)."""
    raw_edges = [
        _make_raw_call(
            from_qn="caller.fn",
            to_qn="Vec.new",  # std type, no internal match
        ),
    ]
    fn_def_map = {
        "new": ["crate.something_else.MyStruct.new"],  # different class
    }

    kept, stats = resolve_and_filter(raw_edges, fn_def_map)

    assert len(kept) == 0
    assert stats["calls_scoped_external_dropped"] == 1, (
        f"expected calls_scoped_external_dropped=1, got {stats}"
    )


def main() -> int:
    tests = [
        ("test_scoped_call_dedupes_duplicate_def_qns", test_scoped_call_dedupes_duplicate_def_qns),
        ("test_scoped_call_drops_when_two_distinct_classes_share_method", test_scoped_call_drops_when_two_distinct_classes_share_method),
        ("test_scoped_call_drops_when_no_internal_match", test_scoped_call_drops_when_no_internal_match),
    ]
    failed = 0
    for name, fn in tests:
        try:
            fn()
            print(f"  PASS  {name}")
        except AssertionError as e:
            print(f"  FAIL  {name}: {e}")
            failed += 1
        except Exception as e:
            print(f"  ERROR {name}: {type(e).__name__}: {e}")
            failed += 1
    if failed:
        print(f"\n{failed}/{len(tests)} failed")
        return 1
    print(f"\nAll {len(tests)} tests passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
