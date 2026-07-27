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


def _is_json_integer(value: Any) -> bool:
    """bool is an int subclass in Python, but not a JSON integer contract."""
    return isinstance(value, int) and not isinstance(value, bool)


def _expect_object(value: Any, path: str, fails: list[str]) -> dict[str, Any] | None:
    if not isinstance(value, dict):
        fails.append(f"{path} must be an object; got {type(value).__name__}")
        return None
    return value


def _expect_array(value: Any, path: str, fails: list[str]) -> list[Any] | None:
    if not isinstance(value, list):
        fails.append(f"{path} must be an array; got {type(value).__name__}")
        return None
    return value


def _require_string(
    obj: dict[str, Any],
    key: str,
    path: str,
    fails: list[str],
) -> None:
    value = obj.get(key)
    if not isinstance(value, str) or not value:
        fails.append(f"{path}.{key} must be a non-empty string")


def _require_string_type(
    obj: dict[str, Any],
    key: str,
    path: str,
    fails: list[str],
) -> None:
    if not isinstance(obj.get(key), str):
        fails.append(f"{path}.{key} must be a string")


def _require_non_negative_integer(
    obj: dict[str, Any],
    key: str,
    path: str,
    fails: list[str],
) -> None:
    value = obj.get(key)
    if not _is_json_integer(value) or value < 0:
        fails.append(f"{path}.{key} must be a non-negative integer")


def _require_positive_integer(
    obj: dict[str, Any],
    key: str,
    path: str,
    fails: list[str],
) -> None:
    value = obj.get(key)
    if not _is_json_integer(value) or value <= 0:
        fails.append(f"{path}.{key} must be a positive integer")


def _require_boolean(
    obj: dict[str, Any],
    key: str,
    path: str,
    fails: list[str],
) -> None:
    if not isinstance(obj.get(key), bool):
        fails.append(f"{path}.{key} must be a boolean")


def _require_object_field(
    obj: dict[str, Any],
    key: str,
    path: str,
    fails: list[str],
) -> None:
    _expect_object(obj.get(key), f"{path}.{key}", fails)


def _require_exact_value(
    obj: dict[str, Any],
    key: str,
    expected: Any,
    path: str,
    fails: list[str],
) -> None:
    value = obj.get(key)
    if type(value) is not type(expected) or value != expected:
        fails.append(f"{path}.{key} must equal {expected!r}; got {value!r}")


def _validate_counted_entries(
    value: Any,
    path: str,
    name_key: str,
    fails: list[str],
) -> None:
    entries = _expect_array(value, path, fails)
    if entries is None:
        return
    for index, raw_entry in enumerate(entries):
        entry_path = f"{path}[{index}]"
        entry = _expect_object(raw_entry, entry_path, fails)
        if entry is None:
            continue
        _require_string(entry, name_key, entry_path, fails)
        _require_non_negative_integer(entry, "count", entry_path, fails)


def _validate_graph_schema(
    _question: dict[str, Any],
    parsed: Any,
    fails: list[str],
) -> None:
    response = _expect_object(parsed, "response", fails)
    if response is None:
        return
    _require_object_field(response, "_metadata", "response", fails)
    projects = _expect_array(response.get("projects"), "projects", fails)
    if projects is None:
        return
    if len(projects) != 1:
        fails.append(f"projects must contain exactly one entry; got {len(projects)}")
    for index, raw_project in enumerate(projects):
        project_path = f"projects[{index}]"
        project = _expect_object(raw_project, project_path, fails)
        if project is None:
            continue
        _require_string(project, "project", project_path, fails)
        _require_boolean(project, "adr_present", project_path, fails)
        schema = _expect_object(project.get("schema"), f"{project_path}.schema", fails)
        if schema is None:
            continue
        _validate_counted_entries(
            schema.get("node_labels"),
            f"{project_path}.schema.node_labels",
            "label",
            fails,
        )

        _validate_counted_entries(
            schema.get("relationship_types"),
            f"{project_path}.schema.relationship_types",
            "type",
            fails,
        )


def _validate_search_graph(
    question: dict[str, Any],
    parsed: Any,
    fails: list[str],
) -> None:
    response = _expect_object(parsed, "response", fails)
    if response is None:
        return
    _require_object_field(response, "_metadata", "response", fails)
    for key in ("total", "limit", "offset"):
        _require_non_negative_integer(response, key, "response", fails)
    _require_boolean(response, "has_more", "response", fails)
    results = _expect_array(response.get("results"), "results", fails)
    if results is None:
        return

    requested_limit = question.get("args", {}).get("limit")
    if _is_json_integer(requested_limit):
        _require_exact_value(response, "limit", requested_limit, "response", fails)
        if len(results) > requested_limit:
            fails.append(
                f"results has {len(results)} entries; requested limit is {requested_limit}"
            )
    requested_offset = question.get("args", {}).get("offset", 0)
    if _is_json_integer(requested_offset):
        _require_exact_value(response, "offset", requested_offset, "response", fails)

    total = response.get("total")
    if _is_json_integer(total) and total < len(results):
        fails.append(f"response.total is {total}, below {len(results)} returned results")
    limit = response.get("limit")
    offset = response.get("offset")
    has_more = response.get("has_more")
    if (
        _is_json_integer(total)
        and _is_json_integer(limit)
        and _is_json_integer(offset)
        and isinstance(has_more, bool)
        and has_more != (offset + limit < total)
    ):
        fails.append("response.has_more disagrees with total/limit/offset")

    requested_label = question.get("args", {}).get("label")
    for index, raw_entry in enumerate(results):
        entry_path = f"results[{index}]"
        entry = _expect_object(raw_entry, entry_path, fails)
        if entry is None:
            continue
        for key in ("project", "name", "qualified_name", "label", "file_path"):
            _require_string(entry, key, entry_path, fails)
        for key in ("start_line", "end_line", "in_degree", "out_degree"):
            _require_non_negative_integer(entry, key, entry_path, fails)
        if isinstance(requested_label, str):
            _require_exact_value(entry, "label", requested_label, entry_path, fails)


def _validate_degree_filter(
    question: dict[str, Any],
    parsed: Any,
    fails: list[str],
) -> None:
    response = _expect_object(parsed, "response", fails)
    if response is None:
        return
    for key in ("project", "label", "direction", "edge_type", "op"):
        _require_string(response, key, "response", fails)
    _require_non_negative_integer(response, "value", "response", fails)
    _require_non_negative_integer(response, "count", "response", fails)
    _require_boolean(response, "exclude_entry_points", "response", fails)

    args = question.get("args", {})
    expected_echoes = {
        "label": args.get("label"),
        "direction": args.get("direction"),
        "edge_type": args.get("edge_type", "CALLS"),
        "op": args.get("op"),
        "value": args.get("value"),
        "exclude_entry_points": args.get("exclude_entry_points", False),
    }
    for key, expected in expected_echoes.items():
        if expected is not None:
            _require_exact_value(response, key, expected, "response", fails)

    examples = _expect_array(response.get("examples"), "examples", fails)
    if examples is None:
        return

    requested_limit = args.get("limit")
    if _is_json_integer(requested_limit) and len(examples) > requested_limit:
        fails.append(
            f"examples has {len(examples)} entries; requested limit is {requested_limit}"
        )
    count = response.get("count")
    if _is_json_integer(count) and len(examples) > count:
        fails.append(f"examples has {len(examples)} entries but count is {count}")
    if _is_json_integer(count) and _is_json_integer(requested_limit):
        expected_examples = min(count, requested_limit)
        if len(examples) != expected_examples:
            fails.append(
                f"examples has {len(examples)} entries; expected {expected_examples}"
            )
    for index, raw_entry in enumerate(examples):
        entry_path = f"examples[{index}]"
        entry = _expect_object(raw_entry, entry_path, fails)
        if entry is None:
            continue
        _require_string(entry, "name", entry_path, fails)
        _require_non_negative_integer(entry, "degree", entry_path, fails)
        for optional_string in ("qualified_name", "file"):
            if optional_string in entry:
                _require_string(entry, optional_string, entry_path, fails)
        if args.get("op") == "eq" and _is_json_integer(args.get("value")):
            _require_exact_value(
                entry,
                "degree",
                args["value"],
                entry_path,
                fails,
            )


def _validate_architecture(
    _question: dict[str, Any],
    parsed: Any,
    fails: list[str],
) -> None:
    response = _expect_object(parsed, "response", fails)
    if response is None:
        return
    _require_string(response, "project", "response", fails)
    _require_object_field(response, "_metadata", "response", fails)
    _require_non_negative_integer(response, "total_nodes", "response", fails)
    _require_non_negative_integer(response, "total_edges", "response", fails)
    _validate_counted_entries(
        response.get("node_labels"),
        "node_labels",
        "label",
        fails,
    )
    _validate_counted_entries(
        response.get("edge_types"),
        "edge_types",
        "type",
        fails,
    )

    for entries_key, total_key in (
        ("node_labels", "total_nodes"),
        ("edge_types", "total_edges"),
    ):
        entries = response.get(entries_key)
        total = response.get(total_key)
        if (
            isinstance(entries, list)
            and _is_json_integer(total)
            and all(
                isinstance(entry, dict)
                and _is_json_integer(entry.get("count"))
                and entry["count"] >= 0
                for entry in entries
            )
        ):
            histogram_total = sum(entry["count"] for entry in entries)
            if histogram_total != total:
                fails.append(
                    f"{entries_key} counts sum to {histogram_total}; "
                    f"{total_key} is {total}"
                )

    detail_keys = {
        "languages",
        "packages",
        "entry_points",
        "routes",
        "hotspots",
        "boundaries",
        "services",
        "layers",
        "clusters",
        "file_tree",
        "adr",
        "adr_hint",
    }
    present_details = sorted(detail_keys.intersection(response))
    if present_details:
        fails.append(f"compact summary contains detail keys: {present_details}")


def _validate_list_projects(
    _question: dict[str, Any],
    parsed: Any,
    fails: list[str],
) -> None:
    projects = _expect_array(parsed, "response", fails)
    if projects is None:
        return
    for index, raw_project in enumerate(projects):
        project_path = f"projects[{index}]"
        project = _expect_object(raw_project, project_path, fails)
        if project is None:
            continue
        for key in (
            "name",
            "root_path",
            "indexed_at",
            "db_path",
            "status",
            "identity_status",
            "identity_reason",
        ):
            _require_string_type(project, key, project_path, fails)
        for key in ("nodes", "edges"):
            _require_non_negative_integer(project, key, project_path, fails)
        _require_boolean(project, "adr_present", project_path, fails)
        if project.get("status") not in {"ready", "degraded"}:
            fails.append(f"{project_path}.status must be ready or degraded")
        if project.get("identity_status") not in {
            "captured",
            "pending",
            "error",
            "missing",
            "stale_source",
        }:
            fails.append(f"{project_path}.identity_status is not recognized")
        if "is_session_project" in project:
            _require_boolean(project, "is_session_project", project_path, fails)
        if "index_identity" in project:
            _expect_object(
                project["index_identity"],
                f"{project_path}.index_identity",
                fails,
            )


def _validate_code_snippet(
    _question: dict[str, Any],
    parsed: Any,
    fails: list[str],
) -> None:
    response = _expect_object(parsed, "response", fails)
    if response is None:
        return
    if "name" in response and "file_path" in response:
        for key in (
            "qualified_name",
            "name",
            "label",
            "file_path",
            "source",
        ):
            _require_string(response, key, "response", fails)
        for key in ("start_line", "end_line"):
            _require_positive_integer(response, key, "response", fails)
        for key in ("callers", "callees"):
            _require_non_negative_integer(response, key, "response", fails)
        _require_object_field(response, "_metadata", "response", fails)
        start_line = response.get("start_line")
        end_line = response.get("end_line")
        if (
            _is_json_integer(start_line)
            and _is_json_integer(end_line)
            and end_line < start_line
        ):
            fails.append("response.end_line must not precede start_line")
        return
    if "status" not in response or "suggestions" not in response:
        return  # The generic alternative-key check reports this contract error.

    if response.get("status") != "ambiguous":
        fails.append("response.status must be 'ambiguous' when suggestions are returned")
    _require_string(response, "message", "response", fails)
    suggestions = _expect_array(response.get("suggestions"), "suggestions", fails)
    if suggestions is None:
        return
    if not suggestions:
        fails.append("suggestions must be non-empty for an ambiguous response")
    for index, raw_suggestion in enumerate(suggestions):
        suggestion_path = f"suggestions[{index}]"
        suggestion = _expect_object(raw_suggestion, suggestion_path, fails)
        if suggestion is None:
            continue
        for key in ("qualified_name", "name", "label", "file_path"):
            _require_string(suggestion, key, suggestion_path, fails)


_OUTPUT_CONTRACT_VALIDATORS = {
    "get_graph_schema": _validate_graph_schema,
    "search_graph": _validate_search_graph,
    "degree_filter": _validate_degree_filter,
    "get_architecture": _validate_architecture,
    "list_projects": _validate_list_projects,
    "get_code_snippet": _validate_code_snippet,
}


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

    validator = _OUTPUT_CONTRACT_VALIDATORS.get(question.get("tool"))
    if validator is not None:
        validator(question, parsed, fails)

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
