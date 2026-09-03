"""LLM-as-judge scorer for the arxiv-benchmark.

Takes an agent response + question + (optional) reference answer and
returns a continuous 0.0-1.0 score plus classification.

Classification thresholds (matching upstream BENCHMARK.md / arXiv):
    PASS    >= 0.80
    PARTIAL  0.40 - 0.79
    FAIL     < 0.40

The judge uses Claude (default Opus 4.7) with a strict rubric. It's
deliberately less expensive than the agent: minimal output tokens, no
tool use.

Loud failure behavior:
    - Judge errors return score=None, classification="JUDGE_ERROR",
      and the error message in `error`.
    - Caller (run_full_eval) treats JUDGE_ERROR as a non-fatal mark and
      records it to results.jsonl so the question can be re-scored later.
"""

from __future__ import annotations

import json
import os
import re
import time
from typing import Any

import anthropic

JUDGE_MODEL = os.environ.get("JUDGE_MODEL", "claude-opus-4-7")

JUDGE_SYSTEM = """You are evaluating a code-understanding agent's response against a structural question about an indexed codebase.

Score the response on a continuous 0.0-1.0 scale based on:
- Factual accuracy (cited counts, names, file paths must be plausible/correct)
- Completeness (does it answer all parts of the question?)
- Use of structural evidence (good answers cite specific nodes / counts / file:lines, not vague summaries)

Scoring guide:
- 1.0: Complete, accurate, well-evidenced
- 0.85: Mostly complete and accurate; minor gaps or vagueness
- 0.65: Partial answer; some parts addressed accurately, others missing or wrong
- 0.40: Significant gaps or factual issues but shows some understanding
- 0.20: Mostly wrong or non-responsive
- 0.0: Hallucinated, refused, or completely off-topic

Output ONLY a JSON object on a single line:
{"score": 0.0-1.0, "rationale": "1-2 sentence explanation"}
"""

JUDGE_USER_TEMPLATE = """Question (for {lang_id} project):
{question_text}

Scoring criteria from the rubric:
{scoring_criteria}

Agent's response:
---
{response}
---

Tool calls used: {tool_calls}
Tools invoked: {tools_used_summary}

Score this response."""


def _parse_judge_output(text: str) -> tuple[float | None, str]:
    """Extract score+rationale from judge JSON. Robust to extra prose."""
    # Find the JSON object
    match = re.search(r"\{[^{}]*\"score\"[^{}]*\}", text, re.DOTALL)
    if not match:
        return None, f"NO_JSON_FOUND in judge output: {text[:200]}"
    try:
        obj = json.loads(match.group(0))
        score = float(obj.get("score", -1))
        rationale = str(obj.get("rationale", ""))
        if 0.0 <= score <= 1.0:
            return score, rationale
        return None, f"OUT_OF_RANGE score={score}: {rationale}"
    except (json.JSONDecodeError, ValueError, TypeError) as e:
        return None, f"PARSE_ERROR {type(e).__name__}: {e} | text: {text[:200]}"


def classify(score: float) -> str:
    """Apply upstream's PASS/PARTIAL/FAIL thresholds."""
    if score >= 0.80:
        return "PASS"
    if score >= 0.40:
        return "PARTIAL"
    return "FAIL"


def score_response(
    lang_id: str,
    question_id: int,
    question_text: str,
    scoring_criteria: str,
    response: str,
    tool_calls: int,
    tools_used: list[dict[str, Any]],
    *,
    model: str = JUDGE_MODEL,
) -> dict[str, Any]:
    """Score a single agent response. Returns dict with score, classification, rationale, error."""
    tools_summary = ", ".join(f"{t.get('name', '?')}" for t in tools_used[:6]) if tools_used else "(none)"
    if len(tools_used) > 6:
        tools_summary += f" + {len(tools_used) - 6} more"

    user_msg = JUDGE_USER_TEMPLATE.format(
        lang_id=lang_id,
        question_text=question_text,
        scoring_criteria=scoring_criteria,
        response=response[:6000],  # truncate huge responses
        tool_calls=tool_calls,
        tools_used_summary=tools_summary,
    )

    client = anthropic.Anthropic()
    start = time.time()
    try:
        resp = client.messages.create(
            model=model,
            max_tokens=400,
            system=JUDGE_SYSTEM,
            messages=[{"role": "user", "content": user_msg}],
        )
        elapsed = time.time() - start
        judge_text = "".join(b.text for b in resp.content if b.type == "text")
        score, rationale = _parse_judge_output(judge_text)

        if score is None:
            # Loud failure mark — caller writes this to JSONL so it can be re-scored
            return {
                "score": None,
                "classification": "JUDGE_PARSE_ERROR",
                "rationale": rationale,  # contains the parse-error reason
                "judge_raw": judge_text[:500],
                "judge_input_tokens": resp.usage.input_tokens,
                "judge_output_tokens": resp.usage.output_tokens,
                "judge_elapsed_s": round(elapsed, 2),
                "judge_model": model,
                "error": rationale,
            }

        return {
            "score": score,
            "classification": classify(score),
            "rationale": rationale,
            "judge_input_tokens": resp.usage.input_tokens,
            "judge_output_tokens": resp.usage.output_tokens,
            "judge_elapsed_s": round(elapsed, 2),
            "judge_model": model,
            "error": None,
        }
    except anthropic.APIError as e:
        return {
            "score": None,
            "classification": "JUDGE_API_ERROR",
            "rationale": str(e),
            "judge_elapsed_s": round(time.time() - start, 2),
            "judge_model": model,
            "error": f"AnthropicAPIError: {e}",
        }
    except Exception as e:
        return {
            "score": None,
            "classification": "JUDGE_ERROR",
            "rationale": str(e),
            "judge_elapsed_s": round(time.time() - start, 2),
            "judge_model": model,
            "error": f"{type(e).__name__}: {e}",
        }


if __name__ == "__main__":
    # Smoke test
    sample = {
        "lang_id": "rust",
        "question_id": 1,
        "question_text": "What is the structure of this codebase? Report nodes, edges, and label counts.",
        "scoring_criteria": "PASS if counts are accurate and labels enumerated; PARTIAL if partial enumeration; FAIL if no schema info.",
        "response": "The codebase has 4414 nodes and 16550 edges, with labels Method (2038), Function (689), Class (357), and 15 others. Edge types include USAGE (5081), CALLS (3339), DEFINES_METHOD (1987).",
        "tool_calls": 1,
        "tools_used": [{"name": "get_graph_schema"}],
    }
    result = score_response(**sample)
    print(json.dumps(result, indent=2))
