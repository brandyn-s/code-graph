# Installing code-graph

Every route below installs the same statically linked binary. Pick the one that
matches how you manage tools; all of them end with registering the server in
your MCP client (see [clients.md](clients.md)) and running `code-graph doctor`.

## One-command installer (macOS, Linux)

```bash
curl -fsSL https://raw.githubusercontent.com/brandyn-s/code-graph/main/install.sh | bash
```

Pin a release with `CODE_GRAPH_VERSION=v0.9.0`, change the destination with
`CODE_GRAPH_INSTALL_DIR`. The script verifies the archive against the release's
`checksums.txt`; when the GitHub CLI is logged in it also verifies build
provenance with `gh attestation verify`.

## Windows

```powershell
irm https://raw.githubusercontent.com/brandyn-s/code-graph/main/install.ps1 | iex
```

## Homebrew

The tap lives at `brandyn-s/homebrew-tap` (created by the maintainer on first
release; until it exists this section is a plan, not a promise):

```bash
brew tap brandyn-s/tap
brew install code-graph
```

Maintainers refresh the formula after each release:

```bash
TAP_DIR=~/src/homebrew-tap scripts/update-homebrew-formula.sh v0.9.0
cd ~/src/homebrew-tap && git commit -am "code-graph v0.9.0" && git push
```

The template is `packaging/homebrew/code-graph.rb`; the script fills the
version and per-platform SHA-256 values from the release's `checksums.txt` and
refuses to write a formula with any placeholder left.

## Nix

`flake.nix` builds the binary with `buildGoModule` (cgo enabled for the
vendored grammars):

```bash
nix run github:brandyn-s/code-graph -- --version
nix profile install github:brandyn-s/code-graph
```

`vendorHash` must be filled once per `go.sum` change: run `nix build`, copy
the hash from the mismatch error into `flake.nix`, and commit. The flake is
maintained on a best-effort basis; the release archives are the supported
artifact.

## Go toolchain

```bash
go install github.com/brandyn-s/code-graph/cmd/code-graph@latest
```

Requires Go 1.26+ and a C compiler. Add `-tags cbm_all` to include the CUDA
grammar.

## Verified install by hand

```bash
REPO=brandyn-s/code-graph TAG=v0.9.0 ASSET=code-graph-darwin-arm64.tar.gz
gh release download "$TAG" --repo "$REPO" --pattern "$ASSET" --pattern checksums.txt
shasum -a 256 -c checksums.txt --ignore-missing
gh attestation verify "$ASSET" --repo "$REPO"
tar -xzf "$ASSET" && install -m 0755 code-graph ~/.local/bin/code-graph
```

`scripts/setup.sh` and `scripts/setup-windows.ps1` automate exactly this path.

## MCP registry

Once a release is published, `docs/registry.md` describes the one-command
publish to the MCP registry so clients that browse it can install code-graph
directly.
