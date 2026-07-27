import subprocess
import sys
import unittest

from scripts.release_version import compare_release_versions, parse_release_version


class ReleaseVersionTests(unittest.TestCase):
    def test_accepts_and_orders_redacted_release_versions(self) -> None:
        self.assertEqual(
            parse_release_version("v0.7.0-redacted.1"),
            (0, 7, 0, 1),
        )
        self.assertGreater(
            compare_release_versions(
                "v0.7.0-redacted.2",
                "v0.7.0-redacted.1",
            ),
            0,
        )
        self.assertGreater(
            compare_release_versions(
                "v0.7.0-redacted.10",
                "v0.7.0-redacted.2",
            ),
            0,
        )

    def test_rejects_noncanonical_release_versions(self) -> None:
        invalid = [
            "0.7.0-redacted.1",
            "v0.7.0",
            "v0.07.0-redacted.1",
            "v0.7.0-redacted.01",
            "v0.7.0-redacted.1;touch-pwned",
        ]
        for version in invalid:
            with self.subTest(version=version):
                with self.assertRaisesRegex(ValueError, "canonical"):
                    parse_release_version(version)

    def test_cli_fails_closed_when_candidate_is_not_newer(self) -> None:
        result = subprocess.run(
            [
                sys.executable,
                "scripts/release_version.py",
                "v0.7.0-redacted.1",
                "v0.7.0-redacted.1",
            ],
            check=False,
            capture_output=True,
            text=True,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("must be newer", result.stderr)


if __name__ == "__main__":
    unittest.main()
