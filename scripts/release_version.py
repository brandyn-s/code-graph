#!/usr/bin/env python3
"""Validate the repository's canonical redacted release-version ordering."""

from __future__ import annotations

import re
import sys
from collections.abc import Sequence

_RELEASE_VERSION = re.compile(
    r"^v(0|[1-9][0-9]*)\."
    r"(0|[1-9][0-9]*)\."
    r"(0|[1-9][0-9]*)-redacted\."
    r"(0|[1-9][0-9]*)$"
)


def parse_release_version(value: str) -> tuple[int, int, int, int]:
    """Return the comparable fields from one canonical release version."""
    match = _RELEASE_VERSION.fullmatch(value)
    if match is None:
        raise ValueError(
            f"{value!r} is not a canonical vMAJOR.MINOR.PATCH-redacted.REVISION version"
        )
    major, minor, patch, revision = (int(field) for field in match.groups())
    return major, minor, patch, revision


def compare_release_versions(candidate: str, current: str) -> int:
    """Compare two canonical release versions."""
    candidate_fields = parse_release_version(candidate)
    current_fields = parse_release_version(current)
    return (candidate_fields > current_fields) - (candidate_fields < current_fields)


def main(argv: Sequence[str]) -> int:
    if len(argv) != 3:
        print(
            f"usage: {argv[0]} <candidate-version> <latest-version>",
            file=sys.stderr,
        )
        return 2

    candidate, latest = argv[1:]
    try:
        ordering = compare_release_versions(candidate, latest)
    except ValueError as exc:
        print(f"release version validation failed: {exc}", file=sys.stderr)
        return 2

    if ordering <= 0:
        print(
            f"release version validation failed: {candidate} must be newer than {latest}",
            file=sys.stderr,
        )
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
