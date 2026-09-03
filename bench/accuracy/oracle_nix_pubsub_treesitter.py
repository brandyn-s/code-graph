"""Tree-sitter-based Nix pub/sub oracle.

Independent re-implementation using tree-sitter-nix's AST instead of
regex. Useful as a third opinion against the hand-oracle and
regex-based oracle_nix_pubsub.py — different parser dialects mean
different blind spots.

Output format matches oracle_nix_pubsub.py's NixServiceOracle for
direct Jaccard comparison via compare scripts.

Install:
    pip install tree-sitter tree-sitter-nix
"""

from __future__ import annotations

import json
import sys
from dataclasses import dataclass, field
from pathlib import Path

import tree_sitter as ts
import tree_sitter_nix as tsn

_LANG = ts.Language(tsn.language())
_PARSER = ts.Parser(_LANG)


@dataclass
class NixOracleTS:
    """Same shape as oracle_nix_pubsub.NixServiceOracle (for compare scripts)."""

    service_name: str = ""
    pub_topic: str = ""
    pub_topic_variants: list[str] = field(default_factory=list)
    sub_topics: list[str] = field(default_factory=list)
    additional_subs: dict[str, list[str]] = field(default_factory=dict)
    runs_binaries: list[str] = field(default_factory=list)
    declared_in: str = ""


def _node_text(node: ts.Node) -> str:
    return node.text.decode("utf-8") if node.text else ""


def _attrpath_segments(attrpath: ts.Node) -> list[str]:
    """Return the dotted segments of an attrpath node, e.g.
    ['options', 'services', 'demo'] for the binding
    `options.services.demo = ...`."""
    segs = []
    for child in attrpath.children:
        if child.type == "identifier":
            segs.append(_node_text(child))
    return segs


def _string_value(node: ts.Node) -> str | None:
    """Extract a literal string from a `string_expression` node, or None."""
    if node.type != "string_expression":
        return None
    raw = _node_text(node)
    if raw.startswith('"') and raw.endswith('"'):
        return raw[1:-1]
    return None


def _list_string_values(node: ts.Node) -> list[str]:
    """Extract literal strings from a `list_expression` node."""
    out = []
    for child in node.named_children:
        if child.type == "string_expression":
            v = _string_value(child)
            if v is not None:
                out.append(v)
    return out


def _find_default_in_mkoption(value: ts.Node) -> ts.Node | None:
    """Given an `apply_expression` like `mkOption { default = X; ... }`,
    return the `default`'s value node, or None."""
    if value.type != "apply_expression":
        return None
    # apply_expression has 2 children: function (variable_expression "mkOption")
    # and argument (attrset_expression).
    arg = None
    for c in value.children:
        if c.type == "attrset_expression":
            arg = c
            break
    if arg is None:
        return None
    # Walk the binding_set inside the attrset for `default = ...`.
    for c in arg.named_children:
        if c.type != "binding_set":
            continue
        for binding in c.named_children:
            if binding.type != "binding":
                continue
            ap = binding.child_by_field_name("attrpath")
            ex = binding.child_by_field_name("expression")
            if ap is None or ex is None:
                continue
            segs = _attrpath_segments(ap)
            if segs == ["default"]:
                return ex
    return None


def _walk_bindings(root: ts.Node, info: NixOracleTS) -> None:
    """Walk all binding nodes in the source tree, pattern-match each.

    Patterns of interest:
      - options.services.<X> → set service_name = X
      - baf.pub_topic{,_<suffix>} = mkOption { default = "Y"; } → pub_topic
      - baf.<X>_sub_topic = mkOption { default = "Y"; } → singular sub
      - baf.sub_topics = mkOption { default = [ ... ]; } → sub_topics
      - services.<X>.additional_sub_topics = [ ... ]
    """
    def visit(node: ts.Node) -> None:
        if node.type == "binding":
            ap = node.child_by_field_name("attrpath")
            ex = node.child_by_field_name("expression")
            if ap is None or ex is None:
                return
            segs = _attrpath_segments(ap)

            # options.services.<X>
            if (len(segs) == 3 and segs[0] == "options"
                    and segs[1] == "services"):
                if not info.service_name:
                    info.service_name = segs[2]

            # baf.pub_topic[_<suffix>]
            if (len(segs) == 2 and segs[0] == "baf"
                    and (segs[1] == "pub_topic" or segs[1].startswith("pub_topic_"))):
                default_node = _find_default_in_mkoption(ex)
                if default_node is not None:
                    val = _string_value(default_node)
                    if val is not None:
                        if not info.pub_topic:
                            info.pub_topic = val
                        else:
                            info.pub_topic_variants.append(val)

            # baf.<X>_sub_topic (singular scalar)
            if (len(segs) == 2 and segs[0] == "baf"
                    and segs[1].endswith("_sub_topic") and segs[1] != "sub_topic"):
                default_node = _find_default_in_mkoption(ex)
                if default_node is not None:
                    val = _string_value(default_node)
                    if val is not None:
                        info.sub_topics.append(val)

            # baf.sub_topics (plural list)
            if len(segs) == 2 and segs[0] == "baf" and segs[1] == "sub_topics":
                default_node = _find_default_in_mkoption(ex)
                if default_node is not None and default_node.type == "list_expression":
                    info.sub_topics.extend(_list_string_values(default_node))

            # services.<X>.additional_sub_topics = [ ... ]
            if (len(segs) == 3 and segs[0] == "services"
                    and segs[2] == "additional_sub_topics"
                    and ex.type == "list_expression"):
                target = segs[1]
                info.additional_subs.setdefault(target, []).extend(_list_string_values(ex))

        # Recurse into children.
        for child in node.named_children:
            visit(child)

    visit(root)


def _scan_for_runs_binaries(source: bytes) -> list[str]:
    """Tree-sitter walks don't easily catch `${pkgs.X}/bin/Y` since
    the string-interpolation handling depends on grammar internals. Use a
    string scan over the raw source — same approach as the regex oracle —
    since this isn't where tree-sitter adds value."""
    # Reuse the regex from oracle_nix_pubsub.py to keep behavior consistent.
    import re
    pattern = re.compile(rb"\$\{pkgs\.([a-zA-Z0-9_-]+)\}/bin/([a-zA-Z_][a-zA-Z0-9_-]*)\b")
    seen = set()
    out = []
    for m in pattern.finditer(source):
        bin_name = m.group(2).decode("utf-8")
        if bin_name in ("pubmsg", "submsg") or bin_name in seen:
            continue
        seen.add(bin_name)
        out.append(bin_name)
    return out


def parse_nix_file(source: bytes) -> NixOracleTS:
    tree = _PARSER.parse(source)
    info = NixOracleTS()
    _walk_bindings(tree.root_node, info)
    info.runs_binaries = _scan_for_runs_binaries(source)
    return info


def scan_repo(repo_root: Path) -> list[NixOracleTS]:
    """Walk all .nix files, return one oracle per file with extracted data."""
    out: list[NixOracleTS] = []
    for path in repo_root.rglob("*.nix"):
        try:
            source = path.read_bytes()
        except OSError:
            continue
        try:
            info = parse_nix_file(source)
        except Exception as e:
            sys.stderr.write(f"parse failed: {path}: {e}\n")
            continue
        if not info.service_name and not info.additional_subs:
            continue
        info.declared_in = str(path.relative_to(repo_root)).replace("\\", "/")
        out.append(info)
    return out


def _to_dict(info: NixOracleTS) -> dict:
    return {
        "service_name": info.service_name,
        "pub_topic": info.pub_topic,
        "pub_topic_variants": info.pub_topic_variants,
        "sub_topics": sorted(info.sub_topics),
        "additional_subs": {k: sorted(v) for k, v in info.additional_subs.items()},
        "runs_binaries": info.runs_binaries,
        "declared_in": info.declared_in,
    }


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print("usage: oracle_nix_pubsub_treesitter.py <repo-root>", file=sys.stderr)
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
