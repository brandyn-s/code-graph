import subprocess
import sys
import unittest

from scripts.release_version import (
    KIND_LEGACY,
    KIND_RC,
    KIND_RELEASE,
    compare_release_versions,
    is_prerelease,
    parse_published_version,
    parse_release_version,
)


class ReleaseVersionTests(unittest.TestCase):
    def test_accepts_and_orders_semver_release_versions(self) -> None:
        self.assertEqual(parse_release_version("v0.9.0"), (0, 9, 0, KIND_RELEASE, 0))
        self.assertGreater(compare_release_versions("v0.9.1", "v0.9.0"), 0)
        self.assertGreater(compare_release_versions("v0.10.0", "v0.9.9"), 0)
        self.assertEqual(compare_release_versions("v0.9.0", "v0.9.0"), 0)
        self.assertLess(compare_release_versions("v0.8.9", "v0.9.0"), 0)

    def test_release_candidates_sort_below_their_release_and_in_order(self) -> None:
        self.assertEqual(parse_release_version("v0.9.0-rc.1"), (0, 9, 0, KIND_RC, 1))
        self.assertTrue(is_prerelease("v0.9.0-rc.1"))
        self.assertFalse(is_prerelease("v0.9.0"))
        self.assertGreater(compare_release_versions("v0.9.0-rc.2", "v0.9.0-rc.1"), 0)
        self.assertGreater(compare_release_versions("v0.9.0", "v0.9.0-rc.7"), 0)
        self.assertLess(compare_release_versions("v0.9.0-rc.1", "v0.9.0"), 0)
        self.assertGreater(compare_release_versions("v0.9.1-rc.1", "v0.9.0"), 0)
        self.assertGreater(compare_release_versions("v0.9.0-rc.1", "v0.8.0-redacted.11"), 0)

    def test_first_public_release_is_newer_than_legacy_internal_tags(self) -> None:
        self.assertEqual(
            parse_published_version("v0.8.0-redacted.11"), (0, 8, 0, KIND_LEGACY, 11)
        )
        self.assertGreater(compare_release_versions("v0.9.0", "v0.8.0-redacted.11"), 0)
        self.assertGreater(compare_release_versions("v0.8.0", "v0.8.0-redacted.11"), 0)
        self.assertLess(compare_release_versions("v0.7.9", "v0.8.0-redacted.11"), 0)

    def test_rejects_noncanonical_candidate_versions(self) -> None:
        invalid = [
            "0.9.0",
            "v0.09.0",
            "v0.9",
            "v0.9.0-redacted.1",
            "v0.9.0-rc.0",
            "v0.9.0-rc.01",
            "v0.9.0-beta.1",
            "v0.9.0;touch-pwned",
        ]
        for version in invalid:
            with self.subTest(version=version):
                with self.assertRaisesRegex(ValueError, "canonical"):
                    parse_release_version(version)

    def test_cli_fails_closed_when_candidate_is_not_newer(self) -> None:
        result = subprocess.run(
            [sys.executable, "scripts/release_version.py", "v0.9.0", "v0.9.0"],
            check=False,
            capture_output=True,
            text=True,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("must be newer", result.stderr)

    def test_cli_reports_prerelease_flag(self) -> None:
        for version, expected in (("v0.9.0-rc.1", "true"), ("v0.9.0", "false")):
            result = subprocess.run(
                [sys.executable, "scripts/release_version.py", "--is-prerelease", version],
                check=False,
                capture_output=True,
                text=True,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout.strip(), expected)


if __name__ == "__main__":
    unittest.main()
