import re
import unittest
from pathlib import Path


REPOSITORY_ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = REPOSITORY_ROOT / ".github" / "workflows" / "core-ci.yml"
DRY_RUN_WORKFLOW = REPOSITORY_ROOT / ".github" / "workflows" / "dry-run.yml"
RELEASE_WORKFLOW = REPOSITORY_ROOT / ".github" / "workflows" / "release.yml"
RELEASE_LINT_ACTION = (
    REPOSITORY_ROOT / ".github" / "actions" / "release-lint" / "action.yml"
)
AGENT_EFFECTIVENESS_WORKFLOW = (
    REPOSITORY_ROOT / ".github" / "workflows" / "agent-effectiveness.yml"
)


def job_block(workflow: str, name: str) -> str:
    match = re.search(rf"^  {re.escape(name)}:\s*$", workflow, re.MULTILINE)
    if match is None:
        return ""
    next_job = re.search(r"^  [a-zA-Z0-9_-]+:\s*$", workflow[match.end():], re.MULTILINE)
    end = len(workflow) if next_job is None else match.end() + next_job.start()
    return workflow[match.start():end]


class CoreCIWorkflowContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.workflow = WORKFLOW.read_text(encoding="utf-8") if WORKFLOW.exists() else ""

    def test_workflow_is_always_present_on_prs_and_main_pushes(self) -> None:
        self.assertTrue(WORKFLOW.is_file(), "core-ci.yml must always exist")
        self.assertIn("\non:\n  pull_request:\n  push:\n    branches: [main]\n", self.workflow)
        self.assertNotIn("\n    paths:", self.workflow)
        self.assertNotIn("\n    paths-ignore:", self.workflow)

    def test_required_jobs_run_core_and_offline_contract_commands(self) -> None:
        core = job_block(self.workflow, "core")
        agent_contract = job_block(self.workflow, "agent-contract")

        self.assertIn("go test ./...", core)
        self.assertIn("CGO_ENABLED=1 go build", core)
        self.assertIn("bash scripts/smoke-test.sh", core)

        self.assertIn("generate_schemas.py --check-only", agent_contract)
        self.assertIn("bench/accuracy/synthetic/go-minimal", agent_contract)
        self.assertIn("--filter-category 6", agent_contract)
        self.assertIn("--strict-contract", agent_contract)
        self.assertIn("--no-skip", agent_contract)
        self.assertIn(
            "AGENT_EFFECTIVENESS_RESULTS_FILE: "
            "${{ runner.temp }}/agent-effectiveness-results.jsonl",
            agent_contract,
        )
        for test_file in (
            "test_generate_schemas.py",
            "test_run_battery_offline.py",
            "test_run_battery_strict.py",
        ):
            with self.subTest(test_file=test_file):
                self.assertIn(
                    f"python3 bench/research/agent-effectiveness/{test_file}",
                    agent_contract,
                )
        self.assertNotIn(
            ": > bench/research/agent-effectiveness/results.jsonl",
            agent_contract,
        )
        for forbidden in (
            "secrets.",
            "ANTHROPIC",
            "VOYAGE",
            "git clone",
            "continue-on-error:",
            "|| true",
        ):
            with self.subTest(forbidden=forbidden):
                self.assertNotIn(forbidden, core + agent_contract)

    def test_pr_dry_run_and_release_share_one_release_lint_contract(self) -> None:
        self.assertTrue(
            RELEASE_LINT_ACTION.is_file(),
            "the exact release lint must have one repository-owned implementation",
        )
        action = RELEASE_LINT_ACTION.read_text(encoding="utf-8")
        dry_run = DRY_RUN_WORKFLOW.read_text(encoding="utf-8")
        release = RELEASE_WORKFLOW.read_text(encoding="utf-8")
        core = job_block(self.workflow, "core")
        dry_run_lint = job_block(dry_run, "lint")
        release_lint = job_block(release, "lint")

        for surface, block in (
            ("required PR core job", core),
            ("manual dry run", dry_run_lint),
            ("release", release_lint),
        ):
            with self.subTest(surface=surface):
                self.assertIn("fetch-depth: 0", block)
                self.assertEqual(
                    block.count("uses: ./.github/actions/release-lint"),
                    1,
                )

        baseline = "832bede03d6118827919fc8727f3c17854047d06"
        self.assertEqual(action.count(baseline), 1)
        self.assertNotIn(baseline, self.workflow + dry_run + release)
        self.assertIn('git merge-base --is-ancestor "$baseline" "$GITHUB_SHA"', action)
        self.assertIn("version: v2.10", action)
        self.assertIn("--new-from-rev=${{ inputs.baseline }}", action)

    def test_core_declares_release_lint_network_endpoints(self) -> None:
        core = job_block(self.workflow, "core")

        self.assertIn("allowed-endpoints: >", core)
        self.assertIn("raw.githubusercontent.com:443", core)
        self.assertIn("golangci-lint.run:443", core)

    def test_merge_gate_fails_closed_over_both_required_jobs(self) -> None:
        merge_gate = job_block(self.workflow, "merge-gate")

        self.assertIn("needs: [core, agent-contract]", merge_gate)
        self.assertIn("if: always()", merge_gate)
        self.assertIn("${{ needs.core.result }}", merge_gate)
        self.assertIn("${{ needs.agent-contract.result }}", merge_gate)
        self.assertIn('if [ "$CORE_RESULT" != "success" ] ||', merge_gate)
        self.assertIn("exit 1", merge_gate)

    def test_path_filtered_category_6_step_does_not_swallow_failures(self) -> None:
        workflow = AGENT_EFFECTIVENESS_WORKFLOW.read_text(encoding="utf-8")
        category_6_start = workflow.index("- name: Run category 6")
        category_1_start = workflow.index("- name: Run category 1", category_6_start)
        category_6_step = workflow[category_6_start:category_1_start]

        self.assertNotIn("|| true", category_6_step)


if __name__ == "__main__":
    unittest.main()
