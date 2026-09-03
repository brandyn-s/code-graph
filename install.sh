#!/usr/bin/env bash
# code-graph installer (macOS + Linux). Needs only curl and tar.
#
#   curl -fsSL https://raw.githubusercontent.com/brandyn-s/code-graph/main/install.sh | bash
#
# Environment:
#   CODE_GRAPH_VERSION      release tag to install (default: latest, e.g. v0.9.0)
#   CODE_GRAPH_INSTALL_DIR  destination directory (default: ~/.local/bin)
#   CODE_GRAPH_REPO         GitHub repository (default: brandyn-s/code-graph)
#
# Every download is checked against the release's checksums.txt. If the GitHub
# CLI (gh) is installed and authenticated, the archive's build provenance is
# also verified with `gh attestation verify`; otherwise that step is skipped
# and reported. For the fully verified path, see scripts/setup.sh.
set -euo pipefail

REPO="${CODE_GRAPH_REPO:-brandyn-s/code-graph}"
INSTALL_DIR="${CODE_GRAPH_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${CODE_GRAPH_VERSION:-latest}"
BINARY="code-graph"

say()  { printf '%s\n' "$*"; }
ok()   { printf '\342\234\223 %s\n' "$*"; }
warn() { printf '\342\232\240 %s\n' "$*" >&2; }
die()  { printf '\342\234\227 %s\n' "$*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required but not found in PATH"; }
need curl
need tar

case "$(uname -s)" in
    Darwin) os="darwin" ;;
    Linux)  os="linux" ;;
    *) die "Unsupported OS: $(uname -s). Windows users: download ${BINARY}-windows-amd64.zip from https://github.com/${REPO}/releases" ;;
esac
case "$(uname -m)" in
    x86_64|amd64)  arch="amd64" ;;
    arm64|aarch64) arch="arm64" ;;
    *) die "Unsupported architecture: $(uname -m)" ;;
esac

asset="${BINARY}-${os}-${arch}.tar.gz"
if [ "$VERSION" = "latest" ]; then
    base_url="https://github.com/${REPO}/releases/latest/download"
else
    base_url="https://github.com/${REPO}/releases/download/${VERSION}"
fi

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

say "Downloading ${asset} (${VERSION}) from ${REPO}..."
curl -fsSL --retry 3 -o "${tmpdir}/${asset}" "${base_url}/${asset}" \
    || die "Download failed. Check https://github.com/${REPO}/releases for available versions."
curl -fsSL --retry 3 -o "${tmpdir}/checksums.txt" "${base_url}/checksums.txt" \
    || die "checksums.txt is missing from the release; refusing to install an unverified archive."

expected="$(awk -v f="$asset" '$2 == f || $2 == "*"f {print $1}' "${tmpdir}/checksums.txt")"
[ -n "$expected" ] || die "checksums.txt has no entry for ${asset}"
if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "${tmpdir}/${asset}" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "${tmpdir}/${asset}" | awk '{print $1}')"
else
    die "Neither sha256sum nor shasum is available to verify the download"
fi
[ "$actual" = "$expected" ] || die "Checksum mismatch for ${asset} (expected ${expected}, got ${actual})"
ok "SHA-256 verified"

if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
    if gh attestation verify "${tmpdir}/${asset}" --repo "$REPO" >/dev/null 2>&1; then
        ok "Build provenance verified with gh attestation"
    else
        die "gh attestation verify failed for ${asset}; refusing to install"
    fi
else
    warn "Build provenance not verified (GitHub CLI not installed or not authenticated); checksum verification only"
fi

tar -xzf "${tmpdir}/${asset}" -C "$tmpdir"
[ -f "${tmpdir}/${BINARY}" ] || die "Archive did not contain ${BINARY}"
mkdir -p "$INSTALL_DIR"
install -m 0755 "${tmpdir}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
ok "Installed ${INSTALL_DIR}/${BINARY} ($("${INSTALL_DIR}/${BINARY}" --version))"

case ":${PATH}:" in
    *":${INSTALL_DIR}:"*) ;;
    *) warn "${INSTALL_DIR} is not in your PATH; add it or use the absolute path below" ;;
esac

cat <<MSG

Next steps
  Claude Code:   claude mcp add code-graph --scope user -- ${INSTALL_DIR}/${BINARY}
  Any client:    add this stdio server to your MCP config
                 {"mcpServers": {"code-graph": {"command": "${INSTALL_DIR}/${BINARY}"}}}
  Auto-configure Claude Code, Codex, Cursor, Windsurf, Gemini CLI, VS Code and Zed:
                 ${INSTALL_DIR}/${BINARY} install

Semantic node search needs VOYAGE_API_KEY; everything else works offline.
Docs: https://github.com/${REPO}#readme
MSG
