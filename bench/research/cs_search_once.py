#!/usr/bin/env python3
"""One-shot code-search query helper for the retrieval-only graph comparison.

Runs INSIDE code-search's own venv (it imports that repo's search modules);
the pilot orchestrator (armC_retrieval.py) shells out to it so neither
environment needs the other's dependencies.

stdout: JSON list of {relative_path, name, parent_name, chunk_type, score}
in rank order plus an exact rank-window contract. Reranker selection follows
code-search production defaults (RERANKER env, default "sonnet").
"""
from __future__ import annotations

import argparse
import json
import math
import os
import sys
from pathlib import Path


class QueryHelperContractError(RuntimeError):
    failure_class = "invalid_experiment"

    def __init__(self, message: str, failure_code: str):
        super().__init__(message)
        self.failure_code = failure_code


def build_rank_evidence(
    *,
    requested_k: int,
    returned_count: int,
    total_candidates: int,
) -> dict[str, int | bool]:
    if (
        isinstance(requested_k, bool)
        or not isinstance(requested_k, int)
        or requested_k < 1
        or isinstance(returned_count, bool)
        or not isinstance(returned_count, int)
        or returned_count < 0
        or isinstance(total_candidates, bool)
        or not isinstance(total_candidates, int)
        or total_candidates < 0
    ):
        raise QueryHelperContractError(
            "rank-window counts are invalid",
            "rank_window_unverifiable",
        )
    effective_k = min(requested_k, total_candidates)
    if returned_count != effective_k:
        raise QueryHelperContractError(
            "search response does not account for the exact candidate window",
            "rank_window_unverifiable",
        )
    return {
        "requested_k": requested_k,
        "returned_count": returned_count,
        "total_candidates": total_candidates,
        "effective_k": effective_k,
        "truncated": total_candidates > effective_k,
    }


def validate_candidates(candidates: object) -> list:
    if not isinstance(candidates, list):
        raise QueryHelperContractError(
            "hybrid candidates are not a list",
            "candidate_schema_invalid",
        )
    for rank, candidate in enumerate(candidates, start=1):
        if (
            not isinstance(candidate, (list, tuple))
            or len(candidate) != 2
            or not isinstance(candidate[0], str)
            or not candidate[0]
            or isinstance(candidate[1], bool)
            or not isinstance(candidate[1], (int, float))
            or not math.isfinite(float(candidate[1]))
        ):
            raise QueryHelperContractError(
                f"hybrid candidate {rank} is malformed",
                "candidate_schema_invalid",
            )
    return candidates


def serialize_results(results: object) -> list[dict]:
    if not isinstance(results, list):
        raise QueryHelperContractError(
            "search results are not a list",
            "candidate_schema_invalid",
        )
    serialized: list[dict] = []
    for rank, result in enumerate(results, start=1):
        relative_path = getattr(result, "relative_path", None)
        name = getattr(result, "name", None)
        parent_name = getattr(result, "parent_name", None)
        chunk_type = getattr(result, "chunk_type", None)
        score = getattr(result, "similarity_score", None)
        if not isinstance(relative_path, str) or not relative_path:
            raise QueryHelperContractError(
                f"search result {rank} relative_path is invalid",
                "candidate_schema_invalid",
            )
        if name is not None and not isinstance(name, str):
            raise QueryHelperContractError(
                f"search result {rank} name is invalid",
                "candidate_schema_invalid",
            )
        if parent_name is not None and not isinstance(parent_name, str):
            raise QueryHelperContractError(
                f"search result {rank} parent_name is invalid",
                "candidate_schema_invalid",
            )
        if not isinstance(chunk_type, str) or not chunk_type:
            raise QueryHelperContractError(
                f"search result {rank} chunk_type is invalid",
                "candidate_schema_invalid",
            )
        if (
            isinstance(score, bool)
            or not isinstance(score, (int, float))
            or not math.isfinite(float(score))
        ):
            raise QueryHelperContractError(
                f"search result {rank} score is invalid",
                "candidate_schema_invalid",
            )
        serialized.append(
            {
                "relative_path": relative_path,
                "name": name,
                "parent_name": parent_name,
                "chunk_type": chunk_type,
                "score": score,
            }
        )
    return serialized


def normalize_reranker_metadata(
    metadata: object,
    *,
    requested_reranker: str,
) -> dict:
    if not isinstance(metadata, dict):
        raise QueryHelperContractError(
            "search did not expose effective reranker metadata",
            "reranker_identity_invalid",
        )
    applied = metadata.get("applied")
    reason = metadata.get("reason")
    latency_ms = metadata.get("latency_ms")
    if (
        not isinstance(applied, bool)
        or not isinstance(reason, str)
        or not reason
        or isinstance(latency_ms, bool)
        or not isinstance(latency_ms, (int, float))
        or not math.isfinite(float(latency_ms))
        or latency_ms < 0
    ):
        raise QueryHelperContractError(
            "effective reranker metadata is malformed",
            "reranker_identity_invalid",
        )
    if applied != (reason == "ok"):
        raise QueryHelperContractError(
            "effective reranker applied state contradicts reason",
            "reranker_identity_invalid",
        )
    effective_model = None
    if applied and requested_reranker == "sonnet":
        effective_model = "claude-sonnet-4-6"
    elif applied and requested_reranker == "listwise":
        effective_model = os.environ.get(
            "ANTHROPIC_MODEL",
            "claude-sonnet-4-6",
        )
        if not effective_model:
            raise QueryHelperContractError(
                "effective reranker model is absent",
                "reranker_identity_invalid",
            )
    elif applied:
        raise QueryHelperContractError(
            "applied reranker mode has no verifiable model identity",
            "reranker_identity_invalid",
        )
    return {
        "requested_mode": requested_reranker,
        "applied": applied,
        "reason": reason,
        "latency_ms": int(latency_ms),
        "model": effective_model,
    }


def _run() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--code-search-root", required=True)
    ap.add_argument("--storage-dir", required=True, help="storage dir used at index time")
    ap.add_argument("--query", required=True)
    ap.add_argument("--k", type=int, default=50)
    args = ap.parse_args()
    if args.k < 1:
        raise QueryHelperContractError(
            "requested rank-window K must be positive",
            "rank_window_unverifiable",
        )

    sys.path.insert(0, args.code_search_root)
    from embeddings.embedder import CodeEmbedder  # noqa: E402
    from search.epoch_manifest import read_with_fallback  # noqa: E402
    from search.indexer import CodeIndexManager  # noqa: E402
    import search.searcher as searcher_module  # noqa: E402

    storage = Path(args.storage_dir)
    index_manager = CodeIndexManager(str(storage / "index"))
    embedder = CodeEmbedder(cache_dir=str(storage / "models"))
    configuration = embedder.configuration
    publication = read_with_fallback(index_manager.storage_dir)
    manifest = publication.manifest
    if publication.freshness != "fresh" or not isinstance(manifest, dict):
        raise QueryHelperContractError(
            "search refused an unverified index generation: "
            f"{publication.freshness}: {publication.detail}",
            "query_embedding_identity_invalid",
        )
    expected_manifest = {
        "provider": configuration.provider,
        "model": configuration.model_name,
        "vector_dim": configuration.output_dimension,
    }
    mismatches = {
        field: (manifest.get(field), expected)
        for field, expected in expected_manifest.items()
        if manifest.get(field) != expected
    }
    if mismatches:
        raise QueryHelperContractError(
            f"query embedder disagrees with the verified index: {mismatches}",
            "query_embedding_identity_mismatch",
        )
    pipeline_version = manifest.get("pipeline_version")
    epoch_id = manifest.get("epoch_id")
    if (
        not isinstance(pipeline_version, str)
        or not pipeline_version
        or not isinstance(epoch_id, str)
        or not epoch_id
    ):
        raise QueryHelperContractError(
            "verified index identity is incomplete",
            "query_embedding_identity_invalid",
        )
    searcher = searcher_module.IntelligentSearcher(index_manager, embedder)
    total_candidates: int | None = None
    original_fusion = searcher_module.reciprocal_rank_fusion

    def capturing_fusion(*fusion_args, **fusion_kwargs):
        nonlocal total_candidates
        fused = validate_candidates(
            original_fusion(*fusion_args, **fusion_kwargs)
        )
        total_candidates = len(fused)
        return fused

    searcher_module.reciprocal_rank_fusion = capturing_fusion
    try:
        results = searcher.search(args.query, k=args.k, search_mode="hybrid")
    finally:
        searcher_module.reciprocal_rank_fusion = original_fusion
    if total_candidates is None:
        raise QueryHelperContractError(
            "search did not expose the hybrid candidate window",
            "rank_window_unverifiable",
        )
    out = serialize_results(results)
    requested_reranker = os.environ.get("RERANKER", "sonnet")
    reranker_identity = normalize_reranker_metadata(
        searcher.last_reranker_metadata,
        requested_reranker=requested_reranker,
    )
    json.dump(
        {
            "results": out,
            "rank_evidence": build_rank_evidence(
                requested_k=args.k,
                returned_count=len(out),
                total_candidates=total_candidates,
            ),
            "effective_search": {
                "embedding": {
                    "provider": configuration.provider,
                    "model": configuration.model_name,
                    "vector_dim": configuration.output_dimension,
                    "content_mode": configuration.content_mode,
                    "pipeline_version": pipeline_version,
                    "manifest_freshness": publication.freshness,
                    "index_epoch_id": epoch_id,
                },
                "reranker": reranker_identity,
            },
        },
        sys.stdout,
    )
    return 0


def main() -> int:
    try:
        return _run()
    except QueryHelperContractError as exc:
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
            "failure_code": "search_helper_failed",
        }
        json.dump(payload, sys.stdout)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
