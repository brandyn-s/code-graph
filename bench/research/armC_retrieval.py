#!/usr/bin/env python3
"""Retrieval-only arm of the matched-depth graph comparison.

Per pinned Loc-Bench instance: clone repo@base_commit (reusing the batch
harness's clone_repo / size cap), index with code-search (Voyage embeddings),
run one hybrid search with the production reranker defaults, persist every
rank through the configured scoring depth, and score with the SAME substring
convention as eval_locbench_batch.score_against_ground_truth.

Cost design (the plan's own): instances are grouped by repo — one clone and
one code-search storage per DISTINCT repo, `git checkout` per instance
commit, incremental (merkle-delta) indexing after the first instance. A
chunk-count Voyage estimate plus a conservative per-query estimate stops new
cases when the advisory threshold is hit. It is not a provider-enforced
billing cap and may overshoot by one case.

Outputs a per-case JSON checkpointed after every instance.

Run from the pilot venv (pandas); code-search work happens in subprocesses
using code-search's own venv (--cs-python).
"""
from __future__ import annotations

import argparse
import hashlib
import json
import math
import os
import shutil
import subprocess
import sys
import time
from collections import defaultdict
from dataclasses import dataclass, field
from decimal import Decimal, InvalidOperation
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from eval_locbench_batch import (  # noqa: E402
    MAX_REPO_MB,
    RUNTIME_CHILD_ENV_KEYS,
    _validate_index_identity,
    allowlisted_child_env,
    clone_repo,
    repo_size_mb,
    score_against_ground_truth,
)

import pandas as pd  # noqa: E402

# Conservative Voyage estimate: avg tokens per code chunk x $/M tokens.
# voyage-4-large list price ~$0.12/M; we meter at $0.20/M + 400 tok/chunk
# so the estimate errs toward overestimating spend. The finding doc reports
# estimated cost, not actual provider billing.
EST_TOKENS_PER_CHUNK = 400
EST_USD_PER_MTOK = 0.20
MARGINAL_QUERY_ESTIMATE_USD = 0.02
PROVIDER_OPERATION_BOUND_POLICY = (
    "provider-enforced-per-operation-usd-required-v1"
)


@dataclass(frozen=True)
class RetrievalIndexOutcome:
    success: bool
    error: str = ""
    failure_class: str = ""
    failure_code: str = ""
    chunks_added: int = 0
    index_identity: dict = field(default_factory=dict)
    embedding_identity: dict = field(default_factory=dict)


@dataclass(frozen=True)
class RetrievalSearchOutcome:
    results: list[dict]
    effective_search: dict
    rank_evidence: dict = field(default_factory=dict)


class EffectiveSearchError(RuntimeError):
    def __init__(self, message: str, effective_search: dict):
        super().__init__(message)
        self.effective_search = effective_search


class InvalidExperimentError(RuntimeError):
    failure_class = "invalid_experiment"

    def __init__(self, message: str, failure_code: str):
        super().__init__(message)
        self.failure_code = failure_code


class RetrievalInfrastructureError(RuntimeError):
    failure_class = "infrastructure"

    def __init__(self, message: str, failure_code: str):
        super().__init__(message)
        self.failure_code = failure_code


def classify_failure(error: Exception) -> tuple[str, str]:
    if isinstance(error, (InvalidExperimentError, RetrievalInfrastructureError)):
        return error.failure_class, error.failure_code
    return "infrastructure", "retrieval_error"


def _typed_child_failure(
    completed: subprocess.CompletedProcess,
) -> tuple[str, str, str] | None:
    try:
        payload = json.loads(
            completed.stdout.decode("utf-8", errors="replace")
        )
    except (AttributeError, json.JSONDecodeError, UnicodeDecodeError):
        return None
    if not isinstance(payload, dict) or payload.get("success") is not False:
        return None
    failure_class = payload.get("failure_class")
    failure_code = payload.get("failure_code")
    error = payload.get("error")
    if (
        failure_class not in {"invalid_experiment", "infrastructure"}
        or not isinstance(failure_code, str)
        or not failure_code
        or not isinstance(error, str)
        or not error
    ):
        return None
    return failure_class, failure_code, error


def apply_failure_to_case(case: dict, error: Exception) -> None:
    failure_class, failure_code = classify_failure(error)
    case["failure_class"] = failure_class
    case["failure_code"] = failure_code
    case["note"] = f"error: {error}"[:300]


def apply_direct_failure(
    case: dict,
    *,
    failure_class: str,
    failure_code: str,
    note: str,
) -> None:
    case["failure_class"] = failure_class
    case["failure_code"] = failure_code
    case["note"] = note[:300]


def require_valid_index_outcome(
    outcome: RetrievalIndexOutcome,
) -> RetrievalIndexOutcome:
    if outcome.success:
        return outcome
    if outcome.failure_class == "invalid_experiment":
        raise InvalidExperimentError(outcome.error, outcome.failure_code)
    raise RuntimeError(f"index failed: {outcome.error}")


def require_pinned_index_identity(
    outcome: RetrievalIndexOutcome,
    expected_revision: str,
) -> None:
    if (
        outcome.index_identity.get("source_revision") != expected_revision
        or outcome.index_identity.get("dirty_fingerprint") != "clean"
    ):
        raise InvalidExperimentError(
            "index identity does not match the clean pinned checkout",
            "index_identity_mismatch",
        )


def validate_rank_evidence(
    evidence: object,
    *,
    requested_k: int,
    returned_count: int,
) -> dict[str, int | bool]:
    def invalid(message: str) -> InvalidExperimentError:
        return InvalidExperimentError(message, "rank_window_unverifiable")

    if not isinstance(evidence, dict):
        raise invalid("retrieval rank-window evidence is absent")
    for field_name in (
        "requested_k",
        "returned_count",
        "total_candidates",
        "effective_k",
    ):
        value = evidence.get(field_name)
        if (
            isinstance(value, bool)
            or not isinstance(value, int)
            or value < 0
        ):
            raise invalid(f"retrieval rank-window {field_name} is invalid")
    if not isinstance(evidence.get("truncated"), bool):
        raise invalid("retrieval rank-window truncation is not explicit")
    if requested_k < 1 or evidence["requested_k"] != requested_k:
        raise invalid("retrieval requested K does not match the run contract")
    if evidence["returned_count"] != returned_count:
        raise invalid("retrieval returned count does not match the response")
    total_candidates = evidence["total_candidates"]
    effective_k = min(requested_k, total_candidates)
    if evidence["effective_k"] != effective_k:
        raise invalid("retrieval effective K is inconsistent")
    if returned_count != effective_k:
        raise invalid("retrieval response does not account for effective K")
    if evidence["truncated"] != (total_candidates > effective_k):
        raise invalid("retrieval truncation flag is inconsistent")
    return {
        "requested_k": requested_k,
        "returned_count": returned_count,
        "total_candidates": total_candidates,
        "effective_k": effective_k,
        "truncated": total_candidates > effective_k,
    }


def sum_case_cost_estimates(cases: list[dict]) -> float:
    """Return the recorded index-plus-query estimate for checkpointed cases."""
    total = sum(
        (
            Decimal(
                str(
                    case.get("cost_usd", {}).get(
                        "total_estimate",
                        0,
                    )
                )
            )
            for case in cases
        ),
        Decimal("0"),
    )
    return float(total.quantize(Decimal("0.000001")))


def _validate_persisted_retrieval_costs(
    artifact: dict,
    cases: list[dict],
) -> None:
    def require_cost(name: str, value: object) -> Decimal:
        if (
            isinstance(value, bool)
            or not isinstance(value, (int, float))
        ):
            raise ValueError(f"{name} cost must be finite and nonnegative")
        parsed = Decimal(str(value))
        if not parsed.is_finite() or parsed < Decimal("0"):
            raise ValueError(f"{name} cost must be finite and nonnegative")
        try:
            quantized = parsed.quantize(Decimal("0.000001"))
        except InvalidOperation as exc:
            raise ValueError(
                f"{name} cost exceeds declared micro-dollar precision"
            ) from exc
        if parsed != quantized:
            raise ValueError(
                f"{name} cost exceeds declared micro-dollar precision"
            )
        return parsed

    checkpoint_total = require_cost(
        "checkpoint total",
        artifact.get("total_cost_usd"),
    )
    expected_cost_fields = {
        "index_embedding_estimate",
        "marginal_query_estimate",
        "total_estimate",
        "total_estimate_scope",
    }
    case_total_sum = Decimal("0")
    for case in cases:
        instance_id = str(case.get("instance_id", "unknown"))
        cost = case.get("cost_usd")
        if not isinstance(cost, dict):
            raise ValueError(
                f"{instance_id} persisted cost must contain exact fields"
            )
        actual_cost_fields = set(cost)
        if actual_cost_fields != expected_cost_fields:
            missing_cost_fields = sorted(
                expected_cost_fields - actual_cost_fields
            )
            unexpected_cost_fields = sorted(
                actual_cost_fields - expected_cost_fields
            )
            raise ValueError(
                f"{instance_id} persisted cost must contain exact fields; "
                f"missing={missing_cost_fields}, "
                f"unexpected={unexpected_cost_fields}"
            )
        if cost["total_estimate_scope"] != "index_plus_marginal_query":
            raise ValueError(
                f"{instance_id} persisted cost scope is invalid"
            )
        parsed_costs = {
            field_name: require_cost(
                f"{instance_id} {field_name}",
                cost[field_name],
            )
            for field_name in (
            "index_embedding_estimate",
            "marginal_query_estimate",
            "total_estimate",
            )
        }
        component_sum = (
            parsed_costs["index_embedding_estimate"]
            + parsed_costs["marginal_query_estimate"]
        )
        if parsed_costs["total_estimate"] != component_sum:
            raise ValueError(
                f"{instance_id} persisted cost total does not equal "
                "the exact component sum"
            )
        case_total_sum += parsed_costs["total_estimate"]
    if checkpoint_total != case_total_sum:
        raise ValueError(
            "checkpoint total cost does not equal the exact case total sum"
        )


def _normalized_stage_latencies(
    latency_s: dict,
    *,
    duration_s: float,
) -> dict[str, float]:
    if not isinstance(latency_s, dict):
        raise ValueError("stage latency evidence must be a dict")
    if (
        isinstance(duration_s, bool)
        or not isinstance(duration_s, (int, float))
        or not math.isfinite(float(duration_s))
        or float(duration_s) < 0.0
    ):
        raise ValueError("invalid total duration")
    normalized: dict[str, float] = {}
    for stage in ("clone", "index", "marginal_query", "total"):
        value = latency_s.get(stage)
        if value is None:
            continue
        if (
            isinstance(value, bool)
            or not isinstance(value, (int, float))
            or not math.isfinite(float(value))
            or float(value) < 0.0
        ):
            raise ValueError(f"invalid {stage} latency")
        normalized[stage] = round(float(value), 6)
    normalized.setdefault("total", round(float(duration_s), 6))
    return normalized


def _validated_embedding_identity(
    identity: object,
    *,
    expected_model: str,
) -> tuple[dict, str]:
    if not isinstance(identity, dict):
        return {}, "effective embedding identity is absent"
    dimension = identity.get("vector_dim")
    required_strings = (
        "provider",
        "model",
        "content_mode",
        "pipeline_version",
        "manifest_freshness",
        "index_epoch_id",
    )
    if any(not isinstance(identity.get(name), str) or not identity[name] for name in required_strings):
        return {}, "effective embedding identity is incomplete"
    if (
        identity["provider"] != "voyage"
        or identity["model"] != expected_model
        or identity["manifest_freshness"] != "fresh"
        or isinstance(dimension, bool)
        or not isinstance(dimension, int)
        or dimension < 1
    ):
        return {}, (
            "effective embedding identity does not match the fresh requested "
            f"Voyage model {expected_model!r}"
        )
    return dict(identity), ""


def _embedding_identity_has_complete_schema(identity: object) -> bool:
    if not isinstance(identity, dict):
        return False
    dimension = identity.get("vector_dim")
    return (
        all(
            isinstance(identity.get(name), str) and bool(identity[name])
            for name in (
                "provider",
                "model",
                "content_mode",
                "pipeline_version",
                "manifest_freshness",
                "index_epoch_id",
            )
        )
        and not isinstance(dimension, bool)
        and isinstance(dimension, int)
        and dimension >= 1
    )


def validate_serialized_search_results(results: object) -> list[dict]:
    if not isinstance(results, list):
        raise InvalidExperimentError(
            "retrieval results are not a list",
            "candidate_schema_invalid",
        )
    normalized: list[dict] = []
    for rank, result in enumerate(results, start=1):
        if not isinstance(result, dict):
            raise InvalidExperimentError(
                f"retrieval result {rank} is not an object",
                "candidate_schema_invalid",
            )
        relative_path = result.get("relative_path")
        name = result.get("name")
        parent_name = result.get("parent_name")
        chunk_type = result.get("chunk_type")
        score = result.get("score")
        if not isinstance(relative_path, str) or not relative_path:
            raise InvalidExperimentError(
                f"retrieval result {rank} relative_path is invalid",
                "candidate_schema_invalid",
            )
        if name is not None and not isinstance(name, str):
            raise InvalidExperimentError(
                f"retrieval result {rank} name is invalid",
                "candidate_schema_invalid",
            )
        if parent_name is not None and not isinstance(parent_name, str):
            raise InvalidExperimentError(
                f"retrieval result {rank} parent_name is invalid",
                "candidate_schema_invalid",
            )
        if not isinstance(chunk_type, str) or not chunk_type:
            raise InvalidExperimentError(
                f"retrieval result {rank} chunk_type is invalid",
                "candidate_schema_invalid",
            )
        if (
            isinstance(score, bool)
            or not isinstance(score, (int, float))
            or not math.isfinite(float(score))
        ):
            raise InvalidExperimentError(
                f"retrieval result {rank} score is invalid",
                "candidate_schema_invalid",
            )
        normalized.append(dict(result))
    return normalized


def validate_effective_reranker_identity(
    identity: object,
    *,
    requested_reranker: str,
) -> dict:
    if not isinstance(identity, dict):
        raise InvalidExperimentError(
            "effective reranker identity is absent",
            "reranker_identity_invalid",
        )
    requested_mode = identity.get("requested_mode")
    applied = identity.get("applied")
    reason = identity.get("reason")
    latency_ms = identity.get("latency_ms")
    model = identity.get("model")
    if (
        not isinstance(requested_mode, str)
        or not requested_mode
        or not isinstance(applied, bool)
        or not isinstance(reason, str)
        or not reason
        or isinstance(latency_ms, bool)
        or not isinstance(latency_ms, (int, float))
        or not math.isfinite(float(latency_ms))
        or latency_ms < 0
        or (
            model is not None
            and (not isinstance(model, str) or not model)
        )
    ):
        raise InvalidExperimentError(
            "effective reranker identity is malformed",
            "reranker_identity_invalid",
        )
    if applied != (reason == "ok"):
        raise InvalidExperimentError(
            "effective reranker applied state contradicts reason",
            "reranker_identity_invalid",
        )
    expected_model = (
        "claude-sonnet-4-6"
        if applied and requested_reranker in {"sonnet", "listwise"}
        else None
    )
    if (
        requested_mode != requested_reranker
        or model != expected_model
    ):
        raise InvalidExperimentError(
            "effective reranker identity does not match the requested policy",
            "reranker_identity_mismatch",
        )
    return dict(identity)


def is_transient_error(message: str) -> bool:
    normalized = message.lower()
    transient_markers = (
        "timed out",
        "timeout",
        "connection reset",
        "connection aborted",
        "connection refused",
        "temporary failure",
        "temporarily unavailable",
        "rate limit",
        "http 429",
        "status 429",
        "http 500",
        "http 502",
        "http 503",
        "http 504",
        "status 500",
        "status 502",
        "status 503",
        "status 504",
    )
    return any(marker in normalized for marker in transient_markers)


def run_with_transient_retries(
    operation_name: str,
    operation,
    *,
    max_attempts: int,
    base_delay_s: float = 1.0,
):
    """Run an operation again only when its error is classified transient."""
    if max_attempts < 1:
        raise ValueError("max_attempts must be positive")
    attempts: list[dict] = []
    for attempt_number in range(1, max_attempts + 1):
        try:
            result = operation()
        except Exception as exc:
            message = str(exc)[:300]
            transient = is_transient_error(message)
            retry = transient and attempt_number < max_attempts
            attempts.append(
                {
                    "operation": operation_name,
                    "attempt": attempt_number,
                    "outcome": "error",
                    "error": message,
                    "transient": transient,
                    "retry": retry,
                }
            )
            if not retry:
                try:
                    setattr(exc, "matched_depth_attempts", list(attempts))
                except Exception:
                    pass
                raise
            time.sleep(base_delay_s * (2 ** (attempt_number - 1)))
        else:
            attempts.append(
                {
                    "operation": operation_name,
                    "attempt": attempt_number,
                    "outcome": "success",
                    "retry": False,
                }
            )
            return result, attempts
    raise AssertionError("unreachable retry loop")


def sha256_file(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def write_json_checkpoint(path: Path, payload: dict) -> None:
    """Atomically replace and fsync a checkpoint."""
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.{os.getpid()}.tmp")
    try:
        with temporary.open("w", encoding="utf-8") as handle:
            json.dump(payload, handle, indent=2)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
        directory_fd = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    finally:
        temporary.unlink(missing_ok=True)


def _exact_budget_decimal(name: str, value: str, *, positive: bool) -> Decimal:
    if not isinstance(value, str) or not value or value.strip() != value:
        raise ValueError(f"{name} must be an exact decimal string")
    try:
        parsed = Decimal(value)
        microdollar_value = parsed.quantize(Decimal("0.000001"))
    except InvalidOperation as exc:
        raise ValueError(f"{name} must be an exact decimal string") from exc
    if (
        not parsed.is_finite()
        or parsed != microdollar_value
        or (parsed <= 0 if positive else parsed < 0)
    ):
        qualifier = "positive" if positive else "nonnegative"
        raise ValueError(
            f"{name} must be finite, {qualifier}, and use at most six decimals"
        )
    return parsed


def build_retrieval_budget_contract(
    *,
    shard_allocation_usd: str,
    arm_ceiling_usd: str,
    total_ceiling_usd: str,
    provider_operation_bound_policy: str,
) -> dict[str, str]:
    shard = _exact_budget_decimal(
        "shard allocation",
        shard_allocation_usd,
        positive=False,
    )
    arm = _exact_budget_decimal("arm ceiling", arm_ceiling_usd, positive=True)
    total = _exact_budget_decimal(
        "total ceiling",
        total_ceiling_usd,
        positive=True,
    )
    if shard > arm:
        raise ValueError("retrieval shard allocation exceeds the arm ceiling")
    if arm > total:
        raise ValueError("retrieval arm ceiling exceeds the total ceiling")
    if provider_operation_bound_policy != PROVIDER_OPERATION_BOUND_POLICY:
        raise ValueError("provider operation-bound policy is not approved")
    return {
        "shard_allocation_usd": shard_allocation_usd,
        "arm_ceiling_usd": arm_ceiling_usd,
        "total_ceiling_usd": total_ceiling_usd,
        "provider_operation_bound_policy": provider_operation_bound_policy,
    }


def build_provenance(
    pin: dict,
    pin_path: Path,
    graph_sha: str,
    score_depth: int,
    embedding_model: str,
    reranker: str,
    *,
    budget_contract: dict[str, str],
) -> dict:
    if len(graph_sha) != 40 or any(c not in "0123456789abcdef" for c in graph_sha):
        raise ValueError("graph_sha must be a lowercase 40-character Git SHA")
    if score_depth != pin.get("score_depth"):
        raise ValueError(
            f"score depth {score_depth} does not match pin depth {pin.get('score_depth')}"
        )
    code_search = pin.get("component_pins", {}).get("code_search", {})
    if code_search.get("tag") != "v0.2.1":
        raise ValueError("pin must select code-search tag v0.2.1")
    expected_digest = (
        "567d4caabdd3b5446bcaa789afc7104fb8cce142ff69d7fc8f1294398532e7e9"
    )
    if code_search.get("artifact_sha256") != expected_digest:
        raise ValueError("pin has the wrong code-search v0.2.1 artifact digest")
    scorer = Path(__file__).resolve().parent / "pilot_compare.py"
    return {
        "graph_sha": graph_sha,
        "code_search": dict(code_search),
        "dataset_sha256": pin["dataset"]["parquet_sha256"],
        "dataset_revision": pin["dataset"]["revision"],
        "pin_sha256": sha256_file(pin_path),
        "query_sha256": None,
        "scorer_sha256": sha256_file(scorer),
        "harness_sha256": sha256_file(Path(__file__).resolve()),
        "model": embedding_model,
        "embedding_model": embedding_model,
        "reranker": "per-case-effective",
        "requested_reranker": reranker,
        "score_depth": score_depth,
        "budget_contract": dict(budget_contract),
    }


def score_ranked_results(
    results: list[dict],
    ground_truth: list[str],
    score_depth: int = 10,
) -> tuple[list[dict], tuple[bool, bool, bool]]:
    """Persist and score exactly ``results[:score_depth]``.

    Empty rank records make a short response explicit rather than silently
    omitting ranks from the evidence artifact.
    """
    if score_depth < 1:
        raise ValueError("score_depth must be positive")
    selected = results[:score_depth]
    ranks = [
        {
            "rank": rank,
            "available": rank <= len(selected),
            "relative_path": (
                selected[rank - 1].get("relative_path") or ""
                if rank <= len(selected)
                else ""
            ),
            "parent_name": (
                selected[rank - 1].get("parent_name") or ""
                if rank <= len(selected)
                else ""
            ),
            "name": (
                selected[rank - 1].get("name") or ""
                if rank <= len(selected)
                else ""
            ),
            "score": (
                selected[rank - 1].get("score")
                if rank <= len(selected)
                else None
            ),
        }
        for rank in range(1, score_depth + 1)
    ]
    blob = "\n".join(
        f"{rank['parent_name']}.{rank['name']} {rank['relative_path']}"
        for rank in ranks
        if rank["available"]
    )
    return ranks, score_against_ground_truth(blob, ground_truth)


def build_retrieval_case(
    row: dict,
    results: list[dict],
    *,
    chunks_added: int,
    duration_s: float,
    latency_s: dict,
    provenance: dict,
    attempts: list[dict],
    index_identity: dict,
    embedding_identity: dict,
    effective_search: dict,
    rank_evidence: dict | None = None,
) -> dict:
    """Build the decision-grade per-case record for a successful retrieval."""
    score_depth = int(provenance["score_depth"])
    ground_truth = list(row.get("edit_functions", []))
    ranks, hits = score_ranked_results(results, ground_truth, score_depth)
    query = row["problem_statement"].split("\n\n")[0].strip()
    identity_error = _validate_index_identity(index_identity)
    if identity_error:
        raise InvalidExperimentError(
            f"retrieval index identity is invalid: {identity_error}",
            "index_identity_invalid",
        )
    if (
        index_identity.get("source_revision") != row["base_commit"]
        or index_identity.get("dirty_fingerprint") != "clean"
    ):
        raise InvalidExperimentError(
            "retrieval index identity does not match the clean pinned checkout",
            "index_identity_mismatch",
        )
    validated_embedding, embedding_error = _validated_embedding_identity(
        embedding_identity,
        expected_model=provenance["embedding_model"],
    )
    if embedding_error:
        raise InvalidExperimentError(
            embedding_error,
            "embedding_identity_invalid",
        )
    if effective_search.get("embedding") != validated_embedding:
        raise InvalidExperimentError(
            "query embedding identity does not match the indexed generation",
            "query_embedding_identity_mismatch",
        )
    validated_rank_evidence = validate_rank_evidence(
        rank_evidence,
        requested_k=score_depth,
        returned_count=len(results),
    )
    index_cost = (
        chunks_added
        * EST_TOKENS_PER_CHUNK
        * EST_USD_PER_MTOK
        / 1_000_000
    )
    file_hit, class_hit, func_hit = hits
    normalized_latency = _normalized_stage_latencies(
        latency_s,
        duration_s=duration_s,
    )
    seen_files: list[str] = []
    for rank in ranks:
        path = rank["relative_path"]
        if path and path not in seen_files:
            seen_files.append(path)
    return {
        "instance_id": row["instance_id"],
        "repo": row["repo"],
        "base_commit": row["base_commit"],
        "category": row.get("category", "Unknown"),
        "ground_truth": ground_truth,
        "query_sha256": hashlib.sha256(query.encode("utf-8")).hexdigest(),
        "status": "ok",
        "failure_class": "",
        "failure_code": "",
        "indexed": True,
        "file_hit": file_hit,
        "class_hit": class_hit,
        "func_hit": func_hit,
        "top_files": seen_files,
        "results": ranks,
        "chunks_added": chunks_added,
        "index_identity": dict(index_identity),
        "embedding_identity": validated_embedding,
        "effective_search": dict(effective_search),
        "rank_evidence": validated_rank_evidence,
        "cost_usd": {
            "index_embedding_estimate": round(index_cost, 6),
            "marginal_query_estimate": MARGINAL_QUERY_ESTIMATE_USD,
            "total_estimate": round(index_cost + MARGINAL_QUERY_ESTIMATE_USD, 6),
            "total_estimate_scope": "index_plus_marginal_query",
        },
        "cost_basis": {
            "index": {
                "method": (
                    f"{EST_TOKENS_PER_CHUNK} tokens/chunk at "
                    f"${EST_USD_PER_MTOK:.2f}/M"
                ),
                "measurement_basis": "chunk-count-static-price-v1",
            },
            "marginal_query": {
                "method": "fixed conservative estimate for hybrid search and rerank",
                "measurement_basis": "fixed-conservative-pilot-estimate-v1",
            },
        },
        "attempts": list(attempts),
        "duration_s": round(duration_s, 3),
        "latency_s": normalized_latency,
        "latency_basis": {
            "full_run": {
                "method": "wall clock from case start through result construction",
                "measurement_basis": "retrieval-reused-case-v1",
            },
            "marginal_query": {
                "method": "wall clock for query stage including bounded retries",
                "measurement_basis": "wall-clock-query-stage-v1",
            },
        },
        "note": "",
    }


def build_retrieval_failure_case(
    row: dict,
    *,
    provenance: dict,
    note: str,
    indexed: bool,
    chunks_added: int,
    query_attempted: bool,
    duration_s: float,
    latency_s: dict,
    attempts: list[dict],
    failure_class: str = "",
    failure_code: str = "",
    index_identity: dict | None = None,
    embedding_identity: dict | None = None,
    effective_search: dict | None = None,
    rank_evidence: dict | None = None,
) -> dict:
    """Build an explicit ten-rank miss for a failed intent-to-treat case."""
    score_depth = int(provenance["score_depth"])
    ground_truth = list(row.get("edit_functions", []))
    ranks, _hits = score_ranked_results([], ground_truth, score_depth)
    query = row["problem_statement"].split("\n\n")[0].strip()
    index_cost = (
        chunks_added
        * EST_TOKENS_PER_CHUNK
        * EST_USD_PER_MTOK
        / 1_000_000
    )
    query_cost = MARGINAL_QUERY_ESTIMATE_USD if query_attempted else 0.0
    normalized_latency = _normalized_stage_latencies(
        latency_s,
        duration_s=duration_s,
    )
    query_cost_basis = {
        "method": (
            "fixed conservative estimate for attempted hybrid search and rerank"
            if query_attempted
            else "query stage not attempted"
        ),
        "measurement_basis": (
            "fixed-conservative-pilot-estimate-v1"
            if query_attempted
            else "not-incurred-v1"
        ),
    }
    latency_basis: dict[str, dict] = {
        "full_run": {
            "method": "wall clock from case start through result construction",
            "measurement_basis": "retrieval-reused-case-v1",
        }
    }
    if query_attempted and "marginal_query" in normalized_latency:
        latency_basis["marginal_query"] = {
            "method": "wall clock for query stage including bounded retries",
            "measurement_basis": "wall-clock-query-stage-v1",
        }
    return {
        "instance_id": row["instance_id"],
        "repo": row["repo"],
        "base_commit": row["base_commit"],
        "category": row.get("category", "Unknown"),
        "ground_truth": ground_truth,
        "query_sha256": hashlib.sha256(query.encode("utf-8")).hexdigest(),
        "status": "miss",
        "failure_class": failure_class,
        "failure_code": failure_code,
        "indexed": indexed,
        "file_hit": False,
        "class_hit": False,
        "func_hit": False,
        "top_files": [],
        "results": ranks,
        "chunks_added": chunks_added,
        "index_identity": dict(index_identity or {}),
        "embedding_identity": dict(embedding_identity or {}),
        "effective_search": dict(effective_search or {}),
        "rank_evidence": dict(rank_evidence or {}),
        "cost_usd": {
            "index_embedding_estimate": round(index_cost, 6),
            "marginal_query_estimate": query_cost,
            "total_estimate": round(index_cost + query_cost, 6),
            "total_estimate_scope": "index_plus_marginal_query",
        },
        "cost_basis": {
            "index": {
                "method": (
                    f"{EST_TOKENS_PER_CHUNK} tokens/chunk at "
                    f"${EST_USD_PER_MTOK:.2f}/M"
                ),
                "measurement_basis": "chunk-count-static-price-v1",
            },
            "marginal_query": query_cost_basis,
        },
        "attempts": list(attempts),
        "duration_s": round(duration_s, 3),
        "latency_s": normalized_latency,
        "latency_basis": latency_basis,
        "note": note,
    }


def build_run_artifact(
    provenance: dict,
    expected_instance_ids: list[str],
    cases: list[dict],
    *,
    aborted_reason: str = "",
) -> dict:
    """Build a shard checkpoint that is complete only at exact coverage."""
    if len(expected_instance_ids) != len(set(expected_instance_ids)):
        raise ValueError("expected instance IDs contain duplicates")
    case_ids = [str(case.get("instance_id", "")) for case in cases]
    duplicate_ids = sorted(
        {instance_id for instance_id in case_ids if case_ids.count(instance_id) > 1}
    )
    if duplicate_ids:
        raise ValueError(f"case checkpoint contains duplicate IDs: {duplicate_ids}")
    extras = sorted(set(case_ids) - set(expected_instance_ids))
    if extras:
        raise ValueError(f"case checkpoint contains unexpected IDs: {extras}")
    invalid_cases = [
        case for case in cases if case.get("failure_class") == "invalid_experiment"
    ]
    if invalid_cases and not aborted_reason:
        invalid = invalid_cases[0]
        aborted_reason = (
            f"invalid_experiment:{invalid.get('failure_code', 'unspecified')}:"
            f"{invalid.get('instance_id', 'unknown')}"
        )
    exact_coverage = (
        len(case_ids) == len(expected_instance_ids)
        and set(case_ids) == set(expected_instance_ids)
    )
    total_cost = sum(
        (
            Decimal(
                str(
                    case.get("cost_usd", {}).get(
                        "total_estimate",
                        0,
                    )
                )
            )
            for case in cases
        ),
        Decimal("0"),
    )
    return {
        "schema_version": 2,
        "arm": "retrieval-only",
        "status": "complete" if exact_coverage and not aborted_reason else "partial",
        "expected_cases": len(expected_instance_ids),
        "accounted_cases": len(case_ids),
        "expected_instance_ids": list(expected_instance_ids),
        "total_cost_usd": float(
            total_cost.quantize(Decimal("0.000001"))
        ),
        "aborted_reason": aborted_reason,
        "provenance": dict(provenance),
        "cases": list(cases),
    }


def load_resume_cases(
    path: Path,
    provenance: dict,
    expected_instance_ids: list[str],
    *,
    allow_implicit_empty: bool = False,
) -> list[dict]:
    """Load a partial/complete checkpoint only under the identical contract."""
    if not path.exists():
        return []
    artifact = json.loads(path.read_text(encoding="utf-8"))
    if artifact.get("arm") != "retrieval-only":
        raise ValueError("checkpoint arm is not retrieval-only")
    status = artifact.get("status")
    if status == "not_evaluated" and allow_implicit_empty:
        cases = artifact.get("cases")
        if not isinstance(cases, list):
            raise ValueError("checkpoint cases must be a list")
        _validate_persisted_retrieval_costs(artifact, cases)
        if cases:
            raise ValueError(
                "implicit not_evaluated checkpoint must not contain cases"
            )
        if Decimal(str(artifact["total_cost_usd"])) != Decimal("0"):
            raise ValueError(
                "implicit not_evaluated checkpoint total cost must be zero"
            )
        return []
    if artifact.get("provenance") != provenance:
        raise ValueError("checkpoint provenance does not match this run")
    if artifact.get("expected_instance_ids") != expected_instance_ids:
        raise ValueError("checkpoint expected instance IDs do not match this shard")
    cases = artifact.get("cases")
    if not isinstance(cases, list):
        raise ValueError("checkpoint cases must be a list")
    _validate_persisted_retrieval_costs(artifact, cases)
    if status == "not_evaluated":
        if cases:
            raise ValueError(
                "explicit not_evaluated checkpoint must not contain cases"
            )
        if Decimal(str(artifact["total_cost_usd"])) != Decimal("0"):
            raise ValueError(
                "explicit not_evaluated checkpoint total cost must be zero"
            )
        raise ValueError(
            "explicit resume checkpoint status is not_evaluated"
        )
    # Rebuild to apply duplicate/foreign-ID validation to the stored cases.
    rebuilt = build_run_artifact(
        provenance,
        expected_instance_ids,
        cases,
        aborted_reason=str(artifact.get("aborted_reason", "")),
    )
    if (
        str(rebuilt.get("aborted_reason", "")).startswith(
            "invalid_experiment:"
        )
        or any(
            case.get("failure_class") == "invalid_experiment"
            for case in cases
        )
    ):
        raise ValueError(
            "checkpoint contains a persisted invalid_experiment abort"
        )
    return cases


def prepare_dataset(
    frame: "pd.DataFrame",
    pin: dict,
    *,
    repository: str | None = None,
) -> tuple[list[dict], list[dict], list[str], str]:
    """Validate the parquet against the pin and return a repository shard."""
    ids = list(pin.get("pinned_instance_ids", []))
    if len(ids) != int(pin.get("n", -1)) or len(ids) != len(set(ids)):
        raise ValueError("pin IDs are missing, duplicated, or inconsistent with n")
    pin_cases = pin.get("cases")
    if not isinstance(pin_cases, list):
        raise ValueError("pin cases must be a list")
    if [case.get("instance_id") for case in pin_cases] != ids:
        raise ValueError("pin case order does not match pinned_instance_ids")

    pinned_frame = frame[frame["instance_id"].isin(set(ids))]
    duplicate_ids = sorted(
        pinned_frame.loc[pinned_frame["instance_id"].duplicated(), "instance_id"]
        .astype(str)
        .unique()
        .tolist()
    )
    if duplicate_ids:
        raise ValueError(f"dataset has duplicate pinned IDs: {duplicate_ids}")
    rows_by_id = {
        str(row["instance_id"]): row.to_dict()
        for _, row in pinned_frame.iterrows()
    }
    missing = [instance_id for instance_id in ids if instance_id not in rows_by_id]
    if missing:
        raise ValueError(f"dataset is missing {len(missing)} pinned IDs: {missing}")

    all_rows: list[dict] = []
    query_records: list[dict] = []
    for pin_case in pin_cases:
        instance_id = pin_case["instance_id"]
        row = rows_by_id[instance_id]
        for field in ("repo", "base_commit", "category"):
            if str(row.get(field, "")) != str(pin_case.get(field, "")):
                raise ValueError(
                    f"dataset {field} mismatch for {instance_id}: "
                    f"{row.get(field)!r} != {pin_case.get(field)!r}"
                )
        problem = row.get("problem_statement")
        if not isinstance(problem, str) or not problem.strip():
            raise ValueError(f"dataset query source missing for {instance_id}")
        edits = row.get("edit_functions")
        if edits is None:
            raise ValueError(f"dataset oracle source missing for {instance_id}")
        row["edit_functions"] = list(edits)
        query = problem.split("\n\n")[0].strip()
        query_records.append({"instance_id": instance_id, "query": query})
        all_rows.append(row)

    query_digest = hashlib.sha256(
        json.dumps(
            query_records, sort_keys=True, separators=(",", ":"), ensure_ascii=False
        ).encode("utf-8")
    ).hexdigest()
    shard_rows = [
        row for row in all_rows if repository is None or row["repo"] == repository
    ]
    if repository is not None and not shard_rows:
        raise ValueError(f"repository {repository!r} has no cases in the canonical pin")
    expected_ids = [row["instance_id"] for row in shard_rows]
    return all_rows, shard_rows, expected_ids, query_digest


def index_with_code_search(
    cs_python: str,
    cs_root: str,
    repo_dir: Path,
    storage: Path,
    force_full: bool,
    embedding_model: str = "voyage-4-large",
    timeout: int = 2400,
) -> RetrievalIndexOutcome:
    """Index once and require observed source plus embedding identity."""
    cmd = [cs_python, str(Path(__file__).resolve().parent / "cs_index_once.py"),
           "--code-search-root", cs_root, "--repo", str(repo_dir),
           "--storage-dir", str(storage)]
    if force_full:
        cmd.append("--force-full")
    env = allowlisted_child_env(
        RUNTIME_CHILD_ENV_KEYS
        | {
            "VOYAGE_API_KEY",
            "VOYAGE_EMBED_MODEL",
            "EMBEDDING_PROVIDER",
            "EMBEDDING_MODEL",
        },
        overrides={
            "EMBEDDING_PROVIDER": "voyage",
            "EMBEDDING_MODEL": embedding_model,
        },
    )
    try:
        r = subprocess.run(cmd, capture_output=True, timeout=timeout, env=env)
    except subprocess.TimeoutExpired:
        return RetrievalIndexOutcome(False, "index timed out")
    if r.returncode != 0:
        typed_failure = _typed_child_failure(r)
        if typed_failure is not None:
            failure_class, failure_code, error = typed_failure
            return RetrievalIndexOutcome(
                False,
                error,
                failure_class=failure_class,
                failure_code=failure_code,
            )
        return RetrievalIndexOutcome(
            False,
            r.stderr.decode("utf-8", errors="replace")[-300:],
        )
    try:
        payload = json.loads(r.stdout.decode("utf-8", errors="replace"))
    except json.JSONDecodeError as exc:
        return RetrievalIndexOutcome(False, f"index response is not JSON: {exc}")
    if not isinstance(payload, dict) or payload.get("success") is not True:
        return RetrievalIndexOutcome(
            False,
            str(payload.get("error", "index did not report success"))
            if isinstance(payload, dict)
            else "index response is not an object",
        )
    if payload.get("index_identity_status") != "ready":
        return RetrievalIndexOutcome(
            False,
            "index identity status is not ready",
            failure_class="invalid_experiment",
            failure_code="index_identity_invalid",
        )
    identity = payload.get("index_identity")
    identity_error = _validate_index_identity(identity)
    if identity_error:
        return RetrievalIndexOutcome(
            False,
            f"index identity is invalid: {identity_error}",
            failure_class="invalid_experiment",
            failure_code="index_identity_invalid",
        )
    embedding, embedding_error = _validated_embedding_identity(
        payload.get("embedding_identity"),
        expected_model=embedding_model,
    )
    if embedding_error:
        return RetrievalIndexOutcome(
            False,
            embedding_error,
            failure_class="invalid_experiment",
            failure_code="embedding_identity_invalid",
        )
    chunks_added = payload.get("chunks_added")
    if (
        isinstance(chunks_added, bool)
        or not isinstance(chunks_added, int)
        or chunks_added < 0
    ):
        return RetrievalIndexOutcome(False, "chunks_added is invalid")
    return RetrievalIndexOutcome(
        True,
        chunks_added=chunks_added,
        index_identity=dict(identity),
        embedding_identity=embedding,
    )


def search_once(
    cs_python: str,
    cs_root: str,
    storage: Path,
    query: str,
    embedding_model: str = "voyage-4-large",
    reranker: str = "sonnet",
    k: int = 50,
    timeout: int = 300,
) -> RetrievalSearchOutcome:
    cmd = [cs_python, str(Path(__file__).resolve().parent / "cs_search_once.py"),
           "--code-search-root", cs_root, "--storage-dir", str(storage),
           "--query", query, "--k", str(k)]
    env = allowlisted_child_env(
        RUNTIME_CHILD_ENV_KEYS
        | {
            "ANTHROPIC_API_KEY",
            "EMBEDDING_MODEL",
            "EMBEDDING_PROVIDER",
            "RERANKER",
            "VOYAGE_API_KEY",
            "VOYAGE_EMBED_MODEL",
        },
        overrides={
            "EMBEDDING_PROVIDER": "voyage",
            "EMBEDDING_MODEL": embedding_model,
            "RERANKER": reranker,
        },
    )
    r = subprocess.run(cmd, capture_output=True, timeout=timeout, env=env)
    if r.returncode != 0:
        typed_failure = _typed_child_failure(r)
        if typed_failure is not None:
            failure_class, failure_code, error = typed_failure
            if failure_class == "invalid_experiment":
                raise InvalidExperimentError(error, failure_code)
            raise RetrievalInfrastructureError(error, failure_code)
        raise RuntimeError(f"search failed: {r.stderr.decode('utf-8', errors='replace')[-300:]}")
    try:
        payload = json.loads(r.stdout.decode("utf-8", errors="replace"))
    except json.JSONDecodeError as exc:
        raise InvalidExperimentError(
            f"search response is not JSON: {exc}",
            "search_response_invalid",
        ) from exc
    if not isinstance(payload, dict):
        raise InvalidExperimentError(
            "search response is not an object",
            "search_response_invalid",
        )
    results = payload.get("results")
    effective_search = payload.get("effective_search")
    if not isinstance(effective_search, dict):
        raise InvalidExperimentError(
            "search response is missing effective identity",
            "search_response_invalid",
        )
    normalized_results = validate_serialized_search_results(results)
    rank_evidence = validate_rank_evidence(
        payload.get("rank_evidence"),
        requested_k=k,
        returned_count=len(normalized_results),
    )
    effective_embedding = effective_search.get("embedding")
    embedding, embedding_error = _validated_embedding_identity(
        effective_embedding,
        expected_model=embedding_model,
    )
    if embedding_error:
        failure_code = (
            "query_embedding_identity_mismatch"
            if _embedding_identity_has_complete_schema(effective_embedding)
            else "query_embedding_identity_invalid"
        )
        raise InvalidExperimentError(embedding_error, failure_code)
    reranker_identity = validate_effective_reranker_identity(
        effective_search.get("reranker"),
        requested_reranker=reranker,
    )
    applied = reranker_identity.get("applied")
    reason = reranker_identity.get("reason")
    normalized_effective = {
        "embedding": embedding,
        "reranker": dict(reranker_identity),
    }
    if applied and reason != "ok":
        raise EffectiveSearchError(
            f"reranker applied with unexpected reason {reason}",
            normalized_effective,
        )
    policy_skips = {
        "skipped_high_confidence",
        "not_invoked_insufficient_candidates",
        "not_invoked_no_candidates",
    }
    if reranker in {"sonnet", "listwise"} and not applied and reason not in policy_skips:
        raise EffectiveSearchError(
            f"effective reranker fallback: {reason}",
            normalized_effective,
        )
    return RetrievalSearchOutcome(
        results=normalized_results,
        effective_search=normalized_effective,
        rank_evidence=rank_evidence,
    )


def checkout(repo_dir: Path, commit: str) -> bool:
    r = subprocess.run(["git", "-C", str(repo_dir), "checkout", "--quiet", commit],
                       capture_output=True, timeout=120,
                       env=allowlisted_child_env(RUNTIME_CHILD_ENV_KEYS))
    return r.returncode == 0


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--pin", required=True, type=Path)
    ap.add_argument("--parquet", required=True, type=Path)
    ap.add_argument("--workdir", required=True, type=Path)
    ap.add_argument("--out", required=True, type=Path)
    ap.add_argument(
        "--resume-from",
        type=Path,
        help="Prior shard checkpoint; rejected unless its full provenance matches",
    )
    ap.add_argument("--graph-sha", required=True)
    ap.add_argument(
        "--repository",
        help="run exactly one repository shard from the canonical pin",
    )
    ap.add_argument("--cs-root", default=str(Path.home() / "Documents/GitHub/code-search"))
    ap.add_argument("--cs-python", default=str(Path.home() / "Documents/GitHub/code-search/.venv/bin/python"))
    ap.add_argument("--embedding-model", default="voyage-4-large")
    ap.add_argument("--reranker", default="sonnet")
    ap.add_argument("--top-files", type=int, default=15)
    ap.add_argument("--score-depth", type=int, default=10)
    ap.add_argument("--max-transient-attempts", type=int, default=3)
    ap.add_argument(
        "--voyage-ceiling-usd",
        required=True,
        help="Exact Decimal allocation for this retrieval shard",
    )
    ap.add_argument(
        "--arm-ceiling-usd",
        required=True,
        help="Exact Decimal ceiling for the complete retrieval arm",
    )
    ap.add_argument(
        "--total-ceiling-usd",
        required=True,
        help="Exact Decimal ceiling for the complete experiment",
    )
    ap.add_argument(
        "--provider-operation-bound-policy",
        required=True,
        help="Required provider-operation-bound policy identity",
    )
    args = ap.parse_args()

    if hasattr(sys.stdout, "reconfigure"):
        sys.stdout.reconfigure(line_buffering=True)

    pin = json.loads(args.pin.read_text(encoding="utf-8"))
    budget_contract = build_retrieval_budget_contract(
        shard_allocation_usd=args.voyage_ceiling_usd,
        arm_ceiling_usd=args.arm_ceiling_usd,
        total_ceiling_usd=args.total_ceiling_usd,
        provider_operation_bound_policy=args.provider_operation_bound_policy,
    )
    shard_budget_usd = Decimal(args.voyage_ceiling_usd)
    provenance = build_provenance(
        pin,
        args.pin,
        args.graph_sha,
        args.score_depth,
        args.embedding_model,
        args.reranker,
        budget_contract=budget_contract,
    )
    required_credentials = ["VOYAGE_API_KEY"]
    if args.reranker in {"sonnet", "listwise"}:
        required_credentials.append("ANTHROPIC_API_KEY")
    missing_credentials = [
        name for name in required_credentials if not os.environ.get(name)
    ]
    if missing_credentials:
        write_json_checkpoint(
            args.out,
            {
                "schema_version": 2,
                "arm": "retrieval-only",
                "status": "not_evaluated",
                "reason_code": "missing_credentials",
                "missing_credentials": missing_credentials,
                "total_cost_usd": 0.0,
                "provenance": provenance,
                "cases": [],
            },
        )
        print(
            "ERROR: retrieval-only not evaluated; required credentials are absent",
            file=sys.stderr,
        )
        return 2
    if not args.parquet.is_file():
        write_json_checkpoint(
            args.out,
            {
                "schema_version": 2,
                "arm": "retrieval-only",
                "status": "not_evaluated",
                "reason_code": "dataset_missing",
                "message": f"pinned parquet is missing: {args.parquet}",
                "total_cost_usd": 0.0,
                "provenance": provenance,
                "cases": [],
            },
        )
        print("ERROR: retrieval-only not evaluated; pinned parquet is missing", file=sys.stderr)
        return 2
    dataset_digest = sha256_file(args.parquet)
    if dataset_digest != provenance["dataset_sha256"]:
        write_json_checkpoint(
            args.out,
            {
                "schema_version": 2,
                "arm": "retrieval-only",
                "status": "not_evaluated",
                "reason_code": "dataset_digest_mismatch",
                "actual_dataset_sha256": dataset_digest,
                "total_cost_usd": 0.0,
                "provenance": provenance,
                "cases": [],
            },
        )
        print(
            "ERROR: retrieval-only not evaluated; dataset digest does not match pin",
            file=sys.stderr,
        )
        return 2
    df = pd.read_parquet(args.parquet)
    try:
        _all_rows, selected_rows, expected_instance_ids, query_digest = prepare_dataset(
            df,
            pin,
            repository=args.repository,
        )
    except ValueError as exc:
        write_json_checkpoint(
            args.out,
            {
                "schema_version": 2,
                "arm": "retrieval-only",
                "status": "not_evaluated",
                "reason_code": "dataset_contract_mismatch",
                "message": str(exc),
                "total_cost_usd": 0.0,
                "provenance": provenance,
                "cases": [],
            },
        )
        print(f"ERROR: retrieval-only not evaluated; {exc}", file=sys.stderr)
        return 2
    provenance["query_sha256"] = query_digest
    provenance["repository"] = args.repository or "all"
    print(
        f"Retrieval-only: {len(selected_rows)} cases in "
        f"{args.repository or 'all repositories'}"
    )

    by_repo: dict[str, list[dict]] = defaultdict(list)
    for row in selected_rows:
        by_repo[row["repo"]].append(row)
    print(f"{len(by_repo)} distinct repos (one clone + one index storage each)")

    args.workdir.mkdir(parents=True, exist_ok=True)
    resume_path = args.resume_from or args.out
    if args.resume_from is not None and not args.resume_from.is_file():
        write_json_checkpoint(
            args.out,
            {
                "schema_version": 2,
                "arm": "retrieval-only",
                "status": "not_evaluated",
                "reason_code": "resume_checkpoint_missing",
                "message": f"requested resume checkpoint is missing: {args.resume_from}",
                "total_cost_usd": 0.0,
                "provenance": provenance,
                "cases": [],
            },
        )
        print("ERROR: requested retrieval resume checkpoint is missing", file=sys.stderr)
        return 2
    try:
        cases = load_resume_cases(
            resume_path,
            provenance,
            expected_instance_ids,
            allow_implicit_empty=args.resume_from is None,
        )
    except (OSError, ValueError, TypeError, KeyError, json.JSONDecodeError) as exc:
        write_json_checkpoint(
            args.out,
            {
                "schema_version": 2,
                "arm": "retrieval-only",
                "status": "not_evaluated",
                "reason_code": "resume_checkpoint_rejected",
                "message": str(exc),
                "total_cost_usd": 0.0,
                "provenance": provenance,
                "cases": [],
            },
        )
        print(f"ERROR: retrieval resume checkpoint rejected: {exc}", file=sys.stderr)
        return 2
    completed_ids = {case["instance_id"] for case in cases}
    estimated_cost_usd = sum_case_cost_estimates(cases)
    aborted = ""

    def checkpoint() -> dict:
        artifact = build_run_artifact(
            provenance,
            expected_instance_ids,
            cases,
            aborted_reason=aborted,
        )
        write_json_checkpoint(args.out, artifact)
        return artifact

    for repo, instances in by_repo.items():
        if aborted:
            break
        slug = repo.replace("/", "__")
        repo_dir = args.workdir / slug
        storage = args.workdir / f"{slug}-cs-storage"
        cloned = False
        first_in_repo = True
        try:
            for row in instances:
                if row["instance_id"] in completed_ids:
                    print(f"\n=== resume: {row['instance_id']} already checkpointed ===")
                    continue
                if Decimal(str(estimated_cost_usd)) >= shard_budget_usd:
                    aborted = (f"estimated-cost threshold ${shard_budget_usd:.2f} hit at "
                               f"~${estimated_cost_usd:.2f} after {len(cases)} instances")
                    print(f"\n!!! {aborted}")
                    break

                iid = row["instance_id"]
                gt = list(row.get("edit_functions", []))
                case = {
                    "instance_id": iid, "repo": repo,
                    "category": row.get("category", "Unknown"),
                    "ground_truth": gt, "indexed": False,
                    "file_hit": False, "class_hit": False, "func_hit": False,
                    "top_files": [], "chunks_added": 0, "note": "",
                }
                case_attempts: list[dict] = []
                chunks = 0
                index_outcome: RetrievalIndexOutcome | None = None
                effective_search: dict = {}
                rank_evidence: dict = {}
                query_attempted = False
                stage_latency_s: dict[str, float] = {}
                t0 = time.monotonic()
                print(f"\n=== retrieval-only {iid} ({repo}, {case['category']}) ===")
                try:
                    if not cloned:
                        clone_attempts: list[dict] = []
                        clone_started = time.monotonic()
                        clone_succeeded = clone_repo(
                            repo,
                            row["base_commit"],
                            repo_dir,
                            max_attempts=args.max_transient_attempts,
                            attempts_out=clone_attempts,
                        )
                        stage_latency_s["clone"] = (
                            time.monotonic() - clone_started
                        )
                        if not clone_succeeded:
                            apply_direct_failure(
                                case,
                                failure_class="infrastructure",
                                failure_code="clone_failed",
                                note="clone failed",
                            )
                            case_attempts.extend(clone_attempts)
                            continue
                        case_attempts.extend(clone_attempts)
                        size_mb = repo_size_mb(repo_dir)
                        if size_mb > MAX_REPO_MB:
                            apply_direct_failure(
                                case,
                                failure_class="measured_outcome",
                                failure_code="repo_too_large",
                                note=f"repo too large ({size_mb:.0f} MB)",
                            )
                            case_attempts.append(
                                {
                                    "operation": "size-check",
                                    "attempt": 1,
                                    "outcome": "error",
                                    "transient": False,
                                    "retry": False,
                                }
                            )
                            # Bail to the next repo after finally checkpoints once.
                            break
                        cloned = True
                    else:
                        checkout_started = time.monotonic()
                        checkout_succeeded = checkout(repo_dir, row["base_commit"])
                        stage_latency_s["clone"] = (
                            time.monotonic() - checkout_started
                        )
                        if not checkout_succeeded:
                            apply_direct_failure(
                                case,
                                failure_class="infrastructure",
                                failure_code="checkout_failed",
                                note="checkout failed",
                            )
                            case_attempts.append(
                                {
                                    "operation": "checkout",
                                    "attempt": 1,
                                    "outcome": "error",
                                    "transient": False,
                                    "retry": False,
                                }
                            )
                            continue

                    def index_operation() -> RetrievalIndexOutcome:
                        outcome = index_with_code_search(
                            args.cs_python,
                            args.cs_root,
                            repo_dir,
                            storage,
                            force_full=first_in_repo,
                            embedding_model=args.embedding_model,
                        )
                        require_valid_index_outcome(outcome)
                        require_pinned_index_identity(
                            outcome,
                            row["base_commit"],
                        )
                        return outcome

                    index_started = time.monotonic()
                    try:
                        index_outcome, index_attempts = run_with_transient_retries(
                            "index",
                            index_operation,
                            max_attempts=args.max_transient_attempts,
                        )
                    finally:
                        stage_latency_s["index"] = (
                            time.monotonic() - index_started
                        )
                    case_attempts.extend(index_attempts)
                    first_in_repo = False
                    chunks = index_outcome.chunks_added
                    case["chunks_added"] = chunks
                    case["indexed"] = True

                    # Same query convention as the graph arm: first paragraph only.
                    short_query = row["problem_statement"].split("\n\n")[0].strip()
                    query_attempted = True
                    query_started = time.monotonic()
                    try:
                        search_outcome, search_attempts = run_with_transient_retries(
                            "search",
                            lambda: search_once(
                                args.cs_python,
                                args.cs_root,
                                storage,
                                short_query,
                                embedding_model=args.embedding_model,
                                reranker=args.reranker,
                                k=args.score_depth,
                            ),
                            max_attempts=args.max_transient_attempts,
                        )
                    finally:
                        stage_latency_s["marginal_query"] = (
                            time.monotonic() - query_started
                        )
                    case_attempts.extend(search_attempts)
                    effective_search = dict(search_outcome.effective_search)
                    rank_evidence = dict(search_outcome.rank_evidence)

                    successful_case = build_retrieval_case(
                        row,
                        search_outcome.results,
                        chunks_added=chunks,
                        duration_s=time.monotonic() - t0,
                        latency_s={
                            **stage_latency_s,
                            "total": time.monotonic() - t0,
                        },
                        provenance=provenance,
                        attempts=case_attempts,
                        index_identity=index_outcome.index_identity,
                        embedding_identity=index_outcome.embedding_identity,
                        effective_search=search_outcome.effective_search,
                        rank_evidence=search_outcome.rank_evidence,
                    )
                    case.clear()
                    case.update(successful_case)
                except Exception as exc:  # noqa: BLE001 — record and continue the batch
                    if isinstance(exc, EffectiveSearchError):
                        effective_search = dict(exc.effective_search)
                    case_attempts.extend(
                        getattr(exc, "matched_depth_attempts", [])
                    )
                    apply_failure_to_case(case, exc)
                finally:
                    duration_s = time.monotonic() - t0
                    stage_latency_s["total"] = duration_s
                    if case.get("status") != "ok":
                        note = case.get("note") or "retrieval failed"
                        indexed = bool(case.get("indexed"))
                        failure_class = str(case.get("failure_class", ""))
                        failure_code = str(case.get("failure_code", ""))
                        case.clear()
                        case.update(
                            build_retrieval_failure_case(
                                row,
                                provenance=provenance,
                                note=note,
                                indexed=indexed,
                                chunks_added=chunks,
                                query_attempted=query_attempted,
                                duration_s=duration_s,
                                latency_s=stage_latency_s,
                                attempts=case_attempts,
                                failure_class=failure_class,
                                failure_code=failure_code,
                                index_identity=(
                                    index_outcome.index_identity
                                    if index_outcome is not None
                                    else None
                                ),
                                embedding_identity=(
                                    index_outcome.embedding_identity
                                    if index_outcome is not None
                                    else None
                                ),
                                effective_search=effective_search,
                                rank_evidence=rank_evidence,
                            )
                        )
                    case["duration_s"] = round(duration_s, 1)
                    case["latency_s"]["total"] = round(duration_s, 6)
                    cases.append(case)
                    if case.get("failure_class") == "invalid_experiment":
                        aborted = (
                            f"invalid_experiment:"
                            f"{case.get('failure_code', 'unspecified')}:{iid}"
                        )
                    estimated_cost_usd = sum_case_cost_estimates(cases)
                    checkpoint()
                    print(
                        f"  -> indexed={case['indexed']} file_hit={case['file_hit']} "
                        f"class_hit={case['class_hit']} func_hit={case['func_hit']} "
                        f"files={len(case['top_files'])} chunks={case['chunks_added']} "
                        f"~estimated=${estimated_cost_usd:.2f} "
                        f"({case['duration_s']}s) {case['note']}"
                    )
                if aborted:
                    break
        finally:
            shutil.rmtree(repo_dir, ignore_errors=True)
            shutil.rmtree(storage, ignore_errors=True)

    n = len(cases)
    if n:
        print(
            f"\nRetrieval-only done: n={n} indexed={sum(c['indexed'] for c in cases)} "
            f"file={sum(c['file_hit'] for c in cases)}/{n} "
            f"class={sum(c['class_hit'] for c in cases)}/{n} "
            f"func={sum(c['func_hit'] for c in cases)}/{n} "
            f"~estimated=${estimated_cost_usd:.2f} "
            f"{('ABORTED: ' + aborted) if aborted else ''}"
        )
    artifact = checkpoint()
    if artifact["aborted_reason"].startswith("invalid_experiment:"):
        return 2
    return 0 if artifact["status"] == "complete" else 1


if __name__ == "__main__":
    raise SystemExit(main())
