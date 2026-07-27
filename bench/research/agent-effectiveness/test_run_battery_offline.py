"""Offline contract tests for the zero-cost category-6 runner."""

import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


HERE = Path(__file__).resolve().parent


class OfflineBatteryContractTests(unittest.TestCase):
    def test_runner_help_does_not_require_anthropic_package(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)
            (temp_path / "sitecustomize.py").write_text(
                """
import builtins

original_import = builtins.__import__


def reject_anthropic(name, *args, **kwargs):
    if name == "anthropic" or name.startswith("anthropic."):
        raise ImportError("anthropic imports are forbidden in the offline lane")
    return original_import(name, *args, **kwargs)


builtins.__import__ = reject_anthropic
""",
                encoding="utf-8",
            )
            env = os.environ.copy()
            env["PYTHONPATH"] = str(temp_path)

            result = subprocess.run(
                [sys.executable, str(HERE / "run_battery.py"), "--help"],
                cwd=HERE,
                env=env,
                check=False,
                capture_output=True,
                text=True,
            )

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("--filter-category", result.stdout)


if __name__ == "__main__":
    unittest.main()
