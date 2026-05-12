"""Per-category binary judges + schema validators.

The continuous 0-1 LLM judge in arxiv-bench/scorer.py captures answer
quality; these binary judges capture per-category failure modes that
the continuous score alone would miss.

Each judge returns:
    {
        "category": int,
        "signal_caught": bool,  # True = failure mode triggered (BAD)
        "evidence": str,         # human-readable evidence
    }
"""
from __future__ import annotations

import json
from typing import Any


def judge_banner_interference(row: dict[str, Any]) -> dict[str, Any]:
    """Category 1: did the first tool response contain the update banner?

    A category-1 failure: first tool was called, returned 74 bytes of
    "⚡ Update available: ..." and nothing else. Agent had no real data.
    """
    tools_used = row.get("agent_tools_used", [])
    response = row.get("agent_response", "") or ""

    signal = False
    evidence = ""
    if response.startswith("⚡ Update available"):
        signal = True
        evidence = "agent_response starts with update banner"
    elif "Update available" in response and len(response) < 200:
        signal = True
        evidence = "agent_response is mostly update banner (<200 chars)"
    elif not tools_used:
        signal = False
        evidence = "no tools called"
    return {"category": 1, "signal_caught": signal, "evidence": evidence}


def judge_verbosity_bloat(row: dict[str, Any]) -> dict[str, Any]:
    """Category 2: did the agent run out of turns due to context bloat?

    Signal triggers when stop_reason indicates exhaustion AND the agent
    used a tool known to emit large default responses.
    """
    stop_reason = row.get("agent_stop_reason", "")
    tools_used = row.get("agent_tools_used", [])
    tokens = (row.get("agent_input_tokens", 0) or 0) + (row.get("agent_output_tokens", 0) or 0)

    # Tools with measured-large default responses
    bloat_tools = {"list_projects", "service_map", "find_rationale", "detect_cycles"}
    used_bloat_tool = any(t.get("name") in bloat_tools for t in tools_used)

    signal = False
    evidence = ""
    if stop_reason in {"max_turns_exhausted", "max_tokens"} and used_bloat_tool:
        signal = True
        evidence = f"stop_reason={stop_reason} after invoking bloat-prone tool"
    elif tokens > 200_000:
        signal = True
        evidence = f"agent consumed {tokens:,} tokens (>200K threshold)"
    return {"category": 2, "signal_caught": signal, "evidence": evidence}


def judge_schema_drift(row: dict[str, Any]) -> dict[str, Any]:
    """Category 3: did any tool call fail because of arg-validation?

    A failed tool call (ok=False) on a non-trivial question is the
    drift signal — the agent passed args the handler refused, almost
    always because the agent's schema told it to use wrong arg names.
    """
    tools_used = row.get("agent_tools_used", [])
    failures = [t for t in tools_used if not t.get("ok", True)]

    signal = len(failures) > 0
    if signal:
        names = sorted({t.get("name", "?") for t in failures})
        evidence = f"{len(failures)} tool-call failures: {names}"
    else:
        evidence = "no tool-call failures"
    return {"category": 3, "signal_caught": signal, "evidence": evidence}


def judge_missing_surface(row: dict[str, Any]) -> dict[str, Any]:
    """Category 4: did the agent exhaust max_turns without producing an answer?

    The canonical signal: stop_reason='max_turns_exhausted' AND the
    response is empty/junk. Same as Q9 pre-degree_filter.
    """
    stop_reason = row.get("agent_stop_reason", "")
    response = (row.get("agent_response") or "").strip()

    signal = False
    evidence = ""
    if stop_reason == "max_turns_exhausted":
        signal = True
        evidence = f"agent hit max_turns; response len={len(response)} chars"
    elif response.startswith("[max_turns") or "max_turns_exhausted" in response:
        signal = True
        evidence = "agent_response carries the max_turns marker"
    return {"category": 4, "signal_caught": signal, "evidence": evidence}


# --- Schema-validation judges (category 6) ---


def _try_parse_json(text: str) -> Any:
    text = (text or "").strip()
    if not text:
        return None
    try:
        return json.loads(text)
    except (json.JSONDecodeError, ValueError):
        return None


def judge_output_shape(question: dict[str, Any], raw_response: str) -> dict[str, Any]:
    """Category 6: validate response shape against the expected schema.

    Direct JSON schema validation — no LLM needed. Returns a binary
    pass/fail with concrete evidence.
    """
    parsed = _try_parse_json(raw_response)
    if parsed is None:
        return {
            "category": 6,
            "signal_caught": True,
            "evidence": f"response did not parse as JSON (head={raw_response[:120]!r})",
        }

    assert_shape = question.get("assert_shape", "")
    expected_keys = question.get("expected_keys", [])
    expected_keys_either = question.get("expected_keys_either", [])
    must_not_contain = question.get("must_not_contain_keys", [])

    fails: list[str] = []

    if assert_shape == "top_level_array":
        if not isinstance(parsed, list):
            fails.append(f"expected top-level array; got {type(parsed).__name__}")
        elif not parsed:
            fails.append("expected non-empty array; got []")
        elif "name" not in (parsed[0] or {}):
            fails.append("array entries missing 'name' field")
    else:
        if not isinstance(parsed, dict):
            fails.append(f"expected object; got {type(parsed).__name__}")
        else:
            if expected_keys:
                missing = [k for k in expected_keys if k not in parsed]
                if missing:
                    fails.append(f"missing keys: {missing}")
            if expected_keys_either:
                matched_one = False
                for key_set in expected_keys_either:
                    if all(k in parsed for k in key_set):
                        matched_one = True
                        break
                if not matched_one:
                    fails.append(f"none of the alternative key sets matched: {expected_keys_either}")
            if must_not_contain:
                present_bad = [k for k in must_not_contain if k in parsed]
                if present_bad:
                    fails.append(f"unexpectedly present (post-compaction-fix): {present_bad}")

    return {
        "category": 6,
        "signal_caught": bool(fails),
        "evidence": "; ".join(fails) if fails else "shape OK",
    }


# --- Category dispatcher ---

CATEGORY_JUDGES = {
    1: judge_banner_interference,
    2: judge_verbosity_bloat,
    3: judge_schema_drift,
    4: judge_missing_surface,
}


def run_category_judge(question: dict[str, Any], row: dict[str, Any]) -> dict[str, Any]:
    """Dispatch the right judge based on the question's category."""
    cat = question.get("category")
    judge = CATEGORY_JUDGES.get(cat)
    if judge is None:
        return {"category": cat, "signal_caught": False, "evidence": "no judge for category"}
    return judge(row)


if __name__ == "__main__":
    # Smoke test the binary judges
    sample_rows = [
        ("banner-fired", {"agent_response": "⚡ Update available: vdev → v0.6.1", "agent_tools_used": []}),
        ("banner-ok",    {"agent_response": "{ nodes: 80000 }", "agent_tools_used": []}),
        ("verbose-fail", {"agent_stop_reason": "max_turns_exhausted",
                          "agent_tools_used": [{"name": "service_map", "ok": True}],
                          "agent_input_tokens": 250000, "agent_output_tokens": 5000}),
        ("schema-fail",  {"agent_tools_used": [{"name": "manage_adr", "ok": False}]}),
        ("missing-fail", {"agent_stop_reason": "max_turns_exhausted",
                          "agent_response": "[max_turns exhausted]",
                          "agent_tools_used": []}),
    ]
    for name, row in sample_rows:
        for cat, judge in CATEGORY_JUDGES.items():
            r = judge(row)
            if r["signal_caught"]:
                print(f"  [{name}] category {cat} TRIGGERED: {r['evidence']}")
