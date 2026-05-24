"""Regression test for the 2026-05-24 oracle binary staleness check.

The original `ensure_oracle_built()` only checked binary EXISTENCE, not
freshness vs source. Result: PR #346 shipped chain-root awareness in
src/main.rs, but the May-2 binary kept being served (binary exists →
short-circuit) and the chain-root drop was a silent no-op. 27 phantom
assetman FNs (diesel `.get_result` fuzzy-matched to in-graph candidates)
remained in the cache until the binary was manually deleted.

This test pins the staleness logic: if any .rs in src/ has a newer mtime
than the binary, rebuild fires.
"""
from __future__ import annotations

import os
import sys
import tempfile
import time
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
import oracle_rust_syn as o  # noqa: E402


def _touch(path: Path, mtime: float) -> None:
    path.touch()
    os.utime(path, (mtime, mtime))


def test_ensure_oracle_built_rebuilds_when_src_newer(tmp_path: Path):
    """src.rs newer than binary → cargo build is invoked."""
    fake_cargo_dir = tmp_path / "tools" / "oracle-rust-syn"
    fake_src_dir = fake_cargo_dir / "src"
    fake_bin = fake_cargo_dir / "target" / "release" / "oracle-rust-syn.exe"
    fake_src_dir.mkdir(parents=True)
    fake_bin.parent.mkdir(parents=True)

    # Binary is OLDER than the .rs file.
    _touch(fake_bin, mtime=1000.0)
    _touch(fake_src_dir / "main.rs", mtime=2000.0)

    build_invoked = {"count": 0}

    def fake_run(argv, **kwargs):
        build_invoked["count"] += 1
        # Simulate successful build by touching the binary with a newer mtime.
        _touch(fake_bin, mtime=3000.0)

        class R:
            returncode = 0
        return R()

    with patch.object(o, "ACCURACY_DIR", tmp_path), \
         patch.object(o, "ORACLE_BIN", fake_bin), \
         patch("subprocess.run", side_effect=fake_run):
        result = o.ensure_oracle_built()

    assert result == fake_bin
    assert build_invoked["count"] == 1, "expected cargo build to fire when src is newer"


def test_ensure_oracle_built_skips_when_binary_fresh(tmp_path: Path):
    """src.rs older than binary → no cargo build."""
    fake_cargo_dir = tmp_path / "tools" / "oracle-rust-syn"
    fake_src_dir = fake_cargo_dir / "src"
    fake_bin = fake_cargo_dir / "target" / "release" / "oracle-rust-syn.exe"
    fake_src_dir.mkdir(parents=True)
    fake_bin.parent.mkdir(parents=True)

    # Binary is NEWER than the .rs file.
    _touch(fake_src_dir / "main.rs", mtime=1000.0)
    _touch(fake_bin, mtime=2000.0)

    build_invoked = {"count": 0}

    def fake_run(argv, **kwargs):
        build_invoked["count"] += 1

        class R:
            returncode = 0
        return R()

    with patch.object(o, "ACCURACY_DIR", tmp_path), \
         patch.object(o, "ORACLE_BIN", fake_bin), \
         patch("subprocess.run", side_effect=fake_run):
        result = o.ensure_oracle_built()

    assert result == fake_bin
    assert build_invoked["count"] == 0, "expected no rebuild when binary is fresh"


def test_ensure_oracle_built_builds_when_missing(tmp_path: Path):
    """Binary missing entirely → cargo build fires (the original behavior)."""
    fake_cargo_dir = tmp_path / "tools" / "oracle-rust-syn"
    fake_src_dir = fake_cargo_dir / "src"
    fake_bin = fake_cargo_dir / "target" / "release" / "oracle-rust-syn.exe"
    fake_src_dir.mkdir(parents=True)
    _touch(fake_src_dir / "main.rs", mtime=1000.0)
    # NO binary on disk.

    build_invoked = {"count": 0}

    def fake_run(argv, **kwargs):
        build_invoked["count"] += 1
        fake_bin.parent.mkdir(parents=True, exist_ok=True)
        _touch(fake_bin, mtime=2000.0)

        class R:
            returncode = 0
        return R()

    with patch.object(o, "ACCURACY_DIR", tmp_path), \
         patch.object(o, "ORACLE_BIN", fake_bin), \
         patch("subprocess.run", side_effect=fake_run):
        result = o.ensure_oracle_built()

    assert result == fake_bin
    assert build_invoked["count"] == 1
