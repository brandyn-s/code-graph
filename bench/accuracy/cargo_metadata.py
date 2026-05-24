"""External-vs-workspace crate classification from `cargo metadata`.

Mirrors code-graph's `internal/pipeline/cargo_metadata.go` so the syn-oracle
applies the same external-drop logic code-graph's resolver does (Tier-2 v0.1,
PR #343). Re-implemented in Python rather than imported as a library to keep
the oracle's measurement substrate independent of the system under test —
both sides read cargo metadata directly and reach the same external-set by
construction.

Added 2026-05-24 (syn-oracle external-chain fix). See
`~/Documents/knowledge-base/plans/2026-05-24-syn-oracle-external-chain-fix.md`.
"""
from __future__ import annotations

import json
import subprocess
from dataclasses import dataclass, field
from pathlib import Path


@dataclass
class CargoMetadataResult:
    """External-vs-workspace classification of crates referenced by a Cargo
    workspace.

    Both sets use Rust-identifier-normalized names (`-` → `_`) so callers can
    match against the first segment of a `use` path or chain root without
    further transformation.
    """
    external_crates: set[str] = field(default_factory=set)
    workspace_members: set[str] = field(default_factory=set)


def normalize_cargo_crate_name(name: str) -> str:
    """Replace `-` with `_`. Cargo package names allow `-`; Rust identifiers
    don't, so source references the crate as `foo_bar` even when the package
    is `foo-bar`. Matches `normalizeCargoCrateName` in cargo_metadata.go.
    """
    if "-" not in name:
        return name
    return name.replace("-", "_")


def parse_cargo_metadata(raw: bytes | str) -> CargoMetadataResult:
    """Parse the JSON output of `cargo metadata --format-version 1 --no-deps`.

    Algorithm matches `parseCargoMetadata` in cargo_metadata.go:
      1. First pass: collect workspace member names from package list.
      2. Second pass: classify each dependency with a non-empty `source` as
         external; if the dep ALSO appears as a workspace member (path-dep
         to a sibling within the workspace), the workspace classification
         wins.

    Raises ValueError on malformed JSON (analog of Go's wrapped error).
    """
    if isinstance(raw, bytes):
        text = raw.decode("utf-8", errors="replace")
    else:
        text = raw
    try:
        doc = json.loads(text)
    except json.JSONDecodeError as e:
        raise ValueError(f"parse cargo metadata: {e}") from e

    result = CargoMetadataResult()

    packages = doc.get("packages", []) or []

    # First pass: register all workspace members so external classification
    # can override on collision.
    for pkg in packages:
        name = pkg.get("name", "")
        if name:
            result.workspace_members.add(normalize_cargo_crate_name(name))

    # Second pass: classify dependencies.
    for pkg in packages:
        for dep in pkg.get("dependencies", []) or []:
            source = dep.get("source") or ""
            if not source:
                # Path/workspace dep — not external.
                continue
            dep_name = normalize_cargo_crate_name(dep.get("name", ""))
            if not dep_name:
                continue
            if dep_name in result.workspace_members:
                # Workspace member overrides.
                continue
            result.external_crates.add(dep_name)

    return result


def run_cargo_metadata(root_dir: Path, timeout: int = 30) -> CargoMetadataResult:
    """Shell out to `cargo metadata --format-version 1 --no-deps` at root_dir.

    Returns an empty result (no external, no workspace) on any failure:
    missing Cargo.toml, missing cargo binary, timeout, malformed JSON.
    Callers should treat empty sets as graceful degradation — the oracle's
    external-drop simply doesn't fire for crates we can't classify, matching
    pre-fix behavior.

    Matches `runCargoMetadata` in cargo_metadata.go (same 30-second timeout).
    """
    cargo_toml = root_dir / "Cargo.toml"
    if not cargo_toml.exists():
        return CargoMetadataResult()

    try:
        result = subprocess.run(
            ["cargo", "metadata", "--format-version", "1", "--no-deps"],
            cwd=str(root_dir),
            capture_output=True,
            timeout=timeout,
        )
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return CargoMetadataResult()

    if result.returncode != 0:
        return CargoMetadataResult()

    try:
        return parse_cargo_metadata(result.stdout)
    except ValueError:
        return CargoMetadataResult()
