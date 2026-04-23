"""HTTP_CALLS ground-truth oracle via Opus + Sonnet ensemble with Opus tiebreaker.

Methodology:
1. Discover candidate files: .py files importing requests/httpx/aiohttp/fastapi/flask/starlette.
   Files without HTTP library imports can't produce HTTP_CALLS edges.
2. For each candidate file, send to Opus and Sonnet independently with the same
   prompt asking for HTTP_CALLS edges.
3. Consensus = edges both models emit (match on from_qn + URL pattern).
4. Tiebreaker: for edges only one model emits, ask Opus "is this a real HTTP
   call, yes/no?" — include in ground truth if yes.
5. Cache per (file_sha, model) so re-runs of an unchanged file are free.

Cost control:
- Only processes files that import an HTTP library (typically <10% of repo).
- Caches aggressively; first run is paid, subsequent runs are free.
- Uses Sonnet for the bulk, Opus only for primary extraction + tiebreak.

Edge format (to_qn for HTTP_CALLS):
    "METHOD path_pattern"  e.g., "POST /api/v1/messages"
    Code-graph stores url_path on HTTP_CALLS edges; we match against that.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import sys
import time
from pathlib import Path

try:
    import anthropic
except ImportError:
    raise SystemExit("pip install anthropic")

sys.path.insert(0, str(Path(__file__).resolve().parent))
from common import (  # noqa: E402
    CACHE_DIR,
    Edge,
    file_sha,
    get_fixture,
    verify_fixture_sha,
    write_edges,
)

HTTP_IMPORT_PATTERNS = re.compile(
    r"\b(import\s+(requests|httpx|aiohttp|urllib\.request)|"
    r"from\s+(requests|httpx|aiohttp|fastapi|flask|starlette|uvicorn|"
    r"werkzeug|bottle|tornado|sanic|quart)\b)",
    re.MULTILINE,
)

OPUS = os.environ.get("ANTHROPIC_MODEL_OPUS", "claude-opus-4-7")
SONNET = os.environ.get("ANTHROPIC_MODEL_SONNET", "claude-sonnet-4-6")

EXTRACT_PROMPT = """You are analyzing Python code for HTTP call sites.

List every HTTP_CALLS edge in this file. An HTTP_CALLS edge is an outbound HTTP request
this code makes to another service (via requests, httpx, aiohttp, urllib, or SDK wrappers).
Do NOT include inbound routes (Flask/FastAPI `@app.route` handlers — those are defined
endpoints, not outbound calls).

Return valid JSON only — no prose before or after. Schema:
{{"edges": [{{"caller_func": "<function/method qualified name in this file>",
            "method": "<HTTP method: GET|POST|PUT|PATCH|DELETE|...>",
            "url_pattern": "<URL or pattern; may contain placeholders>",
            "line": <line number>}}]}}

If no HTTP_CALLS edges exist, return: {{"edges": []}}

Rules:
- `caller_func` should be the qualified name within the module, e.g., "MyClass.fetch" or "main" or "_helper".
- `method` uppercase.
- `url_pattern` should be the literal URL or string template (e.g., "https://api.example.com/v1/users/{{id}}").
- Skip calls that go through internal helper wrappers you cannot resolve — only emit edges you are confident about.

File: {filepath}

```python
{code}
```"""

TIEBREAK_PROMPT = """You evaluated this code earlier. Another model disagrees on whether this specific edge is a real HTTP call.

File: {filepath}
Edge under review:
  caller_func: {caller_func}
  method: {method}
  url_pattern: {url_pattern}
  line: {line}

Is this a real outbound HTTP call from the code? Answer yes/no with one-sentence justification, then JSON: {{{{"is_real": true|false}}}}

Code:
```python
{code}
```"""


def discover_http_candidate_files(fixture_path: Path) -> list[Path]:
    """.py files that import an HTTP library — only these can have HTTP_CALLS."""
    candidates: list[Path] = []
    for py in fixture_path.rglob("*.py"):
        if "__pycache__" in str(py).replace("\\", "/"):
            continue
        if any(p.name.startswith(".") for p in py.parents):
            continue
        try:
            text = py.read_bytes().decode("utf-8", errors="replace")
        except OSError:
            continue
        if HTTP_IMPORT_PATTERNS.search(text):
            candidates.append(py)
    return candidates


def qn_for_file(fixture_path: Path, file_path: Path) -> str:
    """Return the code-graph-style qualified name for this file (post project-strip).

    e.g., airlock/airlock_mcp_server.py -> "airlock.airlock_mcp_server"
    """
    rel = file_path.relative_to(fixture_path)
    parts = list(rel.with_suffix("").parts)
    if parts and parts[-1] == "__init__":
        parts = parts[:-1]
    return ".".join(parts)


def extract_one(
    client: anthropic.Anthropic, model: str, file_path: Path, code: str, cache_key_path: Path
) -> list[dict]:
    """Call an LLM once on a file; cache result by (file_sha, model)."""
    if cache_key_path.exists():
        return json.loads(cache_key_path.read_text(encoding="utf-8"))["edges"]

    prompt = EXTRACT_PROMPT.format(filepath=str(file_path), code=code[:200000])
    for attempt in range(3):
        try:
            resp = client.messages.create(
                model=model,
                max_tokens=4096,
                messages=[{"role": "user", "content": prompt}],
            )
            text = "".join(
                block.text for block in resp.content if block.type == "text"
            )
            # Extract JSON from response (handle markdown fences or prose bleed).
            json_match = re.search(r"\{[\s\S]*\}", text)
            if not json_match:
                raise ValueError(f"no JSON in response: {text[:200]}")
            data = json.loads(json_match.group(0))
            edges = data.get("edges", [])
            cache_key_path.parent.mkdir(parents=True, exist_ok=True)
            cache_key_path.write_bytes(
                json.dumps({"model": model, "edges": edges}, indent=2).encode("utf-8")
            )
            return edges
        except (anthropic.RateLimitError, anthropic.APIStatusError) as e:
            wait = 30 * (2**attempt)
            print(f"    rate-limited ({e}); sleeping {wait}s")
            time.sleep(wait)
        except Exception as e:
            print(f"    {model} failed: {e}")
            return []
    return []


def tiebreak(
    client: anthropic.Anthropic, file_path: Path, code: str, edge: dict
) -> bool:
    """Third Opus call on a disputed edge. Returns True if real."""
    prompt = TIEBREAK_PROMPT.format(
        filepath=str(file_path),
        caller_func=edge.get("caller_func", ""),
        method=edge.get("method", ""),
        url_pattern=edge.get("url_pattern", ""),
        line=edge.get("line", 0),
        code=code[:200000],
    )
    try:
        resp = client.messages.create(
            model=OPUS,
            max_tokens=512,
            messages=[{"role": "user", "content": prompt}],
        )
        text = "".join(b.text for b in resp.content if b.type == "text").lower()
        m = re.search(r'"is_real"\s*:\s*(true|false)', text)
        return bool(m and m.group(1) == "true")
    except Exception as e:
        print(f"    tiebreak failed: {e}; defaulting to False")
        return False


def _normalize_url(url: str) -> str:
    """Canonicalize URL for consensus matching.

    Opus and Sonnet disagree on whether to include the protocol+host. Strip
    them so `/v1/foo` matches `https://api.example.com/v1/foo`.
    Also normalize trailing slashes and whitespace.
    """
    u = url.strip()
    # Strip protocol and host if present.
    m = re.match(r"^https?://[^/]+(/.*)$", u)
    if m:
        u = m.group(1)
    return u.rstrip("/") or "/"


def edge_key(e: dict) -> tuple[str, str, str]:
    """Match key for consensus. URL patterns normalized for protocol/host."""
    return (
        e.get("caller_func", "").strip(),
        e.get("method", "").upper().strip(),
        _normalize_url(e.get("url_pattern", "")),
    )


def build_ground_truth(fixture_id: str, force: bool = False, limit: int | None = None) -> Path:
    fixture = get_fixture(fixture_id)
    verify_fixture_sha(fixture)

    out_path = CACHE_DIR / f"ensemble-http-{fixture_id}-{fixture['short_sha']}.json"
    if out_path.exists() and not force:
        print(f"[ensemble] cache hit: {out_path}")
        return out_path

    if not os.environ.get("ANTHROPIC_API_KEY"):
        raise SystemExit("ANTHROPIC_API_KEY not set")

    client = anthropic.Anthropic()
    fixture_path = Path(fixture["path"])

    candidates = discover_http_candidate_files(fixture_path)
    print(f"[ensemble] {len(candidates)} HTTP-candidate files")
    if limit:
        candidates = candidates[:limit]
        print(f"[ensemble] limited to first {limit}")

    per_model_cache = CACHE_DIR / "llm-per-file"
    all_edges: list[Edge] = []
    t0 = time.time()

    for i, py in enumerate(candidates):
        rel = py.relative_to(fixture_path)
        code = py.read_bytes().decode("utf-8", errors="replace")
        if len(code) > 200000:
            print(f"  [{i+1}/{len(candidates)}] {rel} — SKIP (too large: {len(code)} chars)")
            continue
        fsha = file_sha(py)
        opus_cache = per_model_cache / f"{fsha}-opus.json"
        sonnet_cache = per_model_cache / f"{fsha}-sonnet.json"

        print(f"  [{i+1}/{len(candidates)}] {rel} ...")
        opus_edges = extract_one(client, OPUS, py, code, opus_cache)
        sonnet_edges = extract_one(client, SONNET, py, code, sonnet_cache)

        opus_keys = {edge_key(e): e for e in opus_edges}
        sonnet_keys = {edge_key(e): e for e in sonnet_edges}
        consensus_keys = set(opus_keys) & set(sonnet_keys)
        disagreements = (set(opus_keys) | set(sonnet_keys)) - consensus_keys

        # Accept consensus edges directly; run tiebreaker on disagreements.
        caller_prefix = qn_for_file(fixture_path, py)
        for k in consensus_keys:
            e = opus_keys[k]
            all_edges.append(_to_edge(e, caller_prefix, str(rel)))
        for k in disagreements:
            e = opus_keys.get(k) or sonnet_keys[k]
            if tiebreak(client, py, code, e):
                all_edges.append(_to_edge(e, caller_prefix, str(rel)))

        print(f"    opus={len(opus_edges)} sonnet={len(sonnet_edges)} "
              f"consensus={len(consensus_keys)} disagreed={len(disagreements)}")

    elapsed = time.time() - t0
    print(f"[ensemble] total {len(all_edges)} edges in {elapsed:.1f}s")

    # Dedup
    seen: set[tuple[str, str, str]] = set()
    deduped: list[Edge] = []
    for e in all_edges:
        if e.match_key() not in seen:
            seen.add(e.match_key())
            deduped.append(e)

    write_edges(deduped, out_path)
    print(f"[ensemble] wrote {out_path}")
    return out_path


def _to_edge(llm_edge: dict, caller_file_qn: str, rel_path: str) -> Edge:
    caller_func = (llm_edge.get("caller_func") or "").strip()
    from_qn = f"{caller_file_qn}.{caller_func}" if caller_func else caller_file_qn
    method = (llm_edge.get("method") or "").upper().strip()
    url = (llm_edge.get("url_pattern") or "").strip()
    to_qn = f"{method} {url}" if method else url
    return Edge(
        from_qn=from_qn,
        to_qn=to_qn,
        type="HTTP_CALLS",
        file=rel_path,
        line=int(llm_edge.get("line", 0) or 0),
        source="opus+sonnet",
    )


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("fixture")
    ap.add_argument("--force", action="store_true")
    ap.add_argument("--limit", type=int, help="cap file count for smoke testing")
    args = ap.parse_args()
    build_ground_truth(args.fixture, force=args.force, limit=args.limit)
    return 0


if __name__ == "__main__":
    sys.exit(main())
