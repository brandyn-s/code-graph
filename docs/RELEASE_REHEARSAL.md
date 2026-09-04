# Release rehearsal: cutting and discarding `v0.9.0-rc.1`

The release workflow has never run on this repository, and neither has the
plugin's promotion step against a brandyn-s release. Rehearse with a release
candidate before announcing anything. Release candidates are published as
GitHub prereleases: `/releases/latest`, `install.sh`, and `code-graph update`
on the default channel ignore them, so a rehearsal cannot reach users who did
not opt in.

## Prerequisites

- The repository is public (artifact attestations need it) and Actions is
  enabled.
- `main` is green in Core CI at the commit you will release.
- You have `gh` logged in with `repo` scope on brandyn-s/code-graph.

## 1. Cut the candidate

1. Actions → **Release** → Run workflow → `version` = `v0.9.0-rc.1`, notes
   optional.
2. Watch the jobs in order: preflight (version ordering and immutable-input
   checks), lint, build-unix (4 targets), build-windows, attest, release. The
   release job creates a draft, uploads exactly the expected assets, checks the
   inventory, then flips the draft to published with `--prerelease`.

## 2. Verify what was published

```bash
REPO=brandyn-s/code-graph TAG=v0.9.0-rc.1
gh release view "$TAG" --repo "$REPO" --json isPrerelease,isDraft,assets \
  --jq '{prerelease:.isPrerelease, draft:.isDraft, assets:[.assets[].name]}'
# expect prerelease=true, draft=false, and exactly:
# code-graph-darwin-amd64.tar.gz code-graph-darwin-arm64.tar.gz
# code-graph-linux-amd64.tar.gz  code-graph-linux-arm64.tar.gz
# code-graph-windows-amd64.zip   checksums.txt

# Latest must still be the previous stable release (or none):
gh release view --repo "$REPO" --json tagName --jq .tagName

# Provenance on one asset:
gh release download "$TAG" --repo "$REPO" --pattern code-graph-darwin-arm64.tar.gz --pattern checksums.txt -D /tmp/rc
(cd /tmp/rc && shasum -a 256 -c checksums.txt --ignore-missing)
gh attestation verify /tmp/rc/code-graph-darwin-arm64.tar.gz --repo "$REPO" \
  --signer-workflow "$REPO/.github/workflows/release.yml"
```

## 3. Exercise the install paths against the candidate

```bash
# curl installer, pinned to the rc (latest would skip it):
CODE_GRAPH_VERSION=v0.9.0-rc.1 CODE_GRAPH_INSTALL_DIR=/tmp/rc-bin \
  bash <(curl -fsSL https://raw.githubusercontent.com/brandyn-s/code-graph/main/install.sh)
/tmp/rc-bin/code-graph --version        # 0.9.0-rc.1
/tmp/rc-bin/code-graph doctor           # sane report, toolset core

# self-update: stable channel must NOT see the rc; rc channel must.
/tmp/rc-bin/code-graph update --dry-run
CODE_GRAPH_UPDATE_CHANNEL=rc /tmp/rc-bin/code-graph update --dry-run

# packaging renderers must accept the tag:
scripts/update-homebrew-formula.sh v0.9.0-rc.1 /tmp/rc/code-graph.rb
scripts/update-server-json.sh v0.9.0-rc.1 /tmp/rc/server.json
```

Then, from the plugin repository, point a scratch BOM at the rc assets and run
its `validate_real_installed.py` once so the promotion path is exercised
end-to-end before the real release.

## 4. Discard the rehearsal

Release candidates are disposable. Deleting the release and its tag is the
only time deletion is acceptable; never do this to a plain release.

```bash
gh release delete v0.9.0-rc.1 --repo brandyn-s/code-graph --yes --cleanup-tag
git fetch --prune --prune-tags origin
```

Record what broke in `CHANGELOG.md` under Unreleased, fix it, and rehearse
again with `-rc.2` until a candidate passes untouched. Then run the workflow
with `v0.9.0`.
