#!/usr/bin/env python3
"""Validate the repository's release-version ordering.

Canonical release versions are plain semver tags: ``vMAJOR.MINOR.PATCH``.
The pre-public internal scheme ``vMAJOR.MINOR.PATCH-redacted.N`` is still
accepted as the *current* (already published) version so the first public
release can be validated against it; every plain release compares newer than
any legacy pre-release with the same or lower base version.
"""

from __future__ import annotations

import re
import sys
from collections.abc import Sequence

_RELEASE_VERSION = re.compile(
    r"^v(0|[1-9][0-9]*)\."
    r"(0|[1-9][0-9]*)\."
    r"(0|[1-9][0-9]*)$"
)

_LEGACY_RELEASE_VERSION = re.compile(
    r"^v(0|[1-9][0-9]*)\."
    r"(0|[1-9][0-9]*)\."
    r"(0|[1-9][0-9]*)-redacted\."
    r"(0|[1-9][0-9]*)$"
)

# Comparable fields: (major, minor, patch, is_release, legacy_revision).
# is_release is 1 for plain semver and 0 for the legacy pre-release scheme, so
# v0.8.0 > v0.8.0-redacted.11 while v0.8.1-redacted.1 > v0.8.0.
VersionKey = tuple[int, int, int, int, int]


def parse_release_version(value: str) -> VersionKey:
    """Return the comparable fields from one canonical (plain semver) release version."""
    match = _RELEASE_VERSION.fullmatch(value)
    if match is None:
        raise ValueError(f"{value!r} is not a canonical vMAJOR.MINOR.PATCH version")
    major, minor, patch = (int(field) for field in match.groups())
    return major, minor, patch, 1, 0


def parse_published_version(value: str) -> VersionKey:
    """Return the comparable fields from a published version (canonical or legacy)."""
    match = _LEGACY_RELEASE_VERSION.fullmatch(value)
    if match is not None:
        major, minor, patch, revision = (int(field) for field in match.groups())
        return major, minor, patch, 0, revision
    return parse_release_version(value)


def compare_release_versions(candidate: str, current: str) -> int:
    """Compare a canonical candidate against the currently published version."""
    candidate_fields = parse_release_version(candidate)
    current_fields = parse_published_version(current)
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
