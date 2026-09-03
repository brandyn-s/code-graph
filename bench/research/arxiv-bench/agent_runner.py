"""Agent runner for the upstream arXiv benchmark reproduction.

Runs a single (project, question) pair through Claude Opus 4.7 with a
restricted MCP tool surface backed by our local code-graph binary's
CLI mode.

Why not the MCP server directly? The MCP server is stdio-based and
designed for long-lived clients. For batch benchmarking, invoking the
binary in `cli` mode per tool call is simpler, deterministic, and
doesn't require an MCP transport bridge inside Python.

Output JSON per call:
    {
        "lang_id": "rust",
        "question_id": 1,
        "response": "<final assistant text>",
        "tool_calls": <count>,
        "tools_used": [{"name":..., "input":..., "result_preview":...}],
        "input_tokens": <int>,
        "output_tokens": <int>,
        "elapsed_s": <float>,
        "stop_reason": "end_turn" | "tool_use" | "max_tokens" | ...,
        "error": null | "<msg>"
    }
"""

from __future__ import annotations

import json
import os
import subprocess
import time
from pathlib import Path
from typing import Any

import anthropic

# ---- Configuration -------------------------------------------------------

BINARY = Path(os.environ.get(
    "CODE_GRAPH_BIN",
    str(Path.home() / "Documents" / "GitHub" / "code-graph" / "bin" / "codebase-memory-mcp.exe"),
))
UPSTREAM_BINARY = Path(os.environ.get(
    "UPSTREAM_CBM_BIN",
    str(Path.home() / "code" / "upstream-cbm" / "codebase-memory-mcp.exe"),
))
UPSTREAM_CACHE_DIR = os.environ.get(
    "UPSTREAM_CBM_CACHE_DIR",
    str(Path.home() / ".cache-upstream-cbm"),
)

# Backend selection: "ours" (this fork) or "upstream" (DeusData/codebase-memory-mcp v0.6.1)
BACKEND = os.environ.get("BENCH_BACKEND", "ours")

# Upstream v0.6.1 renamed trace_call_path -> trace_path. Map agent's
# canonical tool names to the backend's actual CLI tool name.
TOOL_NAME_REMAP: dict[str, dict[str, str]] = {
    "upstream": {"trace_call_path": "trace_path"},
}

MODEL = os.environ.get("ANTHROPIC_MODEL", "claude-opus-4-7")
MAX_AGENT_TURNS = int(os.environ.get("MAX_AGENT_TURNS", "12"))
TOOL_RESULT_TRUNCATE = int(os.environ.get("TOOL_RESULT_TRUNCATE", "8000"))

# ---- Tool schema construction --------------------------------------------

# TOOL_SCHEMAS is generated from internal/tools/*.go InputSchema literals by
# bench/research/agent-effectiveness/generate_schemas.py. The CI gate in
# .github/workflows/agent-effectiveness.yml re-runs the generator and
# fails if the committed file is out of sync. This closes the schema-drift
# failure-mode class (14 mismatches + 22 missing tools as of 2026-05-12).
#
# DO NOT add hand-written schema entries here. Add them as InputSchema
# literals in the handler source instead; codegen will pick them up.
from _generated_tool_schemas import TOOL_SCHEMAS  # noqa: E402,F401

# Old hand-written schemas removed below; codegen is the source of truth.



def build_tool_list(allowlist: list[str]) -> list[dict[str, Any]]:
    """Build Anthropic tool schemas for the allowed tool names."""
    tools = []
    for name in allowlist:
        if name not in TOOL_SCHEMAS:
            # Bonus tool not in our base schemas; provide minimal stub
            tools.append({
                "name": name,
                "description": f"fork-specific tool: {name}. Refer to code-graph CLAUDE.md for semantics.",
                "input_schema": {"type": "object", "properties": {"project": {"type": "string"}}, "required": ["project"]},
            })
            continue
        schema = TOOL_SCHEMAS[name]
        tools.append({
            "name": name,
            "description": schema["description"],
            "input_schema": schema["input_schema"],
        })
    return tools


# ---- CLI tool invocation -------------------------------------------------

def invoke_tool(name: str, args: dict[str, Any], backend: str | None = None) -> tuple[str, bool]:
    """Invoke a tool via the binary's CLI mode. Returns (output_text, ok).

    Routes through the requested backend ("ours" / "upstream"). Defaults to
    module-level BACKEND. Handles per-backend tool-name rename + env-var
    isolation (upstream uses CBM_CACHE_DIR).
    """
    if backend is None:
        backend = BACKEND

    if backend == "upstream":
        binary = UPSTREAM_BINARY
        cmd_flags = []  # upstream v0.6.1 doesn't accept --raw; JSON is default
        env = {**os.environ, "CBM_CACHE_DIR": UPSTREAM_CACHE_DIR}
    else:
        binary = BINARY
        cmd_flags = ["--raw"]
        env = None  # inherit parent env

    actual_name = TOOL_NAME_REMAP.get(backend, {}).get(name, name)
    json_args = json.dumps(args)
    cmd = [str(binary), "cli"] + cmd_flags + [actual_name, json_args]
    try:
        result = subprocess.run(
            cmd,
            capture_output=True,
            timeout=120,
            env=env,
        )
        if result.returncode != 0:
            stderr_txt = result.stderr.decode("utf-8", errors="replace")[:2000]
            return f"ERROR (exit {result.returncode}): {stderr_txt}", False
        stdout_txt = result.stdout.decode("utf-8", errors="replace")
        return stdout_txt, True
    except subprocess.TimeoutExpired:
        return f"ERROR: tool {name} timed out after 120s", False
    except Exception as e:
        return f"ERROR: {type(e).__name__}: {e}", False


# ---- Main agent loop -----------------------------------------------------

SYSTEM_PROMPT = """You are answering a structural code-understanding question about an indexed codebase. You have access to MCP tools backed by a code knowledge graph.

The codebase has already been indexed; you do NOT need to call index_repository. The project ID will be given in the question.

Use the tools efficiently. Aim for a clear, factual answer in 2-3 paragraphs at most. Cite specific node names, file paths, or counts when relevant. If a tool returns an error or no data, acknowledge it rather than fabricating an answer."""


def run_question(
    lang_id: str,
    question_id: int,
    project_id: str,
    question_text: str,
    tool_allowlist: list[str],
    *,
    model: str = MODEL,
    max_turns: int = MAX_AGENT_TURNS,
    verbose: bool = False,
    backend: str | None = None,
) -> dict[str, Any]:
    """Run a single question through the agent loop. Returns result dict."""
    client = anthropic.Anthropic()
    tools = build_tool_list(tool_allowlist)

    user_message = (
        f"Project ID: {project_id}\n"
        f"Language: {lang_id}\n\n"
        f"Question:\n{question_text}"
    )

    messages: list[dict[str, Any]] = [{"role": "user", "content": user_message}]
    tool_calls = 0
    tools_used: list[dict[str, Any]] = []
    input_tokens_total = 0
    output_tokens_total = 0
    stop_reason = "unknown"
    final_text = ""
    error: str | None = None

    start = time.time()
    try:
        for turn in range(max_turns):
            resp = client.messages.create(
                model=model,
                max_tokens=4096,
                system=SYSTEM_PROMPT,
                tools=tools,
                messages=messages,
            )
            input_tokens_total += resp.usage.input_tokens
            output_tokens_total += resp.usage.output_tokens
            stop_reason = resp.stop_reason

            # Collect text and tool_use blocks
            text_blocks = [b.text for b in resp.content if b.type == "text"]
            tool_use_blocks = [b for b in resp.content if b.type == "tool_use"]

            if stop_reason == "end_turn" or not tool_use_blocks:
                final_text = "\n".join(text_blocks)
                break

            # Append assistant message with tool_use blocks
            messages.append({"role": "assistant", "content": resp.content})

            # Run each tool_use, collect tool_result blocks
            tool_results = []
            for tu in tool_use_blocks:
                tool_calls += 1
                if verbose:
                    print(f"  turn {turn} tool_use: {tu.name}({json.dumps(tu.input)[:120]})")
                out, ok = invoke_tool(tu.name, tu.input, backend=backend)
                truncated = out[:TOOL_RESULT_TRUNCATE]
                if len(out) > TOOL_RESULT_TRUNCATE:
                    truncated += f"\n... [truncated, full length {len(out)} chars]"
                tools_used.append({
                    "name": tu.name,
                    "input": tu.input,
                    "result_preview": truncated[:500],
                    "ok": ok,
                })
                tool_results.append({
                    "type": "tool_result",
                    "tool_use_id": tu.id,
                    "content": truncated,
                    "is_error": not ok,
                })

            messages.append({"role": "user", "content": tool_results})
        else:
            # max_turns exhausted
            final_text = (final_text + "\n[max_turns exhausted]").strip()
            stop_reason = "max_turns_exhausted"

    except anthropic.APIError as e:
        error = f"AnthropicAPIError: {e}"
    except Exception as e:
        error = f"{type(e).__name__}: {e}"

    elapsed = time.time() - start

    return {
        "lang_id": lang_id,
        "question_id": question_id,
        "project_id": project_id,
        "response": final_text,
        "tool_calls": tool_calls,
        "tools_used": tools_used,
        "input_tokens": input_tokens_total,
        "output_tokens": output_tokens_total,
        "elapsed_s": round(elapsed, 2),
        "stop_reason": stop_reason,
        "error": error,
        "model": model,
        "backend": backend if backend is not None else BACKEND,
    }


# ---- Smoke test entry point ----------------------------------------------

if __name__ == "__main__":
    import argparse

    parser = argparse.ArgumentParser(description="Run a single benchmark question against an indexed project.")
    parser.add_argument("--lang", required=True, help="Language ID (e.g., rust)")
    parser.add_argument("--question", required=True, type=int, help="Question ID (1-12)")
    parser.add_argument("--project", required=True, help="Indexed project ID")
    parser.add_argument("--allowlist", default="primary", help="primary | secondary | tool1,tool2,...")
    parser.add_argument("--verbose", action="store_true")
    args = parser.parse_args()

    questions_path = Path(__file__).parent / "questions.json"
    allowlist_path = Path(__file__).parent / "tool_allowlist.json"

    with open(questions_path) as f:
        questions_data = json.load(f)
    with open(allowlist_path) as f:
        allowlist_data = json.load(f)

    q = next(q for q in questions_data["questions"] if q["id"] == args.question)

    if args.allowlist == "primary":
        allowlist = allowlist_data["primary_14_tool_match"]["tools"]
    elif args.allowlist == "secondary":
        allowlist = allowlist_data["secondary_full_set"]["tools"]
    else:
        allowlist = args.allowlist.split(",")

    result = run_question(
        lang_id=args.lang,
        question_id=args.question,
        project_id=args.project,
        question_text=q["question"],
        tool_allowlist=allowlist,
        verbose=args.verbose,
    )

    print(json.dumps(result, indent=2))
