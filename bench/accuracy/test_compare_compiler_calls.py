"""Contract tests for the independent compiler-edge comparator."""

from __future__ import annotations

import importlib.util
import json
import sqlite3
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "bench" / "accuracy" / "compare_compiler_calls.py"


def load_comparator():
    spec = importlib.util.spec_from_file_location("compare_compiler_calls", SCRIPT)
    if spec is None or spec.loader is None:
        raise AssertionError("could not load comparator")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class CompilerCallsComparatorTests(unittest.TestCase):
    def test_exact_coordinate_metrics_and_artifact_binding(self):
        comparator = load_comparator()
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            oracle_path = root / "oracle.json"
            oracle_path.write_text(
                json.dumps(
                    {
                        "schema_version": 1,
                        "oracle": "go-ssa-rta-all-source-roots-v1",
                        "edges": [
                            {
                                "caller": {"file": "main.go", "line": 3},
                                "callee": {"file": "main.go", "line": 7},
                                "dynamic": False,
                            },
                            {
                                "caller": {"file": "main.go", "line": 7},
                                "callee": {"file": "helper.go", "line": 2},
                                "dynamic": False,
                            },
                        ],
                    }
                ),
                encoding="utf-8",
            )
            artifact = root / "index.scip"
            artifact.write_bytes(b"compiler artifact")
            digest = comparator.sha256_file(artifact)
            database = root / "graph.db"
            connection = sqlite3.connect(database)
            connection.executescript(
                """
                CREATE TABLE nodes (
                    id INTEGER PRIMARY KEY,
                    project TEXT,
                    file_path TEXT,
                    start_line INTEGER
                );
                CREATE TABLE edges (
                    project TEXT,
                    source_id INTEGER,
                    target_id INTEGER,
                    type TEXT,
                    properties TEXT
                );
                INSERT INTO nodes VALUES (1, 'fixture', 'main.go', 3);
                INSERT INTO nodes VALUES (2, 'fixture', 'main.go', 7);
                INSERT INTO nodes VALUES (3, 'fixture', 'helper.go', 2);
                """
            )
            properties = json.dumps(
                {
                    "resolver_rule": "scip-ingest",
                    "resolution_artifact_sha256": digest,
                }
            )
            connection.executemany(
                "INSERT INTO edges VALUES ('fixture', ?, ?, 'CALLS', ?)",
                [(1, 2, properties), (2, 3, properties)],
            )
            connection.commit()
            connection.close()

            oracle = comparator.load_oracle(oracle_path)
            observed = comparator.load_compiler_edges(
                database,
                "fixture",
                digest,
            )
            result = comparator.metrics(oracle, observed)

            self.assertEqual(result["true_positive"], 2)
            self.assertEqual(result["false_positive"], 0)
            self.assertEqual(result["false_negative"], 0)
            self.assertEqual(result["f1"], 1.0)
            with self.assertRaisesRegex(ValueError, "not bound"):
                comparator.load_compiler_edges(database, "fixture", "0" * 64)


if __name__ == "__main__":
    unittest.main()
