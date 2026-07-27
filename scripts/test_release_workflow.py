from pathlib import Path
import unittest


WORKFLOW = Path(".github/workflows/release.yml")
SECURITY_POLICY = Path(".github/repo-security-policy.yml")
README = Path("README.md")
BASELINE = "832bede03d6118827919fc8727f3c17854047d06"
ATTEST_ACTION = "f7c74d28b9d84cb8768d0b8ca14a4bac6ef463e6"


class ReleaseWorkflowContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.workflow = WORKFLOW.read_text(encoding="utf-8")
        cls.security_policy = SECURITY_POLICY.read_text(encoding="utf-8")
        cls.readme = README.read_text(encoding="utf-8")

    def test_serializes_default_branch_exact_head_releases(self) -> None:
        self.assertIn("group: code-graph-release", self.workflow)
        self.assertIn("cancel-in-progress: false", self.workflow)
        self.assertGreaterEqual(
            self.workflow.count('if [ "$GITHUB_REF" != "$expected_ref" ]'),
            2,
        )
        self.assertGreaterEqual(
            self.workflow.count('if [ "$actual_sha" != "$GITHUB_SHA" ]'),
            2,
        )

    def test_uses_reachable_no_new_lint_gate(self) -> None:
        self.assertIn(f"LINT_BASELINE: {BASELINE}", self.workflow)
        self.assertIn("fetch-depth: 0", self.workflow)
        self.assertIn(
            'git merge-base --is-ancestor "$LINT_BASELINE" "$GITHUB_SHA"',
            self.workflow,
        )
        self.assertIn(
            "--new-from-rev=${{ env.LINT_BASELINE }}",
            self.workflow,
        )
        self.assertIn("continue-on-error: true", self.workflow)

    def test_validates_monotonic_nonexistent_release_twice(self) -> None:
        validation = (
            'python3 scripts/release_version.py "$VERSION" "$latest_version"'
        )
        self.assertGreaterEqual(self.workflow.count(validation), 2)
        self.assertGreaterEqual(
            self.workflow.count("git/matching-refs/tags/$VERSION"),
            2,
        )
        self.assertGreaterEqual(
            self.workflow.count('select(.tag_name == \\"$VERSION\\")'),
            2,
        )

    def test_publishes_an_immutable_tag_and_assets(self) -> None:
        self.assertIn('git tag "$VERSION" "$GITHUB_SHA"', self.workflow)
        self.assertIn(
            'git push origin "refs/tags/$VERSION:refs/tags/$VERSION"',
            self.workflow,
        )
        self.assertIn("sha256sum -- *.tar.gz *.zip", self.workflow)
        self.assertIn('gh release create "$VERSION"', self.workflow)
        for forbidden in (
            "replace:",
            "release delete",
            "tag -f",
            "--force",
            "softprops/action-gh-release",
        ):
            with self.subTest(forbidden=forbidden):
                self.assertNotIn(forbidden, self.workflow)

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
        self.assertGreaterEqual(release_job.count("--draft"), 2)
        self.assertIn('gh release upload "$VERSION"', release_job)
        self.assertIn('gh release edit "$VERSION"', release_job)
        self.assertIn("--draft=false", release_job)

        last_create = release_job.rindex('gh release create "$VERSION"')
        upload = release_job.index('gh release upload "$VERSION"')
        publish = release_job.index('gh release edit "$VERSION"')
        self.assertLess(last_create, upload)
        self.assertLess(upload, publish)

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
