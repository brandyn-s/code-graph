import subprocess
import sys
import unittest

from scripts.release_version import (
    compare_release_versions,
    parse_published_version,
    parse_release_version,
)


class ReleaseVersionTests(unittest.TestCase):
    def test_accepts_and_orders_semver_release_versions(self) -> None:
        self.assertEqual(parse_release_version("v0.9.0"), (0, 9, 0, 1, 0))
        self.assertGreater(compare_release_versions("v0.9.1", "v0.9.0"), 0)
        self.assertGreater(compare_release_versions("v0.10.0", "v0.9.9"), 0)
        self.assertEqual(compare_release_versions("v0.9.0", "v0.9.0"), 0)
        self.assertLess(compare_release_versions("v0.8.9", "v0.9.0"), 0)

    def test_first_public_release_is_newer_than_legacy_internal_tags(self) -> None:
        self.assertEqual(
            parse_published_version("v0.8.0-redacted.11"), (0, 8, 0, 0, 11)
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
            "v0.9.0-rc.1",
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


if __name__ == "__main__":
    unittest.main()
