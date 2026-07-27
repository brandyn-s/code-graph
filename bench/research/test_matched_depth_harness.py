from __future__ import annotations

import json
import hashlib
import os
import subprocess
import sys
import types
from pathlib import Path
from types import SimpleNamespace

import pytest


RESEARCH_DIR = Path(__file__).resolve().parent
sys.path.insert(0, str(RESEARCH_DIR))

import armC_retrieval as retrieval_module  # noqa: E402
import cs_index_once as cs_index_helper  # noqa: E402
import cs_search_once as cs_search_helper  # noqa: E402
import pilot_compare  # noqa: E402
from armC_retrieval import (  # noqa: E402
    build_retrieval_case,
    build_retrieval_failure_case,
    build_run_artifact,
    checkout,
    index_with_code_search,
    load_resume_cases,
    prepare_dataset,
    sum_case_cost_estimates,
    run_with_transient_retries,
    score_ranked_results,
    search_once,
    write_json_checkpoint,
)
from pilot_compare import (  # noqa: E402
    classify_retrieval_decision,
    normalize_graph_case,
    reduce_comparison,
    summarize_marginal_cost,
    summarize_marginal_latency,
    validate_comparison,
)
import eval_locbench_batch  # noqa: E402


def _valid_persisted_retrieval_cost(
    *,
    index_estimate: float = 0.0,
    query_estimate: float = 0.0,
    total_estimate: float | None = None,
) -> dict:
    if total_estimate is None:
        total_estimate = round(index_estimate + query_estimate, 6)
    return {
        "index_embedding_estimate": index_estimate,
        "marginal_query_estimate": query_estimate,
        "total_estimate": total_estimate,
        "total_estimate_scope": "index_plus_marginal_query",
    }


def test_retrieval_scoring_uses_exactly_first_ten_results() -> None:
    results = [
        {
            "relative_path": f"src/decoy_{rank}.py",
            "parent_name": "Decoy",
            "name": f"miss_{rank}",
            "score": 1.0 / rank,
        }
        for rank in range(1, 11)
    ]
    results.append(
        {
            "relative_path": "src/target.py",
            "parent_name": "Target",
            "name": "run",
            "score": 0.01,
        }
    )

    ranks, hits = score_ranked_results(
        results,
        ["src/target.py:Target.run"],
        score_depth=10,
    )

    assert [result["rank"] for result in ranks] == list(range(1, 11))
    assert all(result["available"] for result in ranks)
    assert hits == (False, False, False)


def test_secretless_retrieval_dispatch_is_nonzero_and_not_evaluated(
    tmp_path: Path,
) -> None:
    pin = (
        RESEARCH_DIR.parent
        / "accuracy"
        / "baselines"
        / "data"
        / "2026-06-12-matched-depth-n200"
        / "locbench-n200-pin.json"
    )
    output = tmp_path / "retrieval-only.json"
    env = {
        key: value
        for key, value in os.environ.items()
        if key not in {"ANTHROPIC_API_KEY", "VOYAGE_API_KEY"}
    }
    completed = subprocess.run(
        [
            sys.executable,
            str(RESEARCH_DIR / "armC_retrieval.py"),
            "--pin",
            str(pin),
            "--parquet",
            str(tmp_path / "not-downloaded.parquet"),
            "--workdir",
            str(tmp_path / "work"),
            "--out",
            str(output),
            "--graph-sha",
            "1d26c69b912cbfa4a163416699c04ef8a938d7b6",
            "--score-depth",
            "10",
            "--voyage-ceiling-usd",
            "1.000000",
            "--arm-ceiling-usd",
            "12.000000",
            "--total-ceiling-usd",
            "20.000000",
            "--provider-operation-bound-policy",
            "provider-enforced-per-operation-usd-required-v1",
        ],
        cwd=RESEARCH_DIR.parents[1],
        env=env,
        capture_output=True,
        text=True,
    )

    assert completed.returncode != 0
    artifact = json.loads(output.read_text(encoding="utf-8"))
    assert artifact["arm"] == "retrieval-only"
    assert artifact["status"] == "not_evaluated"
    assert artifact["reason_code"] == "missing_credentials"
    assert artifact["total_cost_usd"] == 0.0
    assert artifact["cases"] == []
    assert artifact["provenance"]["graph_sha"] == (
        "1d26c69b912cbfa4a163416699c04ef8a938d7b6"
    )
    assert artifact["provenance"]["code_search"]["tag"] == "v0.2.1"
    assert artifact["provenance"]["code_search"]["artifact_sha256"] == (
        "567d4caabdd3b5446bcaa789afc7104fb8cce142ff69d7fc8f1294398532e7e9"
    )
    assert artifact["provenance"]["score_depth"] == 10
    assert artifact["provenance"]["reranker"] == "per-case-effective"
    assert artifact["provenance"]["requested_reranker"] == "sonnet"


def test_retrieval_case_records_identity_cost_query_and_every_rank() -> None:
    row = {
        "instance_id": "owner__repo-1",
        "repo": "owner/repo",
        "base_commit": "a" * 40,
        "category": "Bug Report",
        "problem_statement": "Find Target.run.\n\nIgnore this paragraph.",
        "edit_functions": ["src/target.py:Target.run"],
    }
    results = [
        {
            "relative_path": "src/target.py" if rank == 1 else f"src/decoy_{rank}.py",
            "parent_name": "Target" if rank == 1 else "Decoy",
            "name": "run" if rank == 1 else f"miss_{rank}",
            "score": 1.0 / rank,
        }
        for rank in range(1, 11)
    ]
    provenance = {
        "code_search": {
            "tag": "v0.2.1",
            "artifact_sha256": (
                "567d4caabdd3b5446bcaa789afc7104fb8cce142ff69d7fc8f1294398532e7e9"
            ),
        },
        "embedding_model": "voyage-4-large",
        "reranker": "sonnet",
        "score_depth": 10,
    }

    observed_index_identity = {
        "schema_version": 1,
        "repository_id": "1" * 64,
        "checkout_id": "2" * 64,
        "source_revision": "a" * 40,
        "dirty_fingerprint": "clean",
        "index_generation": hashlib.sha256(
            f"{'1' * 64}\0{'a' * 40}\0clean".encode()
        ).hexdigest(),
        "captured_at": "2026-07-27T12:00:00Z",
    }
    embedding_identity = {
        "provider": "voyage",
        "model": "voyage-4-large",
        "vector_dim": 1024,
        "content_mode": "code",
        "pipeline_version": "pipeline-v1",
        "manifest_freshness": "fresh",
        "index_epoch_id": "epoch-1",
    }
    effective_search = {
        "embedding": embedding_identity,
        "reranker": {
            "requested_mode": "sonnet",
            "applied": True,
            "reason": "ok",
            "latency_ms": 25,
            "model": "claude-sonnet-4-6",
        },
    }
    case = build_retrieval_case(
        row,
        results,
        chunks_added=125,
        duration_s=1.25,
        latency_s={
            "clone": 0.1,
            "index": 0.8,
            "marginal_query": 0.25,
            "total": 1.25,
        },
        provenance=provenance,
        attempts=[{"operation": "search", "outcome": "success", "retry": False}],
        index_identity=observed_index_identity,
        embedding_identity=embedding_identity,
        effective_search=effective_search,
        rank_evidence={
            "requested_k": 10,
            "returned_count": 10,
            "total_candidates": 25,
            "effective_k": 10,
            "truncated": True,
        },
    )

    assert case["status"] == "ok"
    assert case["failure_class"] == ""
    assert case["failure_code"] == ""
    assert case["query_sha256"]
    assert case["index_identity"] == observed_index_identity
    assert case["embedding_identity"] == embedding_identity
    assert case["effective_search"] == effective_search
    assert case["rank_evidence"]["total_candidates"] == 25
    assert case["rank_evidence"]["truncated"] is True
    assert case["cost_usd"] == {
        "index_embedding_estimate": 0.01,
        "marginal_query_estimate": 0.02,
        "total_estimate": 0.03,
        "total_estimate_scope": "index_plus_marginal_query",
    }
    assert case["cost_basis"]["marginal_query"]["measurement_basis"] == (
        "fixed-conservative-pilot-estimate-v1"
    )
    assert case["latency_s"]["marginal_query"] == 0.25
    assert case["latency_basis"]["marginal_query"]["measurement_basis"] == (
        "wall-clock-query-stage-v1"
    )
    assert [rank["rank"] for rank in case["results"]] == list(range(1, 11))
    assert (case["file_hit"], case["class_hit"], case["func_hit"]) == (
        True,
        True,
        True,
    )
    assert sum_case_cost_estimates([case]) == 0.03


def test_failed_retrieval_case_records_all_ranks_as_intent_to_treat_miss() -> None:
    row = {
        "instance_id": "owner__repo-1",
        "repo": "owner/repo",
        "base_commit": "a" * 40,
        "category": "Bug Report",
        "problem_statement": "Find Target.run.",
        "edit_functions": ["src/target.py:Target.run"],
    }
    provenance = {
        "code_search": {
            "tag": "v0.2.1",
            "artifact_sha256": (
                "567d4caabdd3b5446bcaa789afc7104fb8cce142ff69d7fc8f1294398532e7e9"
            ),
        },
        "embedding_model": "voyage-4-large",
        "reranker": "sonnet",
        "score_depth": 10,
    }

    case = build_retrieval_failure_case(
        row,
        provenance=provenance,
        note="index failed: invalid index identity",
        failure_class="invalid_experiment",
        failure_code="index_identity_invalid",
        indexed=False,
        chunks_added=0,
        query_attempted=False,
        duration_s=0.5,
        latency_s={
            "clone": 0.4,
            "index": 0.0,
            "marginal_query": 0.0,
            "total": 0.5,
        },
        attempts=[
            {
                "operation": "index",
                "outcome": "error",
                "transient": False,
                "retry": False,
            }
        ],
    )

    assert case["status"] == "miss"
    assert case["failure_class"] == "invalid_experiment"
    assert case["failure_code"] == "index_identity_invalid"
    assert [rank["rank"] for rank in case["results"]] == list(range(1, 11))
    assert all(rank["available"] is False for rank in case["results"])
    assert (case["file_hit"], case["class_hit"], case["func_hit"]) == (
        False,
        False,
        False,
    )
    assert case["index_identity"] == {}
    assert case["embedding_identity"] == {}
    assert case["effective_search"] == {}
    assert case["cost_usd"]["total_estimate"] == 0.0
    assert case["latency_s"]["total"] == 0.5


def test_run_artifact_requires_exact_unique_shard_coverage() -> None:
    provenance = {"score_depth": 10}
    expected = ["owner__repo-1", "owner__repo-2"]
    first = {
        "instance_id": expected[0],
        "cost_usd": {"total_estimate": 0.03},
    }
    second = {
        "instance_id": expected[1],
        "cost_usd": {"total_estimate": 0.04},
    }

    partial = build_run_artifact(provenance, expected, [first])
    assert partial["status"] == "partial"
    assert partial["expected_cases"] == 2
    assert partial["accounted_cases"] == 1

    with pytest.raises(ValueError, match="duplicate"):
        build_run_artifact(provenance, expected, [first, first])

    complete = build_run_artifact(provenance, expected, [first, second])
    assert complete["status"] == "complete"
    assert complete["total_cost_usd"] == 0.07


def test_retrieval_artifact_aborts_on_invalid_experiment_case() -> None:
    provenance = {"score_depth": 10}
    expected = ["owner__repo-1", "owner__repo-2"]
    invalid_case = {
        "instance_id": expected[0],
        "failure_class": "invalid_experiment",
        "failure_code": "embedding_identity_invalid",
        "cost_usd": {"total_estimate": 0.0},
    }

    artifact = build_run_artifact(provenance, expected, [invalid_case])

    assert artifact["status"] == "partial"
    assert artifact["aborted_reason"] == (
        "invalid_experiment:embedding_identity_invalid:owner__repo-1"
    )


def test_resume_refuses_checkpoint_provenance_drift(tmp_path: Path) -> None:
    output = tmp_path / "partial.json"
    expected = ["owner__repo-1", "owner__repo-2"]
    case = {
        "instance_id": expected[0],
        "cost_usd": _valid_persisted_retrieval_cost(
            index_estimate=0.01,
            query_estimate=0.02,
        ),
    }
    provenance = {
        "graph_sha": "a" * 40,
        "score_depth": 10,
        "pin_sha256": "b" * 64,
    }
    write_json_checkpoint(
        output,
        build_run_artifact(provenance, expected, [case]),
    )

    assert load_resume_cases(output, provenance, expected) == [case]
    mismatched = {**provenance, "score_depth": 9}
    with pytest.raises(ValueError, match="provenance"):
        load_resume_cases(output, mismatched, expected)


@pytest.mark.parametrize(
    ("mutation", "expected_error"),
    [
        ("valid", "not_evaluated"),
        ("provenance", "provenance"),
        ("expected_ids", "expected instance IDs"),
        ("cases", "must not contain cases"),
        ("cost", "total cost"),
    ],
)
def test_explicit_resume_rejects_not_evaluated_after_contract_validation(
    tmp_path: Path,
    mutation: str,
    expected_error: str,
) -> None:
    provenance = {
        "graph_sha": "a" * 40,
        "score_depth": 10,
        "pin_sha256": "b" * 64,
    }
    expected_ids = ["owner__repo-1"]
    artifact = {
        "schema_version": 2,
        "arm": "retrieval-only",
        "status": "not_evaluated",
        "expected_instance_ids": list(expected_ids),
        "total_cost_usd": 0.0,
        "provenance": dict(provenance),
        "cases": [],
    }
    if mutation == "provenance":
        artifact["provenance"]["score_depth"] = 9
    elif mutation == "expected_ids":
        artifact["expected_instance_ids"] = ["owner__other-2"]
    elif mutation == "cases":
        artifact["cases"] = [
            {
                "instance_id": expected_ids[0],
                "cost_usd": _valid_persisted_retrieval_cost(),
            }
        ]
    elif mutation == "cost":
        artifact["total_cost_usd"] = 0.01
    checkpoint = tmp_path / "not-evaluated.json"
    checkpoint.write_text(json.dumps(artifact), encoding="utf-8")

    with pytest.raises(ValueError, match=expected_error):
        load_resume_cases(checkpoint, provenance, expected_ids)


def test_implicit_output_path_accepts_only_empty_not_evaluated_sentinel(
    tmp_path: Path,
) -> None:
    checkpoint = tmp_path / "output.json"
    checkpoint.write_text(
        json.dumps(
            {
                "schema_version": 2,
                "arm": "retrieval-only",
                "status": "not_evaluated",
                "reason_code": "missing_credentials",
                "total_cost_usd": 0.0,
                "provenance": {"preflight_only": True},
                "cases": [],
            }
        ),
        encoding="utf-8",
    )

    assert load_resume_cases(
        checkpoint,
        {"full": "run-contract"},
        ["owner__repo-1"],
        allow_implicit_empty=True,
    ) == []

    payload = json.loads(checkpoint.read_text(encoding="utf-8"))
    payload["cases"] = [
        {
            "instance_id": "owner__repo-1",
            "cost_usd": _valid_persisted_retrieval_cost(),
        }
    ]
    checkpoint.write_text(json.dumps(payload), encoding="utf-8")
    with pytest.raises(ValueError, match="must not contain cases"):
        load_resume_cases(
            checkpoint,
            {"full": "run-contract"},
            ["owner__repo-1"],
            allow_implicit_empty=True,
        )


@pytest.mark.parametrize(
    ("resume_kind", "expected_message"),
    [
        ("not_evaluated", "not_evaluated"),
        ("submicro_cost", "micro-dollar precision"),
    ],
)
def test_explicit_invalid_resume_rejects_before_provider_work(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    resume_kind: str,
    expected_message: str,
) -> None:
    pin_path = tmp_path / "pin.json"
    parquet = tmp_path / "locbench.parquet"
    output = tmp_path / "retrieval.json"
    resume = tmp_path / "resume.json"
    pin_path.write_text("{}", encoding="utf-8")
    parquet.write_bytes(b"pinned")
    row = {
        "instance_id": "owner__repo-1",
        "repo": "owner/repo",
        "base_commit": "a" * 40,
        "category": "Bug Report",
        "problem_statement": "Find Target.run.",
        "edit_functions": ["src/target.py:Target.run"],
    }
    base_provenance = {
        "dataset_sha256": hashlib.sha256(b"pinned").hexdigest(),
        "embedding_model": "voyage-4-large",
        "score_depth": 10,
        "reranker": "per-case-effective",
    }
    resume_provenance = {
        **base_provenance,
        "query_sha256": "q" * 64,
        "repository": "owner/repo",
    }
    resume_artifact = {
        "schema_version": 2,
        "arm": "retrieval-only",
        "status": "not_evaluated",
        "expected_instance_ids": [row["instance_id"]],
        "total_cost_usd": 0.0,
        "provenance": resume_provenance,
        "cases": [],
    }
    if resume_kind == "submicro_cost":
        resume_artifact.update(
            {
                "status": "partial",
                "total_cost_usd": 0.0000004,
                "cases": [
                    {
                        "instance_id": row["instance_id"],
                        "cost_usd": {
                            "index_embedding_estimate": 0.0000002,
                            "marginal_query_estimate": 0.0000002,
                            "total_estimate": 0.0000004,
                            "total_estimate_scope": (
                                "index_plus_marginal_query"
                            ),
                        },
                    }
                ],
            }
        )
    resume.write_text(json.dumps(resume_artifact), encoding="utf-8")
    monkeypatch.setenv("VOYAGE_API_KEY", "test-only")
    monkeypatch.setenv("ANTHROPIC_API_KEY", "test-only")
    monkeypatch.setattr(
        retrieval_module,
        "build_provenance",
        lambda *args, **kwargs: dict(base_provenance),
    )
    monkeypatch.setattr(
        retrieval_module.pd,
        "read_parquet",
        lambda path: object(),
    )
    monkeypatch.setattr(
        retrieval_module,
        "prepare_dataset",
        lambda *args, **kwargs: (
            [row],
            [row],
            [row["instance_id"]],
            "q" * 64,
        ),
    )
    monkeypatch.setattr(
        retrieval_module,
        "clone_repo",
        lambda *args, **kwargs: pytest.fail(
            "repository or provider work must not start"
        ),
    )
    monkeypatch.setattr(
        retrieval_module,
        "index_with_code_search",
        lambda *args, **kwargs: pytest.fail("provider work must not start"),
    )
    monkeypatch.setattr(
        retrieval_module,
        "search_once",
        lambda *args, **kwargs: pytest.fail("provider work must not start"),
    )
    monkeypatch.setattr(
        sys,
        "argv",
        [
            "armC_retrieval.py",
            "--pin",
            str(pin_path),
            "--parquet",
            str(parquet),
            "--workdir",
            str(tmp_path / "work"),
            "--out",
            str(output),
            "--resume-from",
            str(resume),
            "--graph-sha",
            "a" * 40,
            "--repository",
            "owner/repo",
            "--voyage-ceiling-usd",
            "1.000000",
            "--arm-ceiling-usd",
            "12.000000",
            "--total-ceiling-usd",
            "20.000000",
            "--provider-operation-bound-policy",
            "provider-enforced-per-operation-usd-required-v1",
        ],
    )

    exit_code = retrieval_module.main()
    artifact = json.loads(output.read_text(encoding="utf-8"))

    assert exit_code == 2
    assert artifact["reason_code"] == "resume_checkpoint_rejected"
    assert expected_message in artifact["message"]


def test_dataset_preparation_preserves_pin_order_and_repository_shard() -> None:
    import pandas as pd

    pin = {
        "n": 2,
        "pinned_instance_ids": ["owner__one-1", "owner__two-2"],
        "cases": [
            {
                "instance_id": "owner__one-1",
                "repo": "owner/one",
                "base_commit": "a" * 40,
                "category": "Bug Report",
            },
            {
                "instance_id": "owner__two-2",
                "repo": "owner/two",
                "base_commit": "b" * 40,
                "category": "Feature Request",
            },
        ],
    }
    frame = pd.DataFrame(
        [
            {
                "instance_id": "owner__two-2",
                "repo": "owner/two",
                "base_commit": "b" * 40,
                "category": "Feature Request",
                "problem_statement": "Second query.\n\nMore text.",
                "edit_functions": ["two.py:run"],
            },
            {
                "instance_id": "owner__one-1",
                "repo": "owner/one",
                "base_commit": "a" * 40,
                "category": "Bug Report",
                "problem_statement": "First query.\n\nMore text.",
                "edit_functions": ["one.py:run"],
            },
        ]
    )

    all_rows, shard_rows, expected_ids, query_digest = prepare_dataset(
        frame,
        pin,
        repository="owner/two",
    )

    assert [row["instance_id"] for row in all_rows] == pin["pinned_instance_ids"]
    assert [row["instance_id"] for row in shard_rows] == ["owner__two-2"]
    assert expected_ids == ["owner__two-2"]
    assert len(query_digest) == 64


def test_retry_wrapper_retries_only_transient_failures() -> None:
    transient_calls = 0

    def transient_then_success() -> str:
        nonlocal transient_calls
        transient_calls += 1
        if transient_calls == 1:
            raise RuntimeError("HTTP 503 service unavailable")
        return "ok"

    result, attempts = run_with_transient_retries(
        "search",
        transient_then_success,
        max_attempts=3,
        base_delay_s=0,
    )
    assert result == "ok"
    assert transient_calls == 2
    assert attempts[0]["retry"] is True
    assert attempts[1]["outcome"] == "success"

    deterministic_calls = 0

    def deterministic_failure() -> str:
        nonlocal deterministic_calls
        deterministic_calls += 1
        raise RuntimeError("invalid index identity")

    with pytest.raises(RuntimeError, match="invalid index identity"):
        run_with_transient_retries(
            "index",
            deterministic_failure,
            max_attempts=3,
            base_delay_s=0,
        )
    assert deterministic_calls == 1


def test_clone_retries_only_transient_network_failures(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    calls: list[list[str]] = []
    transient_attempts: list[dict] = []

    def transient_then_success(command, **kwargs):
        calls.append(command)
        if len(calls) == 1:
            raise subprocess.CalledProcessError(
                128,
                command,
                stderr=b"fatal: HTTP 503 service unavailable",
            )
        return subprocess.CompletedProcess(command, 0, stdout=b"", stderr=b"")

    monkeypatch.setattr(eval_locbench_batch.subprocess, "run", transient_then_success)
    monkeypatch.setattr(eval_locbench_batch.time, "sleep", lambda _seconds: None)

    assert (
        eval_locbench_batch.clone_repo(
            "owner/repo",
            "a" * 40,
            tmp_path / "transient",
            max_attempts=3,
            attempts_out=transient_attempts,
        )
        is True
    )
    assert len(calls) == 3
    assert transient_attempts[0]["operation"] == "clone"
    assert transient_attempts[0]["transient"] is True
    assert transient_attempts[0]["retry"] is True
    assert transient_attempts[-1]["operation"] == "checkout"
    assert transient_attempts[-1]["outcome"] == "success"

    calls.clear()
    permanent_attempts: list[dict] = []

    def permanent_failure(command, **kwargs):
        calls.append(command)
        raise subprocess.CalledProcessError(
            128,
            command,
            stderr=b"fatal: repository not found",
        )

    monkeypatch.setattr(eval_locbench_batch.subprocess, "run", permanent_failure)
    assert (
        eval_locbench_batch.clone_repo(
            "owner/repo",
            "a" * 40,
            tmp_path / "permanent",
            max_attempts=3,
            attempts_out=permanent_attempts,
        )
        is False
    )
    assert len(calls) == 1
    assert permanent_attempts == [
        {
            "operation": "clone",
            "attempt": 1,
            "outcome": "error",
            "error": "fatal: repository not found",
            "transient": False,
            "retry": False,
        }
    ]


def test_graph_resume_requires_exact_contract_and_unique_subset(
    tmp_path: Path,
) -> None:
    contract = {
        "schema_version": 1,
        "arm": "graph",
        "graph_sha": "1" * 40,
        "pin_sha256": "2" * 64,
        "expected_instance_ids": ["owner__repo-1", "owner__repo-2"],
    }
    summary = eval_locbench_batch.BatchSummary(
        n_total=2,
        instances=[
            eval_locbench_batch.InstanceResult(
                instance_id="owner__repo-1",
                repo="owner/repo",
                category="Bug Report",
                ground_truth=["target.py:run"],
                note="clone failed",
                failure_class="infrastructure",
                failure_code="clone_failed",
                latency_s={"clone": 0.25, "total": 0.25},
            )
        ],
    )
    checkpoint = tmp_path / "raw-graph.json"
    checkpoint.write_text(
        json.dumps(
            eval_locbench_batch._build_per_case_dict(
                summary,
                checkpoint_contract=contract,
            )
        ),
        encoding="utf-8",
    )

    resumed = eval_locbench_batch.load_graph_resume_instances(
        checkpoint,
        contract,
        contract["expected_instance_ids"],
    )
    assert [instance.instance_id for instance in resumed] == ["owner__repo-1"]
    assert resumed[0].note == "clone failed"
    assert resumed[0].failure_class == "infrastructure"
    assert resumed[0].failure_code == "clone_failed"
    assert resumed[0].latency_s == {"clone": 0.25, "total": 0.25}

    with pytest.raises(ValueError, match="contract"):
        eval_locbench_batch.load_graph_resume_instances(
            checkpoint,
            {**contract, "graph_sha": "3" * 40},
            contract["expected_instance_ids"],
        )

    payload = json.loads(checkpoint.read_text(encoding="utf-8"))
    payload["cases"].append(payload["cases"][0])
    checkpoint.write_text(json.dumps(payload), encoding="utf-8")
    with pytest.raises(ValueError, match="duplicate"):
        eval_locbench_batch.load_graph_resume_instances(
            checkpoint,
            contract,
            contract["expected_instance_ids"],
        )


def test_resume_rejects_persisted_invalid_experiment_abort(
    tmp_path: Path,
) -> None:
    retrieval_provenance = {
        "graph_sha": "a" * 40,
        "score_depth": 10,
        "pin_sha256": "b" * 64,
    }
    expected_ids = ["owner__repo-1", "owner__repo-2"]
    retrieval_checkpoint = tmp_path / "retrieval.json"
    retrieval_checkpoint.write_text(
        json.dumps(
            build_run_artifact(
                retrieval_provenance,
                expected_ids,
                [
                    {
                        "instance_id": expected_ids[0],
                        "failure_class": "invalid_experiment",
                        "failure_code": "index_identity_invalid",
                        "cost_usd": _valid_persisted_retrieval_cost(),
                    }
                ],
            )
        ),
        encoding="utf-8",
    )

    with pytest.raises(ValueError, match="invalid_experiment"):
        load_resume_cases(
            retrieval_checkpoint,
            retrieval_provenance,
            expected_ids,
        )

    graph_contract = {
        "schema_version": 1,
        "arm": "graph",
        "graph_sha": "1" * 40,
        "pin_sha256": "2" * 64,
        "expected_instance_ids": expected_ids,
    }
    graph_summary = eval_locbench_batch.BatchSummary(n_total=2)
    graph_summary.record(
        eval_locbench_batch.InstanceResult(
            instance_id=expected_ids[0],
            repo="owner/repo",
            category="Bug Report",
            ground_truth=[],
            failure_class="invalid_experiment",
            failure_code="agent_envelope_invalid",
        )
    )
    graph_checkpoint = tmp_path / "graph.json"
    graph_checkpoint.write_text(
        json.dumps(
            eval_locbench_batch._build_per_case_dict(
                graph_summary,
                checkpoint_contract=graph_contract,
            )
        ),
        encoding="utf-8",
    )

    with pytest.raises(ValueError, match="invalid_experiment"):
        eval_locbench_batch.load_graph_resume_instances(
            graph_checkpoint,
            graph_contract,
            expected_ids,
        )


@pytest.mark.parametrize(
    ("mutation", "expected_error"),
    [("entities", "entities"), ("usage", "input_tokens")],
)
def test_graph_resume_rejects_malformed_success_agent_envelope(
    tmp_path: Path,
    mutation: str,
    expected_error: str,
) -> None:
    expected_ids = ["owner__repo-1"]
    graph_contract = {
        "schema_version": 1,
        "arm": "graph",
        "graph_sha": "1" * 40,
        "pin_sha256": "2" * 64,
        "expected_instance_ids": expected_ids,
    }
    graph_summary = eval_locbench_batch.BatchSummary(
        n_total=1,
        instances=[
            eval_locbench_batch.InstanceResult(
                instance_id=expected_ids[0],
                repo="owner/repo",
                category="Bug Report",
                ground_truth=[],
                indexed=True,
                agent_ran=True,
                turns=1,
                input_tokens=100,
                output_tokens=10,
                cost_estimate_usd=0.00015,
                agent_json={
                    "code_localize_agent": {
                        "entities": [
                            {
                                "qualified_name": "Target.run",
                                "file_path": "src/target.py",
                                "label": "",
                            }
                        ],
                        "turns": 1,
                        "stop_reason": "finalized",
                        "input_tokens": 100,
                        "output_tokens": 10,
                    }
                },
            )
        ],
    )
    payload = eval_locbench_batch._build_per_case_dict(
        graph_summary,
        checkpoint_contract=graph_contract,
    )
    agent = payload["cases"][0]["agent_envelope"]["code_localize_agent"]
    if mutation == "entities":
        agent["entities"] = "not-a-list"
    else:
        agent["input_tokens"] = "100"
    graph_checkpoint = tmp_path / "graph.json"
    graph_checkpoint.write_text(json.dumps(payload), encoding="utf-8")

    with pytest.raises((TypeError, ValueError), match=expected_error):
        eval_locbench_batch.load_graph_resume_instances(
            graph_checkpoint,
            graph_contract,
            expected_ids,
        )


@pytest.mark.parametrize(
    "invalid_cost",
    [-0.01, float("nan"), float("inf"), True, "0.01"],
)
def test_resume_rejects_invalid_persisted_costs(
    tmp_path: Path,
    invalid_cost: object,
) -> None:
    retrieval_provenance = {
        "graph_sha": "a" * 40,
        "score_depth": 10,
        "pin_sha256": "b" * 64,
    }
    expected_ids = ["owner__repo-1"]
    retrieval_checkpoint = tmp_path / "retrieval.json"
    retrieval_artifact = build_run_artifact(
        retrieval_provenance,
        expected_ids,
        [
            {
                "instance_id": expected_ids[0],
                "cost_usd": {
                    "index_embedding_estimate": invalid_cost,
                    "marginal_query_estimate": 0.0,
                    "total_estimate": 0.0,
                },
            }
        ],
    )
    retrieval_checkpoint.write_text(
        json.dumps(retrieval_artifact),
        encoding="utf-8",
    )

    with pytest.raises(ValueError, match="cost"):
        load_resume_cases(
            retrieval_checkpoint,
            retrieval_provenance,
            expected_ids,
        )

    retrieval_artifact["cases"][0]["cost_usd"] = {
        **_valid_persisted_retrieval_cost(),
    }
    retrieval_artifact["total_cost_usd"] = invalid_cost
    retrieval_checkpoint.write_text(
        json.dumps(retrieval_artifact),
        encoding="utf-8",
    )
    with pytest.raises(ValueError, match="cost"):
        load_resume_cases(
            retrieval_checkpoint,
            retrieval_provenance,
            expected_ids,
        )

    graph_contract = {
        "schema_version": 1,
        "arm": "graph",
        "graph_sha": "1" * 40,
        "pin_sha256": "2" * 64,
        "expected_instance_ids": expected_ids,
    }
    graph_summary = eval_locbench_batch.BatchSummary(
        n_total=1,
        instances=[
            eval_locbench_batch.InstanceResult(
                instance_id=expected_ids[0],
                repo="owner/repo",
                category="Bug Report",
                ground_truth=[],
                cost_estimate_usd=invalid_cost,
            )
        ],
    )
    graph_checkpoint = tmp_path / "graph.json"
    graph_checkpoint.write_text(
        json.dumps(
            eval_locbench_batch._build_per_case_dict(
                graph_summary,
                checkpoint_contract=graph_contract,
            )
        ),
        encoding="utf-8",
    )

    with pytest.raises(ValueError, match="cost"):
        eval_locbench_batch.load_graph_resume_instances(
            graph_checkpoint,
            graph_contract,
            expected_ids,
        )


def test_retrieval_resume_rejects_unrecognized_persisted_cost_field(
    tmp_path: Path,
) -> None:
    provenance = {
        "graph_sha": "a" * 40,
        "score_depth": 10,
        "pin_sha256": "b" * 64,
    }
    expected_ids = ["owner__repo-1"]
    checkpoint = tmp_path / "retrieval.json"
    checkpoint.write_text(
        json.dumps(
            build_run_artifact(
                provenance,
                expected_ids,
                [
                    {
                        "instance_id": expected_ids[0],
                        "cost_usd": {
                            **_valid_persisted_retrieval_cost(),
                            "unvalidated_provider_cost": -1.0,
                        },
                    }
                ],
            )
        ),
        encoding="utf-8",
    )

    with pytest.raises(ValueError, match="cost"):
        load_resume_cases(checkpoint, provenance, expected_ids)


@pytest.mark.parametrize(
    ("mutation", "expected_error"),
    [
        ("missing_index", "exact fields"),
        ("missing_query", "exact fields"),
        ("missing_total", "exact fields"),
        ("missing_scope", "exact fields"),
        ("wrong_scope", "scope"),
        ("case_reconciliation", "component sum"),
        ("top_reconciliation", "case total sum"),
    ],
)
def test_retrieval_resume_requires_exact_reconciled_cost_schema(
    tmp_path: Path,
    mutation: str,
    expected_error: str,
) -> None:
    provenance = {
        "graph_sha": "a" * 40,
        "score_depth": 10,
        "pin_sha256": "b" * 64,
    }
    expected_ids = ["owner__repo-1"]
    artifact = build_run_artifact(
        provenance,
        expected_ids,
        [
            {
                "instance_id": expected_ids[0],
                "cost_usd": _valid_persisted_retrieval_cost(
                    index_estimate=0.01,
                    query_estimate=0.02,
                ),
            }
        ],
    )
    cost = artifact["cases"][0]["cost_usd"]
    if mutation == "missing_index":
        del cost["index_embedding_estimate"]
    elif mutation == "missing_query":
        del cost["marginal_query_estimate"]
    elif mutation == "missing_total":
        del cost["total_estimate"]
    elif mutation == "missing_scope":
        del cost["total_estimate_scope"]
    elif mutation == "wrong_scope":
        cost["total_estimate_scope"] = "query_only"
    elif mutation == "case_reconciliation":
        cost["total_estimate"] = 0.031
        artifact["total_cost_usd"] = 0.031
    else:
        artifact["total_cost_usd"] = 0.031
    checkpoint = tmp_path / "retrieval.json"
    checkpoint.write_text(json.dumps(artifact), encoding="utf-8")

    with pytest.raises(ValueError, match=expected_error):
        load_resume_cases(checkpoint, provenance, expected_ids)


def test_retrieval_resume_reconciles_decimal_costs_exactly(
    tmp_path: Path,
) -> None:
    provenance = {
        "graph_sha": "a" * 40,
        "score_depth": 10,
        "pin_sha256": "b" * 64,
    }
    expected_ids = ["owner__repo-1", "owner__repo-2"]
    cases = [
        {
            "instance_id": expected_ids[0],
            "cost_usd": _valid_persisted_retrieval_cost(
                index_estimate=0.1,
                query_estimate=0.2,
                total_estimate=0.3,
            ),
        },
        {
            "instance_id": expected_ids[1],
            "cost_usd": _valid_persisted_retrieval_cost(
                index_estimate=0.000001,
                query_estimate=0.000002,
                total_estimate=0.000003,
            ),
        },
    ]
    artifact = build_run_artifact(provenance, expected_ids, cases)
    assert artifact["total_cost_usd"] == 0.300003
    checkpoint = tmp_path / "retrieval.json"
    checkpoint.write_text(json.dumps(artifact), encoding="utf-8")

    assert load_resume_cases(checkpoint, provenance, expected_ids) == cases


def test_retrieval_resume_rejects_reconciled_submicro_costs(
    tmp_path: Path,
) -> None:
    provenance = {
        "graph_sha": "a" * 40,
        "score_depth": 10,
        "pin_sha256": "b" * 64,
    }
    expected_ids = ["owner__repo-1"]
    artifact = {
        "schema_version": 2,
        "arm": "retrieval-only",
        "status": "complete",
        "expected_instance_ids": expected_ids,
        "total_cost_usd": 0.0000004,
        "aborted_reason": "",
        "provenance": provenance,
        "cases": [
            {
                "instance_id": expected_ids[0],
                "cost_usd": {
                    "index_embedding_estimate": 0.0000002,
                    "marginal_query_estimate": 0.0000002,
                    "total_estimate": 0.0000004,
                    "total_estimate_scope": "index_plus_marginal_query",
                },
            }
        ],
    }
    checkpoint = tmp_path / "retrieval.json"
    checkpoint.write_text(json.dumps(artifact), encoding="utf-8")

    with pytest.raises(ValueError, match="micro-dollar precision"):
        load_resume_cases(checkpoint, provenance, expected_ids)


def comparison_artifact(
    arm: str,
    ids: list[str],
    *,
    depth: int,
    pin_sha256: str = "d" * 64,
) -> dict:
    return {
        "schema_version": 2,
        "arm": arm,
        "status": "complete",
        "expected_cases": len(ids),
        "accounted_cases": len(ids),
        "expected_instance_ids": ids,
        "total_cost_usd": 0.0,
        "provenance": {
            "graph_sha": "a" * 40,
            "code_search": {
                "tag": "v0.2.1",
                "artifact_sha256": (
                    "567d4caabdd3b5446bcaa789afc7104fb8cce142ff69d7fc8f1294398532e7e9"
                ),
            },
            "dataset_sha256": "b" * 64,
            "dataset_revision": "c" * 40,
            "pin_sha256": pin_sha256,
            "query_sha256": "e" * 64,
            "scorer_sha256": "f" * 64,
            "score_depth": depth,
            "model": "model-id",
            "reranker": "none" if arm == "graph" else "sonnet",
        },
        "cases": [
            {
                "instance_id": instance_id,
                "status": "ok",
                "failure_class": "",
                "failure_code": "",
                "ground_truth": [],
                "file_hit": False,
                "class_hit": False,
                "func_hit": False,
                "rank_evidence": {
                    "requested_k": depth,
                    "returned_count": 0,
                    "total_candidates": 0,
                    "effective_k": 0,
                    "truncated": False,
                },
                "results": [
                    {
                        "rank": rank,
                        "available": False,
                        "relative_path": "",
                        "parent_name": "",
                        "name": "",
                        "score": None,
                    }
                    for rank in range(1, depth + 1)
                ],
                "cost_usd": {"total_estimate": 0.0},
            }
            for instance_id in ids
        ],
    }


def test_comparison_rejects_deliberate_depth_mismatch() -> None:
    ids = [f"owner__repo-{number}" for number in range(200)]
    pin = {
        "n": 200,
        "score_depth": 10,
        "pinned_instance_ids": ids,
    }
    graph = comparison_artifact("graph", ids, depth=9)
    retrieval = comparison_artifact("retrieval-only", ids, depth=10)

    with pytest.raises(ValueError, match="depth"):
        validate_comparison(graph, retrieval, pin)


def test_comparison_rejects_deliberate_pin_mismatch() -> None:
    ids = [f"owner__repo-{number}" for number in range(200)]
    pin = {
        "n": 200,
        "score_depth": 10,
        "pinned_instance_ids": ids,
    }
    graph = comparison_artifact("graph", ids, depth=10, pin_sha256="1" * 64)
    retrieval = comparison_artifact(
        "retrieval-only",
        ids,
        depth=10,
        pin_sha256="2" * 64,
    )

    with pytest.raises(ValueError, match="pin_sha256"):
        validate_comparison(graph, retrieval, pin)


def test_reducer_counts_failures_as_intent_to_treat_misses() -> None:
    ids = [f"owner__repo-{number}" for number in range(200)]
    pin = {
        "n": 200,
        "score_depth": 10,
        "pinned_instance_ids": ids,
    }
    graph = comparison_artifact("graph", ids, depth=10)
    retrieval = comparison_artifact("retrieval-only", ids, depth=10)
    graph_case = graph["cases"][0]
    graph_case.update(
        {
            "status": "ok",
            "ground_truth": ["target.py:Target.run"],
            "file_hit": True,
            "class_hit": True,
            "func_hit": True,
        }
    )
    graph_case["results"][0].update(
        {
            "available": True,
            "relative_path": "target.py",
            "parent_name": "Target",
            "name": "run",
            "score": 1.0,
        }
    )
    retrieval["cases"][0].update(
        {
            "status": "infrastructure_failure",
            "failure_class": "infrastructure",
            "failure_code": "search_timeout",
        }
    )

    summary = reduce_comparison(graph, retrieval, pin, n_boot=100, seed=42)

    assert summary["intent_to_treat"] is True
    assert summary["n"] == 200
    assert summary["metrics"]["file_hit"]["graph"]["hits"] == 1
    assert summary["metrics"]["file_hit"]["retrieval-only"]["hits"] == 0
    assert summary["metrics"]["file_hit"]["graph"]["accuracy"] == 0.005
    assert summary["metrics"]["file_hit"]["retrieval-only"]["accuracy"] == 0.0
    assert summary["decision"]["verdict"] == "inconclusive"
    assert "marginal_cost_not_comparable" in summary["decision"]["reasons"]
    assert "total_cost_usd" not in summary["arms"]["graph"]
    assert summary["cost"]["index_and_total"]["status"] == "not_comparable"
    assert summary["latency"]["full_run"]["status"] == "not_comparable"


@pytest.mark.parametrize(
    ("failure_class", "failure_code", "expected"),
    [
        ("", "", "missing a typed failure classification"),
        ("unknown", "mystery", "unknown failure classification"),
    ],
)
def test_reducer_rejects_untyped_or_unknown_failed_case(
    failure_class: str,
    failure_code: str,
    expected: str,
) -> None:
    ids = [f"owner__repo-{number}" for number in range(200)]
    pin = {
        "n": 200,
        "score_depth": 10,
        "pinned_instance_ids": ids,
    }
    graph = comparison_artifact("graph", ids, depth=10)
    retrieval = comparison_artifact("retrieval-only", ids, depth=10)
    graph["cases"][0].update(
        {
            "status": "miss",
            "failure_class": failure_class,
            "failure_code": failure_code,
        }
    )

    with pytest.raises(ValueError, match=expected):
        validate_comparison(graph, retrieval, pin)


def test_reducer_rejects_unverifiable_retrieval_truncation() -> None:
    ids = [f"owner__repo-{number}" for number in range(200)]
    pin = {
        "n": 200,
        "score_depth": 10,
        "pinned_instance_ids": ids,
    }
    graph = comparison_artifact("graph", ids, depth=10)
    retrieval = comparison_artifact("retrieval-only", ids, depth=10)
    retrieval["cases"][0]["rank_evidence"]["total_candidates"] = None

    with pytest.raises(ValueError, match="rank-window"):
        validate_comparison(graph, retrieval, pin)


@pytest.mark.parametrize("arm", ["graph", "retrieval-only"])
def test_reducer_rejects_invalid_experiment_identity_before_scoring(
    arm: str,
) -> None:
    ids = [f"owner__repo-{number}" for number in range(200)]
    pin = {
        "n": 200,
        "score_depth": 10,
        "pinned_instance_ids": ids,
    }
    graph = comparison_artifact("graph", ids, depth=10)
    retrieval = comparison_artifact("retrieval-only", ids, depth=10)
    artifact = graph if arm == "graph" else retrieval
    artifact["cases"][0].update(
        {
            "failure_class": "invalid_experiment",
            "failure_code": "index_identity_mismatch",
            "note": "observed identity does not match the pinned checkout",
        }
    )

    with pytest.raises(
        ValueError,
        match=rf"{arm} case {ids[0]} is invalid_experiment",
    ):
        reduce_comparison(graph, retrieval, pin, n_boot=10_000, seed=42)


def test_reducer_suppresses_false_prefer_above_infrastructure_falsifier() -> None:
    ids = [f"owner__repo-{number}" for number in range(200)]
    pin = {
        "n": 200,
        "score_depth": 10,
        "pinned_instance_ids": ids,
    }
    graph = comparison_artifact("graph", ids, depth=10)
    retrieval = comparison_artifact("retrieval-only", ids, depth=10)
    for case in graph["cases"]:
        case["cost_usd"] = {"marginal_query_estimate": 0.10}
        case["cost_basis"] = {
            "marginal_query": {"measurement_basis": "metered-usd-v1"}
        }
    for case in retrieval["cases"]:
        case["cost_usd"] = {"marginal_query_estimate": 0.01}
        case["cost_basis"] = {
            "marginal_query": {"measurement_basis": "metered-usd-v1"}
        }
    for case in graph["cases"][:11]:
        case["status"] = "miss"
        case["failure_class"] = "infrastructure"
        case["failure_code"] = "clone_failed"

    summary = reduce_comparison(graph, retrieval, pin, n_boot=10_000, seed=42)

    assert summary["failure_quality"]["graph"]["infrastructure_failures"] == 11
    assert summary["failure_quality"]["graph"]["infrastructure_fraction"] == 0.055
    assert summary["decision"]["verdict"] == "inconclusive"
    assert summary["decision"]["reasons"] == [
        "infrastructure_failure_rate_exceeds_5_percent:graph"
    ]


def test_reducer_discloses_false_reject_suppressed_by_infrastructure_falsifier() -> None:
    ids = [f"owner__repo-{number}" for number in range(200)]
    pin = {
        "n": 200,
        "score_depth": 10,
        "pinned_instance_ids": ids,
    }
    graph = comparison_artifact("graph", ids, depth=10)
    retrieval = comparison_artifact("retrieval-only", ids, depth=10)
    for case in graph["cases"][:20]:
        case.update(
            {
                "status": "ok",
                "ground_truth": ["target.py:Target.run"],
                "file_hit": True,
                "class_hit": True,
                "func_hit": True,
            }
        )
        case["results"][0].update(
            {
                "available": True,
                "relative_path": "target.py",
                "parent_name": "Target",
                "name": "run",
            }
        )
    for case in retrieval["cases"][:11]:
        case["status"] = "miss"
        case["failure_class"] = "infrastructure"
        case["failure_code"] = "search_timeout"

    summary = reduce_comparison(graph, retrieval, pin, n_boot=10_000, seed=42)

    assert summary["decision"]["verdict"] == "inconclusive"
    assert summary["decision"]["suppressed_terminal_verdict"] == (
        "reject_retrieval_only"
    )
    assert summary["decision"]["reasons"] == [
        "infrastructure_failure_rate_exceeds_5_percent:retrieval-only"
    ]


def _decision_metrics(
    *,
    lower: float = -0.049,
    upper: float = 0.01,
    resamples: int = 10_000,
) -> dict:
    return {
        metric: {
            "paired_bootstrap_95ci": [lower, upper],
            "bootstrap_resamples": resamples,
        }
        for metric in ("file_hit", "class_hit", "func_hit")
    }


def _comparable_cost(reduction: float) -> dict:
    return {
        "status": "comparable",
        "graph": {"marginal_query_total_usd": 10.0},
        "retrieval-only": {
            "marginal_query_total_usd": round(10.0 * (1.0 - reduction), 6)
        },
        "reduction_fraction": reduction,
    }


def test_decision_prefers_retrieval_only_only_at_preregistered_thresholds() -> None:
    decision = classify_retrieval_decision(
        _decision_metrics(),
        _comparable_cost(0.90),
    )

    assert decision["verdict"] == "prefer_retrieval_only"
    assert decision["criteria"]["accuracy_ci_lower_bound_strictly_above"] == -0.05
    assert decision["criteria"]["minimum_marginal_cost_reduction"] == 0.90


@pytest.mark.parametrize(
    ("metrics", "cost", "expected_reason"),
    [
        (
            _decision_metrics(lower=-0.05),
            _comparable_cost(0.90),
            "accuracy_noninferiority_not_established",
        ),
        (
            _decision_metrics(),
            _comparable_cost(0.899),
            "marginal_cost_reduction_below_threshold",
        ),
        (
            _decision_metrics(resamples=9_999),
            _comparable_cost(0.90),
            "bootstrap_resamples_not_decision_grade",
        ),
        (
            _decision_metrics(),
            {"status": "not_comparable", "reasons": ["basis mismatch"]},
            "marginal_cost_not_comparable",
        ),
    ],
)
def test_decision_is_inconclusive_at_boundaries_or_without_comparable_cost(
    metrics: dict,
    cost: dict,
    expected_reason: str,
) -> None:
    decision = classify_retrieval_decision(metrics, cost)

    assert decision["verdict"] == "inconclusive"
    assert expected_reason in decision["reasons"]


def test_decision_rejects_material_accuracy_regression() -> None:
    metrics = _decision_metrics()
    metrics["func_hit"]["paired_bootstrap_95ci"] = [-0.12, -0.051]

    decision = classify_retrieval_decision(metrics, {"status": "not_comparable"})

    assert decision["verdict"] == "reject_retrieval_only"
    assert decision["reasons"] == ["material_accuracy_regression:func_hit"]


def test_marginal_cost_requires_one_shared_cross_arm_basis() -> None:
    ids = ["one", "two"]

    def arm_cases(cost: float, basis: str) -> dict[str, dict]:
        return {
            instance_id: {
                "cost_usd": {"marginal_query_estimate": cost},
                "cost_basis": {
                    "marginal_query": {"measurement_basis": basis}
                },
            }
            for instance_id in ids
        }

    comparable = summarize_marginal_cost(
        {
            "graph": arm_cases(5.0, "metered-usd-v1"),
            "retrieval-only": arm_cases(0.5, "metered-usd-v1"),
        },
        ids,
    )
    mismatched = summarize_marginal_cost(
        {
            "graph": arm_cases(5.0, "reported-token-usage-static-price-v1"),
            "retrieval-only": arm_cases(
                0.5, "fixed-conservative-pilot-estimate-v1"
            ),
        },
        ids,
    )

    assert comparable["status"] == "comparable"
    assert comparable["reduction_fraction"] == pytest.approx(0.90)
    assert mismatched["status"] == "not_comparable"
    assert "cross_arm_marginal_query_cost_basis_mismatch" in mismatched["reasons"]


def test_marginal_latency_compares_only_shared_query_stage_measurements() -> None:
    ids = ["one", "two"]
    by_arm = {
        arm: {
            instance_id: {
                "latency_s": {"marginal_query": value},
                "latency_basis": {
                    "marginal_query": {
                        "measurement_basis": "wall-clock-query-stage-v1"
                    }
                },
            }
            for instance_id, value in zip(ids, values, strict=True)
        }
        for arm, values in (
            ("graph", (2.0, 4.0)),
            ("retrieval-only", (0.5, 1.5)),
        )
    }

    latency = summarize_marginal_latency(by_arm, ids)

    assert latency["status"] == "comparable"
    assert latency["graph"]["marginal_query_mean_s"] == 3.0
    assert latency["retrieval-only"]["marginal_query_mean_s"] == 1.0


def test_graph_adapter_records_ten_ranks_identity_and_cost() -> None:
    row = {
        "instance_id": "owner__repo-1",
        "repo": "owner/repo",
        "base_commit": "a" * 40,
        "category": "Bug Report",
        "problem_statement": "Find Target.run.\n\nMore text.",
        "edit_functions": ["src/target.py:Target.run"],
    }
    repository_id = hashlib.sha256(b"repository").hexdigest()
    checkout_id = hashlib.sha256(b"checkout").hexdigest()
    generation = hashlib.sha256(
        f"{repository_id}\0{row['base_commit']}\0clean".encode()
    ).hexdigest()
    observed_identity = {
        "schema_version": 1,
        "repository_id": repository_id,
        "checkout_id": checkout_id,
        "source_revision": row["base_commit"],
        "dirty_fingerprint": "clean",
        "index_generation": generation,
        "captured_at": "2026-07-27T12:00:00Z",
    }
    raw = {
        "instance_id": row["instance_id"],
        "indexed": True,
        "agent_ran": True,
        "input_tokens": 100_000,
        "output_tokens": 5_000,
        "cost_estimate_usd": 0.125,
        "duration_s": 3.5,
        "latency_s": {
            "clone": 0.5,
            "index": 2.0,
            "marginal_query": 0.75,
            "total": 3.5,
        },
        "index_identity": observed_identity,
        "embedding_identity": {
            "status": "captured",
            "count": 12,
            "model": "voyage-code-3",
        },
        "attempts": [
            {
                "operation": "clone",
                "attempt": 1,
                "outcome": "success",
                "retry": False,
            }
        ],
        "agent_envelope": {
            "code_localize_agent": {
                "entities": [
                    {
                        "qualified_name": "Target.run",
                        "file_path": "src/target.py",
                    }
                ]
            }
        },
    }
    provenance = {
        "graph_sha": "1" * 40,
        "score_depth": 10,
        "model": "claude-haiku-4-5-20251001",
        "embedding_model": "voyage-code-3",
        "reranker": "none",
    }

    case = normalize_graph_case(raw, row, provenance)

    assert case["status"] == "ok"
    assert [rank["rank"] for rank in case["results"]] == list(range(1, 11))
    assert case["results"][0]["available"] is True
    assert case["results"][1]["available"] is False
    assert case["index_identity"] == observed_identity
    assert case["embedding_identity"] == raw["embedding_identity"]
    assert case["attempts"][0]["operation"] == "clone"
    assert case["attempts"][-1]["operation"] == "graph-agent"
    assert case["cost_usd"]["total_estimate"] == 0.125
    assert case["cost_usd"]["marginal_query_estimate"] == 0.125
    assert case["cost_basis"]["marginal_query"]["measurement_basis"] == (
        "reported-token-usage-static-price-v1"
    )
    assert case["latency_s"]["marginal_query"] == 0.75
    assert case["latency_basis"]["marginal_query"]["measurement_basis"] == (
        "wall-clock-query-stage-v1"
    )
    assert (case["file_hit"], case["class_hit"], case["func_hit"]) == (
        True,
        True,
        True,
    )


def test_graph_adapter_rejects_missing_or_mismatched_observed_identity() -> None:
    row = {
        "instance_id": "owner__repo-1",
        "repo": "owner/repo",
        "base_commit": "a" * 40,
        "problem_statement": "Find Target.run.",
        "edit_functions": ["src/target.py:Target.run"],
    }
    raw = {
        "instance_id": row["instance_id"],
        "indexed": True,
        "agent_ran": True,
        "agent_envelope": {"code_localize_agent": {"entities": []}},
        "embedding_identity": {
            "status": "captured",
            "count": 12,
            "model": "voyage-code-3",
        },
    }
    provenance = {
        "graph_sha": "1" * 40,
        "score_depth": 10,
        "model": "claude-haiku-4-5-20251001",
        "embedding_model": "voyage-code-3",
        "reranker": "none",
    }

    with pytest.raises(ValueError, match="observed index identity"):
        normalize_graph_case(raw, row, provenance)

    mismatched_repository_id = hashlib.sha256(b"repository").hexdigest()
    mismatched_revision = "b" * 40
    raw["index_identity"] = {
        "repository_id": mismatched_repository_id,
        "checkout_id": hashlib.sha256(b"checkout").hexdigest(),
        "source_revision": mismatched_revision,
        "dirty_fingerprint": "clean",
        "index_generation": hashlib.sha256(
            f"{mismatched_repository_id}\0{mismatched_revision}\0clean".encode()
        ).hexdigest(),
        "captured_at": "2026-07-27T12:00:00Z",
    }
    with pytest.raises(ValueError, match="pinned checkout"):
        normalize_graph_case(raw, row, provenance)


def test_graph_adapter_reuses_full_index_identity_validator(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    row = {
        "instance_id": "owner__repo-1",
        "repo": "owner/repo",
        "base_commit": "a" * 40,
        "problem_statement": "Find Target.run.",
        "edit_functions": ["src/target.py:Target.run"],
    }
    repository_id = hashlib.sha256(b"repository").hexdigest()
    identity = {
        "schema_version": 1,
        "repository_id": repository_id,
        "checkout_id": hashlib.sha256(b"checkout").hexdigest(),
        "source_revision": row["base_commit"],
        "dirty_fingerprint": "clean",
        "index_generation": hashlib.sha256(
            f"{repository_id}\0{row['base_commit']}\0clean".encode()
        ).hexdigest(),
        "captured_at": "2026-07-27T12:00:00Z",
    }
    raw = {
        "instance_id": row["instance_id"],
        "indexed": True,
        "agent_ran": False,
        "index_identity": identity,
        "embedding_identity": {
            "status": "captured",
            "count": 12,
            "model": "voyage-code-3",
        },
        "agent_envelope": {},
    }
    provenance = {
        "graph_sha": "1" * 40,
        "score_depth": 10,
        "model": "claude-haiku-4-5-20251001",
        "embedding_model": "voyage-code-3",
        "reranker": "none",
    }
    observed: list[object] = []

    def reject_with_full_validator(candidate: object) -> str:
        observed.append(candidate)
        return "index identity schema_version is not 1"

    monkeypatch.setattr(
        pilot_compare,
        "_validate_index_identity",
        reject_with_full_validator,
        raising=False,
    )

    with pytest.raises(ValueError, match="schema_version is not 1"):
        normalize_graph_case(raw, row, provenance)

    assert observed == [identity]


def test_graph_adapter_rejects_typed_invalid_experiment_before_normalization() -> None:
    row = {
        "instance_id": "owner__repo-1",
        "repo": "owner/repo",
        "base_commit": "a" * 40,
        "problem_statement": "Find Target.run.",
        "edit_functions": ["src/target.py:Target.run"],
    }
    raw = {
        "instance_id": row["instance_id"],
        "indexed": False,
        "agent_ran": False,
        "failure_class": "invalid_experiment",
        "failure_code": "embedding_identity_mismatch",
        "note": "semantic embedding identity does not match voyage-code-3",
        "agent_envelope": {},
    }
    provenance = {
        "graph_sha": "1" * 40,
        "score_depth": 10,
        "model": "claude-haiku-4-5-20251001",
        "embedding_model": "voyage-code-3",
        "reranker": "none",
    }

    with pytest.raises(
        ValueError,
        match="graph case owner__repo-1 is invalid_experiment",
    ):
        normalize_graph_case(raw, row, provenance)


def test_graph_adapter_preserves_typed_infrastructure_failure() -> None:
    row = {
        "instance_id": "owner__repo-1",
        "repo": "owner/repo",
        "base_commit": "a" * 40,
        "problem_statement": "Find Target.run.",
        "edit_functions": ["src/target.py:Target.run"],
    }
    raw = {
        "instance_id": row["instance_id"],
        "indexed": False,
        "agent_ran": False,
        "failure_class": "infrastructure",
        "failure_code": "clone_failed",
        "note": "clone failed",
        "agent_envelope": {},
    }
    provenance = {
        "graph_sha": "1" * 40,
        "score_depth": 10,
        "model": "claude-haiku-4-5-20251001",
        "embedding_model": "voyage-code-3",
        "reranker": "none",
    }

    case = normalize_graph_case(raw, row, provenance)

    assert case["status"] == "miss"
    assert case["failure_class"] == "infrastructure"
    assert case["failure_code"] == "clone_failed"


@pytest.mark.parametrize("mutated_field", ["graph_sha", "model"])
def test_graph_normalizer_rejects_restamped_raw_checkpoint_contract(
    mutated_field: str,
) -> None:
    expected_contract = {
        "schema_version": 1,
        "arm": "graph",
        "graph_sha": "1" * 40,
        "pin_sha256": "2" * 64,
        "dataset_sha256": "3" * 64,
        "repository": "owner/repo",
        "expected_instance_ids": [],
        "model": "claude-haiku-4-5-20251001",
        "embedding_model": "voyage-code-3",
        "iterations": 2,
        "score_depth": 10,
        "graph_budget_usd": "1.25",
        "harness_sha256": "4" * 64,
        "scorer_sha256": "5" * 64,
    }
    observed_contract = dict(expected_contract)
    observed_contract[mutated_field] = (
        "6" * 40 if mutated_field == "graph_sha" else "different-model"
    )
    raw = {
        "cases": [],
        "checkpoint_contract": observed_contract,
    }

    with pytest.raises(ValueError, match="checkpoint contract"):
        pilot_compare.normalize_graph_artifact(
            raw,
            pin={},
            rows=[],
            expected_instance_ids=[],
            provenance={},
            checkpoint_contract=expected_contract,
            raw_sha256="7" * 64,
        )


@pytest.mark.parametrize(
    ("mutated_field", "mutated_value"),
    [
        ("graph_sha", "9" * 40),
        ("model", "different-model"),
        ("graph_budget_usd", "9.99"),
    ],
)
def test_graph_harness_binds_checkpoint_contract_to_runtime_inputs(
    tmp_path: Path,
    mutated_field: str,
    mutated_value: str,
) -> None:
    canonical_pin = tmp_path / "pin.json"
    parquet = tmp_path / "locbench.parquet"
    canonical_pin.write_bytes(b"canonical pin")
    parquet.write_bytes(b"dataset")
    expected = eval_locbench_batch.expected_graph_checkpoint_contract(
        graph_sha="1" * 40,
        canonical_pin=canonical_pin,
        parquet=parquet,
        repository="owner/repo",
        expected_instance_ids=["owner__repo-1"],
        model="claude-haiku-4-5-20251001",
        embedding_model="voyage-code-3",
        iterations=2,
        score_depth=10,
        graph_budget_usd="1.25",
    )

    assert (
        eval_locbench_batch.validate_graph_checkpoint_contract(expected, expected)
        == expected
    )
    mutated = dict(expected)
    mutated[mutated_field] = mutated_value
    with pytest.raises(ValueError, match="checkpoint contract"):
        eval_locbench_batch.validate_graph_checkpoint_contract(mutated, expected)


def test_graph_main_rejects_runtime_contract_mismatch_before_paid_work(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import pandas as pd

    parquet = tmp_path / "locbench.parquet"
    pd.DataFrame(
        [
            {
                "instance_id": "owner__repo-1",
                "repo": "owner/repo",
                "base_commit": "a" * 40,
                "category": "Bug Report",
                "problem_statement": "Find Target.run.",
                "edit_functions": ["src/target.py:Target.run"],
            }
        ]
    ).to_parquet(parquet)
    shard_pin = tmp_path / "shard-pin.json"
    shard_pin.write_text(
        json.dumps({"pinned_instance_ids": ["owner__repo-1"]}),
        encoding="utf-8",
    )
    canonical_pin = tmp_path / "canonical-pin.json"
    canonical_pin.write_text('{"n": 1}', encoding="utf-8")
    expected = eval_locbench_batch.expected_graph_checkpoint_contract(
        graph_sha="1" * 40,
        canonical_pin=canonical_pin,
        parquet=parquet,
        repository="owner/repo",
        expected_instance_ids=["owner__repo-1"],
        model="claude-haiku-4-5-20251001",
        embedding_model="voyage-code-3",
        iterations=2,
        score_depth=10,
        graph_budget_usd="1.25",
    )
    expected["model"] = "restamped-model"
    contract_path = tmp_path / "contract.json"
    contract_path.write_text(json.dumps(expected), encoding="utf-8")
    monkeypatch.setenv("ANTHROPIC_API_KEY", "test-only")
    monkeypatch.setenv("VOYAGE_API_KEY", "test-only")
    monkeypatch.setenv("ANTHROPIC_MODEL", "claude-haiku-4-5-20251001")
    monkeypatch.setenv("VOYAGE_EMBED_MODEL", "voyage-code-3")
    monkeypatch.setenv("LOCAGENT_ITERATIONS", "2")
    monkeypatch.setattr(
        eval_locbench_batch,
        "evaluate_instance",
        lambda *args, **kwargs: pytest.fail("paid work must not start"),
    )
    monkeypatch.setattr(
        sys,
        "argv",
        [
            "eval_locbench_batch.py",
            "--instances",
            str(shard_pin),
            "--canonical-pin",
            str(canonical_pin),
            "--parquet",
            str(parquet),
            "--repository",
            "owner/repo",
            "--graph-sha",
            "1" * 40,
            "--score-depth",
            "10",
            "--budget-usd",
            "1.25",
            "--checkpoint-contract",
            str(contract_path),
            "--per-case-json",
            str(tmp_path / "raw.json"),
            "--output",
            str(tmp_path / "report.md"),
            "--workdir",
            str(tmp_path / "work"),
        ],
    )

    assert eval_locbench_batch.main() == 2


@pytest.mark.parametrize(
    ("failed_write_number", "expected_paid_calls"),
    [(1, 0), (2, 1)],
)
def test_graph_main_aborts_on_checkpoint_failure_before_more_paid_work(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    failed_write_number: int,
    expected_paid_calls: int,
) -> None:
    import pandas as pd

    parquet = tmp_path / "locbench.parquet"
    pd.DataFrame(
        [
            {
                "instance_id": f"owner__repo-{number}",
                "repo": "owner/repo",
                "base_commit": "a" * 40,
                "category": "Bug Report",
                "problem_statement": "Find Target.run.",
                "edit_functions": ["src/target.py:Target.run"],
            }
            for number in (1, 2)
        ]
    ).to_parquet(parquet)
    expected_ids = ["owner__repo-1", "owner__repo-2"]
    shard_pin = tmp_path / "shard-pin.json"
    shard_pin.write_text(
        json.dumps({"pinned_instance_ids": expected_ids}),
        encoding="utf-8",
    )
    canonical_pin = tmp_path / "canonical-pin.json"
    canonical_pin.write_text('{"n": 1}', encoding="utf-8")
    contract = eval_locbench_batch.expected_graph_checkpoint_contract(
        graph_sha="1" * 40,
        canonical_pin=canonical_pin,
        parquet=parquet,
        repository="owner/repo",
        expected_instance_ids=expected_ids,
        model="claude-haiku-4-5-20251001",
        embedding_model="voyage-code-3",
        iterations=2,
        score_depth=10,
        graph_budget_usd="1.25",
    )
    contract_path = tmp_path / "contract.json"
    contract_path.write_text(json.dumps(contract), encoding="utf-8")
    monkeypatch.setenv("ANTHROPIC_API_KEY", "test-only")
    monkeypatch.setenv("VOYAGE_API_KEY", "test-only")
    monkeypatch.setenv("ANTHROPIC_MODEL", "claude-haiku-4-5-20251001")
    monkeypatch.setenv("VOYAGE_EMBED_MODEL", "voyage-code-3")
    monkeypatch.setenv("LOCAGENT_ITERATIONS", "2")
    paid_calls = 0

    def fake_evaluate(row, *args, **kwargs):
        nonlocal paid_calls
        paid_calls += 1
        return eval_locbench_batch.InstanceResult(
            instance_id=row["instance_id"],
            repo="owner/repo",
            category="Bug Report",
            ground_truth=[],
        )

    monkeypatch.setattr(eval_locbench_batch, "evaluate_instance", fake_evaluate)
    write_calls = 0

    def fail_selected_checkpoint(*args, **kwargs):
        nonlocal write_calls
        write_calls += 1
        if write_calls == failed_write_number:
            raise OSError("durable write failed")

    monkeypatch.setattr(
        eval_locbench_batch,
        "write_graph_checkpoint",
        fail_selected_checkpoint,
    )
    monkeypatch.setattr(
        sys,
        "argv",
        [
            "eval_locbench_batch.py",
            "--instances",
            str(shard_pin),
            "--canonical-pin",
            str(canonical_pin),
            "--parquet",
            str(parquet),
            "--repository",
            "owner/repo",
            "--graph-sha",
            "1" * 40,
            "--score-depth",
            "10",
            "--budget-usd",
            "1.25",
            "--checkpoint-contract",
            str(contract_path),
            "--per-case-json",
            str(tmp_path / "raw.json"),
            "--output",
            str(tmp_path / "report.md"),
            "--workdir",
            str(tmp_path / "work"),
        ],
    )

    assert eval_locbench_batch.main() == 2
    assert paid_calls == expected_paid_calls


def test_graph_main_returns_nonzero_and_stops_after_invalid_experiment(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    import pandas as pd

    parquet = tmp_path / "locbench.parquet"
    rows = [
        {
            "instance_id": f"owner__repo-{number}",
            "repo": "owner/repo",
            "base_commit": "a" * 40,
            "category": "Bug Report",
            "problem_statement": "Find Target.run.",
            "edit_functions": ["src/target.py:Target.run"],
        }
        for number in (1, 2)
    ]
    pd.DataFrame(rows).to_parquet(parquet)
    expected_ids = [row["instance_id"] for row in rows]
    shard_pin = tmp_path / "shard-pin.json"
    shard_pin.write_text(
        json.dumps({"pinned_instance_ids": expected_ids}),
        encoding="utf-8",
    )
    canonical_pin = tmp_path / "canonical-pin.json"
    canonical_pin.write_text('{"n": 2}', encoding="utf-8")
    contract = eval_locbench_batch.expected_graph_checkpoint_contract(
        graph_sha="1" * 40,
        canonical_pin=canonical_pin,
        parquet=parquet,
        repository="owner/repo",
        expected_instance_ids=expected_ids,
        model="claude-haiku-4-5-20251001",
        embedding_model="voyage-code-3",
        iterations=2,
        score_depth=10,
        graph_budget_usd="1.25",
    )
    contract_path = tmp_path / "contract.json"
    contract_path.write_text(json.dumps(contract), encoding="utf-8")
    raw_checkpoint = tmp_path / "raw.json"
    monkeypatch.setenv("ANTHROPIC_API_KEY", "test-only")
    monkeypatch.setenv("VOYAGE_API_KEY", "test-only")
    monkeypatch.setenv("ANTHROPIC_MODEL", "claude-haiku-4-5-20251001")
    monkeypatch.setenv("VOYAGE_EMBED_MODEL", "voyage-code-3")
    monkeypatch.setenv("LOCAGENT_ITERATIONS", "2")
    paid_calls = 0

    def invalid_evaluate(row, *args, **kwargs):
        nonlocal paid_calls
        paid_calls += 1
        return eval_locbench_batch.InstanceResult(
            instance_id=row["instance_id"],
            repo="owner/repo",
            category="Bug Report",
            ground_truth=[],
            failure_class="invalid_experiment",
            failure_code="agent_envelope_invalid",
        )

    monkeypatch.setattr(
        eval_locbench_batch,
        "evaluate_instance",
        invalid_evaluate,
    )
    monkeypatch.setattr(
        sys,
        "argv",
        [
            "eval_locbench_batch.py",
            "--instances",
            str(shard_pin),
            "--canonical-pin",
            str(canonical_pin),
            "--parquet",
            str(parquet),
            "--repository",
            "owner/repo",
            "--graph-sha",
            "1" * 40,
            "--score-depth",
            "10",
            "--budget-usd",
            "1.25",
            "--checkpoint-contract",
            str(contract_path),
            "--per-case-json",
            str(raw_checkpoint),
            "--output",
            str(tmp_path / "report.md"),
            "--workdir",
            str(tmp_path / "work"),
        ],
    )

    exit_code = eval_locbench_batch.main()
    checkpoint = json.loads(raw_checkpoint.read_text(encoding="utf-8"))

    assert exit_code == 2
    assert paid_calls == 1
    assert checkpoint["aborted_reason"] == (
        "invalid_experiment:agent_envelope_invalid:owner__repo-1"
    )


def test_reduce_cli_writes_checksummed_exact_coverage_summary(
    tmp_path: Path,
) -> None:
    ids = [f"owner__repo-{number}" for number in range(200)]
    pin_path = tmp_path / "pin.json"
    pin_path.write_text(
        json.dumps(
            {
                "n": 200,
                "score_depth": 10,
                "pinned_instance_ids": ids,
                "cases": [
                    {
                        "instance_id": instance_id,
                        "repo": "owner/repo",
                    }
                    for instance_id in ids
                ],
            },
            indent=2,
        )
        + "\n",
        encoding="utf-8",
    )
    pin_digest = __import__("hashlib").sha256(pin_path.read_bytes()).hexdigest()
    scorer_digest = __import__("hashlib").sha256(
        (RESEARCH_DIR / "pilot_compare.py").read_bytes()
    ).hexdigest()
    graph = comparison_artifact("graph", ids, depth=10, pin_sha256=pin_digest)
    retrieval = comparison_artifact(
        "retrieval-only",
        ids,
        depth=10,
        pin_sha256=pin_digest,
    )
    graph["provenance"]["scorer_sha256"] = scorer_digest
    retrieval["provenance"]["scorer_sha256"] = scorer_digest
    retrieval["provenance"]["repository"] = "owner/repo"
    retrieval["provenance"]["budget_contract"] = {
        "shard_allocation_usd": "1.000000",
        "arm_ceiling_usd": "1.000000",
        "total_ceiling_usd": "2.000000",
        "provider_operation_bound_policy": (
            "provider-enforced-per-operation-usd-required-v1"
        ),
    }
    graph_path = tmp_path / "graph.json"
    retrieval_path = tmp_path / "retrieval.json"
    graph_path.write_text(json.dumps(graph), encoding="utf-8")
    retrieval_path.write_text(json.dumps(retrieval), encoding="utf-8")
    output = tmp_path / "summary.json"

    completed = subprocess.run(
        [
            sys.executable,
            str(RESEARCH_DIR / "pilot_compare.py"),
            "reduce",
            "--pin",
            str(pin_path),
            "--graph",
            str(graph_path),
            "--retrieval",
            str(retrieval_path),
            "--out",
            str(output),
            "--n-boot",
            "100",
        ],
        cwd=RESEARCH_DIR.parents[1],
        capture_output=True,
        text=True,
    )

    assert completed.returncode == 0, completed.stderr
    summary = json.loads(output.read_text(encoding="utf-8"))
    assert summary["comparison"] == "retrieval-only-vs-graph"
    assert summary["n"] == 200
    digest, filename = Path(str(output) + ".sha256").read_text(
        encoding="utf-8"
    ).strip().split("  ", 1)
    assert digest == __import__("hashlib").sha256(output.read_bytes()).hexdigest()
    assert filename == output.name


def test_normalize_graph_cli_converts_exact_repository_shard(
    tmp_path: Path,
) -> None:
    import hashlib
    import pandas as pd

    parquet = tmp_path / "locbench.parquet"
    pd.DataFrame(
        [
            {
                "instance_id": "owner__repo-1",
                "repo": "owner/repo",
                "base_commit": "a" * 40,
                "category": "Bug Report",
                "problem_statement": "Find Target.run.\n\nMore text.",
                "edit_functions": ["src/target.py:Target.run"],
            }
        ]
    ).to_parquet(parquet)
    pin_path = tmp_path / "pin.json"
    pin_path.write_text(
        json.dumps(
            {
                "n": 1,
                "score_depth": 10,
                "pinned_instance_ids": ["owner__repo-1"],
                "cases": [
                    {
                        "instance_id": "owner__repo-1",
                        "repo": "owner/repo",
                        "base_commit": "a" * 40,
                        "category": "Bug Report",
                    }
                ],
                "dataset": {
                    "parquet_sha256": hashlib.sha256(parquet.read_bytes()).hexdigest(),
                    "revision": "c" * 40,
                },
                "component_pins": {
                    "code_search": {
                        "tag": "v0.2.1",
                        "artifact_sha256": (
                            "567d4caabdd3b5446bcaa789afc7104fb8cce142ff69d7fc8f1294398532e7e9"
                        ),
                    }
                },
            },
            indent=2,
        )
        + "\n",
        encoding="utf-8",
    )
    raw_path = tmp_path / "raw-graph.json"
    repository_id = hashlib.sha256(b"repository").hexdigest()
    checkout_id = hashlib.sha256(b"checkout").hexdigest()
    source_revision = "a" * 40
    raw_path.write_text(
        json.dumps(
            {
                "checkpoint_contract": {
                    "schema_version": 1,
                    "arm": "graph",
                    "graph_sha": "1" * 40,
                    "pin_sha256": hashlib.sha256(
                        pin_path.read_bytes()
                    ).hexdigest(),
                    "dataset_sha256": hashlib.sha256(
                        parquet.read_bytes()
                    ).hexdigest(),
                    "repository": "owner/repo",
                    "expected_instance_ids": ["owner__repo-1"],
                    "model": "claude-haiku-4-5-20251001",
                    "embedding_model": "voyage-code-3",
                    "iterations": 2,
                    "score_depth": 10,
                    "graph_budget_usd": "1.25",
                    "harness_sha256": hashlib.sha256(
                        (RESEARCH_DIR / "eval_locbench_batch.py").read_bytes()
                    ).hexdigest(),
                    "scorer_sha256": hashlib.sha256(
                        (RESEARCH_DIR / "pilot_compare.py").read_bytes()
                    ).hexdigest(),
                },
                "cases": [
                    {
                        "instance_id": "owner__repo-1",
                        "indexed": True,
                        "agent_ran": True,
                        "cost_estimate_usd": 0.125,
                        "index_identity": {
                            "schema_version": 1,
                            "repository_id": repository_id,
                            "checkout_id": checkout_id,
                            "source_revision": source_revision,
                            "dirty_fingerprint": "clean",
                            "index_generation": hashlib.sha256(
                                (
                                    f"{repository_id}\0{source_revision}\0clean"
                                ).encode()
                            ).hexdigest(),
                            "captured_at": "2026-07-27T12:00:00Z",
                        },
                        "embedding_identity": {
                            "status": "captured",
                            "count": 12,
                            "model": "voyage-code-3",
                        },
                        "agent_envelope": {
                            "code_localize_agent": {
                                "entities": [
                                    {
                                        "qualified_name": "Target.run",
                                        "file_path": "src/target.py",
                                    }
                                ]
                            }
                        },
                    }
                ],
            }
        ),
        encoding="utf-8",
    )
    output = tmp_path / "graph.json"

    completed = subprocess.run(
        [
            sys.executable,
            str(RESEARCH_DIR / "pilot_compare.py"),
            "normalize-graph",
            "--raw",
            str(raw_path),
            "--pin",
            str(pin_path),
            "--parquet",
            str(parquet),
            "--repository",
            "owner/repo",
            "--graph-sha",
            "1" * 40,
            "--iterations",
            "2",
            "--graph-budget-usd",
            "1.25",
            "--out",
            str(output),
        ],
        cwd=RESEARCH_DIR.parents[1],
        capture_output=True,
        text=True,
    )

    assert completed.returncode == 0, completed.stderr
    artifact = json.loads(output.read_text(encoding="utf-8"))
    assert artifact["arm"] == "graph"
    assert artifact["status"] == "complete"
    assert artifact["expected_instance_ids"] == ["owner__repo-1"]
    assert artifact["cases"][0]["results"][0]["rank"] == 1
    assert artifact["provenance"]["model"] == "claude-haiku-4-5-20251001"


def test_graph_index_child_environment_allows_only_runtime_and_voyage(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    index_binary = tmp_path / "index"
    index_binary.write_text("", encoding="utf-8")
    monkeypatch.setattr(eval_locbench_batch, "INDEX_BIN", index_binary)
    forbidden = (
        "CODE_INTEL_COMPONENT_TOKEN",
        "GH_TOKEN",
        "GITHUB_TOKEN",
        "ANTHROPIC_API_KEY",
        "UNRELATED_SECRET",
    )
    for name in forbidden:
        monkeypatch.setenv(name, f"{name}-secret")
    monkeypatch.setenv("VOYAGE_API_KEY", "voyage-secret")
    monkeypatch.setenv("VOYAGE_EMBED_MODEL", "voyage-code-3")
    monkeypatch.setenv("PATH", "/safe/bin")
    captured: dict = {}
    repository_id = hashlib.sha256(b"repository").hexdigest()
    checkout_id = hashlib.sha256(b"checkout").hexdigest()
    source_revision = "a" * 40
    generation = hashlib.sha256(
        f"{repository_id}\0{source_revision}\0clean".encode()
    ).hexdigest()
    index_payload = {
        "identity_status": "captured",
        "index_identity": {
            "schema_version": 1,
            "repository_id": repository_id,
            "checkout_id": checkout_id,
            "source_revision": source_revision,
            "dirty_fingerprint": "clean",
            "index_generation": generation,
            "captured_at": "2026-07-27T12:00:00Z",
        },
        "embedding_status": "captured",
        "embedding_count": 12,
        "embedding_models": {"voyage-code-3": 12},
    }

    def fake_run(*args, **kwargs):
        captured["command"] = args[0]
        captured.update(kwargs)
        return subprocess.CompletedProcess(
            args=args,
            returncode=0,
            stdout=json.dumps(index_payload).encode(),
            stderr=b"",
        )

    monkeypatch.setattr(eval_locbench_batch.subprocess, "run", fake_run)

    outcome = eval_locbench_batch.index_repo(tmp_path)
    assert outcome.success is True
    assert outcome.index_identity == index_payload["index_identity"]
    assert outcome.embedding_count == 12
    assert outcome.embedding_model == "voyage-code-3"
    assert "--raw" in captured["command"]
    assert captured["env"]["VOYAGE_API_KEY"] == "voyage-secret"
    assert captured["env"]["VOYAGE_EMBED_MODEL"] == "voyage-code-3"
    assert captured["env"]["PATH"] == "/safe/bin"
    for name in forbidden:
        assert name not in captured["env"]


def test_graph_index_rejects_synthetic_or_nonsemantic_identity(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    index_binary = tmp_path / "index"
    index_binary.write_text("", encoding="utf-8")
    monkeypatch.setattr(eval_locbench_batch, "INDEX_BIN", index_binary)
    monkeypatch.setenv("VOYAGE_API_KEY", "voyage-secret")
    monkeypatch.setenv("VOYAGE_EMBED_MODEL", "voyage-code-3")

    def fake_run(*args, **kwargs):
        return subprocess.CompletedProcess(
            args=args,
            returncode=0,
            stdout=json.dumps(
                {
                    "identity_status": "captured",
                    "index_identity": {
                        "schema_version": 1,
                        "repository_id": "synthetic-repository",
                        "checkout_id": "synthetic-checkout",
                        "source_revision": "a" * 40,
                        "dirty_fingerprint": "clean",
                        "index_generation": "synthetic-generation",
                        "captured_at": "2026-07-27T12:00:00Z",
                    },
                    "embedding_status": "captured",
                    "embedding_count": 0,
                    "embedding_models": {},
                }
            ).encode(),
            stderr=b"",
        )

    monkeypatch.setattr(eval_locbench_batch.subprocess, "run", fake_run)

    outcome = eval_locbench_batch.index_repo(tmp_path)
    assert outcome.success is False
    assert "identity" in outcome.error
    assert outcome.failure_class == "invalid_experiment"
    assert outcome.failure_code == "index_identity_invalid"


def test_graph_index_types_process_failure_as_infrastructure(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    index_binary = tmp_path / "index"
    index_binary.write_text("", encoding="utf-8")
    monkeypatch.setattr(eval_locbench_batch, "INDEX_BIN", index_binary)
    monkeypatch.setattr(
        eval_locbench_batch.subprocess,
        "run",
        lambda *args, **kwargs: subprocess.CompletedProcess(
            args=args,
            returncode=1,
            stdout=b"",
            stderr=b"temporary service failure",
        ),
    )

    outcome = eval_locbench_batch.index_repo(tmp_path)

    assert outcome.success is False
    assert outcome.failure_class == "infrastructure"
    assert outcome.failure_code == "index_process_failed"


def test_graph_index_types_remaining_operational_failures_as_infrastructure(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    index_binary = tmp_path / "index"
    monkeypatch.setattr(eval_locbench_batch, "INDEX_BIN", index_binary)

    missing = eval_locbench_batch.index_repo(tmp_path)
    assert (missing.failure_class, missing.failure_code) == (
        "infrastructure",
        "index_binary_missing",
    )

    index_binary.write_text("", encoding="utf-8")
    monkeypatch.setattr(
        eval_locbench_batch.subprocess,
        "run",
        lambda *args, **kwargs: subprocess.CompletedProcess(
            args=args,
            returncode=0,
            stdout=b"not-json",
            stderr=b"",
        ),
    )
    malformed = eval_locbench_batch.index_repo(tmp_path)
    assert (malformed.failure_class, malformed.failure_code) == (
        "infrastructure",
        "index_response_invalid",
    )

    def timeout(*args, **kwargs):
        raise subprocess.TimeoutExpired(cmd=args[0], timeout=1800)

    monkeypatch.setattr(eval_locbench_batch.subprocess, "run", timeout)
    timed_out = eval_locbench_batch.index_repo(tmp_path)
    assert (timed_out.failure_class, timed_out.failure_code) == (
        "infrastructure",
        "index_timeout",
    )


def test_graph_index_types_embedding_identity_mismatch_as_invalid_experiment(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    index_binary = tmp_path / "index"
    index_binary.write_text("", encoding="utf-8")
    monkeypatch.setattr(eval_locbench_batch, "INDEX_BIN", index_binary)
    monkeypatch.setenv("VOYAGE_API_KEY", "voyage-secret")
    monkeypatch.setenv("VOYAGE_EMBED_MODEL", "voyage-code-3")
    repository_id = hashlib.sha256(b"repository").hexdigest()
    checkout_id = hashlib.sha256(b"checkout").hexdigest()
    source_revision = "a" * 40
    generation = hashlib.sha256(
        f"{repository_id}\0{source_revision}\0clean".encode()
    ).hexdigest()

    def fake_run(*args, **kwargs):
        return subprocess.CompletedProcess(
            args=args,
            returncode=0,
            stdout=json.dumps(
                {
                    "identity_status": "captured",
                    "index_identity": {
                        "schema_version": 1,
                        "repository_id": repository_id,
                        "checkout_id": checkout_id,
                        "source_revision": source_revision,
                        "dirty_fingerprint": "clean",
                        "index_generation": generation,
                        "captured_at": "2026-07-27T12:00:00Z",
                    },
                    "embedding_status": "captured",
                    "embedding_count": 4,
                    "embedding_models": {"wrong-model": 4},
                }
            ).encode(),
            stderr=b"",
        )

    monkeypatch.setattr(eval_locbench_batch.subprocess, "run", fake_run)

    outcome = eval_locbench_batch.index_repo(tmp_path)

    assert outcome.success is False
    assert outcome.failure_class == "invalid_experiment"
    assert outcome.failure_code == "embedding_identity_mismatch"


@pytest.mark.parametrize(
    "embedding_fields",
    [
        {
            "embedding_status": "missing",
            "embedding_count": 0,
            "embedding_models": {},
        },
        {
            "embedding_status": "captured",
            "embedding_count": "four",
            "embedding_models": {},
        },
    ],
)
def test_graph_index_types_invalid_embedding_inventory(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    embedding_fields: dict,
) -> None:
    index_binary = tmp_path / "index"
    index_binary.write_text("", encoding="utf-8")
    monkeypatch.setattr(eval_locbench_batch, "INDEX_BIN", index_binary)
    repository_id = hashlib.sha256(b"repository").hexdigest()
    source_revision = "a" * 40
    payload = {
        "identity_status": "captured",
        "index_identity": {
            "schema_version": 1,
            "repository_id": repository_id,
            "checkout_id": hashlib.sha256(b"checkout").hexdigest(),
            "source_revision": source_revision,
            "dirty_fingerprint": "clean",
            "index_generation": hashlib.sha256(
                f"{repository_id}\0{source_revision}\0clean".encode()
            ).hexdigest(),
            "captured_at": "2026-07-27T12:00:00Z",
        },
        **embedding_fields,
    }
    monkeypatch.setattr(
        eval_locbench_batch.subprocess,
        "run",
        lambda *args, **kwargs: subprocess.CompletedProcess(
            args=args,
            returncode=0,
            stdout=json.dumps(payload).encode(),
            stderr=b"",
        ),
    )

    outcome = eval_locbench_batch.index_repo(tmp_path)

    assert outcome.failure_class == "invalid_experiment"
    assert outcome.failure_code == "embedding_identity_invalid"


def test_graph_evaluation_persists_typed_invalid_experiment_identity_failure(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        eval_locbench_batch,
        "clone_repo",
        lambda *args, **kwargs: True,
    )
    monkeypatch.setattr(eval_locbench_batch, "repo_size_mb", lambda path: 1.0)
    monkeypatch.setattr(
        eval_locbench_batch,
        "index_repo",
        lambda path: eval_locbench_batch.GraphIndexOutcome(
            False,
            "index identity source_revision is not pinned",
            failure_class="invalid_experiment",
            failure_code="index_identity_invalid",
        ),
    )
    row = {
        "instance_id": "owner__repo-1",
        "repo": "owner/repo",
        "base_commit": "a" * 40,
        "category": "Bug Report",
        "problem_statement": "Find Target.run.",
        "edit_functions": ["src/target.py:Target.run"],
    }

    result = eval_locbench_batch.evaluate_instance(row, tmp_path, json_mode=True)

    assert result.failure_class == "invalid_experiment"
    assert result.failure_code == "index_identity_invalid"
    assert result.note == "index identity source_revision is not pinned"


def test_graph_evaluation_types_clone_failure_as_infrastructure(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        eval_locbench_batch,
        "clone_repo",
        lambda *args, **kwargs: False,
    )
    row = {
        "instance_id": "owner__repo-1",
        "repo": "owner/repo",
        "base_commit": "a" * 40,
        "category": "Bug Report",
        "problem_statement": "Find Target.run.",
        "edit_functions": ["src/target.py:Target.run"],
    }

    result = eval_locbench_batch.evaluate_instance(row, tmp_path, json_mode=True)

    assert result.failure_class == "infrastructure"
    assert result.failure_code == "clone_failed"


def test_graph_evaluation_types_repo_size_skip_as_measured_outcome(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        eval_locbench_batch,
        "clone_repo",
        lambda *args, **kwargs: True,
    )
    monkeypatch.setattr(
        eval_locbench_batch,
        "repo_size_mb",
        lambda path: eval_locbench_batch.MAX_REPO_MB + 1,
    )
    row = {
        "instance_id": "owner__repo-1",
        "repo": "owner/repo",
        "base_commit": "a" * 40,
        "category": "Bug Report",
        "problem_statement": "Find Target.run.",
        "edit_functions": ["src/target.py:Target.run"],
    }

    result = eval_locbench_batch.evaluate_instance(row, tmp_path, json_mode=True)

    assert result.failure_class == "measured_outcome"
    assert result.failure_code == "repo_too_large"


def test_graph_evaluation_types_pinned_checkout_identity_mismatch(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        eval_locbench_batch,
        "clone_repo",
        lambda *args, **kwargs: True,
    )
    monkeypatch.setattr(eval_locbench_batch, "repo_size_mb", lambda path: 1.0)
    monkeypatch.setattr(
        eval_locbench_batch,
        "index_repo",
        lambda path: eval_locbench_batch.GraphIndexOutcome(
            True,
            index_identity={
                "source_revision": "b" * 40,
                "dirty_fingerprint": "clean",
            },
            embedding_count=4,
            embedding_model="voyage-code-3",
        ),
    )
    row = {
        "instance_id": "owner__repo-1",
        "repo": "owner/repo",
        "base_commit": "a" * 40,
        "category": "Bug Report",
        "problem_statement": "Find Target.run.",
        "edit_functions": ["src/target.py:Target.run"],
    }

    result = eval_locbench_batch.evaluate_instance(row, tmp_path, json_mode=True)

    assert result.failure_class == "invalid_experiment"
    assert result.failure_code == "index_identity_mismatch"


def test_graph_evaluation_types_missing_index_database_as_infrastructure(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        eval_locbench_batch,
        "clone_repo",
        lambda *args, **kwargs: True,
    )
    monkeypatch.setattr(eval_locbench_batch, "repo_size_mb", lambda path: 1.0)
    monkeypatch.setattr(
        eval_locbench_batch,
        "index_repo",
        lambda path: eval_locbench_batch.GraphIndexOutcome(
            True,
            index_identity={
                "source_revision": "a" * 40,
                "dirty_fingerprint": "clean",
            },
            embedding_count=2,
            embedding_model="voyage-code-3",
        ),
    )
    monkeypatch.setattr(
        eval_locbench_batch,
        "db_path_for",
        lambda path: tmp_path / "missing.db",
    )
    row = {
        "instance_id": "owner__repo-1",
        "repo": "owner/repo",
        "base_commit": "a" * 40,
        "category": "Bug Report",
        "problem_statement": "Find Target.run.",
        "edit_functions": ["src/target.py:Target.run"],
    }

    result = eval_locbench_batch.evaluate_instance(row, tmp_path, json_mode=True)

    assert result.failure_class == "infrastructure"
    assert result.failure_code == "index_database_missing"


def test_graph_evaluation_types_agent_failure_as_infrastructure(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    index_db = tmp_path / "index.db"
    index_db.write_text("", encoding="utf-8")
    monkeypatch.setattr(
        eval_locbench_batch,
        "clone_repo",
        lambda *args, **kwargs: True,
    )
    monkeypatch.setattr(eval_locbench_batch, "repo_size_mb", lambda path: 1.0)
    monkeypatch.setattr(
        eval_locbench_batch,
        "index_repo",
        lambda path: eval_locbench_batch.GraphIndexOutcome(
            True,
            index_identity={
                "source_revision": "a" * 40,
                "dirty_fingerprint": "clean",
            },
            embedding_count=2,
            embedding_model="voyage-code-3",
        ),
    )
    monkeypatch.setattr(eval_locbench_batch, "db_path_for", lambda path: index_db)
    monkeypatch.setattr(
        eval_locbench_batch,
        "run_agent",
        lambda *args, **kwargs: {"error": "provider unavailable"},
    )
    row = {
        "instance_id": "owner__repo-1",
        "repo": "owner/repo",
        "base_commit": "a" * 40,
        "category": "Bug Report",
        "problem_statement": "Find Target.run.",
        "edit_functions": ["src/target.py:Target.run"],
    }

    result = eval_locbench_batch.evaluate_instance(row, tmp_path, json_mode=True)

    assert result.failure_class == "infrastructure"
    assert result.failure_code == "agent_failed"


@pytest.mark.parametrize(
    "agent_envelope",
    [
        {},
        {
            "code_localize_agent": {
                "entities": "not-a-list",
                "turns": 1,
                "stop_reason": "finalized",
                "input_tokens": 100,
                "output_tokens": 10,
            }
        },
        {
            "code_localize_agent": {
                "entities": [{"qualified_name": "Target.run"}],
                "turns": 1,
                "stop_reason": "finalized",
                "input_tokens": 100,
                "output_tokens": 10,
            }
        },
        {
            "code_localize_agent": {
                "entities": [],
                "turns": True,
                "stop_reason": "finalized",
                "input_tokens": 100,
                "output_tokens": 10,
            }
        },
        {
            "code_localize_agent": {
                "entities": [],
                "turns": 1,
                "stop_reason": "",
                "input_tokens": 100,
                "output_tokens": -1,
            }
        },
    ],
)
def test_graph_evaluation_rejects_malformed_success_envelope_before_agent_ran(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    agent_envelope: dict,
) -> None:
    index_db = tmp_path / "index.db"
    index_db.write_text("", encoding="utf-8")
    monkeypatch.setattr(
        eval_locbench_batch,
        "clone_repo",
        lambda *args, **kwargs: True,
    )
    monkeypatch.setattr(eval_locbench_batch, "repo_size_mb", lambda path: 1.0)
    monkeypatch.setattr(
        eval_locbench_batch,
        "index_repo",
        lambda path: eval_locbench_batch.GraphIndexOutcome(
            True,
            index_identity={
                "source_revision": "a" * 40,
                "dirty_fingerprint": "clean",
            },
            embedding_count=2,
            embedding_model="voyage-code-3",
        ),
    )
    monkeypatch.setattr(eval_locbench_batch, "db_path_for", lambda path: index_db)
    monkeypatch.setattr(
        eval_locbench_batch,
        "run_agent",
        lambda *args, **kwargs: {
            "stdout": json.dumps(agent_envelope),
            "agent_json": agent_envelope,
            "turns": (
                agent_envelope.get("code_localize_agent", {}).get("turns", 0)
                if isinstance(agent_envelope.get("code_localize_agent"), dict)
                else 0
            ),
            "input_tokens": (
                agent_envelope.get("code_localize_agent", {}).get(
                    "input_tokens",
                    0,
                )
                if isinstance(agent_envelope.get("code_localize_agent"), dict)
                else 0
            ),
            "output_tokens": (
                agent_envelope.get("code_localize_agent", {}).get(
                    "output_tokens",
                    0,
                )
                if isinstance(agent_envelope.get("code_localize_agent"), dict)
                else 0
            ),
        },
    )
    row = {
        "instance_id": "owner__repo-1",
        "repo": "owner/repo",
        "base_commit": "a" * 40,
        "category": "Bug Report",
        "problem_statement": "Find Target.run.",
        "edit_functions": ["src/target.py:Target.run"],
    }

    result = eval_locbench_batch.evaluate_instance(
        row,
        tmp_path,
        json_mode=True,
        score_depth=10,
    )
    summary = eval_locbench_batch.BatchSummary(n_total=1)

    assert result.agent_ran is False
    assert result.failure_class == "invalid_experiment"
    assert result.failure_code == "agent_envelope_invalid"
    assert result.cost_estimate_usd == 0.0
    assert summary.record(result) is False
    assert summary.aborted_reason == (
        "invalid_experiment:agent_envelope_invalid:owner__repo-1"
    )


def test_graph_zero_exit_non_json_response_aborts_as_invalid_experiment(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        eval_locbench_batch.subprocess,
        "run",
        lambda *args, **kwargs: subprocess.CompletedProcess(
            args=args,
            returncode=0,
            stdout=b"not-json",
            stderr=b"",
        ),
    )
    parsed = eval_locbench_batch.run_agent(
        tmp_path / "index.db",
        "find the target",
        json_mode=True,
    )

    assert parsed["failure_class"] == "invalid_experiment"
    assert parsed["failure_code"] == "agent_envelope_invalid"

    index_db = tmp_path / "index.db"
    index_db.write_text("", encoding="utf-8")
    monkeypatch.setattr(
        eval_locbench_batch,
        "clone_repo",
        lambda *args, **kwargs: True,
    )
    monkeypatch.setattr(eval_locbench_batch, "repo_size_mb", lambda path: 1.0)
    monkeypatch.setattr(
        eval_locbench_batch,
        "index_repo",
        lambda path: eval_locbench_batch.GraphIndexOutcome(
            True,
            index_identity={
                "source_revision": "a" * 40,
                "dirty_fingerprint": "clean",
            },
            embedding_count=2,
            embedding_model="voyage-code-3",
        ),
    )
    monkeypatch.setattr(eval_locbench_batch, "db_path_for", lambda path: index_db)
    monkeypatch.setattr(
        eval_locbench_batch,
        "run_agent",
        lambda *args, **kwargs: parsed,
    )
    row = {
        "instance_id": "owner__repo-1",
        "repo": "owner/repo",
        "base_commit": "a" * 40,
        "category": "Bug Report",
        "problem_statement": "Find Target.run.",
        "edit_functions": ["src/target.py:Target.run"],
    }

    result = eval_locbench_batch.evaluate_instance(
        row,
        tmp_path,
        json_mode=True,
        score_depth=10,
    )

    assert result.agent_ran is False
    assert result.failure_class == "invalid_experiment"
    assert result.failure_code == "agent_envelope_invalid"


def test_graph_batch_types_unhandled_case_exception_as_infrastructure() -> None:
    row = {
        "instance_id": "owner__repo-1",
        "repo": "owner/repo",
        "category": "Bug Report",
        "edit_functions": ["src/target.py:Target.run"],
    }

    result = eval_locbench_batch.build_exception_result(
        row,
        RuntimeError("unexpected"),
    )

    assert result.failure_class == "infrastructure"
    assert result.failure_code == "unhandled_case_exception"
    assert "unexpected" in result.note


def test_graph_checkpoint_write_is_atomic_and_fsynced(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    checkpoint = tmp_path / "raw.json"
    checkpoint.write_text('{"old": true}\n', encoding="utf-8")
    monkeypatch.setattr(
        eval_locbench_batch.os,
        "replace",
        lambda *args, **kwargs: (_ for _ in ()).throw(OSError("replace failed")),
    )

    with pytest.raises(OSError, match="replace failed"):
        eval_locbench_batch.write_graph_checkpoint(
            checkpoint,
            {"new": True},
        )

    assert json.loads(checkpoint.read_text(encoding="utf-8")) == {"old": True}
    assert list(tmp_path.glob(".raw.json.*.tmp")) == []

    monkeypatch.undo()
    fsync_calls: list[int] = []
    monkeypatch.setattr(
        eval_locbench_batch.os,
        "fsync",
        lambda descriptor: fsync_calls.append(descriptor),
    )
    eval_locbench_batch.write_graph_checkpoint(checkpoint, {"new": True})

    assert json.loads(checkpoint.read_text(encoding="utf-8")) == {"new": True}
    assert len(fsync_calls) >= 2


def test_graph_batch_aborts_after_invalid_experiment_result() -> None:
    summary = eval_locbench_batch.BatchSummary(n_total=2)
    result = eval_locbench_batch.InstanceResult(
        instance_id="owner__repo-1",
        repo="owner/repo",
        category="Bug Report",
        ground_truth=[],
        failure_class="invalid_experiment",
        failure_code="index_identity_invalid",
        note="index identity is invalid",
    )

    should_continue = summary.record(result)

    assert should_continue is False
    assert summary.instances == [result]
    assert summary.aborted_reason == (
        "invalid_experiment:index_identity_invalid:owner__repo-1"
    )


def test_retrieval_identity_mismatch_raises_typed_invalid_experiment() -> None:
    row = {
        "instance_id": "owner__repo-1",
        "repo": "owner/repo",
        "base_commit": "a" * 40,
        "category": "Bug Report",
        "problem_statement": "Find Target.run.",
        "edit_functions": ["src/target.py:Target.run"],
    }
    provenance = {
        "score_depth": 10,
        "embedding_model": "voyage-4-large",
    }

    with pytest.raises(Exception) as captured:
        build_retrieval_case(
            row,
            [],
            chunks_added=0,
            duration_s=0.1,
            latency_s={"total": 0.1},
            provenance=provenance,
            attempts=[],
            index_identity={},
            embedding_identity={},
            effective_search={},
        )

    assert captured.value.__class__.__name__ == "InvalidExperimentError"
    assert captured.value.failure_class == "invalid_experiment"
    assert captured.value.failure_code == "index_identity_invalid"


def test_retrieval_exception_classification_preserves_invalid_experiment_type() -> None:
    error = retrieval_module.InvalidExperimentError(
        "query embedding generation mismatch",
        "query_embedding_identity_mismatch",
    )

    failure_class, failure_code = retrieval_module.classify_failure(error)

    assert failure_class == "invalid_experiment"
    assert failure_code == "query_embedding_identity_mismatch"


def test_retrieval_exception_classification_types_operational_failure() -> None:
    failure_class, failure_code = retrieval_module.classify_failure(
        RuntimeError("provider unavailable"),
    )

    assert failure_class == "infrastructure"
    assert failure_code == "retrieval_error"


@pytest.mark.parametrize(
    ("failure_class", "failure_code", "note"),
    [
        ("infrastructure", "clone_failed", "clone failed"),
        ("measured_outcome", "repo_too_large", "repo too large"),
        ("infrastructure", "checkout_failed", "checkout failed"),
    ],
)
def test_retrieval_direct_failure_metadata_is_typed(
    failure_class: str,
    failure_code: str,
    note: str,
) -> None:
    case: dict = {}

    retrieval_module.apply_direct_failure(
        case,
        failure_class=failure_class,
        failure_code=failure_code,
        note=note,
    )

    assert case["failure_class"] == failure_class
    assert case["failure_code"] == failure_code
    assert case["note"] == note


def test_retrieval_resume_binds_exact_decimal_budget_contract(
    tmp_path: Path,
) -> None:
    budget_contract = retrieval_module.build_retrieval_budget_contract(
        shard_allocation_usd="0.010000",
        arm_ceiling_usd="12.000000",
        total_ceiling_usd="20.000000",
        provider_operation_bound_policy=(
            "provider-enforced-per-operation-usd-required-v1"
        ),
    )
    assert budget_contract == {
        "shard_allocation_usd": "0.010000",
        "arm_ceiling_usd": "12.000000",
        "total_ceiling_usd": "20.000000",
        "provider_operation_bound_policy": (
            "provider-enforced-per-operation-usd-required-v1"
        ),
    }
    provenance = {
        "score_depth": 10,
        "budget_contract": budget_contract,
    }
    expected_ids = ["owner__repo-1"]
    checkpoint = tmp_path / "retrieval.json"
    checkpoint.write_text(
        json.dumps(
            build_run_artifact(
                provenance,
                expected_ids,
                [{"instance_id": expected_ids[0]}],
            )
        ),
        encoding="utf-8",
    )
    changed = dict(provenance)
    changed["budget_contract"] = {
        **budget_contract,
        "shard_allocation_usd": "0.010001",
    }

    with pytest.raises(ValueError, match="provenance"):
        load_resume_cases(checkpoint, changed, expected_ids)


def test_retrieval_provenance_persists_budget_contract(tmp_path: Path) -> None:
    pin = {
        "score_depth": 10,
        "dataset": {
            "parquet_sha256": "1" * 64,
            "revision": "2" * 40,
        },
        "component_pins": {
            "code_search": {
                "tag": "v0.2.1",
                "artifact_sha256": (
                    "567d4caabdd3b5446bcaa789afc7104fb8cce142ff69d7fc8f1294398532e7e9"
                ),
            }
        },
    }
    pin_path = tmp_path / "pin.json"
    pin_path.write_text(json.dumps(pin), encoding="utf-8")
    budget_contract = retrieval_module.build_retrieval_budget_contract(
        shard_allocation_usd="0.500000",
        arm_ceiling_usd="12.000000",
        total_ceiling_usd="20.000000",
        provider_operation_bound_policy=(
            "provider-enforced-per-operation-usd-required-v1"
        ),
    )

    provenance = retrieval_module.build_provenance(
        pin,
        pin_path,
        "a" * 40,
        10,
        "voyage-4-large",
        "sonnet",
        budget_contract=budget_contract,
    )

    assert provenance["budget_contract"] == budget_contract


def test_retrieval_shard_merge_allows_distinct_exact_allocations(
    tmp_path: Path,
) -> None:
    pin_sha256 = "1" * 64
    scorer_sha256 = "2" * 64
    repositories = ("owner/alpha", "owner/beta")
    instance_ids = ("owner__alpha-1", "owner__beta-1")
    pin = {
        "pinned_instance_ids": list(instance_ids),
        "cases": [
            {"instance_id": instance_id, "repo": repository}
            for instance_id, repository in zip(
                instance_ids,
                repositories,
                strict=True,
            )
        ],
    }
    paths: list[Path] = []
    for repository, instance_id, allocation in zip(
        repositories,
        instance_ids,
        ("0.400000", "0.600000"),
        strict=True,
    ):
        path = tmp_path / f"{repository.replace('/', '__')}.json"
        path.write_text(
            json.dumps(
                {
                    "arm": "retrieval-only",
                    "status": "complete",
                    "expected_instance_ids": [instance_id],
                    "total_cost_usd": 0.0,
                    "provenance": {
                        "pin_sha256": pin_sha256,
                        "scorer_sha256": scorer_sha256,
                        "repository": repository,
                        "score_depth": 10,
                        "budget_contract": {
                            "shard_allocation_usd": allocation,
                            "arm_ceiling_usd": "1.000000",
                            "total_ceiling_usd": "2.000000",
                            "provider_operation_bound_policy": (
                                "provider-enforced-per-operation-usd-required-v1"
                            ),
                        },
                    },
                    "cases": [
                        {
                            "instance_id": instance_id,
                            "repo": repository,
                        }
                    ],
                }
            ),
            encoding="utf-8",
        )
        paths.append(path)

    merged = pilot_compare.merge_shards(
        paths,
        arm="retrieval-only",
        pin=pin,
        pin_sha256=pin_sha256,
        scorer_sha256=scorer_sha256,
    )

    assert merged["provenance"]["budget_contract"] == {
        "arm_ceiling_usd": "1.000000",
        "total_ceiling_usd": "2.000000",
        "provider_operation_bound_policy": (
            "provider-enforced-per-operation-usd-required-v1"
        ),
        "shard_allocations_usd": {
            "owner/alpha": "0.400000",
            "owner/beta": "0.600000",
        },
        "allocated_sum_usd": "1.000000",
    }


def test_retrieval_shard_merge_binds_allocation_to_repository_coverage(
    tmp_path: Path,
) -> None:
    pin_sha256 = "1" * 64
    scorer_sha256 = "2" * 64
    repositories = ("owner/alpha", "owner/beta")
    instance_ids = ("owner__alpha-1", "owner__beta-1")
    pin = {
        "pinned_instance_ids": list(instance_ids),
        "cases": [
            {"instance_id": instance_id, "repo": repository}
            for instance_id, repository in zip(
                instance_ids,
                repositories,
                strict=True,
            )
        ],
    }
    paths: list[Path] = []
    for repository, wrong_instance_id, allocation in zip(
        repositories,
        reversed(instance_ids),
        ("0.400000", "0.600000"),
        strict=True,
    ):
        path = tmp_path / f"{repository.replace('/', '__')}.json"
        path.write_text(
            json.dumps(
                {
                    "arm": "retrieval-only",
                    "status": "complete",
                    "expected_instance_ids": [wrong_instance_id],
                    "total_cost_usd": 0.0,
                    "provenance": {
                        "pin_sha256": pin_sha256,
                        "scorer_sha256": scorer_sha256,
                        "repository": repository,
                        "score_depth": 10,
                        "budget_contract": {
                            "shard_allocation_usd": allocation,
                            "arm_ceiling_usd": "1.000000",
                            "total_ceiling_usd": "2.000000",
                            "provider_operation_bound_policy": (
                                "provider-enforced-per-operation-usd-required-v1"
                            ),
                        },
                    },
                    "cases": [
                        {
                            "instance_id": wrong_instance_id,
                            "repo": (
                                "owner/beta"
                                if repository == "owner/alpha"
                                else "owner/alpha"
                            ),
                        }
                    ],
                }
            ),
            encoding="utf-8",
        )
        paths.append(path)

    with pytest.raises(ValueError, match="repository case coverage"):
        pilot_compare.merge_shards(
            paths,
            arm="retrieval-only",
            pin=pin,
            pin_sha256=pin_sha256,
            scorer_sha256=scorer_sha256,
        )


def test_retrieval_rejects_unverifiable_rank_window() -> None:
    evidence = {
        "requested_k": 10,
        "returned_count": 10,
        "total_candidates": None,
        "effective_k": 10,
        "truncated": None,
    }

    with pytest.raises(retrieval_module.InvalidExperimentError) as captured:
        retrieval_module.validate_rank_evidence(
            evidence,
            requested_k=10,
            returned_count=10,
        )

    assert captured.value.failure_code == "rank_window_unverifiable"


def test_code_search_helper_discloses_exact_rank_window() -> None:
    evidence = cs_search_helper.build_rank_evidence(
        requested_k=10,
        returned_count=10,
        total_candidates=25,
    )

    assert evidence == {
        "requested_k": 10,
        "returned_count": 10,
        "total_candidates": 25,
        "effective_k": 10,
        "truncated": True,
    }


def test_code_search_helper_types_short_rank_window_as_invalid_experiment() -> None:
    with pytest.raises(cs_search_helper.QueryHelperContractError) as captured:
        cs_search_helper.build_rank_evidence(
            requested_k=10,
            returned_count=9,
            total_candidates=25,
        )

    assert captured.value.failure_class == "invalid_experiment"
    assert captured.value.failure_code == "rank_window_unverifiable"


@pytest.mark.parametrize(
    ("failure_kind", "expected_class", "expected_code", "expected_error"),
    [
        (
            "query_identity_mismatch",
            "invalid_experiment",
            "query_embedding_identity_mismatch",
            "query embedder disagrees with the verified index: "
            "{'model': ('wrong-query-model', 'voyage-4-large')}",
        ),
        (
            "unverified_generation",
            "invalid_experiment",
            "query_embedding_identity_invalid",
            "search refused an unverified index generation: stale: "
            "digest mismatch",
        ),
        (
            "incomplete_identity",
            "invalid_experiment",
            "query_embedding_identity_invalid",
            "verified index identity is incomplete",
        ),
        (
            "rank_window_mismatch",
            "invalid_experiment",
            "rank_window_unverifiable",
            "search response does not account for the exact candidate window",
        ),
        (
            "candidate_window_missing",
            "invalid_experiment",
            "rank_window_unverifiable",
            "search did not expose the hybrid candidate window",
        ),
        (
            "candidate_results_not_list",
            "invalid_experiment",
            "candidate_schema_invalid",
            "search results are not a list",
        ),
        (
            "candidate_record_invalid",
            "invalid_experiment",
            "candidate_schema_invalid",
            "search result 1 relative_path is invalid",
        ),
        (
            "reranker_missing",
            "invalid_experiment",
            "reranker_identity_invalid",
            "search did not expose effective reranker metadata",
        ),
        (
            "reranker_malformed",
            "invalid_experiment",
            "reranker_identity_invalid",
            "effective reranker metadata is malformed",
        ),
        (
            "reranker_applied_with_failure_reason",
            "invalid_experiment",
            "reranker_identity_invalid",
            "effective reranker applied state contradicts reason",
        ),
        (
            "reranker_not_applied_with_ok_reason",
            "invalid_experiment",
            "reranker_identity_invalid",
            "effective reranker applied state contradicts reason",
        ),
        (
            "provider_error",
            "infrastructure",
            "search_helper_failed",
            "provider unavailable",
        ),
    ],
)
def test_code_search_query_helper_emits_typed_failure_envelope(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    failure_kind: str,
    expected_class: str,
    expected_code: str,
    expected_error: str,
) -> None:
    def install_module(name: str, **attributes: object) -> types.ModuleType:
        module = types.ModuleType(name)
        for key, value in attributes.items():
            setattr(module, key, value)
        monkeypatch.setitem(sys.modules, name, module)
        return module

    embeddings_package = install_module("embeddings")
    embeddings_package.__path__ = []
    search_package = install_module("search")
    search_package.__path__ = []
    configuration = SimpleNamespace(
        provider="voyage",
        model_name="voyage-4-large",
        output_dimension=1024,
        content_mode="code",
    )

    class FakeEmbedder:
        def __init__(self, **kwargs):
            if failure_kind == "provider_error":
                raise RuntimeError("provider unavailable")
            self.configuration = configuration

    class FakeIndexManager:
        def __init__(self, storage_dir: str):
            self.storage_dir = storage_dir

    class FakeSearchResult:
        def __init__(self, rank: int):
            self.relative_path = f"src/result_{rank}.py"
            self.name = f"result_{rank}"
            self.parent_name = "Result"
            self.chunk_type = "function"
            self.similarity_score = 1.0 / rank

    class FakeSearcher:
        def __init__(self, *args, **kwargs):
            if failure_kind == "reranker_missing":
                self.last_reranker_metadata = None
            elif failure_kind == "reranker_malformed":
                self.last_reranker_metadata = {
                    "applied": True,
                    "reason": "ok",
                    "latency_ms": float("nan"),
                }
            elif failure_kind == "reranker_applied_with_failure_reason":
                self.last_reranker_metadata = {
                    "applied": True,
                    "reason": "rate_limit",
                    "latency_ms": 1,
                }
            elif failure_kind == "reranker_not_applied_with_ok_reason":
                self.last_reranker_metadata = {
                    "applied": False,
                    "reason": "ok",
                    "latency_ms": 1,
                }
            else:
                self.last_reranker_metadata = {
                    "applied": True,
                    "reason": "ok",
                    "latency_ms": 1,
                }

        def search(self, *args, **kwargs):
            if failure_kind != "candidate_window_missing":
                searcher_module.reciprocal_rank_fusion([], [])
            result_count = 9 if failure_kind == "rank_window_mismatch" else 10
            results = [FakeSearchResult(rank) for rank in range(1, result_count + 1)]
            if failure_kind == "candidate_results_not_list":
                return tuple(results)
            if failure_kind == "candidate_record_invalid":
                results[0].relative_path = ""
            return results

    install_module("embeddings.embedder", CodeEmbedder=FakeEmbedder)
    install_module(
        "search.epoch_manifest",
        read_with_fallback=lambda storage_dir: SimpleNamespace(
            freshness=(
                "stale" if failure_kind == "unverified_generation" else "fresh"
            ),
            detail=(
                "digest mismatch"
                if failure_kind == "unverified_generation"
                else ""
            ),
            manifest={
                "provider": "voyage",
                "model": (
                    "wrong-query-model"
                    if failure_kind == "query_identity_mismatch"
                    else "voyage-4-large"
                ),
                "vector_dim": 1024,
                "pipeline_version": (
                    ""
                    if failure_kind == "incomplete_identity"
                    else "pipeline-v1"
                ),
                "epoch_id": "epoch-1",
            },
        ),
    )
    install_module("search.indexer", CodeIndexManager=FakeIndexManager)
    searcher_module = install_module(
        "search.searcher",
        IntelligentSearcher=FakeSearcher,
        reciprocal_rank_fusion=lambda *args, **kwargs: [
            (f"candidate-{rank}", 1.0 / rank)
            for rank in range(1, 26)
        ],
    )
    monkeypatch.setattr(
        sys,
        "argv",
        [
            "cs_search_once.py",
            "--code-search-root",
            str(tmp_path),
            "--storage-dir",
            str(tmp_path / "storage"),
            "--query",
            "find the target",
            "--k",
            "10",
        ],
    )

    exit_code = cs_search_helper.main()
    payload = json.loads(capsys.readouterr().out)

    assert exit_code != 0
    assert payload == {
        "success": False,
        "error": expected_error,
        "failure_class": expected_class,
        "failure_code": expected_code,
    }


def _query_helper_success_payload(
    *,
    returned_count: int = 10,
    total_candidates: int = 25,
) -> dict:
    effective_k = min(10, total_candidates)
    return {
        "results": [
            {
                "relative_path": f"src/result_{rank}.py",
                "name": f"result_{rank}",
                "parent_name": "Result",
                "chunk_type": "function",
                "score": 1.0 / rank,
            }
            for rank in range(1, returned_count + 1)
        ],
        "rank_evidence": {
            "requested_k": 10,
            "returned_count": returned_count,
            "total_candidates": total_candidates,
            "effective_k": effective_k,
            "truncated": total_candidates > effective_k,
        },
        "effective_search": {
            "embedding": {
                "provider": "voyage",
                "model": "voyage-4-large",
                "vector_dim": 1024,
                "content_mode": "code",
                "pipeline_version": "pipeline-v1",
                "manifest_freshness": "fresh",
                "index_epoch_id": "epoch-1",
            },
            "reranker": {
                "requested_mode": "sonnet",
                "applied": True,
                "reason": "ok",
                "latency_ms": 1,
                "model": "claude-sonnet-4-6",
            },
        },
    }


def test_retrieval_parent_rejects_returned_nine_of_twenty_five_at_k_ten(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    payload = _query_helper_success_payload(
        returned_count=9,
        total_candidates=25,
    )
    monkeypatch.setattr(
        retrieval_module.subprocess,
        "run",
        lambda *args, **kwargs: subprocess.CompletedProcess(
            args=args,
            returncode=0,
            stdout=json.dumps(payload).encode(),
            stderr=b"",
        ),
    )

    with pytest.raises(retrieval_module.InvalidExperimentError) as captured:
        search_once(
            sys.executable,
            str(tmp_path),
            tmp_path / "storage",
            "find the target",
            k=10,
        )

    assert captured.value.failure_code == "rank_window_unverifiable"


@pytest.mark.parametrize(
    ("mutation", "expected_code"),
    [
        ("embedding_model", "query_embedding_identity_mismatch"),
        ("reranker_model", "reranker_identity_mismatch"),
        ("reranker_applied_with_failure_reason", "reranker_identity_invalid"),
        ("reranker_not_applied_with_ok_reason", "reranker_identity_invalid"),
    ],
)
def test_retrieval_parent_types_effective_identity_mismatches(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    mutation: str,
    expected_code: str,
) -> None:
    payload = _query_helper_success_payload()
    if mutation == "embedding_model":
        payload["effective_search"]["embedding"]["model"] = "wrong-model"
    elif mutation == "reranker_model":
        payload["effective_search"]["reranker"]["model"] = (
            "claude-haiku-4-5"
        )
    elif mutation == "reranker_applied_with_failure_reason":
        payload["effective_search"]["reranker"]["reason"] = "rate_limit"
    else:
        payload["effective_search"]["reranker"].update(
            {
                "applied": False,
                "reason": "ok",
                "model": None,
            }
        )
    monkeypatch.setattr(
        retrieval_module.subprocess,
        "run",
        lambda *args, **kwargs: subprocess.CompletedProcess(
            args=args,
            returncode=0,
            stdout=json.dumps(payload).encode(),
            stderr=b"",
        ),
    )

    with pytest.raises(retrieval_module.InvalidExperimentError) as captured:
        search_once(
            sys.executable,
            str(tmp_path),
            tmp_path / "storage",
            "find the target",
            k=10,
        )

    assert captured.value.failure_class == "invalid_experiment"
    assert captured.value.failure_code == expected_code


@pytest.mark.parametrize(
    ("failure_kind", "expected_class", "expected_code", "expected_error"),
    [
        (
            "embedding_identity_mismatch",
            "invalid_experiment",
            "embedding_identity_mismatch",
            "verified manifest disagrees with the effective embedder: "
            "{'model': ('wrong-index-model', 'voyage-4-large')}",
        ),
        (
            "source_identity_changed",
            "invalid_experiment",
            "index_identity_mismatch",
            "source identity changed during indexing",
        ),
        (
            "unverified_generation",
            "invalid_experiment",
            "embedding_identity_invalid",
            "index manifest is not a fresh verified generation: stale: "
            "digest mismatch",
        ),
        (
            "incomplete_identity",
            "invalid_experiment",
            "embedding_identity_invalid",
            "verified index manifest has no epoch_id",
        ),
        (
            "provider_error",
            "infrastructure",
            "index_helper_failed",
            "provider unavailable",
        ),
    ],
)
def test_code_search_index_helper_emits_typed_failure_envelope(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    failure_kind: str,
    expected_class: str,
    expected_code: str,
    expected_error: str,
) -> None:
    def install_module(name: str, **attributes: object) -> types.ModuleType:
        module = types.ModuleType(name)
        for key, value in attributes.items():
            setattr(module, key, value)
        monkeypatch.setitem(sys.modules, name, module)
        return module

    for package_name in ("chunking", "embeddings", "merkle", "mcp_server", "search"):
        package = install_module(package_name)
        package.__path__ = []

    configuration = SimpleNamespace(
        provider="voyage",
        model_name="voyage-4-large",
        output_dimension=1024,
        content_mode="code",
    )
    repository_id = hashlib.sha256(b"repository").hexdigest()
    source_revision = "a" * 40
    identity_payload = {
        "schema_version": 1,
        "repository_id": repository_id,
        "checkout_id": hashlib.sha256(b"checkout").hexdigest(),
        "source_revision": source_revision,
        "dirty_fingerprint": "clean",
        "index_generation": hashlib.sha256(
            f"{repository_id}\0{source_revision}\0clean".encode()
        ).hexdigest(),
        "captured_at": "2026-07-27T12:00:00Z",
    }
    start_identity = SimpleNamespace(
        **identity_payload,
        to_dict=lambda: dict(identity_payload),
    )
    changed_identity_payload = {
        **identity_payload,
        "checkout_id": hashlib.sha256(b"changed checkout").hexdigest(),
    }
    end_identity = SimpleNamespace(
        **changed_identity_payload,
        to_dict=lambda: dict(changed_identity_payload),
    )
    identities = iter(
        (
            start_identity,
            end_identity
            if failure_kind == "source_identity_changed"
            else start_identity,
        )
    )

    class FakeEmbedder:
        def __init__(self, **kwargs):
            if failure_kind == "provider_error":
                raise RuntimeError("provider unavailable")
            self.configuration = configuration

    class FakeIndexManager:
        def __init__(self, storage_dir: str):
            self.storage_dir = storage_dir

        def bind_embedding_configuration(self, *args, **kwargs) -> None:
            return None

    class FakeIncrementalIndexer:
        def __init__(self, **kwargs):
            return None

        def incremental_index(self, *args, **kwargs):
            return SimpleNamespace(
                success=True,
                error="",
                to_dict=lambda: {"success": True, "chunks_added": 1},
            )

    install_module(
        "chunking.multi_language_chunker",
        MultiLanguageChunker=lambda repo: object(),
    )
    install_module("embeddings.embedder", CodeEmbedder=FakeEmbedder)
    install_module(
        "merkle.snapshot_manager",
        SnapshotManager=lambda **kwargs: object(),
    )
    install_module(
        "mcp_server.code_search_server",
        get_pipeline_version=lambda config: "pipeline-v1",
    )
    install_module(
        "search.epoch_manifest",
        read_with_fallback=lambda storage_dir: SimpleNamespace(
            freshness=(
                "stale" if failure_kind == "unverified_generation" else "fresh"
            ),
            detail=(
                "digest mismatch"
                if failure_kind == "unverified_generation"
                else ""
            ),
            manifest={
                "provider": "voyage",
                "model": (
                    "wrong-index-model"
                    if failure_kind == "embedding_identity_mismatch"
                    else "voyage-4-large"
                ),
                "vector_dim": 1024,
                "pipeline_version": "pipeline-v1",
                "epoch_id": (
                    None
                    if failure_kind == "incomplete_identity"
                    else "epoch-1"
                ),
            },
        ),
    )
    install_module(
        "search.index_identity",
        capture_index_identity=lambda repo: next(identities),
    )
    install_module(
        "search.incremental_indexer",
        IncrementalIndexer=FakeIncrementalIndexer,
    )
    install_module("search.indexer", CodeIndexManager=FakeIndexManager)
    monkeypatch.setattr(
        sys,
        "argv",
        [
            "cs_index_once.py",
            "--code-search-root",
            str(tmp_path),
            "--repo",
            str(tmp_path / "repo"),
            "--storage-dir",
            str(tmp_path / "storage"),
        ],
    )

    exit_code = cs_index_helper.main()
    payload = json.loads(capsys.readouterr().out)

    assert exit_code != 0
    assert payload == {
        "success": False,
        "error": expected_error,
        "failure_class": expected_class,
        "failure_code": expected_code,
    }


def test_retrieval_failure_case_captures_typed_exception_metadata() -> None:
    case = {"note": ""}
    error = retrieval_module.InvalidExperimentError(
        "index source revision mismatch",
        "index_identity_mismatch",
    )

    retrieval_module.apply_failure_to_case(case, error)

    assert case["failure_class"] == "invalid_experiment"
    assert case["failure_code"] == "index_identity_mismatch"
    assert case["note"] == "error: index source revision mismatch"


def test_retrieval_main_aborts_before_second_query_after_invalid_identity(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    pin_path = tmp_path / "pin.json"
    parquet = tmp_path / "locbench.parquet"
    output = tmp_path / "retrieval.json"
    pin_path.write_text("{}", encoding="utf-8")
    parquet.write_bytes(b"pinned")
    rows = [
        {
            "instance_id": f"owner__repo-{number}",
            "repo": "owner/repo",
            "base_commit": "a" * 40,
            "category": "Bug Report",
            "problem_statement": "Find Target.run.",
            "edit_functions": ["src/target.py:Target.run"],
        }
        for number in (1, 2)
    ]
    provenance = {
        "dataset_sha256": hashlib.sha256(b"pinned").hexdigest(),
        "embedding_model": "voyage-4-large",
        "score_depth": 10,
        "reranker": "per-case-effective",
    }
    monkeypatch.setenv("VOYAGE_API_KEY", "test-only")
    monkeypatch.setenv("ANTHROPIC_API_KEY", "test-only")
    monkeypatch.setattr(
        retrieval_module,
        "build_provenance",
        lambda *args, **kwargs: dict(provenance),
    )
    monkeypatch.setattr(
        retrieval_module.pd,
        "read_parquet",
        lambda path: object(),
    )
    monkeypatch.setattr(
        retrieval_module,
        "prepare_dataset",
        lambda *args, **kwargs: (
            rows,
            rows,
            [row["instance_id"] for row in rows],
            "q" * 64,
        ),
    )
    monkeypatch.setattr(
        retrieval_module,
        "clone_repo",
        lambda *args, **kwargs: True,
    )
    monkeypatch.setattr(retrieval_module, "repo_size_mb", lambda path: 1.0)
    repository_id = "1" * 64
    index_identity = {
        "schema_version": 1,
        "repository_id": repository_id,
        "checkout_id": "2" * 64,
        "source_revision": "a" * 40,
        "dirty_fingerprint": "clean",
        "index_generation": hashlib.sha256(
            f"{repository_id}\0{'a' * 40}\0clean".encode()
        ).hexdigest(),
        "captured_at": "2026-07-27T12:00:00Z",
    }
    embedding_identity = {
        "provider": "voyage",
        "model": "voyage-4-large",
        "vector_dim": 1024,
        "content_mode": "code",
        "pipeline_version": "pipeline-v1",
        "manifest_freshness": "fresh",
        "index_epoch_id": "epoch-1",
    }
    monkeypatch.setattr(
        retrieval_module,
        "index_with_code_search",
        lambda *args, **kwargs: retrieval_module.RetrievalIndexOutcome(
            True,
            chunks_added=1,
            index_identity=index_identity,
            embedding_identity=embedding_identity,
        ),
    )
    search_calls = 0

    def mismatched_search(*args, **kwargs):
        nonlocal search_calls
        search_calls += 1
        assert kwargs["k"] == 10
        mismatched = dict(embedding_identity)
        mismatched["index_epoch_id"] = "wrong-epoch"
        return retrieval_module.RetrievalSearchOutcome(
            results=[],
            effective_search={
                "embedding": mismatched,
                "reranker": {
                    "requested_mode": "sonnet",
                    "applied": True,
                    "reason": "ok",
                    "latency_ms": 1,
                    "model": "claude-sonnet-4-6",
                },
            },
            rank_evidence={
                "requested_k": 10,
                "returned_count": 0,
                "total_candidates": 0,
                "effective_k": 0,
                "truncated": False,
            },
        )

    monkeypatch.setattr(retrieval_module, "search_once", mismatched_search)
    monkeypatch.setattr(
        sys,
        "argv",
        [
            "armC_retrieval.py",
            "--pin",
            str(pin_path),
            "--parquet",
            str(parquet),
            "--workdir",
            str(tmp_path / "work"),
            "--out",
            str(output),
            "--graph-sha",
            "a" * 40,
            "--repository",
            "owner/repo",
            "--voyage-ceiling-usd",
            "1.000000",
            "--arm-ceiling-usd",
            "12.000000",
            "--total-ceiling-usd",
            "20.000000",
            "--provider-operation-bound-policy",
            "provider-enforced-per-operation-usd-required-v1",
        ],
    )

    exit_code = retrieval_module.main()
    artifact = json.loads(output.read_text(encoding="utf-8"))

    assert exit_code == 2
    assert search_calls == 1
    assert artifact["cases"][0]["failure_class"] == "invalid_experiment"
    assert artifact["cases"][0]["failure_code"] == (
        "query_embedding_identity_mismatch"
    )
    assert artifact["aborted_reason"].startswith("invalid_experiment:")


@pytest.mark.parametrize(
    ("mutation", "expected_code"),
    [
        ("source", "index_identity_mismatch"),
        ("embedding", "embedding_identity_invalid"),
        ("query_embedding", "query_embedding_identity_mismatch"),
    ],
)
def test_retrieval_types_every_deterministic_identity_validation(
    mutation: str,
    expected_code: str,
) -> None:
    row = {
        "instance_id": "owner__repo-1",
        "repo": "owner/repo",
        "base_commit": "a" * 40,
        "category": "Bug Report",
        "problem_statement": "Find Target.run.",
        "edit_functions": ["src/target.py:Target.run"],
    }
    repository_id = "1" * 64
    index_identity = {
        "schema_version": 1,
        "repository_id": repository_id,
        "checkout_id": "2" * 64,
        "source_revision": row["base_commit"],
        "dirty_fingerprint": "clean",
        "index_generation": hashlib.sha256(
            f"{repository_id}\0{row['base_commit']}\0clean".encode()
        ).hexdigest(),
        "captured_at": "2026-07-27T12:00:00Z",
    }
    embedding_identity = {
        "provider": "voyage",
        "model": "voyage-4-large",
        "vector_dim": 1024,
        "content_mode": "code",
        "pipeline_version": "pipeline-v1",
        "manifest_freshness": "fresh",
        "index_epoch_id": "epoch-1",
    }
    query_embedding = dict(embedding_identity)
    if mutation == "source":
        index_identity["source_revision"] = "b" * 40
        index_identity["index_generation"] = hashlib.sha256(
            f"{repository_id}\0{'b' * 40}\0clean".encode()
        ).hexdigest()
    elif mutation == "embedding":
        embedding_identity["model"] = "wrong-model"
    else:
        query_embedding["index_epoch_id"] = "wrong-epoch"

    with pytest.raises(Exception) as captured:
        build_retrieval_case(
            row,
            [],
            chunks_added=0,
            duration_s=0.1,
            latency_s={"total": 0.1},
            provenance={
                "score_depth": 10,
                "embedding_model": "voyage-4-large",
            },
            attempts=[],
            index_identity=index_identity,
            embedding_identity=embedding_identity,
            effective_search={"embedding": query_embedding},
        )

    assert captured.value.__class__.__name__ == "InvalidExperimentError"
    assert captured.value.failure_class == "invalid_experiment"
    assert captured.value.failure_code == expected_code


def test_retrieval_helpers_reject_missing_identity_and_expose_reranker_fallback(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    def malformed_index_run(*args, **kwargs):
        return subprocess.CompletedProcess(
            args=args,
            returncode=0,
            stdout=b'{"success":true,"chunks_added":1}',
            stderr=b"",
        )

    monkeypatch.setattr(eval_locbench_batch.subprocess, "run", malformed_index_run)
    index_outcome = index_with_code_search(
        sys.executable,
        str(tmp_path),
        tmp_path / "repo",
        tmp_path / "storage",
        force_full=True,
    )
    assert index_outcome.success is False
    assert "identity" in index_outcome.error
    assert index_outcome.failure_class == "invalid_experiment"
    assert index_outcome.failure_code == "index_identity_invalid"

    def fallback_search_run(*args, **kwargs):
        return subprocess.CompletedProcess(
            args=args,
            returncode=0,
            stdout=json.dumps(
                {
                    "results": [],
                    "effective_search": {
                        "embedding": {
                            "provider": "voyage",
                            "model": "voyage-4-large",
                            "vector_dim": 1024,
                            "content_mode": "code",
                            "pipeline_version": "pipeline-v1",
                            "manifest_freshness": "fresh",
                            "index_epoch_id": "epoch-1",
                        },
                        "reranker": {
                            "requested_mode": "sonnet",
                            "applied": False,
                            "reason": "rate_limit",
                            "latency_ms": 100,
                            "model": None,
                        },
                    },
                    "rank_evidence": {
                        "requested_k": 50,
                        "returned_count": 0,
                        "total_candidates": 0,
                        "effective_k": 0,
                        "truncated": False,
                    },
                }
            ).encode(),
            stderr=b"",
        )

    monkeypatch.setattr(eval_locbench_batch.subprocess, "run", fallback_search_run)
    with pytest.raises(RuntimeError, match="rate_limit") as captured:
        search_once(
            sys.executable,
            str(tmp_path),
            tmp_path / "storage",
            "find the target",
        )
    assert captured.value.effective_search["reranker"]["model"] is None


@pytest.mark.parametrize(
    ("payload", "expected_code"),
    [
        (
            {
                "success": True,
                "index_identity_status": "ready",
                "index_identity": {"schema_version": 1},
                "embedding_identity": {},
                "chunks_added": 1,
            },
            "index_identity_invalid",
        ),
        (
            {
                "success": True,
                "index_identity_status": "ready",
                "index_identity": {
                    "schema_version": 1,
                    "repository_id": "1" * 64,
                    "checkout_id": "2" * 64,
                    "source_revision": "a" * 40,
                    "dirty_fingerprint": "clean",
                    "index_generation": hashlib.sha256(
                        f"{'1' * 64}\0{'a' * 40}\0clean".encode()
                    ).hexdigest(),
                    "captured_at": "2026-07-27T12:00:00Z",
                },
                "embedding_identity": {},
                "chunks_added": 1,
            },
            "embedding_identity_invalid",
        ),
    ],
)
def test_retrieval_index_helper_types_all_identity_failures(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    payload: dict,
    expected_code: str,
) -> None:
    monkeypatch.setattr(
        eval_locbench_batch.subprocess,
        "run",
        lambda *args, **kwargs: subprocess.CompletedProcess(
            args=args,
            returncode=0,
            stdout=json.dumps(payload).encode(),
            stderr=b"",
        ),
    )

    outcome = index_with_code_search(
        sys.executable,
        str(tmp_path),
        tmp_path / "repo",
        tmp_path / "storage",
        force_full=True,
    )

    assert outcome.failure_class == "invalid_experiment"
    assert outcome.failure_code == expected_code


def test_retrieval_index_gate_raises_typed_invalid_experiment() -> None:
    outcome = retrieval_module.RetrievalIndexOutcome(
        False,
        "embedding epoch is stale",
        failure_class="invalid_experiment",
        failure_code="embedding_identity_invalid",
    )

    with pytest.raises(retrieval_module.InvalidExperimentError) as captured:
        retrieval_module.require_valid_index_outcome(outcome)

    assert captured.value.failure_code == "embedding_identity_invalid"


def test_retrieval_index_gate_rejects_wrong_pinned_checkout_identity() -> None:
    outcome = retrieval_module.RetrievalIndexOutcome(
        True,
        index_identity={
            "source_revision": "b" * 40,
            "dirty_fingerprint": "clean",
        },
    )

    with pytest.raises(retrieval_module.InvalidExperimentError) as captured:
        retrieval_module.require_pinned_index_identity(outcome, "a" * 40)

    assert captured.value.failure_code == "index_identity_mismatch"


@pytest.mark.parametrize(
    ("operation", "failure_class", "failure_code"),
    [
        ("index", "invalid_experiment", "embedding_identity_mismatch"),
        ("index", "infrastructure", "index_provider_failed"),
        ("search", "invalid_experiment", "query_embedding_identity_mismatch"),
        ("search", "infrastructure", "search_provider_failed"),
    ],
)
def test_retrieval_subprocess_boundary_preserves_typed_child_failure(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    operation: str,
    failure_class: str,
    failure_code: str,
) -> None:
    child_payload = {
        "success": False,
        "error": "typed child failure",
        "failure_class": failure_class,
        "failure_code": failure_code,
    }
    monkeypatch.setattr(
        retrieval_module.subprocess,
        "run",
        lambda *args, **kwargs: subprocess.CompletedProcess(
            args=args,
            returncode=2,
            stdout=json.dumps(child_payload).encode(),
            stderr=b"child stderr",
        ),
    )

    if operation == "index":
        outcome = index_with_code_search(
            sys.executable,
            str(tmp_path),
            tmp_path / "repo",
            tmp_path / "storage",
            force_full=True,
        )

        assert outcome.success is False
        assert outcome.error == "typed child failure"
        assert outcome.failure_class == failure_class
        assert outcome.failure_code == failure_code
        return

    if failure_class == "invalid_experiment":
        with pytest.raises(retrieval_module.InvalidExperimentError) as captured:
            search_once(
                sys.executable,
                str(tmp_path),
                tmp_path / "storage",
                "find the target",
            )
        assert str(captured.value) == "typed child failure"
        assert captured.value.failure_code == failure_code
    else:
        with pytest.raises(
            retrieval_module.RetrievalInfrastructureError,
            match="typed child failure",
        ) as captured:
            search_once(
                sys.executable,
                str(tmp_path),
                tmp_path / "storage",
                "find the target",
            )
        assert retrieval_module.classify_failure(captured.value) == (
            failure_class,
            failure_code,
        )


def test_all_benchmark_children_receive_only_their_required_credentials(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    ambient = {
        "ANTHROPIC_API_KEY": "anthropic-secret",
        "VOYAGE_API_KEY": "voyage-secret",
        "CODE_INTEL_COMPONENT_TOKEN": "component-secret",
        "GH_TOKEN": "github-secret",
        "UNRELATED_SECRET": "unrelated-secret",
        "ANTHROPIC_MODEL": "claude-haiku-4-5-20251001",
        "LOCAGENT_ITERATIONS": "2",
        "PATH": "/safe/bin",
    }
    for name, value in ambient.items():
        monkeypatch.setenv(name, value)

    calls: list[dict] = []
    repository_id = hashlib.sha256(b"repository").hexdigest()
    checkout_id = hashlib.sha256(b"checkout").hexdigest()
    source_revision = "a" * 40
    index_identity = {
        "schema_version": 1,
        "repository_id": repository_id,
        "checkout_id": checkout_id,
        "source_revision": source_revision,
        "dirty_fingerprint": "clean",
        "index_generation": hashlib.sha256(
            f"{repository_id}\0{source_revision}\0clean".encode()
        ).hexdigest(),
        "captured_at": "2026-07-27T12:00:00Z",
    }
    embedding_identity = {
        "provider": "voyage",
        "model": "voyage-4-large",
        "vector_dim": 1024,
        "content_mode": "code",
        "pipeline_version": "pipeline-v1",
        "manifest_freshness": "fresh",
        "index_epoch_id": "epoch-1",
    }

    def fake_run(command, **kwargs):
        calls.append({"command": command, **kwargs})
        command_text = " ".join(str(part) for part in command)
        if "cs_index_once.py" in command_text:
            stdout = json.dumps(
                {
                    "success": True,
                    "chunks_added": 1,
                    "index_identity_status": "ready",
                    "index_identity": index_identity,
                    "embedding_identity": embedding_identity,
                }
            ).encode()
        elif "cs_search_once.py" in command_text:
            stdout = json.dumps(
                {
                    "results": [],
                    "effective_search": {
                        "embedding": embedding_identity,
                        "reranker": {
                            "requested_mode": "sonnet",
                            "applied": False,
                            "reason": "not_invoked_insufficient_candidates",
                            "latency_ms": 0,
                            "model": None,
                        },
                    },
                    "rank_evidence": {
                        "requested_k": 50,
                        "returned_count": 0,
                        "total_candidates": 0,
                        "effective_k": 0,
                        "truncated": False,
                    },
                }
            ).encode()
        elif "-json" in command:
            stdout = b'{"code_localize_agent":{"entities":[]}}'
        else:
            stdout = b""
        return subprocess.CompletedProcess(
            args=command,
            returncode=0,
            stdout=stdout,
            stderr=b"",
        )

    monkeypatch.setattr(eval_locbench_batch.subprocess, "run", fake_run)

    repo = tmp_path / "repo"
    assert eval_locbench_batch.clone_repo("owner/repo", "a" * 40, repo) is True
    assert eval_locbench_batch.run_agent(
        tmp_path / "index.db",
        "find the target",
        json_mode=True,
    ).get("error") is None
    index_outcome = index_with_code_search(
        sys.executable,
        str(tmp_path),
        repo,
        tmp_path / "storage",
        force_full=True,
    )
    assert index_outcome.success is True
    assert index_outcome.chunks_added == 1
    search_outcome = search_once(
        sys.executable,
        str(tmp_path),
        tmp_path / "storage",
        "find the target",
    )
    assert search_outcome.results == []
    assert search_outcome.rank_evidence["effective_k"] == 0
    assert checkout(repo, "b" * 40) is True

    forbidden = {"CODE_INTEL_COMPONENT_TOKEN", "GH_TOKEN", "UNRELATED_SECRET"}
    git_calls = [
        call
        for call in calls
        if Path(str(call["command"][0])).name == "git"
    ]
    graph_agent = next(call for call in calls if "-json" in call["command"])
    retrieval_index = next(
        call for call in calls if "cs_index_once.py" in " ".join(map(str, call["command"]))
    )
    retrieval_search = next(
        call for call in calls if "cs_search_once.py" in " ".join(map(str, call["command"]))
    )

    for call in git_calls:
        assert "ANTHROPIC_API_KEY" not in call["env"]
        assert "VOYAGE_API_KEY" not in call["env"]
    assert graph_agent["env"]["ANTHROPIC_API_KEY"] == "anthropic-secret"
    assert graph_agent["env"]["VOYAGE_API_KEY"] == "voyage-secret"
    assert retrieval_index["env"]["VOYAGE_API_KEY"] == "voyage-secret"
    assert "ANTHROPIC_API_KEY" not in retrieval_index["env"]
    assert retrieval_search["env"]["ANTHROPIC_API_KEY"] == "anthropic-secret"
    assert retrieval_search["env"]["VOYAGE_API_KEY"] == "voyage-secret"
    for call in calls:
        assert call["env"]["PATH"] == "/safe/bin"
        assert forbidden.isdisjoint(call["env"])
