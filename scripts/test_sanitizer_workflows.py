"""Structure tests for the sanitizer lanes: asan.yml, tsan.yml, soak.yml, the
Makefile targets they call, and the docs that describe them. Run with
`python3 -m unittest scripts.test_sanitizer_workflows` (Core CI does)."""

import re
import unittest
from pathlib import Path

REPOSITORY_ROOT = Path(__file__).resolve().parents[1]
WORKFLOWS = REPOSITORY_ROOT / ".github" / "workflows"
ASAN = WORKFLOWS / "asan.yml"
TSAN = WORKFLOWS / "tsan.yml"
SOAK = WORKFLOWS / "soak.yml"
MAKEFILE = REPOSITORY_ROOT / "Makefile"
SOAK_SCRIPT = REPOSITORY_ROOT / "scripts" / "soak-index.sh"
CI_DOC = REPOSITORY_ROOT / "docs" / "ci.md"
CONTRIBUTING = REPOSITORY_ROOT / "CONTRIBUTING.md"
LINT_BASELINE = "32fc4dd857497addff22115d6858dde2289e8e04"
PINNED_USES = re.compile(r"^\s+uses:\s+\S+@[0-9a-f]{40}\s+#\s+v\d", re.MULTILINE)
ANY_USES = re.compile(r"^\s+uses:\s+", re.MULTILINE)


def read(path: Path) -> str:
    return path.read_text(encoding="utf-8") if path.exists() else ""


class SanitizerWorkflowContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.asan = read(ASAN)
        cls.tsan = read(TSAN)
        cls.soak = read(SOAK)
        cls.makefile = read(MAKEFILE)
        cls.soak_script = read(SOAK_SCRIPT)
        cls.ci_doc = read(CI_DOC)
        cls.contributing = read(CONTRIBUTING)

    def test_workflows_exist_with_least_privilege_and_pinned_actions(self) -> None:
        for name, text in (("asan", self.asan), ("tsan", self.tsan), ("soak", self.soak)):
            with self.subTest(workflow=name):
                self.assertTrue(text, f"{name}.yml must exist")
                self.assertIn("permissions:\n  contents: read\n", text)
                self.assertNotIn("pull_request_target", text)
                self.assertNotIn("self-hosted", text)
                self.assertNotIn("secrets.", text)
                self.assertEqual(
                    len(ANY_USES.findall(text)),
                    len(PINNED_USES.findall(text)),
                    f"every action in {name}.yml must be pinned to a commit SHA",
                )

    def test_nightly_lanes_run_on_schedule_and_dispatch(self) -> None:
        for name, text, cron in (("tsan", self.tsan, "17 4 * * *"), ("soak", self.soak, "17 5 * * *")):
            with self.subTest(workflow=name):
                self.assertIn(f"- cron: '{cron}'", text)
                self.assertIn("workflow_dispatch:", text)
                self.assertIn("persist-credentials: false", text)
                self.assertIn("cancel-in-progress: true", text)
                self.assertIn('go-version: "1.26"', text)

    def test_tsan_lane_uses_the_makefile_target(self) -> None:
        self.assertIn("run: make test-tsan", self.tsan)
        self.assertIn('- ".github/workflows/tsan.yml"', self.tsan)

    def test_soak_lane_builds_asan_binary_and_runs_fifty_iterations(self) -> None:
        self.assertIn("run: make build-asan", self.soak)
        self.assertIn('run: bash scripts/soak-index.sh "$ITERATIONS" "$FIXTURE"', self.soak)
        self.assertIn("default: '50'", self.soak)
        self.assertIn("default: 'bench/accuracy/synthetic/post-battery'", self.soak)
        self.assertIn("if: failure()", self.soak)
        self.assertIn("SOAK_LOG_DIR: ${{ runner.temp }}/soak-logs", self.soak)
        self.assertIn("path: ${{ runner.temp }}/soak-logs/", self.soak)

    def test_makefile_sanitizer_targets(self) -> None:
        self.assertRegex(self.makefile, r"^\.PHONY:.*\btest-tsan\b", "test-tsan must be .PHONY")
        for target in ("test-asan", "test-tsan", "build-asan", "soak"):
            with self.subTest(target=target):
                self.assertRegex(self.makefile, rf"(?m)^{target}:", f"{target} target missing")
        tsan = self.makefile[self.makefile.index("test-tsan:"):self.makefile.index("build-asan:")]
        self.assertIn('CGO_CFLAGS="-fsanitize=thread', tsan)
        self.assertIn('CGO_LDFLAGS="-fsanitize=thread"', tsan)
        self.assertIn("./internal/cbm/...", self.makefile)
        asan = self.makefile[self.makefile.index("test-asan:"):self.makefile.index("test-tsan:")]
        self.assertIn('CGO_CFLAGS="-fsanitize=address', asan)
        self.assertIn('CGO_LDFLAGS="-fsanitize=address"', asan)
        self.assertIn("SOAK_ITERATIONS ?= 50", self.makefile)
        self.assertIn("soak: build-asan", self.makefile)

    def test_soak_script_fails_closed(self) -> None:
        self.assertIn("set -euo pipefail", self.soak_script)
        self.assertIn('cli index_repository "$ARGS"', self.soak_script)
        self.assertIn('"force":true', self.soak_script)
        self.assertIn("ERROR: (Address|Thread|Leak|UndefinedBehavior)Sanitizer", self.soak_script)
        self.assertIn("SOAK_MAX_DB_GROWTH", self.soak_script)
        self.assertIn('export CODE_GRAPH_CACHE_DIR="$WORK/cache"', self.soak_script)
        self.assertTrue(SOAK_SCRIPT.stat().st_mode & 0o111, "soak-index.sh must be executable")

    def test_docs_describe_lanes_and_local_lint_reproduction(self) -> None:
        for needle in ("`tsan.yml`", "`soak.yml`", "make test-tsan", "make soak", "Reproducing CI lint locally"):
            with self.subTest(doc="docs/ci.md", needle=needle):
                self.assertIn(needle, self.ci_doc)
        for text, name in ((self.ci_doc, "docs/ci.md"), (self.contributing, "CONTRIBUTING.md")):
            with self.subTest(doc=name):
                self.assertIn("GOTOOLCHAIN=go1.26.1", text)
                self.assertIn("golangci-lint 2.10.1", text)
                self.assertIn(f"--new-from-rev={LINT_BASELINE}", text)


if __name__ == "__main__":
    unittest.main()
