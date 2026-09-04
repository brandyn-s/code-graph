#!/usr/bin/env bash
# vendor-grammar-from-manifest.sh: vendor a tree-sitter grammar at the exact
# commit pinned by upstream codebase-memory-mcp's grammar manifest.
#
# Usage:
#   ./scripts/vendor-grammar-from-manifest.sh <upstream-clone> <name> [<name>...]
#
#   upstream-clone: a checkout of https://github.com/DeusData/codebase-memory-mcp
#   name:           grammar directory name as listed in the manifest (e.g. lua)
#
# For each grammar this reads `internal/cbm/vendored/grammars/MANIFEST.md` in
# the upstream clone for the upstream repository and pinned commit, clones that
# repository at the pinned commit, copies parser.c / scanner.c / tag.h /
# tree_sitter headers / LICENSE into internal/cbm/vendored/grammars/<name>/,
# and prints a THIRD_PARTY_NOTICES.md row. Wiring (grammar_<name>.c, the
# CBM_LANG enum, lang_specs.c, internal/lang) is still a manual step; see
# docs/extending.md.
set -euo pipefail

UPSTREAM="${1:?upstream clone path}"
shift
[ "$#" -ge 1 ] || { echo "usage: $0 <upstream-clone> <name> [<name>...]" >&2; exit 2; }
MANIFEST="$UPSTREAM/internal/cbm/vendored/grammars/MANIFEST.md"
[ -f "$MANIFEST" ] || { echo "manifest not found: $MANIFEST" >&2; exit 2; }

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

for NAME in "$@"; do
  # Manifest row: | name | abi | owner/repo | `commit` | status | ok |
  ROW="$(grep -E "^\| ${NAME} \|" "$MANIFEST" | head -1 || true)"
  [ -n "$ROW" ] || { echo "$NAME: not in manifest" >&2; exit 1; }
  ABI="$(echo "$ROW" | awk -F'|' '{gsub(/ /,"",$3); print $3}')"
  REPO="$(echo "$ROW" | awk -F'|' '{gsub(/ /,"",$4); print $4}')"
  COMMIT="$(echo "$ROW" | awk -F'|' '{gsub(/[ `]/,"",$5); print $5}')"
  [ -n "$REPO" ] && [ -n "$COMMIT" ] || { echo "$NAME: could not parse manifest row: $ROW" >&2; exit 1; }

  DEST="$PROJECT_DIR/internal/cbm/vendored/grammars/$NAME"
  echo "== $NAME: $REPO @ $COMMIT (ABI $ABI)"
  rm -rf "${WORK:?}/${NAME:?}"
  git clone -q "https://github.com/$REPO" "$WORK/$NAME"
  git -C "$WORK/$NAME" checkout -q "$COMMIT"
  SRC="$WORK/$NAME/src"
  [ -f "$SRC/parser.c" ] || { echo "$NAME: no src/parser.c at $COMMIT" >&2; exit 1; }
  mkdir -p "$DEST/tree_sitter"
  cp "$SRC/parser.c" "$DEST/"
  [ -f "$SRC/scanner.c" ] && cp "$SRC/scanner.c" "$DEST/"
  [ -f "$SRC/scanner.cc" ] && echo "$NAME: WARNING C++ scanner needs manual handling" >&2
  [ -f "$SRC/tag.h" ] && cp "$SRC/tag.h" "$DEST/"
  cp "$SRC"/tree_sitter/*.h "$DEST/tree_sitter/" 2>/dev/null || true
  for lic in LICENSE LICENSE.md LICENSE.txt COPYING; do
    if [ -f "$WORK/$NAME/$lic" ]; then cp "$WORK/$NAME/$lic" "$DEST/LICENSE"; break; fi
  done
  [ -f "$DEST/LICENSE" ] || echo "$NAME: WARNING no LICENSE found" >&2
  LICNAME="$(head -3 "$DEST/LICENSE" 2>/dev/null | tr '\n' ' ' | sed -E 's/.*(MIT|Apache License|Creative Commons|BSD)[^ ]*.*/\1/' )"
  echo "| $NAME | [$REPO](https://github.com/$REPO) | \`${COMMIT:0:12}\` | $ABI | ${LICNAME:-see LICENSE} |"
done
