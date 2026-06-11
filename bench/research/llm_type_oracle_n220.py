"""LLM type-oracle experiment 2: n≈220 across four strata vs SCIP truth.

Extends the n=40 pilot (bench/research/llm_type_oracle_pilot.py, PR #380)
per its pre-registered next-experiment design:

  corrective-suffix  60  heuristic suffix_match edges, truth = in-repo SCIP target
  corrective-unique  60  heuristic unique_name edges, truth = in-repo SCIP target
  none-heavy         50  heuristic emitted an edge; SCIP has NO same-name edge
                         from that caller → truth NONE. Heuristic is 0% by
                         construction; measures the oracle's external-call
                         false-positive behavior (the pilot had only 4/40).
  recall             50  SCIP edges with NO heuristic edge for (caller,
                         callee-name) — calls the resolver dropped. Heuristic
                         recall is 0% by construction; measures oracle recovery.

Usage:
  --prep   H_DB=<path> S_DB=<path> build dataset (no API spend)
  --run    ANTHROPIC_API_KEY required; ~220 Haiku calls (~$0.4)

Reports per-stratum accuracy with Wilson 95% intervals.
"""
from __future__ import annotations

import json
import math
import os
import random
import sqlite3
import sys
from pathlib import Path

H_DB = os.environ.get("H_DB", "/tmp/claude/cbm-h2/.cache/codebase-memory-mcp/Users-brandyn.schult-Documents-GitHub-code-graph.db")
S_DB = os.environ.get("S_DB", "/tmp/claude/cbm-s2/.cache/codebase-memory-mcp/Users-brandyn.schult-Documents-GitHub-code-graph.db")
REPO = Path(os.environ.get("ORACLE_REPO", str(Path.home() / "Documents/GitHub/code-graph")))
DATASET = Path("/tmp/claude/type_oracle_n200_dataset.json")
RESULTS = Path("/tmp/claude/type_oracle_n200_results.json")

SEED = 20260612
MAX_SNIPPET_LINES = 60
MAX_CANDIDATES = 12
STRATA = {"corrective-suffix": 60, "corrective-unique": 60, "none-heavy": 50, "recall": 50}


def rows(db, q, args=()):
    con = sqlite3.connect(db)
    con.row_factory = sqlite3.Row
    try:
        return [dict(r) for r in con.execute(q, args).fetchall()]
    finally:
        con.close()


def file_snippet(file_path: str, start: int, end: int) -> str:
    p = REPO / file_path
    try:
        lines = p.read_text(encoding="utf-8", errors="replace").splitlines()
    except OSError:
        return ""
    seg = lines[max(0, start - 1): min(len(lines), end)]
    if len(seg) > MAX_SNIPPET_LINES:
        seg = seg[:MAX_SNIPPET_LINES] + ["    ... (truncated)"]
    return "\n".join(seg)


def load_join():
    covered = {
        r["file_path"]
        for r in rows(S_DB, """
            SELECT DISTINCT n.file_path AS file_path
            FROM edges e JOIN nodes n ON e.source_id = n.id
            WHERE e.type='CALLS'
              AND json_extract(e.properties,'$.resolver_rule')='scip-ingest'
        """)
    }
    h_edges = [
        e for e in rows(H_DB, """
            SELECT c.qualified_name AS caller_qn, c.file_path AS caller_file,
                   c.start_line AS caller_start, c.end_line AS caller_end,
                   t.qualified_name AS h_target_qn, t.name AS callee_name,
                   json_extract(e.properties,'$.resolution_strategy') AS strategy
            FROM edges e
            JOIN nodes c ON e.source_id = c.id
            JOIN nodes t ON e.target_id = t.id
            WHERE e.type='CALLS'
              AND json_extract(e.properties,'$.resolution_strategy')
                  IN ('fuzzy','suffix_match','unique_name')
        """)
        if e["caller_file"] in covered
    ]
    scip = {}
    scip_callers = {}
    for r in rows(S_DB, """
        SELECT c.qualified_name AS caller_qn, c.file_path AS caller_file,
               c.start_line AS caller_start, c.end_line AS caller_end,
               t.name AS callee_name, t.qualified_name AS target_qn
        FROM edges e
        JOIN nodes c ON e.source_id = c.id
        JOIN nodes t ON e.target_id = t.id
        WHERE e.type='CALLS'
          AND json_extract(e.properties,'$.resolver_rule')='scip-ingest'
    """):
        scip.setdefault((r["caller_qn"], r["callee_name"]), set()).add(r["target_qn"])
        scip_callers[r["caller_qn"]] = (
            r["caller_file"], r["caller_start"], r["caller_end"]
        )
    return covered, h_edges, scip, scip_callers


def prep() -> None:
    covered, h_edges, scip, scip_callers = load_join()
    print(f"covered files: {len(covered)}; ambiguous heuristic edges: {len(h_edges)}; scip pairs: {len(scip)}")

    seen = set()
    corrective = {"suffix_match": [], "unique_name": []}
    none_pool = []
    for e in h_edges:
        key = (e["caller_qn"], e["callee_name"])
        if key in seen:
            continue
        seen.add(key)
        truth_set = scip.get(key, set())
        if len(truth_set) > 1:
            continue
        if truth_set:
            e["truth"] = next(iter(truth_set))
            if e["strategy"] in corrective:
                corrective[e["strategy"]].append(e)
        else:
            e["truth"] = "NONE"
            none_pool.append(e)

    h_pairs = set(seen)
    recall_pool = []
    for (caller_qn, callee_name), targets in scip.items():
        if (caller_qn, callee_name) in h_pairs or len(targets) > 1:
            continue
        loc = scip_callers.get(caller_qn)
        if not loc:
            continue
        recall_pool.append({
            "caller_qn": caller_qn, "caller_file": loc[0],
            "caller_start": loc[1], "caller_end": loc[2],
            "callee_name": callee_name, "h_target_qn": "NONE_EMITTED",
            "strategy": "recall", "truth": next(iter(targets)),
        })

    print(f"pools: suffix={len(corrective['suffix_match'])} unique={len(corrective['unique_name'])} "
          f"none={len(none_pool)} recall={len(recall_pool)}")

    random.seed(SEED)
    sample = []
    for name, pool, n in (
        ("corrective-suffix", corrective["suffix_match"], STRATA["corrective-suffix"]),
        ("corrective-unique", corrective["unique_name"], STRATA["corrective-unique"]),
        ("none-heavy", none_pool, STRATA["none-heavy"]),
        ("recall", recall_pool, STRATA["recall"]),
    ):
        random.shuffle(pool)
        take = pool[:n]
        for s in take:
            s["stratum"] = name
        print(f"  {name}: sampled {len(take)}")
        sample.extend(take)

    for s in sample:
        s["caller_snippet"] = file_snippet(s["caller_file"], s["caller_start"], s["caller_end"])
        cands = rows(H_DB, """
            SELECT qualified_name, file_path, start_line, end_line
            FROM nodes WHERE name = ? AND label IN ('Function','Method')
            ORDER BY qualified_name
        """, (s["callee_name"],))[:MAX_CANDIDATES]
        for c in cands:
            c["signature"] = file_snippet(c["file_path"], c["start_line"],
                                          min(c["end_line"], c["start_line"] + 2))
        s["candidates"] = cands

    DATASET.write_text(json.dumps(sample, indent=1), encoding="utf-8")
    print(f"dataset: {len(sample)} sites -> {DATASET}")


PROMPT = """You are resolving a static call graph edge in a Go repository.

The function below contains a call to `__CALLEE__`. Decide which of the
candidate definitions is the one actually invoked, using receiver types,
package context, and imports visible in the source.

CALLER (__CALLER_QN__, file __CALLER_FILE__):
```go
__SNIPPET__
```

CANDIDATE DEFINITIONS for `__CALLEE__`:
__CANDIDATES__

If the call's real target is NOT among the candidates (e.g. it's a standard
library or external dependency function, or a method on an external type),
answer NONE.

Reply with ONLY a JSON object: {"answer": "<qualified_name or NONE>"}"""


def wilson(p_hat: float, n: int, z: float = 1.96) -> tuple[float, float]:
    if n == 0:
        return (0.0, 1.0)
    denom = 1 + z * z / n
    center = (p_hat + z * z / (2 * n)) / denom
    half = z * math.sqrt(p_hat * (1 - p_hat) / n + z * z / (4 * n * n)) / denom
    return (max(0.0, center - half), min(1.0, center + half))


def run() -> None:
    import re

    import anthropic

    sample = json.loads(DATASET.read_text(encoding="utf-8"))
    client = anthropic.Anthropic()
    model = os.environ.get("ANTHROPIC_MODEL", "claude-haiku-4-5-20251001")

    results = []
    for i, s in enumerate(sample):
        cand_text = "\n".join(
            f"{j+1}. {c['qualified_name']}  ({c['file_path']}:{c['start_line']})\n"
            f"   ```go\n   {c['signature'].splitlines()[0] if c['signature'] else '?'}\n   ```"
            for j, c in enumerate(s["candidates"])
        ) or "(no in-repo candidates found)"
        prompt = (PROMPT
                  .replace("__CALLEE__", s["callee_name"])
                  .replace("__CALLER_QN__", s["caller_qn"])
                  .replace("__CALLER_FILE__", s["caller_file"])
                  .replace("__SNIPPET__", s["caller_snippet"])
                  .replace("__CANDIDATES__", cand_text))
        try:
            msg = client.messages.create(
                model=model, max_tokens=1500, temperature=0,
                messages=[{"role": "user", "content": prompt}])
            text = msg.content[0].text.strip()
            m = re.search(r'\{\s*"answer"\s*:\s*"([^"]*)"\s*\}', text)
            answer = m.group(1) if m else "PARSE_ERROR"
            usage = {"in": msg.usage.input_tokens, "out": msg.usage.output_tokens}
        except Exception as exc:  # noqa: BLE001
            answer, usage = f"ERROR: {exc}", {}
        results.append({
            "stratum": s["stratum"], "caller_qn": s["caller_qn"],
            "callee_name": s["callee_name"], "strategy": s["strategy"],
            "truth": s["truth"], "heuristic": s["h_target_qn"],
            "llm": answer, "usage": usage,
        })
        if (i + 1) % 20 == 0 or i + 1 == len(sample):
            print(f"[{i+1}/{len(sample)}]", flush=True)

    RESULTS.write_text(json.dumps(results, indent=1), encoding="utf-8")

    print("\n=== ACCURACY vs SCIP truth (Wilson 95%) ===")
    print(f"{'stratum':18s} {'n':>3s} {'heuristic':>16s} {'llm':>22s}")
    for name in STRATA:
        rs = [r for r in results if r["stratum"] == name]
        n = len(rs)
        if not n:
            continue
        h = sum(1 for r in rs if r["heuristic"] == r["truth"])
        l = sum(1 for r in rs if r["llm"] == r["truth"])
        lo, hi = wilson(l / n, n)
        print(f"{name:18s} {n:3d} {h:6d} ({h/n:4.0%})     "
              f"{l:4d} ({l/n:4.0%}) [{lo:.0%},{hi:.0%}]")
    n = len(results)
    h = sum(1 for r in results if r["heuristic"] == r["truth"])
    l = sum(1 for r in results if r["llm"] == r["truth"])
    print(f"{'TOTAL':18s} {n:3d} {h:6d} ({h/n:4.0%})     {l:4d} ({l/n:4.0%})")
    tok_in = sum(r["usage"].get("in", 0) for r in results)
    tok_out = sum(r["usage"].get("out", 0) for r in results)
    print(f"tokens: {tok_in} in / {tok_out} out "
          f"(~${tok_in/1e6*1.0 + tok_out/1e6*5.0:.2f} at Haiku pricing)")
    errors = sum(1 for r in results if str(r["llm"]).startswith(("ERROR", "PARSE_ERROR")))
    print(f"errors/parse failures: {errors}")


if __name__ == "__main__":
    {"--prep": prep, "--run": run}[sys.argv[1]]()
