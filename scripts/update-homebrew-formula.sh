#!/usr/bin/env bash
# Fill packaging/homebrew/code-graph.rb from a release's checksums.txt.
#
# Usage:
#   scripts/update-homebrew-formula.sh v0.9.0 [output-path]
#
# Downloads checksums.txt for the tag from GitHub releases, substitutes the
# version and per-platform SHA-256 values into the formula template, and
# writes the result (default: Formula/code-graph.rb under $TAP_DIR, or stdout
# when TAP_DIR is unset and no output path is given). Commit the result to the
# brandyn-s/homebrew-tap repository.
set -euo pipefail

REPO="${CODE_GRAPH_REPO:-brandyn-s/code-graph}"
TAG="${1:-}"
OUT="${2:-}"
TEMPLATE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)/packaging/homebrew/code-graph.rb"

if [ -z "$TAG" ]; then
    echo "usage: $0 <tag> [output-path]" >&2
    exit 2
fi
case "$TAG" in
    v[0-9]*) ;;
    *) echo "tag must look like v0.9.0, got: $TAG" >&2; exit 2 ;;
esac
VERSION="${TAG#v}"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
curl -fsSL --retry 3 -o "$tmp/checksums.txt" \
    "https://github.com/${REPO}/releases/download/${TAG}/checksums.txt" \
    || { echo "checksums.txt not found for ${TAG}; is the release published?" >&2; exit 1; }

sha_for() {
    local asset="$1" sha
    sha="$(awk -v f="$asset" '$2 == f || $2 == "*"f {print $1}' "$tmp/checksums.txt")"
    if [ -z "$sha" ]; then
        echo "no checksum for ${asset} in ${TAG}" >&2
        exit 1
    fi
    printf '%s' "$sha"
}

rendered="$(sed \
    -e "s/@VERSION@/${VERSION}/g" \
    -e "s/@SHA256_DARWIN_ARM64@/$(sha_for code-graph-darwin-arm64.tar.gz)/" \
    -e "s/@SHA256_DARWIN_AMD64@/$(sha_for code-graph-darwin-amd64.tar.gz)/" \
    -e "s/@SHA256_LINUX_ARM64@/$(sha_for code-graph-linux-arm64.tar.gz)/" \
    -e "s/@SHA256_LINUX_AMD64@/$(sha_for code-graph-linux-amd64.tar.gz)/" \
    "$TEMPLATE")"

if grep -q '@[A-Z0-9_]*@' <<<"$rendered"; then
    echo "unfilled placeholder remains in rendered formula" >&2
    exit 1
fi

if [ -z "$OUT" ] && [ -n "${TAP_DIR:-}" ]; then
    OUT="${TAP_DIR}/Formula/code-graph.rb"
fi
if [ -n "$OUT" ]; then
    mkdir -p "$(dirname "$OUT")"
    printf '%s\n' "$rendered" > "$OUT"
    echo "wrote $OUT for ${TAG}"
else
    printf '%s\n' "$rendered"
fi
