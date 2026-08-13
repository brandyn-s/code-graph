#!/usr/bin/env python3
"""Compare TypeScript graph IMPORTS with a compiler-resolved file-pair oracle."""

from __future__ import annotations

import argparse
import json
import sqlite3
from pathlib import Path


ORACLE = "typescript-compiler-api-module-resolution-v1"


def load_oracle(path: Path) -> tuple[set[str], set[tuple[str, str]], dict]:
    document = json.loads(path.read_text(encoding="utf-8"))
    if (
        document.get("schema_version") != 1
        or document.get("imports_oracle") != ORACLE
        or not isinstance(document.get("project_files"), list)
        or not isinstance(document.get("imports"), list)
    ):
        raise ValueError("oracle has an unsupported IMPORTS contract")
    files = set(document["project_files"])
    if not files or any(not isinstance(file, str) or not file for file in files):
        raise ValueError("oracle project_files must contain non-empty paths")
    edges: set[tuple[str, str]] = set()
    for entry in document["imports"]:
        source = entry.get("source") if isinstance(entry, dict) else None
        target = entry.get("target") if isinstance(entry, dict) else None
        pair = (
            source.get("file") if isinstance(source, dict) else None,
            target.get("file") if isinstance(target, dict) else None,
        )
        if not all(isinstance(value, str) and value for value in pair):
            raise ValueError("oracle import lacks source/target file")
        if pair[0] not in files or pair[1] not in files:
            raise ValueError("oracle import escapes project_files scope")
        edges.add(pair)
    return files, edges, document


def load_observed(
    database: Path,
    project: str,
    source_scope: set[str],
) -> set[tuple[str, str]]:
    connection = sqlite3.connect(f"file:{database.resolve()}?mode=ro", uri=True)
    try:
        rows = connection.execute(
            """
            SELECT source.file_path, target.file_path
            FROM edges AS edge
            JOIN nodes AS source ON source.id = edge.source_id
            JOIN nodes AS target ON target.id = edge.target_id
            WHERE edge.project = ? AND edge.type = 'IMPORTS'
              AND source.label = 'Module' AND target.label = 'Module'
            """,
            (project,),
        ).fetchall()
    finally:
        connection.close()
    # Scope by compiler-owned source files. A graph edge from an in-scope source
    # to any other project file remains visible as a false positive; files the
    # tsconfig never compiled are neither positives nor negatives.
    return {(source, target) for source, target in rows if source in source_scope}


def calculate(
    expected: set[tuple[str, str]],
    observed: set[tuple[str, str]],
) -> dict[str, float | int]:
    true_positive = len(expected & observed)
    false_positive = len(observed - expected)
    false_negative = len(expected - observed)
    precision = true_positive / max(true_positive + false_positive, 1)
    recall = true_positive / max(true_positive + false_negative, 1)
    f1 = 2 * precision * recall / max(precision + recall, 1e-15)
    return {
        "oracle_edges": len(expected),
        "observed_edges": len(observed),
        "true_positive": true_positive,
        "false_positive": false_positive,
        "false_negative": false_negative,
        "precision": precision,
        "recall": recall,
        "f1": f1,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--oracle", required=True, type=Path)
    parser.add_argument("--database", required=True, type=Path)
    parser.add_argument("--project", required=True)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()

    files, expected, document = load_oracle(args.oracle)
    observed = load_observed(args.database, args.project, files)
    result = {
        "schema_version": 1,
        "oracle": ORACLE,
        "oracle_scope": document["imports_oracle_scope"],
        "typescript_version": document["typescript_version"],
        "tsconfig_sha256": document["tsconfig_sha256"],
        "project_files": len(files),
        **calculate(expected, observed),
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(
        json.dumps(result, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    print(json.dumps(result, sort_keys=True, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
