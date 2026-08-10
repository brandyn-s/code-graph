"""Contract tests for the agent tool-schema generator."""

import importlib.util
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import generate_schemas


def load_generated_schemas():
    spec = importlib.util.spec_from_file_location("generated_tool_schemas", generate_schemas.OUT)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot import generated schemas from {generate_schemas.OUT}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module.TOOL_SCHEMAS


class GenerateSchemasContractTests(unittest.TestCase):
    def test_export_preserves_distinct_trace_and_graph_schema_tools(self) -> None:
        runtime = generate_schemas.extract_handlers()
        generated = load_generated_schemas()

        for schemas in (runtime, generated):
            trace = schemas["trace_call_path"]
            self.assertTrue(
                trace["description"].startswith("Trace the call path of a function")
            )
            self.assertLessEqual(
                {
                    "function_name",
                    "edge_types",
                    "min_confidence",
                },
                trace["input_schema"]["properties"].keys(),
            )
            confidence = trace["input_schema"]["properties"]["min_confidence"]
            self.assertEqual(confidence["minimum"], 0)
            self.assertEqual(confidence["maximum"], 1)

            graph_schema = schemas["get_graph_schema"]
            self.assertTrue(
                graph_schema["description"].startswith(
                    "Return the schema of the indexed code graph"
                )
            )
            self.assertEqual(
                set(graph_schema["input_schema"]["properties"]),
                {"project"},
            )

    def test_runtime_and_generated_name_sets_are_the_same_38_tools(self) -> None:
        runtime = generate_schemas.extract_handlers()
        generated = load_generated_schemas()

        self.assertEqual(len(runtime), 38)
        self.assertEqual(len(generated), 38)
        self.assertEqual(set(runtime), set(generated))

    def test_search_code_pagination_bounds_match_runtime(self) -> None:
        runtime = generate_schemas.extract_handlers()
        generated = load_generated_schemas()

        for schemas in (runtime, generated):
            properties = schemas["search_code"]["input_schema"]["properties"]
            self.assertEqual(properties["max_results"]["minimum"], 1)
            self.assertEqual(properties["max_results"]["maximum"], 1000)
            self.assertEqual(properties["offset"]["minimum"], 0)
            self.assertEqual(properties["offset"]["maximum"], 1_000_000)

    def test_check_only_rejects_a_deliberately_mutated_generated_file(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            mutated = Path(temp_dir) / "_generated_tool_schemas.py"
            contents = generate_schemas.OUT.read_text(encoding="utf-8")
            mutated.write_text(
                contents.replace('"trace_call_path"', '"mutated_trace_call_path"', 1),
                encoding="utf-8",
            )

            result = subprocess.run(
                [
                    sys.executable,
                    generate_schemas.__file__,
                    "--check-only",
                    "--out",
                    str(mutated),
                ],
                cwd=generate_schemas.REPO,
                check=False,
                capture_output=True,
                text=True,
            )

        self.assertEqual(result.returncode, 1)
        self.assertIn("out of sync with handler source", result.stderr)


if __name__ == "__main__":
    unittest.main()
