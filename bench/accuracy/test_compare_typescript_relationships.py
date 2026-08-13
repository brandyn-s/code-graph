"""Contract tests for TypeScript type-relationship measurement."""

from __future__ import annotations

import importlib.util
import hashlib
import json
import sqlite3
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "bench" / "accuracy" / "compare_typescript_relationships.py"


def load_comparator():
    spec = importlib.util.spec_from_file_location(
        "compare_typescript_relationships", SCRIPT
    )
    if spec is None or spec.loader is None:
        raise AssertionError("could not load comparator")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class TypeScriptRelationshipComparatorTests(unittest.TestCase):
    def test_rejects_oracle_manifest_not_derived_from_declared_file_hashes(self):
        comparator = load_comparator()
        with tempfile.TemporaryDirectory() as temporary:
            oracle_path = Path(temporary) / "oracle.json"
            bound_hashes = {"src/main.ts": "a" * 64}
            oracle_path.write_text(
                json.dumps(
                    {
                        "schema_version": 1,
                        "type_relationships_oracle": (
                            "typescript-compiler-api-type-relationships-v1"
                        ),
                        "type_relationships_oracle_scope": (
                            "declared_project_local_extends_and_implements"
                        ),
                        "oracle_implementation_sha256": "b" * 64,
                        "typescript_version": "5.9.3",
                        "tsconfig_sha256": "c" * 64,
                        "project_files": ["src/main.ts"],
                        "project_file_sha256": {"src/main.ts": "d" * 64},
                        "project_manifest_sha256": comparator.manifest_sha256(
                            bound_hashes
                        ),
                        "type_relationships": [],
                    }
                ),
                encoding="utf-8",
            )

            with self.assertRaisesRegex(ValueError, "manifest"):
                comparator.load_oracle(oracle_path)

    def test_exact_metrics_preserve_clause_kind_and_confidence_band(self):
        comparator = load_comparator()
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            revision = "1" * 40
            (root / "src").mkdir()
            (root / "src" / "base.ts").write_text("class Base {}\n", encoding="utf-8")
            (root / "src" / "main.ts").write_text("class Child {}\n", encoding="utf-8")
            file_hashes = {
                file: hashlib.sha256((root / file).read_bytes()).hexdigest()
                for file in ("src/base.ts", "src/main.ts")
            }
            oracle_path = root / "oracle.json"
            oracle_path.write_text(
                json.dumps(
                    {
                        "schema_version": 1,
                        "type_relationships_oracle": (
                            "typescript-compiler-api-type-relationships-v1"
                        ),
                        "type_relationships_oracle_scope": (
                            "declared_project_local_extends_and_implements"
                        ),
                        "oracle_implementation_sha256": "c" * 64,
                        "typescript_version": "5.9.3",
                        "tsconfig_sha256": "d" * 64,
                        "project_files": sorted(file_hashes),
                        "project_file_sha256": file_hashes,
                        "project_manifest_sha256": comparator.manifest_sha256(
                            file_hashes
                        ),
                        "type_relationships": [
                            {
                                "kind": "extends",
                                "source": {
                                    "file": "src/main.ts",
                                    "line": 3,
                                    "name": "Child",
                                },
                                "target": {
                                    "file": "src/base.ts",
                                    "line": 1,
                                    "name": "Base",
                                },
                            },
                            {
                                "kind": "implements",
                                "source": {
                                    "file": "src/main.ts",
                                    "line": 3,
                                    "name": "Child",
                                },
                                "target": {
                                    "file": "src/base.ts",
                                    "line": 5,
                                    "name": "Contract",
                                },
                            },
                        ],
                    }
                ),
                encoding="utf-8",
            )
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
                CREATE TABLE file_hashes (
                    project TEXT,
                    rel_path TEXT,
                    sha256 TEXT
                );
                CREATE TABLE index_identity (
                    project TEXT,
                    source_revision TEXT,
                    identity_status TEXT
                );
                CREATE TABLE projects (
                    name TEXT,
                    root_path TEXT
                );
                INSERT INTO nodes VALUES (1, 'fixture', 'src/main.ts', 3);
                INSERT INTO nodes VALUES (2, 'fixture', 'src/base.ts', 1);
                INSERT INTO nodes VALUES (3, 'fixture', 'src/base.ts', 5);
                INSERT INTO nodes VALUES (4, 'fixture', 'src/base.ts', 9);
                """
            )
            connection.executemany(
                "INSERT INTO file_hashes VALUES ('fixture', ?, ?)",
                [(file, digest[:16]) for file, digest in sorted(file_hashes.items())],
            )
            connection.execute(
                "INSERT INTO projects VALUES ('fixture', ?)",
                (str(root.resolve()),),
            )
            connection.execute(
                "INSERT INTO index_identity VALUES ('fixture', ?, 'captured')",
                (revision,),
            )
            connection.executemany(
                "INSERT INTO edges VALUES ('fixture', 1, ?, ?, ?)",
                [
                    (2, "INHERITS", json.dumps({"confidence_tier": "INFERRED"})),
                    (3, "IMPLEMENTS", json.dumps({"confidence_tier": "INFERRED"})),
                    (4, "IMPLEMENTS", json.dumps({"confidence_tier": "AMBIGUOUS"})),
                ],
            )
            connection.commit()
            connection.close()

            oracle = comparator.load_oracle(oracle_path)
            observed = comparator.load_observed(
                database,
                "fixture",
                root,
                oracle.project_file_sha256,
                source_revision=revision,
                project_manifest_sha256=oracle.project_manifest_sha256,
            )
            result = comparator.build_metrics(oracle, observed)

            self.assertEqual(
                result["operating_points"]["all_bands"],
                {
                    "oracle_edges": 2,
                    "observed_edges": 3,
                    "true_positive": 2,
                    "false_positive": 1,
                    "false_negative": 0,
                    "precision": 2 / 3,
                    "recall": 1.0,
                    "f1": 0.8,
                },
            )
            self.assertEqual(
                result["operating_points"]["non_ambiguous"]["false_positive"],
                0,
            )
            self.assertEqual(
                result["by_kind"]["extends"]["all_bands"]["true_positive"],
                1,
            )
            self.assertEqual(
                result["by_kind"]["implements"]["all_bands"]["true_positive"],
                1,
            )


if __name__ == "__main__":
    unittest.main()
