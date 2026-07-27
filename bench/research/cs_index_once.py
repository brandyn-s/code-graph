#!/usr/bin/env python3
"""One-shot code-search indexing helper for the retrieval-only graph comparison.

Runs INSIDE code-search's venv. Mirrors the MCP server's component wiring
(CodeIndexManager + CodeEmbedder + MultiLanguageChunker + SnapshotManager →
IncrementalIndexer.incremental_index) but with fully isolated storage —
including the merkle snapshot dir — so pilot runs leave zero residue in the
user's ~/.claude_code_search store.

NOTE: scripts/index_codebase.py is stale against the current chunker API
(chunk_directory now takes directory_path; the chunker binds its root in the
constructor) — discovered 2026-06-11 during the pilot smoke. This helper uses
the production IncrementalIndexer path instead.
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path


class IndexHelperContractError(RuntimeError):
    failure_class = "invalid_experiment"

    def __init__(self, message: str, failure_code: str):
        super().__init__(message)
        self.failure_code = failure_code


def _run() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--code-search-root", required=True)
    ap.add_argument("--repo", required=True)
    ap.add_argument("--storage-dir", required=True)
    ap.add_argument("--force-full", action="store_true",
                    help="Force a full reindex; omit to let merkle change "
                         "detection embed only the delta vs the snapshot "
                         "(the per-repo cost optimization for multi-instance repos).")
    args = ap.parse_args()

    sys.path.insert(0, args.code_search_root)
    from chunking.multi_language_chunker import MultiLanguageChunker  # noqa: E402
    from embeddings.embedder import CodeEmbedder  # noqa: E402
    from merkle.snapshot_manager import SnapshotManager  # noqa: E402
    from mcp_server.code_search_server import get_pipeline_version  # noqa: E402
    from search.epoch_manifest import read_with_fallback  # noqa: E402
    from search.index_identity import capture_index_identity  # noqa: E402
    from search.incremental_indexer import IncrementalIndexer  # noqa: E402
    from search.indexer import CodeIndexManager  # noqa: E402

    repo = str(Path(args.repo).resolve())
    storage = Path(args.storage_dir)
    storage.mkdir(parents=True, exist_ok=True)

    start_identity = capture_index_identity(repo)
    index_manager = CodeIndexManager(str(storage / "index"))
    embedder = CodeEmbedder(cache_dir=str(storage / "models"))
    configuration = embedder.configuration
    pipeline_version = get_pipeline_version(configuration)
    index_manager.bind_embedding_configuration(
        configuration,
        pipeline_version=pipeline_version,
    )
    indexer = IncrementalIndexer(
        indexer=index_manager,
        embedder=embedder,
        chunker=MultiLanguageChunker(repo),
        snapshot_manager=SnapshotManager(storage_dir=storage / "merkle"),
    )
    result = indexer.incremental_index(repo, project_name=Path(repo).name, force_full=args.force_full)
    if result.success is not True:
        raise RuntimeError(f"index did not complete successfully: {result.error}")
    end_identity = capture_index_identity(repo)
    source_fields = (
        "repository_id",
        "checkout_id",
        "source_revision",
        "dirty_fingerprint",
        "index_generation",
    )
    if any(
        getattr(start_identity, field) != getattr(end_identity, field)
        for field in source_fields
    ):
        raise IndexHelperContractError(
            "source identity changed during indexing",
            "index_identity_mismatch",
        )

    publication = read_with_fallback(index_manager.storage_dir)
    manifest = publication.manifest
    if publication.freshness != "fresh" or not isinstance(manifest, dict):
        raise IndexHelperContractError(
            "index manifest is not a fresh verified generation: "
            f"{publication.freshness}: {publication.detail}",
            "embedding_identity_invalid",
        )
    expected_manifest = {
        "provider": configuration.provider,
        "model": configuration.model_name,
        "vector_dim": configuration.output_dimension,
        "pipeline_version": pipeline_version,
    }
    mismatches = {
        field: (manifest.get(field), expected)
        for field, expected in expected_manifest.items()
        if manifest.get(field) != expected
    }
    if mismatches:
        raise IndexHelperContractError(
            f"verified manifest disagrees with the effective embedder: {mismatches}",
            "embedding_identity_mismatch",
        )
    epoch_id = manifest.get("epoch_id")
    if not isinstance(epoch_id, str) or not epoch_id:
        raise IndexHelperContractError(
            "verified index manifest has no epoch_id",
            "embedding_identity_invalid",
        )

    payload = result.to_dict() if hasattr(result, "to_dict") else {"repr": repr(result)}
    payload.update(
        {
            "index_identity_status": "ready",
            "index_identity": end_identity.to_dict(),
            "embedding_identity": {
                "provider": configuration.provider,
                "model": configuration.model_name,
                "vector_dim": configuration.output_dimension,
                "content_mode": configuration.content_mode,
                "pipeline_version": pipeline_version,
                "manifest_freshness": publication.freshness,
                "index_epoch_id": epoch_id,
            },
        }
    )
    json.dump(payload, sys.stdout)
    return 0


def main() -> int:
    try:
        return _run()
    except IndexHelperContractError as exc:
        payload = {
            "success": False,
            "error": str(exc),
            "failure_class": exc.failure_class,
            "failure_code": exc.failure_code,
        }
        json.dump(payload, sys.stdout)
        return 2
    except Exception as exc:  # noqa: BLE001 - serialize child infrastructure failure
        payload = {
            "success": False,
            "error": str(exc),
            "failure_class": "infrastructure",
            "failure_code": "index_helper_failed",
        }
        json.dump(payload, sys.stdout)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
