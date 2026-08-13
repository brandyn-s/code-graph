#!/usr/bin/env python3
"""Compare TypeScript graph type/method relationships with a compiler oracle."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import sqlite3
from pathlib import Path
from typing import NamedTuple


TYPE_ORACLE = "typescript-compiler-api-type-relationships-v1"
TYPE_ORACLE_SCOPE = "declared_project_local_extends_and_implements"
METHOD_ORACLE = "typescript-compiler-api-method-relationships-v1"
METHOD_ORACLE_SCOPE = "direct_declared_project_local_overrides_and_implements"
KINDS = {
    "type_extends": "INHERITS",
    "type_implements": "IMPLEMENTS",
    "method_overrides": "OVERRIDE",
    "method_implements": "OVERRIDE",
}


class Relationship(NamedTuple):
    kind: str
    source_file: str
    source_line: int
    target_file: str
    target_line: int


class ObservedRelationship(NamedTuple):
    relationship: Relationship
    confidence_tier: str


class OracleDocument(NamedTuple):
    oracle: str
    oracle_scope: str
    method_oracle: str
    method_oracle_scope: str
    oracle_implementation_sha256: str
    typescript_version: str
    tsconfig_sha256: str
    project_files: frozenset[str]
    project_file_sha256: dict[str, str]
    project_manifest_sha256: str
    relationships: frozenset[Relationship]


def canonical_json(value: object) -> bytes:
    return json.dumps(
        value,
        sort_keys=True,
        separators=(",", ":"),
        ensure_ascii=False,
    ).encode("utf-8")


def manifest_sha256(file_hashes: dict[str, str]) -> str:
    return hashlib.sha256(canonical_json(file_hashes)).hexdigest()


def same_file(left: Path, right: Path) -> bool:
    try:
        return os.path.samefile(left, right)
    except OSError:
        return False


def _digest(value: object, field: str) -> str:
    if (
        not isinstance(value, str)
        or len(value) != 64
        or any(character not in "0123456789abcdef" for character in value)
    ):
        raise ValueError(f"oracle {field} must be lowercase SHA-256")
    return value


def _endpoint(entry: object, field: str, project_files: frozenset[str]) -> tuple[str, int]:
    endpoint = entry.get(field) if isinstance(entry, dict) else None
    file = endpoint.get("file") if isinstance(endpoint, dict) else None
    line = endpoint.get("line") if isinstance(endpoint, dict) else None
    if (
        not isinstance(file, str)
        or not file
        or file not in project_files
        or not isinstance(line, int)
        or isinstance(line, bool)
        or line < 1
    ):
        raise ValueError(f"oracle relationship has invalid {field} endpoint")
    return file, line


def load_oracle(path: Path) -> OracleDocument:
    document = json.loads(path.read_text(encoding="utf-8"))
    files = document.get("project_files") if isinstance(document, dict) else None
    file_hashes = (
        document.get("project_file_sha256") if isinstance(document, dict) else None
    )
    if (
        not isinstance(document, dict)
        or document.get("schema_version") != 1
        or document.get("type_relationships_oracle") != TYPE_ORACLE
        or document.get("type_relationships_oracle_scope") != TYPE_ORACLE_SCOPE
        or document.get("method_relationships_oracle") != METHOD_ORACLE
        or document.get("method_relationships_oracle_scope") != METHOD_ORACLE_SCOPE
        or not isinstance(document.get("typescript_version"), str)
        or not document.get("typescript_version")
        or not isinstance(files, list)
        or not files
        or any(not isinstance(file, str) or not file for file in files)
        or len(files) != len(set(files))
        or not isinstance(file_hashes, dict)
        or set(file_hashes) != set(files)
        or any(_digest(value, "project_file_sha256") != value for value in file_hashes.values())
        or not isinstance(document.get("type_relationships"), list)
        or not isinstance(document.get("method_relationships"), list)
    ):
        raise ValueError("oracle has an unsupported type-relationship contract")
    project_files = frozenset(files)
    declared_manifest = _digest(
        document.get("project_manifest_sha256"),
        "project_manifest_sha256",
    )
    if manifest_sha256(file_hashes) != declared_manifest:
        raise ValueError("oracle project manifest does not match declared file hashes")
    relationships: set[Relationship] = set()
    for entry in document["type_relationships"]:
        kind = entry.get("kind") if isinstance(entry, dict) else None
        if kind not in {"extends", "implements"}:
            raise ValueError("oracle relationship has invalid kind")
        source_file, source_line = _endpoint(entry, "source", project_files)
        target_file, target_line = _endpoint(entry, "target", project_files)
        relationships.add(
            Relationship(
                f"type_{kind}",
                source_file,
                source_line,
                target_file,
                target_line,
            )
        )
    for entry in document["method_relationships"]:
        kind = entry.get("kind") if isinstance(entry, dict) else None
        if kind not in {"overrides", "implements"}:
            raise ValueError("oracle method relationship has invalid kind")
        source_file, source_line = _endpoint(entry, "source", project_files)
        target_file, target_line = _endpoint(entry, "target", project_files)
        relationships.add(
            Relationship(
                f"method_{kind}",
                source_file,
                source_line,
                target_file,
                target_line,
            )
        )
    declared_relationship_count = len(document["type_relationships"]) + len(
        document["method_relationships"]
    )
    if len(relationships) != declared_relationship_count:
        raise ValueError("oracle relationships must be unique")
    if not relationships:
        raise ValueError("oracle relationship scope is empty")
    return OracleDocument(
        oracle=TYPE_ORACLE,
        oracle_scope=document["type_relationships_oracle_scope"],
        method_oracle=METHOD_ORACLE,
        method_oracle_scope=document["method_relationships_oracle_scope"],
        oracle_implementation_sha256=_digest(
            document.get("oracle_implementation_sha256"),
            "oracle_implementation_sha256",
        ),
        typescript_version=document["typescript_version"],
        tsconfig_sha256=_digest(document.get("tsconfig_sha256"), "tsconfig_sha256"),
        project_files=project_files,
        project_file_sha256=dict(file_hashes),
        project_manifest_sha256=declared_manifest,
        relationships=frozenset(relationships),
    )


def load_observed(
    database: Path,
    project: str,
    repository_root: Path,
    project_file_sha256: dict[str, str],
    *,
    source_revision: str,
    project_manifest_sha256: str,
) -> frozenset[ObservedRelationship]:
    repository_root = repository_root.resolve()
    connection = sqlite3.connect(f"file:{database.resolve()}?mode=ro", uri=True)
    try:
        identity = connection.execute(
            "SELECT source_revision, identity_status FROM index_identity WHERE project=?",
            (project,),
        ).fetchone()
        if identity != (source_revision, "captured"):
            raise ValueError("graph index is not bound to the requested source revision")
        stored_root = connection.execute(
            "SELECT root_path FROM projects WHERE name=?",
            (project,),
        ).fetchone()
        if (
            stored_root is None
            or not same_file(Path(stored_root[0]), repository_root)
        ):
            raise ValueError("graph project root does not match the measured repository")
        project_files = frozenset(project_file_sha256)
        placeholders = ",".join("?" for _ in project_files)
        rows = connection.execute(
            f"SELECT rel_path FROM file_hashes "
            f"WHERE project=? AND rel_path IN ({placeholders})",
            (project, *sorted(project_files)),
        ).fetchall()
        if {path for (path,) in rows} != set(project_files):
            raise ValueError("graph index lacks the complete compiler project file scope")
        actual_hashes = {}
        for relative_path in sorted(project_files):
            source_path = repository_root / relative_path
            if not source_path.is_file() or source_path.is_symlink():
                raise ValueError("compiler project source file is missing or unsafe")
            actual_hashes[relative_path] = hashlib.sha256(source_path.read_bytes()).hexdigest()
        if actual_hashes != project_file_sha256:
            raise ValueError("measured source files do not match the oracle")
        if manifest_sha256(actual_hashes) != project_manifest_sha256:
            raise ValueError("measured project manifest does not match the oracle")
        type_rows = connection.execute(
            """
            SELECT edge.type,
                   source.file_path, source.start_line,
                   target.file_path, target.start_line,
                   COALESCE(json_extract(edge.properties, '$.confidence_tier'), 'EXTRACTED')
            FROM edges AS edge
            JOIN nodes AS source ON source.id = edge.source_id
            JOIN nodes AS target ON target.id = edge.target_id
            WHERE edge.project = ? AND edge.type IN ('INHERITS', 'IMPLEMENTS')
            """,
            (project,),
        ).fetchall()
        method_rows = connection.execute(
            """
            SELECT owner_relationship.type,
                   source.file_path, source.start_line,
                   target.file_path, target.start_line,
                   COALESCE(json_extract(edge.properties, '$.confidence_tier'), 'EXTRACTED')
            FROM edges AS edge
            JOIN nodes AS source ON source.id = edge.source_id
            JOIN nodes AS target ON target.id = edge.target_id
            JOIN edges AS source_definition
              ON source_definition.project = edge.project
             AND source_definition.target_id = source.id
             AND source_definition.type = 'DEFINES_METHOD'
            JOIN edges AS target_definition
              ON target_definition.project = edge.project
             AND target_definition.target_id = target.id
             AND target_definition.type = 'DEFINES_METHOD'
            JOIN edges AS owner_relationship
              ON owner_relationship.project = edge.project
             AND owner_relationship.source_id = source_definition.source_id
             AND owner_relationship.target_id = target_definition.source_id
             AND owner_relationship.type IN ('INHERITS', 'IMPLEMENTS')
            WHERE edge.project = ? AND edge.type = 'OVERRIDE'
            """,
            (project,),
        ).fetchall()
    finally:
        connection.close()
    type_kind_by_edge = {
        "INHERITS": "type_extends",
        "IMPLEMENTS": "type_implements",
    }
    observed = {
        ObservedRelationship(
            Relationship(
                type_kind_by_edge[edge_type],
                source,
                source_line,
                target,
                target_line,
            ),
            confidence,
        )
        for edge_type, source, source_line, target, target_line, confidence in type_rows
        if source in project_files and target in project_files
    }
    method_kind_by_owner_edge = {
        "INHERITS": "method_overrides",
        "IMPLEMENTS": "method_implements",
    }
    observed.update(
        ObservedRelationship(
            Relationship(
                method_kind_by_owner_edge[owner_edge],
                source,
                source_line,
                target,
                target_line,
            ),
            confidence,
        )
        for owner_edge, source, source_line, target, target_line, confidence in method_rows
        if source in project_files and target in project_files
    )
    return frozenset(observed)


def calculate(
    expected: set[Relationship] | frozenset[Relationship],
    observed: set[Relationship] | frozenset[Relationship],
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


def _operating_points(
    expected: frozenset[Relationship],
    observed: frozenset[ObservedRelationship],
) -> dict[str, dict[str, float | int]]:
    all_bands = frozenset(entry.relationship for entry in observed)
    non_ambiguous = frozenset(
        entry.relationship
        for entry in observed
        if entry.confidence_tier != "AMBIGUOUS"
    )
    return {
        "all_bands": calculate(expected, all_bands),
        "non_ambiguous": calculate(expected, non_ambiguous),
    }


def build_metrics(
    oracle: OracleDocument,
    observed: frozenset[ObservedRelationship],
) -> dict:
    by_kind = {}
    for kind in sorted(KINDS):
        expected_kind = frozenset(
            relationship
            for relationship in oracle.relationships
            if relationship.kind == kind
        )
        observed_kind = frozenset(
            entry for entry in observed if entry.relationship.kind == kind
        )
        by_kind[kind] = _operating_points(expected_kind, observed_kind)
    return {
        "schema_version": 1,
        "oracle": oracle.oracle,
        "oracle_scope": oracle.oracle_scope,
        "method_oracle": oracle.method_oracle,
        "method_oracle_scope": oracle.method_oracle_scope,
        "oracle_implementation_sha256": oracle.oracle_implementation_sha256,
        "typescript_version": oracle.typescript_version,
        "tsconfig_sha256": oracle.tsconfig_sha256,
        "project_manifest_sha256": oracle.project_manifest_sha256,
        "project_files": len(oracle.project_files),
        "operating_points": _operating_points(oracle.relationships, observed),
        "by_kind": by_kind,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--oracle", required=True, type=Path)
    parser.add_argument("--database", required=True, type=Path)
    parser.add_argument("--project", required=True)
    parser.add_argument("--repository-root", required=True, type=Path)
    parser.add_argument("--source-revision", required=True)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()

    oracle = load_oracle(args.oracle)
    observed = load_observed(
        args.database,
        args.project,
        args.repository_root,
        oracle.project_file_sha256,
        source_revision=args.source_revision,
        project_manifest_sha256=oracle.project_manifest_sha256,
    )
    result = build_metrics(oracle, observed)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(
        json.dumps(result, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    print(json.dumps(result, sort_keys=True, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
