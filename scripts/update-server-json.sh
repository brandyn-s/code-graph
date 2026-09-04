#!/usr/bin/env bash
# Render server.json for the MCP registry from a published release.
#
# Usage:
#   scripts/update-server-json.sh v0.9.0 [output-path]   (default: ./server.json)
#
# The registry's "mcpb" package type points at a release artifact URL plus its
# SHA-256, so the file is regenerated per release from checksums.txt.
set -euo pipefail

REPO="${CODE_GRAPH_REPO:-brandyn-s/code-graph}"
TAG="${1:-}"
OUT="${2:-server.json}"
TEMPLATE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)/packaging/mcp-registry/server.json.tmpl"

if [ -z "$TAG" ]; then
    echo "usage: $0 <tag> [output-path]" >&2
    exit 2
fi
VERSION="${TAG#v}"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
curl -fsSL --retry 3 -o "$tmp/checksums.txt" \
    "https://github.com/${REPO}/releases/download/${TAG}/checksums.txt" \
    || { echo "checksums.txt not found for ${TAG}; is the release published?" >&2; exit 1; }

sha_for() {
    local asset="$1" sha
    sha="$(awk -v f="$asset" '$2 == f || $2 == "*"f {print $1}' "$tmp/checksums.txt")"
    [ -n "$sha" ] || { echo "no checksum for ${asset}" >&2; exit 1; }
    printf '%s' "$sha"
}

sed \
    -e "s/@VERSION@/${VERSION}/g" \
    -e "s#@REPO@#${REPO}#g" \
    -e "s/@SHA256_DARWIN_ARM64@/$(sha_for code-graph-darwin-arm64.tar.gz)/" \
    -e "s/@SHA256_DARWIN_AMD64@/$(sha_for code-graph-darwin-amd64.tar.gz)/" \
    -e "s/@SHA256_LINUX_ARM64@/$(sha_for code-graph-linux-arm64.tar.gz)/" \
    -e "s/@SHA256_LINUX_AMD64@/$(sha_for code-graph-linux-amd64.tar.gz)/" \
    -e "s/@SHA256_WINDOWS_AMD64@/$(sha_for code-graph-windows-amd64.zip)/" \
    "$TEMPLATE" > "$OUT"

if grep -q '@[A-Z0-9_]*@' "$OUT"; then
    echo "unfilled placeholder remains in $OUT" >&2
    exit 1
fi
python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$OUT"
echo "wrote $OUT for ${TAG}"
