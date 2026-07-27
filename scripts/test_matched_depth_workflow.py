from __future__ import annotations

import re
import unittest
from pathlib import Path


REPOSITORY_ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = REPOSITORY_ROOT / ".github" / "workflows" / "matched-depth.yml"


def job_block(workflow: str, name: str) -> str:
    match = re.search(rf"^  {re.escape(name)}:\s*$", workflow, re.MULTILINE)
    if match is None:
        return ""
    next_job = re.search(
        r"^  [a-zA-Z0-9_-]+:\s*$",
        workflow[match.end() :],
        re.MULTILINE,
    )
    end = len(workflow) if next_job is None else match.end() + next_job.start()
    return workflow[match.start() : end]


def step_block(job: str, name: str) -> str:
    marker = f"      - name: {name}\n"
    start = job.find(marker)
    if start < 0:
        return ""
    next_step = job.find("\n      - ", start + len(marker))
    return job[start:] if next_step < 0 else job[start:next_step]


class MatchedDepthWorkflowContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.workflow = WORKFLOW.read_text(encoding="utf-8") if WORKFLOW.exists() else ""

    def test_manual_workflow_is_retrieval_only_vs_graph(self) -> None:
        self.assertTrue(WORKFLOW.is_file())
        self.assertIn("name: matched-depth-retrieval-only-vs-graph", self.workflow)
        self.assertIn("\non:\n  workflow_dispatch:\n", self.workflow)
        self.assertNotIn("\n  pull_request:", self.workflow)
        self.assertNotIn("\n  push:", self.workflow)
        self.assertNotIn("SweRank", self.workflow)

    def test_preflight_fails_closed_with_zero_cost_diagnostic(self) -> None:
        prepare = job_block(self.workflow, "prepare")
        self.assertIn("max_total_usd", prepare)
        self.assertIn("ANTHROPIC_API_KEY", prepare)
        self.assertIn("VOYAGE_API_KEY", prepare)
        self.assertIn("CODE_INTEL_COMPONENT_TOKEN", prepare)
        self.assertIn('"missing_component_credential"', prepare)
        self.assertIn('"hard_provider_cost_bound_unavailable"', prepare)
        self.assertIn('"status": "not_evaluated"', prepare)
        self.assertIn('"total_cost_usd": 0.0', prepare)
        self.assertIn("exit 1", prepare)
        self.assertIn("if: always()", prepare)

    def test_shard_budgets_use_exact_decimal_allocation(self) -> None:
        prepare = job_block(self.workflow, "prepare")
        matrix = step_block(
            prepare,
            "Build repository matrix and bounded arm budgets",
        )
        self.assertIn("allocate_decimal_budget", matrix)
        self.assertIn("Decimal", matrix)
        self.assertNotIn("float(", matrix)
        self.assertNotIn("round(", matrix)
        retrieval_run = step_block(
            job_block(self.workflow, "retrieval"),
            "Run retrieval-only shard with per-case checkpoints",
        )
        self.assertIn(
            '--voyage-ceiling-usd "${{ matrix.retrieval_budget_usd }}"',
            retrieval_run,
        )
        self.assertIn(
            '--arm-ceiling-usd "${{ inputs.retrieval_max_usd }}"',
            retrieval_run,
        )
        self.assertIn(
            '--total-ceiling-usd "${{ inputs.max_total_usd }}"',
            retrieval_run,
        )
        self.assertIn(
            "--provider-operation-bound-policy "
            "provider-enforced-per-operation-usd-required-v1",
            retrieval_run,
        )

    def test_repository_shards_always_upload_partial_artifacts(self) -> None:
        for job_name in ("graph", "retrieval"):
            block = job_block(self.workflow, job_name)
            with self.subTest(job=job_name):
                self.assertIn("matrix.repository", block)
                self.assertIn("max-parallel: 4", block)
                self.assertIn("if: always()", block)
                self.assertIn("actions/upload-artifact@", block)

    def test_cross_run_resume_is_same_repository_and_contract_checked(self) -> None:
        self.assertIn("resume_run_id:", self.workflow)
        self.assertIn("actions: read", self.workflow)
        for job_name, artifact_prefix, resume_flag in (
            ("graph", "matched-depth-graph-", "--resume-per-case-json"),
            ("retrieval", "matched-depth-retrieval-", "--resume-from"),
        ):
            block = job_block(self.workflow, job_name)
            with self.subTest(job=job_name):
                self.assertIn("actions/download-artifact@", block)
                self.assertIn("run-id: ${{ inputs.resume_run_id }}", block)
                self.assertIn("github-token: ${{ github.token }}", block)
                self.assertIn(artifact_prefix, block)
                self.assertIn(resume_flag, block)
        graph = job_block(self.workflow, "graph")
        self.assertIn("--checkpoint-contract", graph)
        graph_run = step_block(
            graph,
            "Run graph shard with per-case checkpoints",
        )
        for bound_argument in (
            '--canonical-pin "$PIN"',
            '--repository "${{ matrix.repository }}"',
            '--graph-sha "$GITHUB_SHA"',
            "--score-depth 10",
        ):
            self.assertIn(bound_argument, graph_run)
        normalize = step_block(graph, "Normalize graph ranks and provenance")
        self.assertIn("--iterations 2", normalize)
        self.assertIn(
            '--graph-budget-usd "${{ matrix.graph_budget_usd }}"',
            normalize,
        )
        for job, step_name in (
            (graph, "Run graph shard with per-case checkpoints"),
            (
                job_block(self.workflow, "retrieval"),
                "Run retrieval-only shard with per-case checkpoints",
            ),
        ):
            run_step = step_block(job, step_name)
            shell_body = run_step.split("        run: |", 1)[-1]
            self.assertIn("$RESUME_RUN_ID", shell_body)
            self.assertNotIn("${{ inputs.resume_run_id }}", shell_body)

    def test_pins_public_dataset_and_code_search_wheel(self) -> None:
        self.assertIn("c44cf3b74e07ca642cec841b471a9939907c12a7", self.workflow)
        self.assertIn(
            "8df0833c2c1276c5837aab923d489ab97d7654529abe759d0f59242c4978a662",
            self.workflow,
        )
        self.assertIn("redacted_code_search-0.2.1-py3-none-any.whl", self.workflow)
        self.assertIn(
            "567d4caabdd3b5446bcaa789afc7104fb8cce142ff69d7fc8f1294398532e7e9",
            self.workflow,
        )

    def test_reducer_requires_exact_coverage_without_failure_swallowing(self) -> None:
        reduce_block = job_block(self.workflow, "reduce")
        self.assertIn("pilot_compare.py reduce", reduce_block)
        self.assertIn("--n-boot 10000", reduce_block)
        self.assertIn("if: always()", reduce_block)
        for forbidden in ("continue-on-error:", "|| true"):
            self.assertNotIn(forbidden, self.workflow)

    def test_downloads_do_not_retry_deterministic_errors(self) -> None:
        self.assertNotIn("--retry-all-errors", self.workflow)
        self.assertIn("--retry 3 --retry-delay 1 --retry-max-time 60", self.workflow)

    def test_model_credentials_are_scoped_to_paid_execution_steps(self) -> None:
        graph = job_block(self.workflow, "graph")
        retrieval = job_block(self.workflow, "retrieval")
        for job in (graph, retrieval):
            job_header = job.split("\n    steps:", 1)[0]
            self.assertNotIn("ANTHROPIC_API_KEY", job_header)
            self.assertNotIn("VOYAGE_API_KEY", job_header)

        graph_run = step_block(graph, "Run graph shard with per-case checkpoints")
        retrieval_run = step_block(
            retrieval,
            "Run retrieval-only shard with per-case checkpoints",
        )
        for paid_step in (graph_run, retrieval_run):
            self.assertIn("ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}", paid_step)
            self.assertIn("VOYAGE_API_KEY: ${{ secrets.VOYAGE_API_KEY }}", paid_step)

        self.assertNotIn(
            "ANTHROPIC_API_KEY",
            step_block(graph, "Install graph harness dependencies"),
        )
        self.assertNotIn(
            "VOYAGE_API_KEY",
            step_block(retrieval, "Download exact private code-search wheel"),
        )

    def test_component_token_is_scoped_to_private_asset_download(self) -> None:
        retrieval = job_block(self.workflow, "retrieval")
        job_header = retrieval.split("\n    steps:", 1)[0]
        download = step_block(
            retrieval,
            "Download exact private code-search wheel",
        )
        install = step_block(
            retrieval,
            "Verify and install exact code-search wheel",
        )

        self.assertNotIn("CODE_INTEL_COMPONENT_TOKEN", job_header)
        self.assertIn(
            "GH_TOKEN: ${{ secrets.CODE_INTEL_COMPONENT_TOKEN }}",
            download,
        )
        self.assertIn("gh release download", download)
        self.assertNotIn("pip install", download)
        self.assertNotIn("CODE_INTEL_COMPONENT_TOKEN", install)
        self.assertNotIn("GH_TOKEN", install)
        self.assertIn("pip install", install)


if __name__ == "__main__":
    unittest.main()
