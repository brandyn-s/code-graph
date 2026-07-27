"""Executable contract tests for the deterministic category-6 lane."""

from contextlib import redirect_stderr, redirect_stdout
from io import StringIO
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

import run_battery


VALID_CATEGORY_6_RESPONSES = {
    30: {
        "_metadata": {"provenance": {"data_source": "graph_db"}},
        "projects": [
            {
                "project": "fixture-project",
                "adr_present": False,
                "schema": {
                    "node_labels": [{"label": "Function", "count": 2}],
                    # This is the production key emitted by store.SchemaInfo.
                    "relationship_types": [{"type": "CALLS", "count": 1}],
                },
            }
        ]
    },
    31: {
        "_metadata": {"provenance": {"data_source": "index"}},
        "total": 1,
        "limit": 5,
        "offset": 0,
        "has_more": False,
        "results": [
            {
                "project": "fixture-project",
                "name": "main",
                "qualified_name": "fixture.main",
                "label": "Function",
                "file_path": "main.go",
                "start_line": 1,
                "end_line": 10,
                "in_degree": 0,
                "out_degree": 1,
            },
        ]
    },
    32: {
        "project": "fixture-project",
        "label": "Function",
        "direction": "inbound",
        "edge_type": "CALLS",
        "op": "eq",
        "value": 0,
        "count": 1,
        "examples": [
            {
                "name": "main",
                "qualified_name": "fixture.main",
                "file": "main.go",
                "degree": 0,
            }
        ],
        "exclude_entry_points": False,
    },
    33: {
        "_metadata": {"provenance": {"data_source": "graph_db"}},
        "project": "fixture-project",
        "total_nodes": 2,
        "total_edges": 1,
        "node_labels": [{"label": "Function", "count": 2}],
        "edge_types": [{"type": "CALLS", "count": 1}],
    },
    34: [
        {
            "name": "fixture-project",
            "root_path": "/tmp/fixture",
            "indexed_at": "2026-07-27T00:00:00Z",
            "nodes": 2,
            "edges": 1,
            "db_path": "/tmp/fixture.db",
            "adr_present": False,
            "status": "ready",
            "identity_status": "captured",
            "identity_reason": "",
        }
    ],
    35: {
        "_metadata": {"provenance": {"data_source": "graph_db"}},
        "qualified_name": "fixture.main",
        "name": "main",
        "label": "Function",
        "file_path": "/tmp/fixture/main.go",
        "start_line": 1,
        "end_line": 10,
        "source": "1: package main",
        "callers": 0,
        "callees": 1,
    },
}


# Each payload satisfies the old top-level key check but violates the
# question/tool-specific response contract.
MALFORMED_CATEGORY_6_RESPONSES = {
    30: {
        "_metadata": {"provenance": {"data_source": "graph_db"}},
        "projects": [
            {
                "project": "fixture-project",
                "adr_present": False,
                "schema": {
                    "node_labels": [{"label": "Function", "count": 2}],
                    # The stale benchmark spelling is top-level plausible but
                    # is not the production store.SchemaInfo contract.
                    "edge_types": [{"type": "CALLS", "count": 1}],
                },
            }
        ]
    },
    31: {
        "_metadata": {"provenance": {"data_source": "index"}},
        "total": 6,
        "limit": 5,
        "offset": 0,
        "has_more": True,
        "results": [
            {
                "project": "fixture-project",
                "name": f"function-{index}",
                "qualified_name": f"fixture.function-{index}",
                "label": "Function" if index < 5 else 7,
                "file_path": "main.go",
                "start_line": index + 1,
                "end_line": index + 1,
                "in_degree": 0,
                "out_degree": 0,
            }
            for index in range(6)
        ]
    },
    32: {
        "project": "fixture-project",
        "label": "Function",
        "direction": "inbound",
        "edge_type": "CALLS",
        "op": "eq",
        "value": 0,
        "count": "4",
        "examples": [
            {"name": f"function-{index}", "degree": None}
            for index in range(4)
        ],
        "exclude_entry_points": False,
    },
    33: {
        "_metadata": {"provenance": {"data_source": "graph_db"}},
        "project": "fixture-project",
        "total_nodes": None,
        "total_edges": 1,
        "node_labels": [{"label": "Function", "count": "2"}],
        "edge_types": [{"type": "CALLS", "count": 1}],
    },
    34: [{"name": 42}],
    35: {
        "status": "ambiguous",
        "suggestions": [{"name": "main", "label": "Function"}],
    },
}


def category_6_questions() -> dict:
    return {
        "questions": [
            {
                "id": question_id,
                "category": 6,
                "kind": "schema",
                "targets": ["fixture"],
            }
            for question_id in range(30, 36)
        ]
    }


def schema_result(question: dict, *, signal: bool = False) -> dict:
    return {
        "question_id": question["id"],
        "target_id": "fixture",
        "category": 6,
        "kind": "schema",
        "signal_caught": signal,
        "evidence": "deliberate bad shape" if signal else "shape OK",
        "agent_status": "ok",
        "judge_status": "ok",
    }


class StrictCategory6ContractTests(unittest.TestCase):
    def run_battery(
        self,
        *,
        strict: bool,
        target: str = "fixture",
        completed: set[tuple[int, str]] | None = None,
        failing_question: int | None = None,
        no_skip: bool = True,
    ) -> tuple[int, str]:
        with tempfile.TemporaryDirectory() as temp_dir:
            results_file = Path(temp_dir) / "results.jsonl"
            argv = [
                "run_battery.py",
                "--filter-category",
                "6",
                "--filter-target",
                target,
            ]
            if no_skip:
                argv.append("--no-skip")

            def fake_schema(question: dict, _target: dict) -> dict:
                return schema_result(
                    question,
                    signal=question["id"] == failing_question,
                )

            strict_env = {"AGENT_EFFECTIVENESS_STRICT_CONTRACT": "1"} if strict else {}
            stdout = StringIO()
            stderr = StringIO()
            with (
                mock.patch.object(run_battery, "RESULTS_FILE", results_file),
                mock.patch.object(run_battery, "load_questions", side_effect=category_6_questions),
                mock.patch.object(
                    run_battery,
                    "load_targets",
                    return_value={
                        "fixture": {
                            "id": "fixture",
                            "project_id": "fixture-project",
                        }
                    },
                ),
                mock.patch.object(run_battery, "load_allowlist", return_value=[]),
                mock.patch.object(
                    run_battery,
                    "load_completed",
                    return_value=completed or set(),
                ),
                mock.patch.object(run_battery, "run_schema_question", side_effect=fake_schema),
                mock.patch.object(sys, "argv", argv),
                mock.patch.dict(os.environ, strict_env, clear=False),
                redirect_stdout(stdout),
                redirect_stderr(stderr),
            ):
                if not strict:
                    os.environ.pop("AGENT_EFFECTIVENESS_STRICT_CONTRACT", None)
                exit_code = run_battery.main()
            return exit_code, stdout.getvalue() + stderr.getvalue()

    def test_strict_contract_rejects_zero_selected_checks(self) -> None:
        exit_code, output = self.run_battery(strict=True, target="missing")

        self.assertNotEqual(exit_code, 0)
        self.assertIn("zero selected checks", output)

    def test_strict_contract_rejects_one_bad_shape_out_of_six(self) -> None:
        exit_code, output = self.run_battery(strict=True, failing_question=30)

        self.assertNotEqual(exit_code, 0)
        self.assertIn("1 category-6 signal", output)

    def test_strict_contract_rejects_skipped_checks(self) -> None:
        completed = {(question_id, "fixture") for question_id in range(30, 36)}
        exit_code, output = self.run_battery(
            strict=True,
            completed=completed,
            no_skip=False,
        )

        self.assertNotEqual(exit_code, 0)
        self.assertIn("6 skipped checks", output)
        self.assertIn("zero executed checks", output)

    def test_default_mode_keeps_existing_sixty_percent_threshold(self) -> None:
        exit_code, _ = self.run_battery(strict=False, failing_question=30)

        self.assertEqual(exit_code, 0)


class Category6ResponseContractTests(unittest.TestCase):
    @staticmethod
    def question(question_id: int) -> dict:
        data = run_battery.load_questions()
        question = next(q for q in data["questions"] if q["id"] == question_id)
        return {**question, "targets": ["fixture"]}

    def run_strict_response(
        self,
        question_id: int,
        response: object,
        *,
        returncode: int = 0,
        stderr_text: str = "",
    ) -> tuple[int, str]:
        question = self.question(question_id)
        with tempfile.TemporaryDirectory() as temp_dir:
            stdout = StringIO()
            stderr = StringIO()
            completed_process = subprocess.CompletedProcess(
                args=["codebase-memory-mcp", "cli"],
                returncode=returncode,
                stdout=json.dumps(response).encode(),
                stderr=stderr_text.encode(),
            )
            argv = [
                "run_battery.py",
                "--filter-category",
                "6",
                "--filter-question",
                str(question_id),
                "--filter-target",
                "fixture",
                "--strict-contract",
                "--no-skip",
            ]
            with (
                mock.patch.object(
                    run_battery,
                    "RESULTS_FILE",
                    Path(temp_dir) / "results.jsonl",
                ),
                mock.patch.object(
                    run_battery,
                    "load_questions",
                    return_value={"questions": [question]},
                ),
                mock.patch.object(
                    run_battery,
                    "load_targets",
                    return_value={
                        "fixture": {
                            "id": "fixture",
                            "project_id": "fixture-project",
                        }
                    },
                ),
                mock.patch.object(run_battery, "load_allowlist", return_value=[]),
                mock.patch.object(
                    run_battery.subprocess,
                    "run",
                    return_value=completed_process,
                ),
                mock.patch.object(sys, "argv", argv),
                redirect_stdout(stdout),
                redirect_stderr(stderr),
            ):
                exit_code = run_battery.main()
        return exit_code, stdout.getvalue() + stderr.getvalue()

    def test_known_good_production_shapes_pass(self) -> None:
        for question_id, response in VALID_CATEGORY_6_RESPONSES.items():
            with self.subTest(question_id=question_id):
                exit_code, output = self.run_strict_response(question_id, response)

                self.assertEqual(exit_code, 0, output)

    def test_each_contract_rejects_a_top_level_plausible_malformed_response(self) -> None:
        for question_id, response in MALFORMED_CATEGORY_6_RESPONSES.items():
            with self.subTest(question_id=question_id):
                exit_code, output = self.run_strict_response(question_id, response)

                self.assertNotEqual(exit_code, 0, output)
                self.assertIn("1 category-6 signal", output)

    def test_q30_criterion_and_judge_track_runtime_relationship_types_key(self) -> None:
        question = self.question(30)
        stale_shape = {
            **VALID_CATEGORY_6_RESPONSES[30],
            "projects": [
                {
                    "project": "fixture-project",
                    "adr_present": False,
                    "schema": {
                        "node_labels": [{"label": "Function", "count": 2}],
                        "edge_types": [{"type": "CALLS", "count": 1}],
                    },
                }
            ],
        }

        self.assertIn("schema.relationship_types", question["scoring_criteria"])
        self.assertNotIn("schema.edge_types", question["scoring_criteria"])
        judged = run_battery.judge_output_shape(question, json.dumps(stale_shape))
        self.assertTrue(judged["signal_caught"], judged["evidence"])

    def test_cli_invocation_preserves_nonzero_exit_and_stderr(self) -> None:
        response = VALID_CATEGORY_6_RESPONSES[31]
        completed_process = subprocess.CompletedProcess(
            args=["codebase-memory-mcp", "cli"],
            returncode=7,
            stdout=json.dumps(response).encode(),
            stderr=b"fixture subprocess failed",
        )
        with mock.patch.object(
            run_battery.subprocess,
            "run",
            return_value=completed_process,
        ):
            result = run_battery.invoke_tool_cli("search_graph", {"limit": 5})

        self.assertEqual(result.returncode, 7)
        self.assertEqual(result.stdout, json.dumps(response))
        self.assertEqual(result.stderr, "fixture subprocess failed")

    def test_nonzero_cli_exit_marks_schema_row_failed_despite_valid_stdout(self) -> None:
        question = self.question(31)
        completed_process = subprocess.CompletedProcess(
            args=["codebase-memory-mcp", "cli"],
            returncode=7,
            stdout=json.dumps(VALID_CATEGORY_6_RESPONSES[31]).encode(),
            stderr=b"fixture subprocess failed",
        )
        with mock.patch.object(
            run_battery.subprocess,
            "run",
            return_value=completed_process,
        ):
            row = run_battery.run_schema_question(
                question,
                {"id": "fixture", "project_id": "fixture-project"},
            )

        self.assertNotEqual(row["agent_status"], "ok")
        self.assertTrue(row["signal_caught"])
        self.assertEqual(row["tool_exit_code"], 7)
        self.assertEqual(row["tool_stderr"], "fixture subprocess failed")

    def test_strict_contract_rejects_nonzero_cli_exit_with_valid_stdout(self) -> None:
        exit_code, output = self.run_strict_response(
            31,
            VALID_CATEGORY_6_RESPONSES[31],
            returncode=7,
            stderr_text="fixture subprocess failed",
        )

        self.assertNotEqual(exit_code, 0, output)
        self.assertIn("1 runner failures", output)


if __name__ == "__main__":
    unittest.main()
