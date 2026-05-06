"""Shared schema for the Loc-Bench eval/audit/compare harness.

Get-well plan Phase 1 (2026-05-06): writer/reader contract drift was
the root cause of four same-class bugs surfaced in a single session
(see ROUNDTABLE_T2_T3_OUTCOMES.md and the harness get-well plan). This
module is the single source of truth for the per-case JSON shape; both
producers (eval_locbench_batch.py) and consumers (locbench_failure_audit.py,
d2_accuracy_compare.py) construct/parse via these dataclasses.

Why dataclasses (not Pydantic): zero new dependency. The harness
already has pandas / pyarrow as heavy deps; adding Pydantic for one
schema is overhead. dataclasses + a small `from_dict` validator gives
us 90% of the value with no install footprint.

Why raise on missing required fields: the legacy readers used
chain-of-`.get()` fallbacks that silently degraded to empty output
when the writer changed key names. The four found bugs all had the
same shape: writer-changed-key + reader-fell-back-silently. Raising
turns these into loud `KeyError` at the field-access site instead of
a "0 cases classified" silent pass at the verdict site.

Schema stability: the field names here MUST match what
eval_locbench_batch.py writes to the per-case JSON. To rename a field,
update this module first, then both writer and reader will fail-loud
until they're updated. That's the discipline this module enforces.
"""
from __future__ import annotations

import time
from dataclasses import dataclass, field, asdict
from typing import Any


def _required(d: dict, key: str) -> Any:
    """Return d[key] or raise a KeyError with context. Used by from_dict
    to convert silent-fallback bugs into loud failures at parse time."""
    if key not in d:
        raise KeyError(
            f"required field {key!r} missing from input dict; "
            f"got keys: {sorted(d.keys())}"
        )
    return d[key]


def _optional(d: dict, key: str, default: Any) -> Any:
    """Return d[key] or default. Use ONLY when the field is genuinely
    optional in the schema (e.g., agent_envelope is empty when the
    case wasn't agent-run). Most fields should be _required."""
    return d.get(key, default)


# ---------- AgentEnvelope (the inner agent-loop result) ----------


@dataclass(frozen=True)
class LocalizedEntity:
    """One entity returned by the localization agent.

    Mirrors locagent.LocalizedEntity (Go side) one-to-one. The MCP tool
    serializes this as a JSON object; this dataclass is the Python
    binding.
    """
    file_path: str = ""
    qualified_name: str = ""
    label: str = ""

    @classmethod
    def from_dict(cls, d: dict) -> "LocalizedEntity":
        return cls(
            file_path=str(_optional(d, "file_path", "")),
            qualified_name=str(_optional(d, "qualified_name", "")),
            label=str(_optional(d, "label", "")),
        )


@dataclass(frozen=True)
class CodeLocalizeAgentResult:
    """The structured output of the code_localize_agent MCP tool, as
    consumed by the eval harness. Field names match the JSON keys the
    Go MCP tool emits (see internal/tools/localize_agent.go).

    Schema-fidelity note: the eval CLI's envelope places
    `issue_description`/`project`/`top_k` at the OUTER envelope level
    (alongside `code_localize_agent`), not inside this dataclass. The
    inner `code_localize_agent` dict in the on-disk JSON contains only
    agent-loop output: `entities`, `iterations`, `turns`, `stop_reason`,
    `transcript`, `input_tokens`, `output_tokens`. This dataclass
    matches that on-disk shape; `entities` is the load-bearing field
    for the audit harness.
    """
    entities: list[LocalizedEntity] = field(default_factory=list)
    iterations: list[list[LocalizedEntity]] = field(default_factory=list)
    transcript: list[dict] = field(default_factory=list)
    turns: int = 0
    stop_reason: str = ""
    input_tokens: int = 0
    output_tokens: int = 0
    note: str = ""

    @classmethod
    def from_dict(cls, d: dict) -> "CodeLocalizeAgentResult":
        ents_raw = _optional(d, "entities", []) or []
        iters_raw = _optional(d, "iterations", []) or []
        return cls(
            entities=[LocalizedEntity.from_dict(e) for e in ents_raw if isinstance(e, dict)],
            iterations=[
                [LocalizedEntity.from_dict(e) for e in iter_list if isinstance(e, dict)]
                for iter_list in iters_raw
                if isinstance(iter_list, list)
            ],
            transcript=list(_optional(d, "transcript", []) or []),
            turns=int(_optional(d, "turns", 0)),
            stop_reason=str(_optional(d, "stop_reason", "")),
            input_tokens=int(_optional(d, "input_tokens", 0)),
            output_tokens=int(_optional(d, "output_tokens", 0)),
            note=str(_optional(d, "note", "")),
        )


@dataclass(frozen=True)
class AgentEnvelope:
    """The wrapper shape the eval CLI emits around a single agent run.

    Eval writes `agent_envelope` per case; that envelope contains
    `code_localize_agent` (the agent's structured output) and any
    auxiliary fields. Pre-Phase-1, readers had to chain `.get()` calls
    through this nesting; from_dict now raises if `code_localize_agent`
    is absent while the case claims agent_ran=True.
    """
    code_localize_agent: CodeLocalizeAgentResult | None = None

    @classmethod
    def from_dict(cls, d: dict | None, agent_ran: bool) -> "AgentEnvelope":
        """Parse an agent envelope. If the case is marked agent_ran but
        the envelope is missing or malformed, raise — that combination
        is the silent-failure pattern this schema exists to catch."""
        if d is None or not isinstance(d, dict):
            if agent_ran:
                raise KeyError(
                    "agent_envelope missing/malformed but case has agent_ran=True"
                )
            return cls(code_localize_agent=None)
        cla_raw = d.get("code_localize_agent")
        if cla_raw is None:
            if agent_ran:
                raise KeyError(
                    "agent_envelope.code_localize_agent missing but case has agent_ran=True"
                )
            return cls(code_localize_agent=None)
        if not isinstance(cla_raw, dict):
            raise TypeError(
                f"agent_envelope.code_localize_agent must be a dict, got {type(cla_raw).__name__}"
            )
        return cls(code_localize_agent=CodeLocalizeAgentResult.from_dict(cla_raw))


# ---------- PerCaseRecord (one Loc-Bench instance × one mode) ----------


@dataclass(frozen=True)
class PerCaseRecord:
    """A single instance's row in the per-case JSON. Eval writes;
    audit + compare read.

    Required fields raise KeyError on absence in from_dict — that's the
    contract enforcement that makes writer/reader drift loud instead
    of silent.
    """
    instance_id: str
    repo: str
    category: str
    ground_truth: list[str]
    indexed: bool
    agent_ran: bool
    file_hit: bool
    class_hit: bool
    func_hit: bool
    file_correct: bool
    class_correct: bool
    func_correct: bool
    turns: int
    input_tokens: int
    output_tokens: int
    cost_estimate_usd: float
    duration_s: float
    note: str
    agent_envelope: AgentEnvelope

    def to_dict(self) -> dict:
        """Serialize to the on-disk JSON shape eval writes."""
        env_dict: dict = {}
        if self.agent_envelope.code_localize_agent is not None:
            env_dict = {
                "code_localize_agent": _agent_result_to_dict(
                    self.agent_envelope.code_localize_agent
                )
            }
        return {
            "instance_id": self.instance_id,
            "repo": self.repo,
            "category": self.category,
            "ground_truth": list(self.ground_truth),
            "indexed": self.indexed,
            "agent_ran": self.agent_ran,
            "file_hit": self.file_hit,
            "class_hit": self.class_hit,
            "func_hit": self.func_hit,
            "file_correct": self.file_correct,
            "class_correct": self.class_correct,
            "func_correct": self.func_correct,
            "turns": self.turns,
            "input_tokens": self.input_tokens,
            "output_tokens": self.output_tokens,
            "cost_estimate_usd": self.cost_estimate_usd,
            "duration_s": self.duration_s,
            "note": self.note,
            "agent_envelope": env_dict,
        }

    @classmethod
    def from_dict(cls, d: dict) -> "PerCaseRecord":
        agent_ran = bool(_required(d, "agent_ran"))
        return cls(
            instance_id=str(_required(d, "instance_id")),
            repo=str(_required(d, "repo")),
            category=str(_required(d, "category")),
            ground_truth=list(_required(d, "ground_truth")),
            indexed=bool(_required(d, "indexed")),
            agent_ran=agent_ran,
            file_hit=bool(_required(d, "file_hit")),
            class_hit=bool(_required(d, "class_hit")),
            func_hit=bool(_required(d, "func_hit")),
            file_correct=bool(_optional(d, "file_correct", d.get("file_hit", False))),
            class_correct=bool(_optional(d, "class_correct", d.get("class_hit", False))),
            func_correct=bool(_optional(d, "func_correct", d.get("func_hit", False))),
            turns=int(_optional(d, "turns", 0)),
            input_tokens=int(_optional(d, "input_tokens", 0)),
            output_tokens=int(_optional(d, "output_tokens", 0)),
            cost_estimate_usd=float(_optional(d, "cost_estimate_usd", 0.0)),
            duration_s=float(_optional(d, "duration_s", 0.0)),
            note=str(_optional(d, "note", "")),
            agent_envelope=AgentEnvelope.from_dict(
                _optional(d, "agent_envelope", None), agent_ran=agent_ran
            ),
        )

    # ----- Convenience accessors that previous readers had to do via
    # ----- chain-of-.get() fallbacks. Centralized here so the readers
    # ----- can call one method instead of reproducing the lookup logic.

    @property
    def predicted_files(self) -> list[str]:
        """File paths the agent returned. Empty if agent didn't run."""
        cla = self.agent_envelope.code_localize_agent
        if cla is None:
            return []
        return [e.file_path for e in cla.entities if e.file_path]

    @property
    def iterations_files(self) -> list[list[str]]:
        """Per-iteration file path lists. Empty when iter=1 or no iters."""
        cla = self.agent_envelope.code_localize_agent
        if cla is None:
            return []
        return [
            [e.file_path for e in iter_list if e.file_path]
            for iter_list in cla.iterations
        ]

    @property
    def stop_reason(self) -> str:
        cla = self.agent_envelope.code_localize_agent
        return cla.stop_reason if cla else ""


def _agent_result_to_dict(r: CodeLocalizeAgentResult) -> dict:
    """Inverse of CodeLocalizeAgentResult.from_dict for to_dict path.

    Matches the on-disk shape eval emits: agent-loop output only.
    Identifying metadata (issue_description / project / top_k) lives
    at the envelope level, not here.
    """
    return {
        "entities": [asdict(e) for e in r.entities],
        "iterations": [[asdict(e) for e in il] for il in r.iterations],
        "transcript": list(r.transcript),
        "turns": r.turns,
        "stop_reason": r.stop_reason,
        "input_tokens": r.input_tokens,
        "output_tokens": r.output_tokens,
        "note": r.note,
    }


# ---------- BatchSummary (top-level per-case JSON envelope) ----------

SCHEMA_VERSION = 1


@dataclass(frozen=True)
class BatchSummaryRecord:
    """The top-level envelope eval_locbench_batch writes as the per-case
    JSON. Wraps a list of PerCaseRecord plus run-level metadata.

    Field names MUST match the on-disk JSON keys; this is the contract
    audit + compare both depend on.
    """
    schema_version: int
    generated_at: str
    n_total: int
    n_indexed: int
    n_agent_ran: int
    n_file_hit: int
    n_class_hit: int
    n_func_hit: int
    aborted_reason: str
    cases: list[PerCaseRecord]

    def to_dict(self) -> dict:
        return {
            "schema_version": self.schema_version,
            "generated_at": self.generated_at,
            "n_total": self.n_total,
            "n_indexed": self.n_indexed,
            "n_agent_ran": self.n_agent_ran,
            "n_file_hit": self.n_file_hit,
            "n_class_hit": self.n_class_hit,
            "n_func_hit": self.n_func_hit,
            "aborted_reason": self.aborted_reason,
            "cases": [c.to_dict() for c in self.cases],
        }

    @classmethod
    def from_dict(cls, d: dict) -> "BatchSummaryRecord":
        cases_raw = _required(d, "cases")
        if not isinstance(cases_raw, list):
            raise TypeError(
                f"'cases' must be a list, got {type(cases_raw).__name__}"
            )
        return cls(
            schema_version=int(_optional(d, "schema_version", SCHEMA_VERSION)),
            generated_at=str(_optional(d, "generated_at", "")),
            n_total=int(_required(d, "n_total")),
            n_indexed=int(_required(d, "n_indexed")),
            n_agent_ran=int(_required(d, "n_agent_ran")),
            n_file_hit=int(_required(d, "n_file_hit")),
            n_class_hit=int(_required(d, "n_class_hit")),
            n_func_hit=int(_required(d, "n_func_hit")),
            aborted_reason=str(_optional(d, "aborted_reason", "")),
            cases=[PerCaseRecord.from_dict(c) for c in cases_raw if isinstance(c, dict)],
        )


def now_iso_utc() -> str:
    """Used by eval_locbench_batch for the BatchSummaryRecord.generated_at
    field. Centralized here so the format is consistent across writers."""
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
