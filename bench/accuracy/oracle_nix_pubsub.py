"""Nix pub/sub oracle for psm style modules.

Independent implementation of the same extraction surface as
`internal/pipeline/nix_services.go`. Used to compute F1 against
code-graph's output.

Why independent: shared-code oracle measures consistency, not correctness.
This oracle uses Python's `re` engine (vs Go's RE2) and a slightly
different parse order, which surfaces genuine extractor bugs. It does NOT
use tree-sitter-nix — that would be a second independent oracle
(deferred; adds a dependency and nix grammar parity concerns).

Scope: same as v1 Go extractor
    - options.services.<name>                → Service
    - baf.pub_topic mkOption default literal         → Service PUBLISHES_TO Topic
    - baf.sub_topics mkOption default list           → Service SUBSCRIBES_TO Topic
        (base literal topics only; conditional appends captured as a
         separate `conditional_subs` set for AMBIGUOUS-tier comparison)
    - services.X.additional_sub_topics       → X SUBSCRIBES_TO Topic
    - `/bin/pubmsg TOPIC` / `/bin/submsg TOPIC` ...  → imperative (tier: INFERRED)
"""

from __future__ import annotations

import json
import re
import sys
from dataclasses import dataclass, field
from pathlib import Path

# --- regex patterns (Python dialect; largely parallel to Go) ------------

_RE_SERVICE_DECL = re.compile(
    r"(?m)^\s*options\.services\.([a-zA-Z_][a-zA-Z0-9_-]*)\s*=",
)

_RE_PUB_TOPIC = re.compile(
    r"baf\.pub_topic(?:_[a-zA-Z_][a-zA-Z0-9_]*)?\s*=\s*mkOption\s*\{[^}]*?default\s*=\s*\"([^\"]+)\"",
    re.DOTALL,
)

_RE_SUB_TOPIC_SINGULAR = re.compile(
    r"baf\.([a-zA-Z_][a-zA-Z0-9_]*)_sub_topic\s*=\s*mkOption\s*\{[^}]*?default\s*=\s*\"([^\"]+)\"",
    re.DOTALL,
)

_RE_SUB_TOPICS_BASE = re.compile(
    r"baf\.sub_topics\s*=\s*mkOption\s*\{[^}]*?default\s*=\s*\[([^\]]*)\]",
    re.DOTALL,
)

_RE_SUB_TOPICS_FULL = re.compile(
    r"baf\.sub_topics\s*=\s*mkOption\s*\{[^}]*?default\s*=\s*([^;]+);",
    re.DOTALL,
)

_RE_ADDITIONAL_SUBS = re.compile(
    r"services\.([a-zA-Z_][a-zA-Z0-9_-]*)\.additional_sub_topics\s*=\s*\[([^\]]*)\]",
    re.DOTALL,
)

_RE_STR_LIT = re.compile(r"\"([^\"]+)\"")

_RE_PUBMSG = re.compile(r"/bin/pubmsg[ \t]+([a-zA-Z0-9_][a-zA-Z0-9_-]*)\b")

_RE_SUBMSG = re.compile(r"/bin/submsg[ \t]+([^\n|\\]+)")

_RE_INTERPOLATION = re.compile(r"\$\{[^}]*\}")


@dataclass
class NixServiceOracle:
    """Ground-truth view of one Nix module file."""

    service_name: str = ""
    pub_topic: str = ""
    pub_topic_variants: set[str] = field(default_factory=set)
    sub_topics: set[str] = field(default_factory=set)
    conditional_subs: set[str] = field(default_factory=set)
    imp_pub_topics: set[str] = field(default_factory=set)
    imp_sub_topics: set[str] = field(default_factory=set)
    additional_subs: dict[str, set[str]] = field(default_factory=dict)
    declared_in: str = ""


_NIX_KEYWORDS = {
    "if", "then", "else", "let", "in", "with", "true", "false", "null",
    "builtins", "toString", "concatStringsSep", "pkgs",
    "cfg", "baf", "bin",
}


def _extract_submsg_topics(chunk: str) -> set[str]:
    stripped = _RE_INTERPOLATION.sub("", chunk)
    quoted = _RE_STR_LIT.findall(stripped)
    if quoted:
        return set(quoted)
    bare = re.findall(r"\b([a-zA-Z_][a-zA-Z0-9_-]*)\b", stripped)
    return {tok for tok in bare if tok not in _NIX_KEYWORDS}


def parse_nix_file(source: str) -> NixServiceOracle:
    """Apply the oracle regexes to one Nix module source and return the
    extracted ground truth."""
    info = NixServiceOracle()

    m = _RE_SERVICE_DECL.search(source)
    if m:
        info.service_name = m.group(1)

    pub_matches = list(_RE_PUB_TOPIC.finditer(source))
    if pub_matches:
        info.pub_topic = pub_matches[0].group(1)
        for m in pub_matches[1:]:
            info.pub_topic_variants.add(m.group(1))

    m = _RE_SUB_TOPICS_BASE.search(source)
    if m:
        info.sub_topics = set(_RE_STR_LIT.findall(m.group(1)))

    # Singular `baf.<name>_sub_topic = "X"` — adsbd's pattern.
    for m in _RE_SUB_TOPIC_SINGULAR.finditer(source):
        info.sub_topics.add(m.group(2))

    m = _RE_SUB_TOPICS_FULL.search(source)
    if m:
        all_topics = set(_RE_STR_LIT.findall(m.group(1)))
        info.conditional_subs = all_topics - info.sub_topics

    for m in _RE_ADDITIONAL_SUBS.finditer(source):
        svc = m.group(1)
        topics = set(_RE_STR_LIT.findall(m.group(2)))
        info.additional_subs.setdefault(svc, set()).update(topics)

    for m in _RE_PUBMSG.finditer(source):
        info.imp_pub_topics.add(m.group(1))
    for m in _RE_SUBMSG.finditer(source):
        info.imp_sub_topics.update(_extract_submsg_topics(m.group(1)))

    return info


def scan_repo(repo_root: Path) -> list[NixServiceOracle]:
    """Walk all .nix files under repo_root; return one oracle per file
    that declares a service or has additional_sub_topics."""
    out: list[NixServiceOracle] = []
    for path in repo_root.rglob("*.nix"):
        try:
            source = path.read_text(encoding="utf-8", errors="replace")
        except (OSError, UnicodeError):
            continue
        info = parse_nix_file(source)
        if not info.service_name and not info.additional_subs:
            continue
        info.declared_in = str(path.relative_to(repo_root)).replace("\\", "/")
        out.append(info)
    return out


def _to_dict(info: NixServiceOracle) -> dict:
    return {
        "service_name": info.service_name,
        "pub_topic": info.pub_topic,
        "sub_topics": sorted(info.sub_topics),
        "conditional_subs": sorted(info.conditional_subs),
        "imp_pub_topics": sorted(info.imp_pub_topics),
        "imp_sub_topics": sorted(info.imp_sub_topics),
        "additional_subs": {k: sorted(v) for k, v in info.additional_subs.items()},
        "declared_in": info.declared_in,
    }


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print("usage: oracle_nix_pubsub.py <repo-root>", file=sys.stderr)
        return 2
    root = Path(argv[1]).resolve()
    if not root.is_dir():
        print(f"not a directory: {root}", file=sys.stderr)
        return 2
    oracles = scan_repo(root)
    json.dump([_to_dict(o) for o in oracles], sys.stdout, indent=2)
    print()
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
