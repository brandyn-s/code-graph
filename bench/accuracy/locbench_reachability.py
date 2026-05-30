#!/usr/bin/env python3
"""Loc-Bench reachability partitioner + stable-subset pinner.

WHY (2026-05-12 unreachable-tail finding): 67/200 Loc-Bench instances became
permanently unreachable on GitHub (PR base_commit SHAs garbage-collected), and
the missing tail is category-skewed (42% Security Vulnerability). That makes any
re-baseline against "whatever is reachable today" NOT apples-to-apples with the
2026-05-04 baseline, which is why the 2026-05-12 re-run was REFUSED for
publication (CLAUDE.md). The headline 86.0/84.5/73.5 numbers are stuck as a
*historical* measurement until we re-baseline on a defensible corpus.

This tool fixes that by producing a **stable pinned subset**:

  1. partition every instance into github-reachable / SWH-recoverable / unreachable
     (Software Heritage archives many force-pushed/GC'd commits — PR #299),
  2. PIN the recoverable subset to a versioned manifest so every future
     re-baseline runs the SAME instances (apples-to-apples over time),
  3. quantify the category skew of the unreachable tail vs the full population,
     so the pinned subset's representativeness is stated, not assumed.

The resolver is pluggable; the partition + skew math is pure and self-tested.

Usage:
    python bench/accuracy/locbench_reachability.py --instances locbench_n200.json \\
        --pin bench/accuracy/locbench_reachable_pin.json
    python bench/accuracy/locbench_reachability.py --selftest
"""
from __future__ import annotations

import argparse
import json
import os
import urllib.error
import urllib.request
from collections import Counter
from dataclasses import dataclass, field
from typing import Callable, Dict, List

GITHUB = "github"
SWH = "swh"
UNREACHABLE = "unreachable"


class RateLimitError(RuntimeError):
    """Raised when the GitHub API rate-limits us. Surfaced (not swallowed) so a
    run aborts loudly rather than misclassifying reachable commits as
    unreachable — set GITHUB_TOKEN (CI provides it automatically) to fix."""


@dataclass
class Partition:
    by_source: Dict[str, List[dict]] = field(default_factory=lambda: {
        GITHUB: [], SWH: [], UNREACHABLE: []})

    @property
    def recoverable(self) -> List[dict]:
        return self.by_source[GITHUB] + self.by_source[SWH]


def github_reachable(repo: str, sha: str, token: str = "", timeout: int = 20) -> bool:
    """True if `sha` still exists on GitHub, via the commits API
    (GET /repos/{repo}/commits/{sha}: 200 present, 404/422 GC'd).

    NOTE: an earlier version used `git ls-remote --exit-code <repo> <sha>`, but
    that only matches REF TIPS. A base_commit is an ancestor, not a tip, so
    ls-remote false-negatives EVERY instance (empirically verified 2026-05-30:
    5/5 sampled commits reported 'unreachable' by ls-remote while the commits
    API confirmed 4/5 present). The commits API is the correct check. It is
    rate-limited (60/hr unauthenticated, ~5000/hr with a token), so pass
    GITHUB_TOKEN (CI provides it automatically); a 403/429 raises
    RateLimitError rather than being misread as 'unreachable'."""
    url = f"https://api.github.com/repos/{repo}/commits/{sha}"
    headers = {"Accept": "application/vnd.github+json", "User-Agent": "locbench-reachability"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    try:
        with urllib.request.urlopen(urllib.request.Request(url, headers=headers), timeout=timeout) as r:
            return r.status == 200
    except urllib.error.HTTPError as e:
        if e.code in (403, 429):
            raise RateLimitError(f"GitHub API {e.code} on {repo}@{sha[:10]} — set GITHUB_TOKEN")
        if e.code in (404, 422):
            return False  # commit GC'd / not found
        return False
    except Exception:
        return False


def swh_recoverable(sha: str, timeout: int = 20) -> bool:
    """True if the commit is archived in Software Heritage (PR #299 fallback)."""
    api = f"https://archive.softwareheritage.org/api/1/revision/{sha}/"
    try:
        req = urllib.request.Request(api, method="GET")
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return resp.status == 200
    except Exception:
        return False


def default_resolver(inst: dict) -> str:
    """Network resolver: GitHub commits API first (token from GITHUB_TOKEN),
    then Software Heritage, else unreachable. Propagates RateLimitError."""
    repo, sha = inst.get("repo", ""), inst.get("base_commit", "")
    if not sha:
        return UNREACHABLE
    if github_reachable(repo, sha, token=os.environ.get("GITHUB_TOKEN", "")):
        return GITHUB
    if swh_recoverable(sha):
        return SWH
    return UNREACHABLE


def partition(instances: List[dict], resolver: Callable[[dict], str]) -> Partition:
    p = Partition()
    for inst in instances:
        p.by_source[resolver(inst)].append(inst)
    return p


def category_skew(full: List[dict], subset: List[dict]) -> Dict[str, dict]:
    """Per-category share in the full population vs the (recoverable) subset.

    A large share delta means the subset is NOT representative — a re-baseline
    on it cannot be compared to a baseline taken over the full population
    without acknowledging the skew.
    """
    def dist(rows: List[dict]) -> Dict[str, float]:
        c = Counter(r.get("category", "?") for r in rows)
        n = sum(c.values()) or 1
        return {k: c[k] / n for k in c}

    fd, sd = dist(full), dist(subset)
    cats = sorted(set(fd) | set(sd))
    out = {}
    for cat in cats:
        out[cat] = {
            "full_share": round(fd.get(cat, 0.0), 4),
            "subset_share": round(sd.get(cat, 0.0), 4),
            "delta": round(sd.get(cat, 0.0) - fd.get(cat, 0.0), 4),
        }
    return out


def max_abs_skew(skew: Dict[str, dict]) -> float:
    return max((abs(v["delta"]) for v in skew.values()), default=0.0)


def build_pin(instances: List[dict], resolver: Callable[[dict], str]) -> dict:
    p = partition(instances, resolver)
    skew = category_skew(instances, p.recoverable)
    return {
        "n_total": len(instances),
        "n_recoverable": len(p.recoverable),
        "n_github": len(p.by_source[GITHUB]),
        "n_swh": len(p.by_source[SWH]),
        "n_unreachable": len(p.by_source[UNREACHABLE]),
        "max_abs_category_skew": round(max_abs_skew(skew), 4),
        "category_skew": skew,
        "pinned_instance_ids": sorted(i.get("instance_id", "") for i in p.recoverable),
        "unreachable_instance_ids": sorted(i.get("instance_id", "") for i in p.by_source[UNREACHABLE]),
    }


def _selftest() -> None:
    insts = [
        {"instance_id": "a", "repo": "o/r", "base_commit": "1", "category": "Bug Report"},
        {"instance_id": "b", "repo": "o/r", "base_commit": "2", "category": "Bug Report"},
        {"instance_id": "c", "repo": "o/r", "base_commit": "3", "category": "Security Vulnerability"},
        {"instance_id": "d", "repo": "o/r", "base_commit": "4", "category": "Security Vulnerability"},
        {"instance_id": "e", "repo": "o/r", "base_commit": "", "category": "Feature Request"},
    ]
    # Stub resolver: a→github, b→swh, c,d→unreachable, e→unreachable(no sha).
    stub = {"a": GITHUB, "b": SWH, "c": UNREACHABLE, "d": UNREACHABLE, "e": UNREACHABLE}
    pin = build_pin(insts, lambda i: stub[i["instance_id"]])

    assert pin["n_total"] == 5
    assert pin["n_recoverable"] == 2 and pin["n_github"] == 1 and pin["n_swh"] == 1
    assert pin["n_unreachable"] == 3
    assert pin["pinned_instance_ids"] == ["a", "b"]
    # Full is 40% Security; recoverable subset (a,b) is 0% Security → skew -0.4.
    assert pin["category_skew"]["Security Vulnerability"]["full_share"] == 0.4
    assert pin["category_skew"]["Security Vulnerability"]["subset_share"] == 0.0
    assert abs(pin["category_skew"]["Security Vulnerability"]["delta"] + 0.4) < 1e-9
    assert abs(pin["max_abs_category_skew"] - 0.6) < 1e-9  # Bug Report 40%→100%
    print("locbench_reachability.py self-tests passed")


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--instances", help="Loc-Bench instances JSON (list of dicts)")
    ap.add_argument("--pin", help="output path for the pinned-subset manifest")
    ap.add_argument("--selftest", action="store_true")
    args = ap.parse_args()

    if args.selftest:
        _selftest()
        return 0
    if not args.instances:
        ap.error("--instances is required (or use --selftest)")

    with open(args.instances, "r", encoding="utf-8") as f:
        instances = json.load(f)

    pin = build_pin(instances, default_resolver)
    print(f"total={pin['n_total']} recoverable={pin['n_recoverable']} "
          f"(github={pin['n_github']} swh={pin['n_swh']}) "
          f"unreachable={pin['n_unreachable']}")
    print(f"max abs category skew (recoverable vs full): {pin['max_abs_category_skew']}")
    for cat, v in pin["category_skew"].items():
        print(f"  {cat:24s} full={v['full_share']:.3f} subset={v['subset_share']:.3f} Δ={v['delta']:+.3f}")
    if pin["max_abs_category_skew"] > 0.10:
        print("\n  WARNING: subset is category-skewed >10pp from the full population.")
        print("  A re-baseline on it is NOT apples-to-apples with a full-population baseline.")

    if args.pin:
        with open(args.pin, "w", encoding="utf-8") as f:
            json.dump(pin, f, indent=2)
        print(f"\npinned subset written: {args.pin}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
