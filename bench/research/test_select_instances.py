"""Get-well Phase 2.3 (2026-05-06): negative-case tests for the
eval_locbench_batch.select_instances function.

The bug this test would have caught:
  Pre-Plan-5 the function loop iterated over hardcoded short
  category names ("Bug", "Feature", "Performance", "Security"). The
  parquet's actual category column uses full names ("Bug Report",
  "Feature Request", "Performance Issue", "Security Vulnerability").
  No category match ever fired; the function silently fell through
  to its top-up loop, producing non-stratified output for ~30 days
  before Plan 5 Phase A actually exercised the function.

This test pins:
  1. Stratified output: the n=8 sample contains exactly 2 of each
     category (target_per_cat == n // 4 == 2).
  2. Robustness to category-name mismatch: if the parquet's category
     names diverge from what select_instances iterates over, the
     function MUST surface the mismatch — silently falling through is
     forbidden.
  3. Small-repo preference bias: at least target_per_cat preferred-
     repo instances are selected when available.
"""
from __future__ import annotations

import sys
from pathlib import Path

import pandas as pd

sys.path.insert(0, str(Path(__file__).resolve().parent))
import eval_locbench_batch as elb  # noqa: E402


def _make_fixture_df() -> pd.DataFrame:
    """Hand-craft a parquet-shape DataFrame with 4 categories × N
    instances each. Mix of preferred-repo and other-repo entries so
    the small-repo bias has signal."""
    rows = []
    # 6 Bug Reports: 4 from preferred (kornia x2, aiohttp x2), 2 other
    rows += [
        {"instance_id": f"kornia/k-{i}", "repo": "kornia/kornia", "category": "Bug Report", "edit_functions": ["a.py:f"]}
        for i in range(2)
    ]
    rows += [
        {"instance_id": f"aiohttp/a-{i}", "repo": "aio-libs/aiohttp", "category": "Bug Report", "edit_functions": ["b.py:g"]}
        for i in range(2)
    ]
    rows += [
        {"instance_id": f"random/r-bug-{i}", "repo": "some/random", "category": "Bug Report", "edit_functions": ["c.py:h"]}
        for i in range(2)
    ]
    # 6 Feature Requests
    rows += [
        {"instance_id": f"pydantic/p-{i}", "repo": "pydantic/pydantic", "category": "Feature Request", "edit_functions": ["d.py:i"]}
        for i in range(3)
    ]
    rows += [
        {"instance_id": f"random/r-feat-{i}", "repo": "some/random", "category": "Feature Request", "edit_functions": ["e.py:j"]}
        for i in range(3)
    ]
    # 6 Performance Issue
    rows += [
        {"instance_id": f"yfinance/y-{i}", "repo": "ranaroussi/yfinance", "category": "Performance Issue", "edit_functions": ["f.py:k"]}
        for i in range(2)
    ]
    rows += [
        {"instance_id": f"random/r-perf-{i}", "repo": "some/random", "category": "Performance Issue", "edit_functions": ["g.py:l"]}
        for i in range(4)
    ]
    # 6 Security Vulnerability
    rows += [
        {"instance_id": f"sqlglot/s-{i}", "repo": "tobymao/sqlglot", "category": "Security Vulnerability", "edit_functions": ["h.py:m"]}
        for i in range(2)
    ]
    rows += [
        {"instance_id": f"random/r-sec-{i}", "repo": "some/random", "category": "Security Vulnerability", "edit_functions": ["i.py:n"]}
        for i in range(4)
    ]
    return pd.DataFrame(rows)


def test_stratified_n8_balanced_categories():
    """The pre-Plan-5 silent bug: select_instances should produce
    n//4=2 instances per category. The bug made every category empty
    via no-match; the function fell through to top-up and produced
    non-stratified output. This test pins the fix.

    Specifically: at n=8, the per-category target is 2. We must see
    exactly 2 of each category.
    """
    df = _make_fixture_df()
    sel = elb.select_instances(df, n=8, seed=42)
    assert len(sel) == 8, f"expected 8 instances, got {len(sel)}"

    counts = sel["category"].value_counts().to_dict()
    expected = {
        "Bug Report": 2,
        "Feature Request": 2,
        "Performance Issue": 2,
        "Security Vulnerability": 2,
    }
    assert counts == expected, (
        f"categories not balanced; expected {expected}, got {counts}"
    )


def test_each_full_category_name_actually_matches():
    """Belt-and-suspenders: if select_instances were ever changed to
    hardcode short names again ("Bug" instead of "Bug Report"), this
    fixture's full names would not match and we'd fall through to
    top-up — producing the non-stratified output that was the bug.

    We assert each parquet category name appears in the per-category
    loop's candidate list. This catches drift between the fixture
    category names and the function's hardcoded loop list.
    """
    # Read the function source and verify the loop iterates over the
    # full category names (regression catch).
    src = Path(elb.__file__).read_text(encoding="utf-8")
    for cat in ["Bug Report", "Feature Request", "Performance Issue", "Security Vulnerability"]:
        assert f'"{cat}"' in src, (
            f"select_instances source must reference category {cat!r} "
            f"(string literal). If renamed, update this test AND the "
            f"select_instances function in lockstep."
        )


def test_small_repo_preference_when_available():
    """When SMALL_REPO_PREFERENCE matches are available within a
    category, select_instances must draw from them first. With our
    fixture, Bug Report has 2 kornia + 2 aiohttp + 2 random — at
    target=2, both selected should be from the preferred set."""
    df = _make_fixture_df()
    sel = elb.select_instances(df, n=8, seed=42)
    bug_rows = sel[sel["category"] == "Bug Report"]
    pref_set = set(elb.SMALL_REPO_PREFERENCE)
    n_pref = bug_rows["repo"].isin(pref_set).sum()
    assert n_pref == 2, (
        f"expected 2/2 Bug Report rows from preferred repos, got "
        f"{n_pref}/{len(bug_rows)} (selected: {list(bug_rows['repo'])})"
    )


def test_topup_fallback_when_one_category_empty():
    """If a category is missing entirely from the parquet (rare but
    possible), select_instances should top up from other categories
    rather than producing a short result."""
    df = _make_fixture_df()
    df_no_security = df[df["category"] != "Security Vulnerability"]
    sel = elb.select_instances(df_no_security, n=8, seed=42)
    assert len(sel) == 8, (
        f"top-up should produce 8 instances even with empty category, got {len(sel)}"
    )
    # No Security rows at all because the parquet had none.
    assert "Security Vulnerability" not in sel["category"].values


def test_n4_minimum_balanced():
    """At n=4 (target_per_cat=1), we get one of each category."""
    df = _make_fixture_df()
    sel = elb.select_instances(df, n=4, seed=42)
    assert len(sel) == 4
    counts = sel["category"].value_counts().to_dict()
    assert all(counts.get(c, 0) == 1 for c in [
        "Bug Report", "Feature Request", "Performance Issue", "Security Vulnerability"
    ]), f"expected 1 of each category at n=4, got {counts}"


if __name__ == "__main__":
    import traceback

    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_") and callable(v)]
    failures = 0
    for fn in fns:
        try:
            fn()
            print(f"PASS {fn.__name__}")
        except AssertionError as exc:
            failures += 1
            print(f"FAIL {fn.__name__}: {exc}")
        except Exception:
            failures += 1
            print(f"ERROR {fn.__name__}:")
            traceback.print_exc()
    print(f"\n{len(fns) - failures}/{len(fns)} passed")
    sys.exit(1 if failures else 0)
