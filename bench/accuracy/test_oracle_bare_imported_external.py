"""Regression test for the 2026-05-24 Phase-2 external-chain extension.

ExprCall (free function call) doesn't go through `walk_chain_root` — the
chain-root field is None. The previous chain-root drop only fires on
ExprMethodCall. Phase 2 closes that hole using the per-module IMPORTS
map: when a bare-name function call resolves through a `use` from an
external crate, drop the edge.

Concrete pattern that motivated this:
  use futures_util::future::ready;
  fn new_transform(&self, s: S) -> Self::Future {
      ready(Ok(MyService { service: s }))
  }

Pre-fix: `ready` → `BareRepositoryObjectImpl.ready` (phantom).
Post-fix: imports map shows `ready → futures_util` (external); drop.

The PSM assetman fixture had 3 such FNs (TailscaleAuthMiddleware,
FeatureGate, FeatureGateService) all of shape `... → ...ready`.
"""
from __future__ import annotations

import sys
from pathlib import Path
from unittest.mock import MagicMock

sys.path.insert(0, str(Path(__file__).resolve().parent))
from oracle_rust_syn import resolve_and_filter  # noqa: E402


def _import(from_qn: str, to_qn: str):
    return {"type": "IMPORTS", "from_qn": from_qn, "to_qn": to_qn}


def _bare_call(from_qn: str, callee_name: str):
    return {
        "type": "CALLS",
        "from_qn": from_qn,
        "to_qn": callee_name,
        "chain_root_path": None,
        "file": "src/feature_gate.rs",
        "line": 34,
    }


def _make_cargo(external: set[str], workspace: set[str]):
    cargo = MagicMock()
    cargo.external_crates = external
    cargo.workspace_members = workspace
    return cargo


def test_bare_call_to_external_imported_fn_is_dropped():
    """`use futures_util::future::ready; ready(Ok(...))` → drop."""
    raw = [
        _import("proj.src.feature_gate", "futures_util.future.ready"),
        _bare_call("proj.src.feature_gate.FeatureGate.new_transform", "ready"),
    ]
    fn_def_map = {
        "ready": ["proj.src.shared.git.bare_repository_service.BareRepositoryObjectImpl.ready"],
    }
    cargo = _make_cargo(external={"futures_util"}, workspace={"proj"})

    kept, stats = resolve_and_filter(raw, fn_def_map, cargo=cargo)

    assert len(kept) == 0, f"expected 0 kept edges, got {len(kept)}"
    assert stats["calls_bare_imported_external_dropped"] == 1
    assert stats["calls_bare_resolved"] == 0


def test_bare_call_to_internal_imported_fn_is_kept():
    """`use crate::util::helper; helper()` → resolve to internal def."""
    raw = [
        _import("proj.src.main", "crate.util.helper"),
        _bare_call("proj.src.main.run", "helper"),
    ]
    fn_def_map = {"helper": ["proj.src.util.helper"]}
    cargo = _make_cargo(external={"futures_util"}, workspace={"proj"})

    kept, stats = resolve_and_filter(raw, fn_def_map, cargo=cargo)

    assert len(kept) == 1
    assert kept[0].to_qn == "proj.src.util.helper"
    assert stats["calls_bare_imported_external_dropped"] == 0
    assert stats["calls_bare_resolved"] == 1


def test_bare_call_without_caller_module_imports_falls_through():
    """When the caller's module has no IMPORTS edges, no drop fires; the
    bare call goes through the existing fn_def_map ambiguity path.
    """
    raw = [
        _bare_call("proj.src.main.run", "do_thing"),
    ]
    fn_def_map = {"do_thing": ["proj.src.lib.do_thing"]}
    cargo = _make_cargo(external={"futures_util"}, workspace={"proj"})

    kept, stats = resolve_and_filter(raw, fn_def_map, cargo=cargo)

    assert len(kept) == 1
    assert stats["calls_bare_imported_external_dropped"] == 0
    assert stats["calls_bare_resolved"] == 1


def test_bare_call_to_workspace_member_is_kept():
    """`use crate_in_workspace::helper` should NOT be classified as external."""
    raw = [
        _import("proj.src.main", "sibling_crate.util.helper"),
        _bare_call("proj.src.main.run", "helper"),
    ]
    fn_def_map = {"helper": ["sibling_crate.src.util.helper"]}
    cargo = _make_cargo(
        external={"diesel", "futures_util"},
        workspace={"proj", "sibling_crate"},
    )

    kept, stats = resolve_and_filter(raw, fn_def_map, cargo=cargo)

    assert len(kept) == 1
    assert stats["calls_bare_imported_external_dropped"] == 0
