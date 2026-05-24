"""Test parity between Python cargo_metadata.py and the Go reference impl.

Reuses code-graph's existing test fixtures at
`internal/pipeline/testdata/cargo-metadata-{simple,workspace}.json` so the
two implementations are pinned against identical input. If either side
diverges, this test fails.

Run with: python3 -m pytest test_cargo_metadata.py -v
Or:       python3 test_cargo_metadata.py
"""
from __future__ import annotations

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from cargo_metadata import (  # noqa: E402
    normalize_cargo_crate_name,
    parse_cargo_metadata,
)

REPO_ROOT = Path(__file__).resolve().parents[2]
FIXTURES = REPO_ROOT / "internal" / "pipeline" / "testdata"


def test_parse_simple():
    """Single-crate fixture: `my_app` with crates.io deps.

    External set must be {serde, tokio, anyhow, serde_json} (note
    serde-json normalizes to serde_json). Workspace = {my_app}.
    """
    raw = (FIXTURES / "cargo-metadata-simple.json").read_bytes()
    res = parse_cargo_metadata(raw)

    expected_external = {"serde", "tokio", "anyhow", "serde_json"}
    assert res.external_crates == expected_external, (
        f"external_crates: got {res.external_crates}, want {expected_external}"
    )
    assert "my_app" in res.workspace_members
    assert "my_app" not in res.external_crates


def test_parse_workspace():
    """Workspace dedup fixture: 3 path-dep siblings + tokio external.

    External = {tokio}; workspace = {service_a, service_b, service_c}
    (note `-` → `_` normalization). service-b must NOT appear as
    external even though it's a dependency, because it's a path dep
    (source: null) AND a workspace member.
    """
    raw = (FIXTURES / "cargo-metadata-workspace.json").read_bytes()
    res = parse_cargo_metadata(raw)

    assert res.workspace_members == {"service_a", "service_b", "service_c"}, (
        f"workspace_members: got {res.workspace_members}"
    )
    assert res.external_crates == {"tokio"}, (
        f"external_crates: got {res.external_crates}"
    )


def test_parse_malformed():
    """Malformed JSON must raise ValueError (graceful failure mode)."""
    try:
        parse_cargo_metadata(b"not json {")
    except ValueError:
        return
    raise AssertionError("expected ValueError on malformed JSON")


def test_normalize():
    """Pin the `-` → `_` convention. Mirrors TestNormalizeCargoCrateName in Go."""
    cases = {
        "serde": "serde",
        "serde_json": "serde_json",
        "serde-json": "serde_json",
        "futures-util": "futures_util",
        "tracing": "tracing",
        "a-b-c": "a_b_c",
    }
    for inp, want in cases.items():
        got = normalize_cargo_crate_name(inp)
        assert got == want, f"normalize({inp!r}) = {got!r}, want {want!r}"


def main() -> int:
    """Run tests directly without pytest dependency."""
    tests = [
        ("test_parse_simple", test_parse_simple),
        ("test_parse_workspace", test_parse_workspace),
        ("test_parse_malformed", test_parse_malformed),
        ("test_normalize", test_normalize),
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
