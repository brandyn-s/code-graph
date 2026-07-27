#!/usr/bin/env python3
"""Normalize and score matched-depth retrieval-only versus graph artifacts.

The reducer accepts only exact canonical-pin coverage, keeps failed cases as
intent-to-treat misses, and computes paired bootstrap intervals over all 200
instances. It never intersects only operationally successful cases, and it
will not issue a preference when the arms use different marginal-cost bases.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import math
import random
import sys
from decimal import Decimal, InvalidOperation
from pathlib import Path

from eval_locbench_batch import _validate_index_identity

METRICS = ("file_hit", "class_hit", "func_hit")
COMMON_PROVENANCE_KEYS = (
    "graph_sha",
    "code_search",
    "dataset_sha256",
    "dataset_revision",
    "pin_sha256",
    "query_sha256",
    "scorer_sha256",
    "score_depth",
)
USD_QUANTUM = Decimal("0.000001")
RETRIEVAL_BUDGET_FIELDS = {
    "shard_allocation_usd",
    "arm_ceiling_usd",
    "total_ceiling_usd",
    "provider_operation_bound_policy",
}
PROVIDER_OPERATION_BOUND_POLICY = (
    "provider-enforced-per-operation-usd-required-v1"
)


def _exact_nonnegative_usd(name: str, value: object) -> Decimal:
    if not isinstance(value, str) or not value or value.strip() != value:
        raise ValueError(f"{name} must be an exact decimal string")
    try:
        parsed = Decimal(value)
        quantized = parsed.quantize(USD_QUANTUM)
    except InvalidOperation as exc:
        raise ValueError(f"{name} must be an exact decimal string") from exc
    if not parsed.is_finite() or parsed < 0 or parsed != quantized:
        raise ValueError(
            f"{name} must be finite, nonnegative, and use at most six decimals"
        )
    return parsed


def _canonical_usd(value: Decimal) -> str:
    return format(value.quantize(USD_QUANTUM), "f")


def _validate_artifact_shape(
    artifact: dict,
    *,
    arm: str,
    pinned_ids: list[str],
) -> None:
    if artifact.get("arm") != arm:
        raise ValueError(f"expected {arm} artifact, got {artifact.get('arm')!r}")
    if artifact.get("status") != "complete":
        raise ValueError(f"{arm} artifact is not complete")
    cases = artifact.get("cases")
    if not isinstance(cases, list):
        raise ValueError(f"{arm} cases must be a list")
    case_ids = [str(case.get("instance_id", "")) for case in cases]
    duplicate_ids = sorted(
        {instance_id for instance_id in case_ids if case_ids.count(instance_id) > 1}
    )
    if duplicate_ids:
        raise ValueError(f"{arm} has duplicate case IDs: {duplicate_ids}")
    missing = sorted(set(pinned_ids) - set(case_ids))
    extras = sorted(set(case_ids) - set(pinned_ids))
    if missing or extras or len(case_ids) != len(pinned_ids):
        raise ValueError(
            f"{arm} coverage mismatch: missing={missing} extras={extras} "
            f"accounted={len(case_ids)} expected={len(pinned_ids)}"
        )
    depth = artifact.get("provenance", {}).get("score_depth")
    if not isinstance(depth, int) or depth < 1:
        raise ValueError(f"{arm} score depth is missing or invalid")
    for case in cases:
        failure_class = str(case.get("failure_class", ""))
        failure_code = str(case.get("failure_code", ""))
        if failure_class == "invalid_experiment":
            raise ValueError(
                f"{arm} case {case.get('instance_id')} is invalid_experiment: "
                f"{failure_code or 'unspecified'}"
            )
        if failure_class not in ("", "infrastructure", "measured_outcome"):
            raise ValueError(
                f"{arm} case {case.get('instance_id')} has unknown failure "
                f"classification {failure_class!r}"
            )
        if case.get("status") != "ok" and (not failure_class or not failure_code):
            raise ValueError(
                f"{arm} case {case.get('instance_id')} is missing a typed "
                "failure classification"
            )
        if case.get("status") == "ok" and failure_class:
            raise ValueError(
                f"{arm} case {case.get('instance_id')} is successful but has "
                f"failure classification {failure_class!r}"
            )
        ranks = case.get("results")
        if not isinstance(ranks, list):
            raise ValueError(f"{arm} case {case.get('instance_id')} has missing ranks")
        actual_ranks = [rank.get("rank") for rank in ranks if isinstance(rank, dict)]
        if actual_ranks != list(range(1, depth + 1)):
            raise ValueError(
                f"{arm} case {case.get('instance_id')} has missing ranks: "
                f"{actual_ranks}"
            )
        if arm == "retrieval-only" and case.get("status") == "ok":
            evidence = case.get("rank_evidence")
            if not isinstance(evidence, dict):
                raise ValueError(
                    f"{arm} case {case.get('instance_id')} rank-window evidence "
                    "is absent"
                )
            counts: dict[str, int] = {}
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
                    raise ValueError(
                        f"{arm} case {case.get('instance_id')} rank-window "
                        f"{field_name} is invalid"
                    )
                counts[field_name] = value
            if not isinstance(evidence.get("truncated"), bool):
                raise ValueError(
                    f"{arm} case {case.get('instance_id')} rank-window "
                    "truncation is not explicit"
                )
            available_count = sum(
                rank.get("available") is True for rank in ranks
            )
            effective_k = min(depth, counts["total_candidates"])
            if (
                counts["requested_k"] != depth
                or counts["returned_count"] != available_count
                or counts["effective_k"] != effective_k
                or available_count != effective_k
                or evidence["truncated"] != (
                    counts["total_candidates"] > effective_k
                )
            ):
                raise ValueError(
                    f"{arm} case {case.get('instance_id')} rank-window "
                    "evidence is inconsistent"
                )
            if any(
                rank.get("available") is not (rank["rank"] <= available_count)
                for rank in ranks
            ):
                raise ValueError(
                    f"{arm} case {case.get('instance_id')} rank-window "
                    "availability is non-contiguous"
                )


def validate_comparison(graph: dict, retrieval: dict, pin: dict) -> None:
    """Fail closed before scoring any incomplete or unequal-depth comparison."""
    pinned_ids = list(pin.get("pinned_instance_ids", []))
    if pin.get("n") != len(pinned_ids) or len(pinned_ids) < 200:
        raise ValueError(
            f"intent-to-treat pin must contain at least 200 unique cases; "
            f"n={pin.get('n')} ids={len(pinned_ids)}"
        )
    if len(set(pinned_ids)) != len(pinned_ids):
        raise ValueError("intent-to-treat pin contains duplicate IDs")
    _validate_artifact_shape(graph, arm="graph", pinned_ids=pinned_ids)
    _validate_artifact_shape(retrieval, arm="retrieval-only", pinned_ids=pinned_ids)
    graph_depth = graph["provenance"]["score_depth"]
    retrieval_depth = retrieval["provenance"]["score_depth"]
    pin_depth = pin.get("score_depth")
    if graph_depth != retrieval_depth or graph_depth != pin_depth:
        raise ValueError(
            "score depth mismatch: "
            f"graph={graph_depth} retrieval-only={retrieval_depth} pin={pin_depth}"
        )
    for key in COMMON_PROVENANCE_KEYS:
        graph_value = graph["provenance"].get(key)
        retrieval_value = retrieval["provenance"].get(key)
        if graph_value in (None, "", {}) or retrieval_value in (None, "", {}):
            raise ValueError(f"required provenance field {key} is missing")
        if graph_value != retrieval_value:
            raise ValueError(
                f"provenance {key} mismatch: "
                f"graph={graph_value!r} retrieval-only={retrieval_value!r}"
            )
    expected_code_search = {
        "tag": "v0.2.1",
        "artifact_sha256": (
            "567d4caabdd3b5446bcaa789afc7104fb8cce142ff69d7fc8f1294398532e7e9"
        ),
    }
    if graph["provenance"]["code_search"] != expected_code_search:
        raise ValueError("code-search provenance is not the pinned v0.2.1 artifact")
    for arm_name, artifact in (("graph", graph), ("retrieval-only", retrieval)):
        for field in ("model", "reranker"):
            if artifact["provenance"].get(field) in (None, ""):
                raise ValueError(f"{arm_name} provenance field {field} is missing")


def _derive_hits(results: list[dict], ground_truths: list[str]) -> tuple[bool, bool, bool]:
    blob = "\n".join(
        f"{rank.get('parent_name', '')}.{rank.get('name', '')} "
        f"{rank.get('relative_path', '')}"
        for rank in results
        if rank.get("available")
    )
    file_hit = class_hit = func_hit = False
    for ground_truth in ground_truths:
        if ":" not in ground_truth:
            continue
        file_part, symbol_part = ground_truth.split(":", 1)
        if file_part in blob:
            file_hit = True
        symbol_components = symbol_part.split(".")
        if len(symbol_components) >= 2:
            if symbol_components[0] in blob:
                class_hit = True
            if symbol_components[-1] in blob:
                func_hit = True
        elif symbol_part in blob:
            func_hit = True
    return file_hit, class_hit, func_hit


def normalize_graph_case(raw: dict, row: dict, provenance: dict) -> dict:
    """Convert one eval_locbench_batch case to the common ten-rank schema."""
    if raw.get("failure_class") == "invalid_experiment":
        raise ValueError(
            f"graph case {row['instance_id']} is invalid_experiment: "
            f"{raw.get('failure_code', 'unspecified')}"
        )
    depth = int(provenance["score_depth"])
    indexed = bool(raw.get("indexed"))
    index_identity: dict = {}
    embedding_identity: dict = {}
    if indexed:
        observed_identity = raw.get("index_identity")
        if not isinstance(observed_identity, dict):
            identity_error = _validate_index_identity(observed_identity)
            raise ValueError(
                f"graph case {row['instance_id']} has invalid observed index "
                f"identity: {identity_error}"
            )
        if (
            observed_identity.get("source_revision") != row["base_commit"]
            or observed_identity.get("dirty_fingerprint") != "clean"
        ):
            raise ValueError(
                f"graph case {row['instance_id']} index identity does not match "
                "the clean pinned checkout"
            )
        identity_error = _validate_index_identity(observed_identity)
        if identity_error:
            raise ValueError(
                f"graph case {row['instance_id']} has invalid observed index "
                f"identity: {identity_error}"
            )
        observed_embedding = raw.get("embedding_identity")
        if not isinstance(observed_embedding, dict):
            raise ValueError(
                f"graph case {row['instance_id']} has no observed embedding identity"
            )
        embedding_count = observed_embedding.get("count")
        if (
            observed_embedding.get("status") != "captured"
            or isinstance(embedding_count, bool)
            or not isinstance(embedding_count, int)
            or embedding_count < 1
            or observed_embedding.get("model") != provenance.get("embedding_model")
        ):
            raise ValueError(
                f"graph case {row['instance_id']} embedding identity does not "
                "match the requested semantic model"
            )
        index_identity = dict(observed_identity)
        embedding_identity = dict(observed_embedding)
    agent = raw.get("agent_envelope", {}).get("code_localize_agent") or {}
    entities = agent.get("entities") if isinstance(agent, dict) else []
    if not isinstance(entities, list):
        entities = []
    selected = entities[:depth]
    ranks: list[dict] = []
    for rank_number in range(1, depth + 1):
        if rank_number <= len(selected) and isinstance(selected[rank_number - 1], dict):
            entity = selected[rank_number - 1]
            qualified_name = str(entity.get("qualified_name", ""))
            components = qualified_name.split(".")
            name = components[-1] if components else ""
            parent_name = components[-2] if len(components) >= 2 else ""
            ranks.append(
                {
                    "rank": rank_number,
                    "available": True,
                    "relative_path": str(entity.get("file_path", "")),
                    "parent_name": parent_name,
                    "name": name,
                    "score": None,
                }
            )
        else:
            ranks.append(
                {
                    "rank": rank_number,
                    "available": False,
                    "relative_path": "",
                    "parent_name": "",
                    "name": "",
                    "score": None,
                }
            )
    successful = indexed and bool(raw.get("agent_ran"))
    ground_truth = list(row.get("edit_functions", []))
    hits = _derive_hits(ranks, ground_truth) if successful else (False, False, False)
    query = row["problem_statement"].split("\n\n")[0].strip()
    file_hit, class_hit, func_hit = hits
    cost = float(raw.get("cost_estimate_usd", 0.0))
    if not _finite_nonnegative_number(cost):
        raise ValueError(
            f"graph case {row['instance_id']} has invalid marginal cost evidence"
        )
    input_tokens = raw.get("input_tokens", 0)
    output_tokens = raw.get("output_tokens", 0)
    if (
        successful
        and isinstance(input_tokens, int)
        and not isinstance(input_tokens, bool)
        and isinstance(output_tokens, int)
        and not isinstance(output_tokens, bool)
        and (input_tokens > 0 or output_tokens > 0)
    ):
        marginal_cost_basis = {
            "method": "reported token usage multiplied by static model prices",
            "measurement_basis": "reported-token-usage-static-price-v1",
            "model": provenance["model"],
        }
    elif raw.get("agent_ran"):
        marginal_cost_basis = {
            "method": "fixed fallback estimate because token usage was absent",
            "measurement_basis": "fixed-graph-fallback-estimate-v1",
            "model": provenance["model"],
        }
    else:
        marginal_cost_basis = {
            "method": "query stage not completed",
            "measurement_basis": "not-incurred-v1",
            "model": provenance["model"],
        }
    raw_latency = raw.get("latency_s", {})
    if raw_latency is None:
        raw_latency = {}
    if not isinstance(raw_latency, dict):
        raise ValueError(
            f"graph case {row['instance_id']} has malformed stage latency evidence"
        )
    latency_s: dict[str, float] = {}
    for stage in ("clone", "index", "marginal_query", "total"):
        value = raw_latency.get(stage)
        if value is None:
            continue
        if not _finite_nonnegative_number(value):
            raise ValueError(
                f"graph case {row['instance_id']} has invalid {stage} latency"
            )
        latency_s[stage] = round(float(value), 6)
    duration_s = raw.get("duration_s", 0.0)
    if not _finite_nonnegative_number(duration_s):
        raise ValueError(
            f"graph case {row['instance_id']} has invalid total duration"
        )
    latency_s.setdefault("total", round(float(duration_s), 6))
    latency_basis: dict[str, dict] = {
        "full_run": {
            "method": "wall clock from clone start through cleanup",
            "measurement_basis": "graph-full-case-v1",
        }
    }
    if "marginal_query" in latency_s:
        latency_basis["marginal_query"] = {
            "method": "wall clock for query stage including bounded retries",
            "measurement_basis": "wall-clock-query-stage-v1",
        }
    raw_attempts = raw.get("attempts", [])
    if not isinstance(raw_attempts, list) or any(
        not isinstance(attempt, dict) for attempt in raw_attempts
    ):
        raise ValueError(
            f"graph case {row['instance_id']} has malformed attempt evidence"
        )
    attempts = [dict(attempt) for attempt in raw_attempts]
    attempts.append(
        {
            "operation": "graph-agent",
            "attempt": 1,
            "outcome": "success" if successful else "miss",
            "retry": False,
        }
    )
    return {
        "instance_id": row["instance_id"],
        "repo": row["repo"],
        "base_commit": row["base_commit"],
        "category": row.get("category", "Unknown"),
        "ground_truth": ground_truth,
        "query_sha256": hashlib.sha256(query.encode("utf-8")).hexdigest(),
        "status": "ok" if successful else "miss",
        "failure_class": str(raw.get("failure_class", "")),
        "failure_code": str(raw.get("failure_code", "")),
        "indexed": indexed,
        "file_hit": file_hit,
        "class_hit": class_hit,
        "func_hit": func_hit,
        "results": ranks,
        "index_identity": index_identity,
        "embedding_identity": embedding_identity,
        "cost_usd": {
            "index_embedding_estimate": None,
            "marginal_query_estimate": round(cost, 6),
            "total_estimate": round(cost, 6),
            "total_estimate_scope": "marginal_query_only",
        },
        "cost_basis": {
            "index": {
                "method": "not metered by graph index response",
                "measurement_basis": None,
            },
            "marginal_query": marginal_cost_basis,
        },
        "duration_s": float(duration_s),
        "latency_s": latency_s,
        "latency_basis": latency_basis,
        "attempts": attempts,
        "note": str(raw.get("note", "")),
    }


def _score_case_from_ranks(case: dict) -> tuple[bool, bool, bool]:
    if case.get("status") != "ok":
        stored = tuple(bool(case.get(metric)) for metric in METRICS)
        if any(stored):
            raise ValueError(
                f"failed case {case.get('instance_id')} records a positive hit"
            )
        return False, False, False
    recomputed = _derive_hits(case["results"], case.get("ground_truth", []))
    stored = tuple(bool(case.get(metric)) for metric in METRICS)
    if stored != recomputed:
        raise ValueError(
            f"case {case.get('instance_id')} stored hits {stored} "
            f"do not match rank-derived hits {recomputed}"
        )
    return recomputed


def _finite_nonnegative_number(value: object) -> bool:
    return (
        not isinstance(value, bool)
        and isinstance(value, (int, float))
        and math.isfinite(float(value))
        and float(value) >= 0.0
    )


def summarize_marginal_cost(
    by_arm: dict[str, dict[str, dict]],
    instance_ids: list[str],
) -> dict:
    """Compare only marginal-query USD recorded on one shared basis.

    Total estimates are deliberately excluded: the graph artifact does not
    meter Voyage indexing, while retrieval records a chunk-based index
    estimate. Treating those totals as comparable would manufacture the
    pre-registered 90% result.
    """
    reasons: list[str] = []
    arms: dict[str, dict] = {}
    basis_keys: dict[str, str] = {}
    raw_totals: dict[str, float] = {}
    for arm in ("graph", "retrieval-only"):
        values: list[float] = []
        observed_keys: set[str] = set()
        for instance_id in instance_ids:
            case = by_arm[arm][instance_id]
            value = case.get("cost_usd", {}).get("marginal_query_estimate")
            if not _finite_nonnegative_number(value):
                reasons.append(f"{arm}:missing_or_invalid_marginal_query_cost")
                continue
            values.append(float(value))
            marginal_basis = case.get("cost_basis", {}).get("marginal_query", {})
            key = (
                marginal_basis.get("measurement_basis")
                if isinstance(marginal_basis, dict)
                else None
            )
            if not isinstance(key, str) or not key:
                reasons.append(f"{arm}:missing_marginal_query_cost_basis")
            else:
                observed_keys.add(key)
        if len(observed_keys) != 1:
            if observed_keys:
                reasons.append(f"{arm}:mixed_marginal_query_cost_basis")
        else:
            basis_keys[arm] = next(iter(observed_keys))
        total = math.fsum(values)
        raw_totals[arm] = total
        arms[arm] = {
            "marginal_query_total_usd": round(total, 6),
            "marginal_query_per_case_usd": round(total / len(instance_ids), 9),
            "measurement_basis": basis_keys.get(arm),
        }
    if (
        len(basis_keys) == 2
        and basis_keys["graph"] != basis_keys["retrieval-only"]
    ):
        reasons.append("cross_arm_marginal_query_cost_basis_mismatch")
    graph_total = raw_totals["graph"]
    if graph_total <= 0.0:
        reasons.append("graph_marginal_query_cost_is_not_positive")
    if reasons:
        return {
            "status": "not_comparable",
            "reasons": sorted(set(reasons)),
            **arms,
        }
    retrieval_total = raw_totals["retrieval-only"]
    ratio = retrieval_total / graph_total
    return {
        "status": "comparable",
        **arms,
        "retrieval_to_graph_ratio": ratio,
        "reduction_fraction": 1.0 - ratio,
    }


def summarize_marginal_latency(
    by_arm: dict[str, dict[str, dict]],
    instance_ids: list[str],
) -> dict:
    """Summarize wall-clock query-stage latency without comparing full runs."""
    reasons: list[str] = []
    arms: dict[str, dict] = {}
    basis_keys: dict[str, str] = {}
    for arm in ("graph", "retrieval-only"):
        values: list[float] = []
        observed_keys: set[str] = set()
        for instance_id in instance_ids:
            case = by_arm[arm][instance_id]
            value = case.get("latency_s", {}).get("marginal_query")
            if not _finite_nonnegative_number(value):
                reasons.append(f"{arm}:missing_or_invalid_marginal_query_latency")
                continue
            values.append(float(value))
            marginal_basis = case.get("latency_basis", {}).get(
                "marginal_query", {}
            )
            key = (
                marginal_basis.get("measurement_basis")
                if isinstance(marginal_basis, dict)
                else None
            )
            if not isinstance(key, str) or not key:
                reasons.append(f"{arm}:missing_marginal_query_latency_basis")
            else:
                observed_keys.add(key)
        if len(observed_keys) != 1:
            if observed_keys:
                reasons.append(f"{arm}:mixed_marginal_query_latency_basis")
        else:
            basis_keys[arm] = next(iter(observed_keys))
        arms[arm] = {
            "observed_cases": len(values),
            "marginal_query_total_s": round(sum(values), 6),
            "marginal_query_mean_s": (
                round(sum(values) / len(values), 6) if values else None
            ),
            "measurement_basis": basis_keys.get(arm),
        }
    if (
        len(basis_keys) == 2
        and basis_keys["graph"] != basis_keys["retrieval-only"]
    ):
        reasons.append("cross_arm_marginal_query_latency_basis_mismatch")
    if reasons:
        return {
            "status": "not_comparable",
            "reasons": sorted(set(reasons)),
            **arms,
        }
    return {"status": "comparable", **arms}


def summarize_failure_quality(
    by_arm: dict[str, dict[str, dict]],
    instance_ids: list[str],
) -> dict:
    summary: dict[str, dict] = {}
    for arm in ("graph", "retrieval-only"):
        infrastructure_failures = sum(
            by_arm[arm][instance_id].get("failure_class") == "infrastructure"
            for instance_id in instance_ids
        )
        summary[arm] = {
            "infrastructure_failures": infrastructure_failures,
            "infrastructure_fraction": infrastructure_failures / len(instance_ids),
            "falsifier_threshold": 0.05,
            "falsified": infrastructure_failures / len(instance_ids) > 0.05,
        }
    return summary


def classify_retrieval_decision(
    metric_summary: dict[str, dict],
    marginal_cost: dict,
) -> dict:
    """Apply the immutable Step 10 prefer/reject/inconclusive rule."""
    criteria = {
        "accuracy_ci_lower_bound_strictly_above": -0.05,
        "minimum_marginal_cost_reduction": 0.90,
        "required_bootstrap_resamples": 10_000,
        "material_accuracy_regression": (
            "paired_95ci_upper_bound_strictly_below_-0.05"
        ),
    }
    material_regressions: list[str] = []
    reasons: list[str] = []
    accuracy_ready = True
    bootstrap_ready = True
    for metric in METRICS:
        evidence = metric_summary.get(metric, {})
        interval = evidence.get("paired_bootstrap_95ci")
        if (
            not isinstance(interval, list)
            or len(interval) != 2
            or not all(
                not isinstance(value, bool)
                and isinstance(value, (int, float))
                and math.isfinite(float(value))
                for value in interval
            )
        ):
            reasons.append(f"accuracy_evidence_invalid:{metric}")
            accuracy_ready = False
            continue
        low, high = (float(interval[0]), float(interval[1]))
        if high < -0.05:
            material_regressions.append(metric)
        if low <= -0.05:
            accuracy_ready = False
        if evidence.get("bootstrap_resamples") != 10_000:
            bootstrap_ready = False
    if material_regressions:
        return {
            "verdict": "reject_retrieval_only",
            "criteria": criteria,
            "reasons": [
                f"material_accuracy_regression:{metric}"
                for metric in material_regressions
            ],
        }
    if not accuracy_ready and not any(
        reason.startswith("accuracy_evidence_invalid") for reason in reasons
    ):
        reasons.append("accuracy_noninferiority_not_established")
    if not bootstrap_ready:
        reasons.append("bootstrap_resamples_not_decision_grade")
    cost_ready = marginal_cost.get("status") == "comparable"
    if not cost_ready:
        reasons.append("marginal_cost_not_comparable")
    else:
        reduction = marginal_cost.get("reduction_fraction")
        if (
            isinstance(reduction, bool)
            or not isinstance(reduction, (int, float))
            or not math.isfinite(float(reduction))
            or float(reduction) < 0.90
        ):
            reasons.append("marginal_cost_reduction_below_threshold")
            cost_ready = False
    if accuracy_ready and bootstrap_ready and cost_ready and not reasons:
        return {
            "verdict": "prefer_retrieval_only",
            "criteria": criteria,
            "reasons": [],
        }
    return {
        "verdict": "inconclusive",
        "criteria": criteria,
        "reasons": reasons,
    }


def reduce_comparison(
    graph: dict,
    retrieval: dict,
    pin: dict,
    *,
    n_boot: int = 10_000,
    seed: int = 42,
) -> dict:
    """Reduce exactly the 200 pinned cases with failures retained as misses."""
    validate_comparison(graph, retrieval, pin)
    ids = list(pin["pinned_instance_ids"])
    by_arm = {
        "graph": {case["instance_id"]: case for case in graph["cases"]},
        "retrieval-only": {
            case["instance_id"]: case for case in retrieval["cases"]
        },
    }
    scored = {
        arm: {
            instance_id: _score_case_from_ranks(cases[instance_id])
            for instance_id in ids
        }
        for arm, cases in by_arm.items()
    }
    metric_summary: dict[str, dict] = {}
    for metric_index, metric in enumerate(METRICS):
        graph_values = [
            int(scored["graph"][instance_id][metric_index]) for instance_id in ids
        ]
        retrieval_values = [
            int(scored["retrieval-only"][instance_id][metric_index])
            for instance_id in ids
        ]
        deltas = [
            retrieval_value - graph_value
            for graph_value, retrieval_value in zip(
                graph_values, retrieval_values, strict=True
            )
        ]
        delta, low, high = paired_bootstrap(
            deltas,
            n_boot=n_boot,
            seed=seed,
        )
        graph_hits = sum(graph_values)
        retrieval_hits = sum(retrieval_values)
        metric_summary[metric] = {
            "graph": {
                "hits": graph_hits,
                "accuracy": graph_hits / len(ids),
            },
            "retrieval-only": {
                "hits": retrieval_hits,
                "accuracy": retrieval_hits / len(ids),
            },
            "delta_retrieval_minus_graph": delta,
            "paired_bootstrap_95ci": [low, high],
            "bootstrap_resamples": n_boot,
        }
    marginal_cost = summarize_marginal_cost(by_arm, ids)
    marginal_latency = summarize_marginal_latency(by_arm, ids)
    failure_quality = summarize_failure_quality(by_arm, ids)
    decision = classify_retrieval_decision(metric_summary, marginal_cost)
    falsified_arms = [
        arm
        for arm in ("graph", "retrieval-only")
        if failure_quality[arm]["falsified"]
    ]
    if falsified_arms:
        suppressed_verdict = decision["verdict"]
        decision = {
            "verdict": "inconclusive",
            "suppressed_terminal_verdict": (
                suppressed_verdict
                if suppressed_verdict != "inconclusive"
                else None
            ),
            "criteria": {
                **decision["criteria"],
                "maximum_infrastructure_failure_fraction": 0.05,
            },
            "reasons": [
                f"infrastructure_failure_rate_exceeds_5_percent:{arm}"
                for arm in falsified_arms
            ],
        }
    return {
        "schema_version": 2,
        "comparison": "retrieval-only-vs-graph",
        "intent_to_treat": True,
        "n": len(ids),
        "score_depth": graph["provenance"]["score_depth"],
        "provenance": {
            key: graph["provenance"][key] for key in COMMON_PROVENANCE_KEYS
        },
        "arms": {
            arm: {
                "status_counts": {
                    status: sum(
                        case.get("status") == status for case in artifact["cases"]
                    )
                    for status in sorted(
                        {str(case.get("status")) for case in artifact["cases"]}
                    )
                },
                "reported_total_estimate_usd": float(
                    artifact.get("total_cost_usd", 0.0)
                ),
                "reported_total_scopes": sorted(
                    {
                        str(case.get("cost_usd", {}).get("total_estimate_scope"))
                        for case in artifact["cases"]
                        if case.get("cost_usd", {}).get("total_estimate_scope")
                    }
                ),
                "model": artifact["provenance"]["model"],
                "reranker": artifact["provenance"]["reranker"],
            }
            for arm, artifact in (("graph", graph), ("retrieval-only", retrieval))
        },
        "metrics": metric_summary,
        "failure_quality": failure_quality,
        "cost": {
            "decision_basis": "marginal_query_only",
            "marginal_query": marginal_cost,
            "index_and_total": {
                "status": "not_comparable",
                "reason": (
                    "graph indexing usage is not metered; reported arm totals "
                    "have different scopes"
                ),
            },
        },
        "latency": {
            "decision_basis": "marginal_query_stage_only",
            "marginal_query": marginal_latency,
            "full_run": {
                "status": "not_comparable",
                "reason": (
                    "graph cases include a fresh clone and full index while "
                    "retrieval reuses repository storage and incremental indexes"
                ),
            },
        },
        "decision": decision,
    }


def sha256_file(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def write_checksummed_json(path: Path, payload: dict) -> None:
    encoded = (json.dumps(payload, indent=2) + "\n").encode("utf-8")
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(encoded)
    Path(str(path) + ".sha256").write_text(
        f"{hashlib.sha256(encoded).hexdigest()}  {path.name}\n",
        encoding="utf-8",
    )


def merge_shards(
    paths: list[Path],
    *,
    arm: str,
    pin: dict,
    pin_sha256: str,
    scorer_sha256: str,
) -> dict:
    if not paths:
        raise ValueError(f"no {arm} artifacts supplied")
    artifacts = [json.loads(path.read_text(encoding="utf-8")) for path in paths]
    pinned_ids = list(pin["pinned_instance_ids"])
    combined_cases: dict[str, dict] = {}
    first_provenance: dict | None = None
    total_cost = 0.0
    retrieval_allocations: dict[str, str] = {}
    retrieval_common_budget: dict[str, str] | None = None
    pinned_repositories: list[str] = []
    pinned_repo_by_id: dict[str, str] = {}
    if arm == "retrieval-only":
        pin_cases = pin.get("cases")
        if (
            not isinstance(pin_cases, list)
            or any(not isinstance(case, dict) for case in pin_cases)
        ):
            raise ValueError("canonical pin cases are missing")
        pinned_repo_by_id = {
            str(case.get("instance_id", "")): str(case.get("repo", ""))
            for case in pin_cases
        }
        if (
            len(pinned_repo_by_id) != len(pin_cases)
            or set(pinned_repo_by_id) != set(pinned_ids)
            or any(not repo for repo in pinned_repo_by_id.values())
        ):
            raise ValueError("canonical pin repository coverage is invalid")
        pinned_repositories = list(
            dict.fromkeys(pinned_repo_by_id.values())
        )
        if not pinned_repositories:
            raise ValueError("canonical pin repository coverage is invalid")
    for path, artifact in zip(paths, artifacts, strict=True):
        if artifact.get("arm") != arm:
            raise ValueError(f"{path} is not a {arm} artifact")
        if artifact.get("status") != "complete":
            raise ValueError(f"{path} is not complete")
        provenance = artifact.get("provenance")
        if not isinstance(provenance, dict):
            raise ValueError(f"{path} has no provenance")
        if provenance.get("pin_sha256") != pin_sha256:
            raise ValueError(f"{path} pin_sha256 does not match the supplied pin")
        if provenance.get("scorer_sha256") != scorer_sha256:
            raise ValueError(f"{path} scorer_sha256 does not match this scorer")
        excluded_provenance = {"repository"}
        if arm == "retrieval-only":
            excluded_provenance.add("budget_contract")
            repository = provenance.get("repository")
            if (
                not isinstance(repository, str)
                or repository not in pinned_repositories
                or repository in retrieval_allocations
            ):
                raise ValueError(
                    f"retrieval shard repository coverage is invalid at {path}"
                )
            budget = provenance.get("budget_contract")
            if (
                not isinstance(budget, dict)
                or set(budget) != RETRIEVAL_BUDGET_FIELDS
            ):
                raise ValueError(
                    f"retrieval shard budget contract is malformed at {path}"
                )
            allocation = _exact_nonnegative_usd(
                f"{repository} shard allocation",
                budget["shard_allocation_usd"],
            )
            arm_ceiling = _exact_nonnegative_usd(
                "retrieval arm ceiling",
                budget["arm_ceiling_usd"],
            )
            total_ceiling = _exact_nonnegative_usd(
                "experiment total ceiling",
                budget["total_ceiling_usd"],
            )
            policy = budget["provider_operation_bound_policy"]
            if (
                arm_ceiling <= 0
                or total_ceiling <= 0
                or allocation > arm_ceiling
                or arm_ceiling > total_ceiling
                or policy != PROVIDER_OPERATION_BOUND_POLICY
            ):
                raise ValueError(
                    f"retrieval shard budget contract is invalid at {path}"
                )
            common_budget = {
                "arm_ceiling_usd": str(budget["arm_ceiling_usd"]),
                "total_ceiling_usd": str(budget["total_ceiling_usd"]),
                "provider_operation_bound_policy": str(policy),
            }
            if retrieval_common_budget is None:
                retrieval_common_budget = common_budget
            elif common_budget != retrieval_common_budget:
                raise ValueError(
                    f"retrieval shard common budget provenance mismatch at {path}"
                )
            retrieval_allocations[repository] = _canonical_usd(allocation)
        comparable = {
            key: value
            for key, value in provenance.items()
            if key not in excluded_provenance
        }
        if first_provenance is None:
            first_provenance = dict(provenance)
            first_comparable = comparable
        elif comparable != first_comparable:
            raise ValueError(f"{arm} shard provenance mismatch at {path}")
        expected = artifact.get("expected_instance_ids")
        cases = artifact.get("cases")
        if not isinstance(expected, list) or not isinstance(cases, list):
            raise ValueError(f"{path} has invalid expected IDs or cases")
        case_ids = [case.get("instance_id") for case in cases]
        if len(case_ids) != len(set(case_ids)) or set(case_ids) != set(expected):
            raise ValueError(f"{path} does not have exact unique shard coverage")
        if arm == "retrieval-only" and (
            any(
                pinned_repo_by_id.get(str(instance_id)) != repository
                for instance_id in expected
            )
        ):
            raise ValueError(
                f"retrieval shard repository case coverage is invalid at {path}"
            )
        for case in cases:
            instance_id = case["instance_id"]
            if instance_id in combined_cases:
                raise ValueError(f"duplicate ID across {arm} shards: {instance_id}")
            combined_cases[instance_id] = case
        total_cost += float(artifact.get("total_cost_usd", 0.0))
    missing = sorted(set(pinned_ids) - set(combined_cases))
    extras = sorted(set(combined_cases) - set(pinned_ids))
    if missing or extras or len(combined_cases) != len(pinned_ids):
        raise ValueError(
            f"{arm} exact coverage required: missing={missing} extras={extras}"
        )
    assert first_provenance is not None
    first_provenance["repository"] = "all"
    if arm == "retrieval-only":
        missing_repositories = sorted(
            set(pinned_repositories) - set(retrieval_allocations)
        )
        extra_repositories = sorted(
            set(retrieval_allocations) - set(pinned_repositories)
        )
        if missing_repositories or extra_repositories:
            raise ValueError(
                "retrieval shard allocation coverage mismatch: "
                f"missing={missing_repositories} extras={extra_repositories}"
            )
        assert retrieval_common_budget is not None
        allocated_sum = sum(
            (Decimal(value) for value in retrieval_allocations.values()),
            Decimal("0"),
        )
        arm_ceiling = Decimal(retrieval_common_budget["arm_ceiling_usd"])
        if allocated_sum != arm_ceiling:
            raise ValueError(
                "retrieval shard allocations do not exactly sum to the arm ceiling"
            )
        first_provenance["budget_contract"] = {
            **retrieval_common_budget,
            "shard_allocations_usd": {
                repository: retrieval_allocations[repository]
                for repository in pinned_repositories
            },
            "allocated_sum_usd": _canonical_usd(allocated_sum),
        }
    return {
        "schema_version": 2,
        "arm": arm,
        "status": "complete",
        "expected_cases": len(pinned_ids),
        "accounted_cases": len(pinned_ids),
        "expected_instance_ids": pinned_ids,
        "total_cost_usd": round(total_cost, 6),
        "provenance": first_provenance,
        "cases": [combined_cases[instance_id] for instance_id in pinned_ids],
    }


def normalize_graph_artifact(
    raw: dict,
    pin: dict,
    rows: list[dict],
    expected_instance_ids: list[str],
    provenance: dict,
    *,
    checkpoint_contract: dict,
    raw_sha256: str,
) -> dict:
    if raw.get("checkpoint_contract") != checkpoint_contract:
        raise ValueError(
            "raw graph checkpoint contract does not match normalization inputs"
        )
    raw_cases = raw.get("cases")
    if not isinstance(raw_cases, list):
        raise ValueError("raw graph artifact cases must be a list")
    raw_by_id: dict[str, dict] = {}
    for case in raw_cases:
        instance_id = str(case.get("instance_id", ""))
        if instance_id in raw_by_id:
            raise ValueError(f"raw graph artifact has duplicate ID: {instance_id}")
        raw_by_id[instance_id] = case
    missing = sorted(set(expected_instance_ids) - set(raw_by_id))
    extras = sorted(set(raw_by_id) - set(expected_instance_ids))
    if missing or extras:
        raise ValueError(
            f"raw graph shard coverage mismatch: missing={missing} extras={extras}"
        )
    rows_by_id = {row["instance_id"]: row for row in rows}
    cases = [
        normalize_graph_case(
            raw_by_id[instance_id],
            rows_by_id[instance_id],
            provenance,
        )
        for instance_id in expected_instance_ids
    ]
    return {
        "schema_version": 2,
        "arm": "graph",
        "status": "complete",
        "expected_cases": len(expected_instance_ids),
        "accounted_cases": len(cases),
        "expected_instance_ids": list(expected_instance_ids),
        "total_cost_usd": round(
            sum(case["cost_usd"]["total_estimate"] for case in cases),
            6,
        ),
        "provenance": dict(provenance),
        "input_raw_sha256": raw_sha256,
        "cases": cases,
    }


def load_batch_cases(path: Path) -> dict[str, dict]:
    data = json.loads(path.read_text(encoding="utf-8"))
    out = {}
    for c in data.get("cases", []):
        if c.get("indexed") and c.get("agent_ran", True):
            out[c["instance_id"]] = c
    return out


def paired_bootstrap(deltas: list[int], n_boot: int = 10000, seed: int = 42) -> tuple[float, float, float]:
    rng = random.Random(seed)
    n = len(deltas)
    mean = sum(deltas) / n
    samples = []
    for _ in range(n_boot):
        s = [deltas[rng.randrange(n)] for _ in range(n)]
        samples.append(sum(s) / n)
    samples.sort()
    lo = samples[int(0.025 * n_boot)]
    hi = samples[int(0.975 * n_boot)]
    return mean, lo, hi


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    reduce_parser = subparsers.add_parser(
        "reduce",
        help="merge exact repository shards and score retrieval-only vs graph",
    )
    reduce_parser.add_argument("--pin", required=True, type=Path)
    reduce_parser.add_argument("--graph", required=True, type=Path, action="append")
    reduce_parser.add_argument(
        "--retrieval", required=True, type=Path, action="append"
    )
    reduce_parser.add_argument("--out", required=True, type=Path)
    reduce_parser.add_argument("--n-boot", type=int, default=10_000)
    reduce_parser.add_argument("--seed", type=int, default=42)
    normalize_parser = subparsers.add_parser(
        "normalize-graph",
        help="convert one graph-agent batch shard to the common rank schema",
    )
    normalize_parser.add_argument("--raw", required=True, type=Path)
    normalize_parser.add_argument("--pin", required=True, type=Path)
    normalize_parser.add_argument("--parquet", required=True, type=Path)
    normalize_parser.add_argument("--repository", required=True)
    normalize_parser.add_argument("--graph-sha", required=True)
    normalize_parser.add_argument(
        "--model",
        default="claude-haiku-4-5-20251001",
    )
    normalize_parser.add_argument(
        "--embedding-model",
        default="voyage-code-3",
    )
    normalize_parser.add_argument("--iterations", required=True, type=int)
    normalize_parser.add_argument("--graph-budget-usd", required=True)
    normalize_parser.add_argument("--out", required=True, type=Path)
    args = parser.parse_args()

    if args.command == "reduce":
        try:
            pin = json.loads(args.pin.read_text(encoding="utf-8"))
            pin_digest = sha256_file(args.pin)
            scorer_digest = sha256_file(Path(__file__).resolve())
            graph = merge_shards(
                args.graph,
                arm="graph",
                pin=pin,
                pin_sha256=pin_digest,
                scorer_sha256=scorer_digest,
            )
            retrieval = merge_shards(
                args.retrieval,
                arm="retrieval-only",
                pin=pin,
                pin_sha256=pin_digest,
                scorer_sha256=scorer_digest,
            )
            summary = reduce_comparison(
                graph,
                retrieval,
                pin,
                n_boot=args.n_boot,
                seed=args.seed,
            )
            summary["input_artifacts"] = {
                "graph": {
                    str(path): sha256_file(path) for path in args.graph
                },
                "retrieval-only": {
                    str(path): sha256_file(path) for path in args.retrieval
                },
            }
            write_checksummed_json(args.out, summary)
        except (OSError, ValueError, KeyError, json.JSONDecodeError) as exc:
            print(f"ERROR: {exc}", file=sys.stderr)
            return 1
        print(
            f"wrote {args.out}: retrieval-only-vs-graph "
            f"n={summary['n']} depth={summary['score_depth']} "
            f"decision={summary['decision']['verdict']}"
        )
        return 0
    if args.command == "normalize-graph":
        try:
            if len(args.graph_sha) != 40 or any(
                character not in "0123456789abcdef"
                for character in args.graph_sha
            ):
                raise ValueError("graph-sha must be a lowercase 40-character Git SHA")
            pin = json.loads(args.pin.read_text(encoding="utf-8"))
            dataset_digest = sha256_file(args.parquet)
            if dataset_digest != pin["dataset"]["parquet_sha256"]:
                raise ValueError(
                    "parquet digest does not match the pinned LocBench bundle"
                )
            import pandas as pd
            from armC_retrieval import prepare_dataset

            frame = pd.read_parquet(args.parquet)
            _all_rows, shard_rows, expected_ids, query_digest = prepare_dataset(
                frame,
                pin,
                repository=args.repository,
            )
            provenance = {
                "graph_sha": args.graph_sha,
                "code_search": dict(pin["component_pins"]["code_search"]),
                "dataset_sha256": dataset_digest,
                "dataset_revision": pin["dataset"]["revision"],
                "pin_sha256": sha256_file(args.pin),
                "query_sha256": query_digest,
                "scorer_sha256": sha256_file(Path(__file__).resolve()),
                "harness_sha256": sha256_file(
                    Path(__file__).resolve().parent / "eval_locbench_batch.py"
                ),
                "model": args.model,
                "embedding_model": args.embedding_model,
                "reranker": "none",
                "score_depth": int(pin["score_depth"]),
                "repository": args.repository,
            }
            raw = json.loads(args.raw.read_text(encoding="utf-8"))
            checkpoint_contract = {
                "schema_version": 1,
                "arm": "graph",
                "graph_sha": provenance["graph_sha"],
                "pin_sha256": provenance["pin_sha256"],
                "dataset_sha256": provenance["dataset_sha256"],
                "repository": provenance["repository"],
                "expected_instance_ids": expected_ids,
                "model": provenance["model"],
                "embedding_model": provenance["embedding_model"],
                "iterations": args.iterations,
                "score_depth": provenance["score_depth"],
                "graph_budget_usd": args.graph_budget_usd,
                "harness_sha256": provenance["harness_sha256"],
                "scorer_sha256": provenance["scorer_sha256"],
            }
            artifact = normalize_graph_artifact(
                raw,
                pin,
                shard_rows,
                expected_ids,
                provenance,
                checkpoint_contract=checkpoint_contract,
                raw_sha256=sha256_file(args.raw),
            )
            write_checksummed_json(args.out, artifact)
        except (OSError, ValueError, KeyError, json.JSONDecodeError) as exc:
            print(f"ERROR: {exc}", file=sys.stderr)
            return 1
        print(
            f"wrote {args.out}: graph repository={args.repository} "
            f"n={artifact['accounted_cases']}"
        )
        return 0
    raise AssertionError(f"unhandled command: {args.command}")


if __name__ == "__main__":
    raise SystemExit(main())
