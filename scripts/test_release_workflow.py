import unittest
from pathlib import Path

WORKFLOW = Path(".github/workflows/release.yml")
RELEASE_HELPER = Path("scripts/release_workflow.sh")
RELEASE_LINT_ACTION = Path(".github/actions/release-lint/action.yml")
SECURITY_POLICY = Path(".github/repo-security-policy.yml")
README = Path("README.md")
BASELINE = "832bede03d6118827919fc8727f3c17854047d06"
ATTEST_ACTION = "1e69f48acb82d1966a394da916b4c1698aa569d6"


class ReleaseWorkflowContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.workflow = WORKFLOW.read_text(encoding="utf-8")
        cls.release_helper = RELEASE_HELPER.read_text(encoding="utf-8")
        cls.release_lint_action = RELEASE_LINT_ACTION.read_text(encoding="utf-8")
        cls.security_policy = SECURITY_POLICY.read_text(encoding="utf-8")
        cls.readme = README.read_text(encoding="utf-8")

    def test_serializes_default_branch_exact_head_releases(self) -> None:
        self.assertIn("group: code-graph-release", self.workflow)
        self.assertIn("cancel-in-progress: false", self.workflow)
        self.assertGreaterEqual(
            self.workflow.count("run: bash scripts/release_workflow.sh validate"),
            2,
        )
        self.assertGreaterEqual(
            self.release_helper.count('if [ "$GITHUB_REF" != "$expected_ref" ]'),
            1,
        )
        self.assertGreaterEqual(
            self.release_helper.count('if [ "$actual_sha" != "$GITHUB_SHA" ]'),
            1,
        )

    def test_uses_reachable_no_new_lint_gate(self) -> None:
        self.assertIn(f'default: "{BASELINE}"', self.release_lint_action)
        self.assertIn("fetch-depth: 0", self.workflow)
        self.assertIn(
            'git merge-base --is-ancestor "$baseline" "$GITHUB_SHA"',
            self.release_lint_action,
        )
        self.assertIn(
            "--new-from-rev=${{ inputs.baseline }}",
            self.release_lint_action,
        )
        self.assertEqual(
            self.workflow.count("uses: ./.github/actions/release-lint"),
            1,
        )
        self.assertIn("continue-on-error: true", self.workflow)

    def test_release_retry_state_machine_is_fail_closed_and_resumable(
        self,
    ) -> None:
        self.assertIn(
            "scripts.test_release_workflow_acceptance",
            self.workflow,
        )
        self.assertIn(
            'python3 "$SCRIPT_DIR/release_version.py" "$VERSION" "$latest_version"',
            self.release_helper,
        )
        self.assertIn(
            'if [ -n "$tag_sha" ] && [ "$tag_sha" != "$GITHUB_SHA" ]',
            self.release_helper,
        )
        self.assertIn('release_state="absent"', self.release_helper)
        self.assertIn('release_state="draft"', self.release_helper)
        self.assertIn(
            "Release $VERSION is already published",
            self.release_helper,
        )
        self.assertIn(
            "run: bash scripts/release_workflow.sh tag",
            self.workflow,
        )
        self.assertIn(
            "run: bash scripts/release_workflow.sh publish",
            self.workflow,
        )
        self.assertIn("--clobber", self.release_helper)
        self.assertIn(
            "reject_unexpected_draft_assets",
            self.release_helper,
        )
        self.assertIn(
            "require_exact_release_inventory",
            self.release_helper,
        )

    def test_publishes_an_immutable_tag_and_assets(self) -> None:
        self.assertIn(
            'git tag "$VERSION" "$GITHUB_SHA"',
            self.release_helper,
        )
        self.assertIn(
            'git push origin "refs/tags/$VERSION:refs/tags/$VERSION"',
            self.release_helper,
        )
        self.assertIn("sha256sum -- *.tar.gz *.zip", self.workflow)
        self.assertIn(
            'gh release create "$VERSION"',
            self.release_helper,
        )
        for forbidden in (
            "replace:",
            "release delete",
            "tag -f",
            "--force",
            "softprops/action-gh-release",
        ):
            with self.subTest(forbidden=forbidden):
                self.assertNotIn(forbidden, self.workflow)
                self.assertNotIn(forbidden, self.release_helper)

    def test_attests_release_archives_before_publication_with_least_privilege(
        self,
    ) -> None:
        self.assertIn("\n  attest:\n", self.workflow)
        attest_start = self.workflow.index("\n  attest:\n")
        release_start = self.workflow.index("\n  release:\n")
        self.assertLess(attest_start, release_start)

        attest_job = self.workflow[attest_start:release_start]
        self.assertIn("needs: [build-unix, build-windows]", attest_job)
        self.assertIn("attestations: write", attest_job)
        self.assertIn("contents: read", attest_job)
        self.assertIn("id-token: write", attest_job)
        self.assertIn(f"uses: actions/attest@{ATTEST_ACTION}", attest_job)
        self.assertIn("subject-checksums: checksums.txt", attest_job)

        release_job = self.workflow[release_start:]
        self.assertIn("needs: [attest]", release_job)

    def test_repository_policy_requires_attestations(self) -> None:
        self.assertIn("require-attestation: true", self.security_policy)

    def test_stages_release_assets_on_a_draft_before_publication(self) -> None:
        release_start = self.workflow.index("\n  release:\n")
        release_job = self.workflow[release_start:]
        self.assertIn(
            "run: bash scripts/release_workflow.sh publish",
            release_job,
        )
        self.assertGreaterEqual(self.release_helper.count("--draft"), 2)
        self.assertIn(
            'gh release upload "$VERSION"',
            self.release_helper,
        )
        self.assertIn(
            'gh release edit "$VERSION"',
            self.release_helper,
        )
        self.assertIn("--draft=false", self.release_helper)

        last_create = self.release_helper.rindex('gh release create "$VERSION"')
        upload = self.release_helper.index('gh release upload "$VERSION"')
        inventory = self.release_helper.index(
            "require_exact_release_inventory",
            upload,
        )
        publish = self.release_helper.index(
            'gh release edit "$VERSION"',
            inventory,
        )
        self.assertLess(last_create, upload)
        self.assertLess(upload, inventory)
        self.assertLess(inventory, publish)

    def test_documents_release_attestation_verification(self) -> None:
        self.assertIn(
            "https://github.com/redacted-org/code-graph/releases",
            self.readme,
        )
        self.assertIn(
            "gh attestation verify PATH -R redacted-org/code-graph",
            self.readme,
        )
        self.assertIn(
            "gh release verify-asset TAG PATH -R redacted-org/code-graph",
            self.readme,
        )
        self.assertIn(
            "`v0.7.0-redacted.2` has an immutable-release attestation",
            self.readme,
        )
        self.assertIn(
            "does not have retroactive build provenance",
            self.readme,
        )


if __name__ == "__main__":
    unittest.main()
