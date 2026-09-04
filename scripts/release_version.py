#!/usr/bin/env python3
"""Validate the repository's release-version ordering.

Canonical release versions are plain semver tags, ``vMAJOR.MINOR.PATCH``, or
release candidates, ``vMAJOR.MINOR.PATCH-rc.N``. A release candidate is
published as a GitHub prerelease and sorts below the plain release with the
same base version, so ``v0.9.0-rc.1 < v0.9.0-rc.2 < v0.9.0``.

The pre-public internal scheme ``vMAJOR.MINOR.PATCH-redacted.N`` is still
accepted as the *current* (already published) version so the first public
release can be validated against it; every canonical version compares newer
than any legacy tag with the same or lower base version.
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

_RC_VERSION = re.compile(
    r"^v(0|[1-9][0-9]*)\."
    r"(0|[1-9][0-9]*)\."
    r"(0|[1-9][0-9]*)-rc\."
    r"([1-9][0-9]*)$"
)

_LEGACY_RELEASE_VERSION = re.compile(
    r"^v(0|[1-9][0-9]*)\."
    r"(0|[1-9][0-9]*)\."
    r"(0|[1-9][0-9]*)-redacted\."
    r"(0|[1-9][0-9]*)$"
)

# Comparable fields: (major, minor, patch, kind, revision).
# kind orders the schemes that can share a base version:
#   0 legacy internal pre-release (redacted.N)
#   1 release candidate (rc.N)
#   2 plain release
KIND_LEGACY = 0
KIND_RC = 1
KIND_RELEASE = 2
VersionKey = tuple[int, int, int, int, int]


def parse_release_version(value: str) -> VersionKey:
    """Return the comparable fields from one canonical candidate version.

    Canonical means plain semver or an ``-rc.N`` release candidate.
    """
    match = _RELEASE_VERSION.fullmatch(value)
    if match is not None:
        major, minor, patch = (int(field) for field in match.groups())
        return major, minor, patch, KIND_RELEASE, 0
    match = _RC_VERSION.fullmatch(value)
    if match is not None:
        major, minor, patch, revision = (int(field) for field in match.groups())
        return major, minor, patch, KIND_RC, revision
    raise ValueError(
        f"{value!r} is not a canonical vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-rc.N version"
    )


def parse_published_version(value: str) -> VersionKey:
    """Return the comparable fields from a published version (canonical or legacy)."""
    match = _LEGACY_RELEASE_VERSION.fullmatch(value)
    if match is not None:
        major, minor, patch, revision = (int(field) for field in match.groups())
        return major, minor, patch, KIND_LEGACY, revision
    return parse_release_version(value)


def is_prerelease(value: str) -> bool:
    """True when the canonical version is a release candidate."""
    return parse_release_version(value)[3] == KIND_RC


def compare_release_versions(candidate: str, current: str) -> int:
    """Compare a canonical candidate against the currently published version."""
    candidate_fields = parse_release_version(candidate)
    current_fields = parse_published_version(current)
    return (candidate_fields > current_fields) - (candidate_fields < current_fields)


def main(argv: Sequence[str]) -> int:
    if len(argv) == 3 and argv[1] == "--is-prerelease":
        try:
            print("true" if is_prerelease(argv[2]) else "false")
        except ValueError as exc:
            print(f"release version validation failed: {exc}", file=sys.stderr)
            return 2
        return 0
    if len(argv) != 3:
        print(
            f"usage: {argv[0]} <candidate-version> <latest-version>\n"
            f"       {argv[0]} --is-prerelease <version>",
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
