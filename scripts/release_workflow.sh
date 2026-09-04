#!/usr/bin/env bash
#
# Fail-closed release state machine used by .github/workflows/release.yml.
# Keeping the GitHub mutations here makes retry and negative paths executable
# in local acceptance tests instead of relying on textual workflow assertions.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR

die() {
    echo "::error::$*" >&2
    exit 1
}

require_env() {
    local name
    for name in "$@"; do
        if [ -z "${!name:-}" ]; then
            die "Required environment variable $name is not set"
        fi
    done
}

release_records() {
    gh api --paginate \
        "repos/$GITHUB_REPOSITORY/releases?per_page=100" \
        --jq '.[] | [.tag_name, (if .draft then "draft" else "published" end)] | @tsv'
}

exact_tag_sha() {
    local records ref sha
    if ! records=$(
        gh api \
            "repos/$GITHUB_REPOSITORY/git/matching-refs/tags/$VERSION" \
            --jq '.[] | [.ref, .object.sha] | @tsv'
    ); then
        return 1
    fi
    while IFS=$'\t' read -r ref sha; do
        [ -n "$ref" ] || continue
        if [ "$ref" = "refs/tags/$VERSION" ]; then
            printf '%s\n' "$sha"
        fi
    done <<< "$records"
}

exact_release_states() {
    local records tag state
    if ! records=$(release_records); then
        return 1
    fi
    while IFS=$'\t' read -r tag state; do
        [ -n "$tag" ] || continue
        if [ "$tag" = "$VERSION" ]; then
            printf '%s\n' "$state"
        fi
    done <<< "$records"
}

validate_release_state() {
    require_env \
        DEFAULT_BRANCH \
        GH_TOKEN \
        GITHUB_REF \
        GITHUB_REPOSITORY \
        GITHUB_SHA \
        VERSION

    local expected_ref actual_sha latest_version tag_sha release_states
    local release_state tag_exists

    expected_ref="refs/heads/$DEFAULT_BRANCH"
    if [ "$GITHUB_REF" != "$expected_ref" ]; then
        die "Release must be dispatched from $expected_ref, got $GITHUB_REF"
    fi

    actual_sha=$(git rev-parse HEAD)
    if [ "$actual_sha" != "$GITHUB_SHA" ]; then
        die "Checkout HEAD $actual_sha does not match event SHA $GITHUB_SHA"
    fi

    if ! latest_version=$(gh release view \
        --repo "$GITHUB_REPOSITORY" \
        --json tagName \
        --jq .tagName); then
        die "Unable to determine the latest published release"
    fi
    python3 "$SCRIPT_DIR/release_version.py" "$VERSION" "$latest_version"

    if ! tag_sha=$(exact_tag_sha); then
        die "Unable to determine the exact remote tag state for $VERSION"
    fi
    if [ "$(printf '%s\n' "$tag_sha" | sed '/^$/d' | wc -l | tr -d ' ')" -gt 1 ]; then
        die "Tag $VERSION resolved to multiple exact refs"
    fi
    if [ -n "$tag_sha" ] && [ "$tag_sha" != "$GITHUB_SHA" ]; then
        die "Tag $VERSION points to $tag_sha, expected $GITHUB_SHA"
    fi

    if ! release_states=$(exact_release_states); then
        die "Unable to determine the exact release state for $VERSION"
    fi
    release_state="absent"
    case "$release_states" in
        "")
            ;;
        "draft")
            release_state="draft"
            ;;
        "published")
            die "Release $VERSION is already published"
            ;;
        *)
            die "Release $VERSION has ambiguous state: $release_states"
            ;;
    esac

    if [ -n "$tag_sha" ]; then
        tag_exists="true"
    else
        tag_exists="false"
    fi

    if [ -n "${GITHUB_OUTPUT:-}" ]; then
        {
            printf 'tag_exists=%s\n' "$tag_exists"
            printf 'release_state=%s\n' "$release_state"
        } >> "$GITHUB_OUTPUT"
    fi
    printf 'Validated resumable release state: %s\n' "$release_state"
}

create_immutable_tag() {
    require_env GITHUB_SHA TAG_EXISTS VERSION
    # shellcheck disable=SC2153  # TAG_EXISTS is validated dynamically above.
    local tag_exists_input="$TAG_EXISTS"

    case "$tag_exists_input" in
        "false")
            git tag "$VERSION" "$GITHUB_SHA"
            git push origin "refs/tags/$VERSION:refs/tags/$VERSION"
            ;;
        "true")
            printf 'Reusing immutable tag %s at %s\n' "$VERSION" "$GITHUB_SHA"
            ;;
        *)
            die "Invalid TAG_EXISTS value: $tag_exists_input"
            ;;
    esac
}

expected_asset_names() {
    printf '%s\n' \
        "code-graph-linux-amd64.tar.gz" \
        "code-graph-linux-arm64.tar.gz" \
        "code-graph-darwin-amd64.tar.gz" \
        "code-graph-darwin-arm64.tar.gz" \
        "code-graph-windows-amd64.zip" \
        "checksums.txt"
}

require_expected_assets() {
    local name
    while IFS= read -r name; do
        if [ ! -f "$name" ]; then
            die "Required release asset is missing: $name"
        fi
    done < <(expected_asset_names)
}

release_asset_names() {
    gh release view "$VERSION" \
        --repo "$GITHUB_REPOSITORY" \
        --json assets \
        --jq '.assets[].name'
}

asset_name_is_expected() {
    local candidate="$1"
    local expected
    while IFS= read -r expected; do
        if [ "$candidate" = "$expected" ]; then
            return 0
        fi
    done < <(expected_asset_names)
    return 1
}

reject_unexpected_draft_assets() {
    local assets name
    if ! assets=$(release_asset_names); then
        die "Unable to inspect existing assets for draft release $VERSION"
    fi
    while IFS= read -r name; do
        [ -n "$name" ] || continue
        if ! asset_name_is_expected "$name"; then
            die "Draft release $VERSION contains unexpected asset: $name"
        fi
    done <<< "$assets"
}

require_exact_release_inventory() {
    local expected actual
    expected=$(expected_asset_names | LC_ALL=C sort)
    if ! actual=$(release_asset_names | LC_ALL=C sort); then
        die "Unable to inspect final assets for draft release $VERSION"
    fi
    if [ "$actual" != "$expected" ]; then
        die "Draft release $VERSION does not contain the exact expected asset inventory"
    fi
}

require_draft_release() {
    local is_draft
    if ! is_draft=$(gh release view "$VERSION" \
        --repo "$GITHUB_REPOSITORY" \
        --json isDraft \
        --jq .isDraft); then
        die "Unable to inspect draft state for release $VERSION"
    fi
    if [ "$is_draft" != "true" ]; then
        die "Release $VERSION is not a resumable draft"
    fi
}

publish_release() {
    require_env GH_TOKEN GITHUB_REPOSITORY RELEASE_STATE VERSION
    require_expected_assets
    # shellcheck disable=SC2153  # RELEASE_STATE is validated dynamically above.
    local release_state_input="$RELEASE_STATE"

    # Release candidates (vX.Y.Z-rc.N) publish as GitHub prereleases so
    # /releases/latest, install.sh, and self-update on the stable channel
    # never pick them up.
    local prerelease_flag=()
    if [ "$(python3 "$SCRIPT_DIR/release_version.py" --is-prerelease "$VERSION")" = "true" ]; then
        prerelease_flag=(--prerelease)
    fi

    case "$release_state_input" in
        "absent")
            if [ -n "${RELEASE_NOTES:-}" ]; then
                gh release create "$VERSION" \
                    --repo "$GITHUB_REPOSITORY" \
                    --verify-tag \
                    --draft \
                    ${prerelease_flag[@]+"${prerelease_flag[@]}"} \
                    --notes "$RELEASE_NOTES"
            else
                gh release create "$VERSION" \
                    --repo "$GITHUB_REPOSITORY" \
                    --verify-tag \
                    --draft \
                    ${prerelease_flag[@]+"${prerelease_flag[@]}"} \
                    --generate-notes
            fi
            ;;
        "draft")
            printf 'Resuming existing draft release %s\n' "$VERSION"
            ;;
        *)
            die "Invalid RELEASE_STATE value: $release_state_input"
            ;;
    esac

    require_draft_release
    reject_unexpected_draft_assets

    local upload_paths=()
    local name
    while IFS= read -r name; do
        upload_paths+=("./$name")
    done < <(expected_asset_names)

    gh release upload "$VERSION" \
        --repo "$GITHUB_REPOSITORY" \
        --clobber \
        "${upload_paths[@]}"

    require_draft_release
    require_exact_release_inventory

    gh release edit "$VERSION" \
        --repo "$GITHUB_REPOSITORY" \
        --draft=false
}

case "${1:-}" in
    validate)
        validate_release_state
        ;;
    tag)
        create_immutable_tag
        ;;
    publish)
        publish_release
        ;;
    *)
        die "usage: $0 {validate|tag|publish}"
        ;;
esac
