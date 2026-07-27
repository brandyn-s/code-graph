from pathlib import Path
import unittest


WORKFLOW = Path(".github/workflows/release.yml")
BASELINE = "832bede03d6118827919fc8727f3c17854047d06"


class ReleaseWorkflowContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.workflow = WORKFLOW.read_text(encoding="utf-8")

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


if __name__ == "__main__":
    unittest.main()
