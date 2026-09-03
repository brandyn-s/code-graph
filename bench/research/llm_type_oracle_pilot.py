"""LLM type-oracle pilot on code-graph's ambiguous-dispatch buckets.

Question: on call sites the heuristic resolver handles with low-precision
strategies (fuzzy / suffix_match / unique_name), can an LLM given the call
site source + candidate definitions pick the correct callee?

Ground truth: SCIP compiler-grade edges (scip-go) for the same call sites.

Phases:
  --prep  join heuristic DB x SCIP DB, sample stratified call sites,
          write dataset to /tmp/claude/type_oracle_dataset.json
  --run   send each site to Haiku, score LLM vs heuristic vs SCIP truth,
          write /tmp/claude/type_oracle_results.json + print summary

Join key is (caller_qn, callee_short_name). Sites where SCIP reports
multiple distinct same-named targets from one caller are skipped (join
ambiguity). Truth = the SCIP target QN, or NONE when the caller's file is
SCIP-covered but SCIP has no same-named in-repo edge from that caller
(external callee or heuristic false positive).
"""
from __future__ import annotations

import json
import random
import sqlite3
import sys
from pathlib import Path

H_DB = "/tmp/claude/cbm-heuristic/.cache/code-graph/Users-brandyn.schult-Documents-GitHub-code-graph.db"
S_DB = str(Path.home() / ".cache/code-graph/Users-brandyn.schult-Documents-GitHub-code-graph.db")
REPO = Path.home() / "Documents/GitHub/code-graph"
DATASET = Path("/tmp/claude/type_oracle_dataset.json")
RESULTS = Path("/tmp/claude/type_oracle_results.json")

BUCKETS = ["fuzzy", "suffix_match", "unique_name"]
PER_BUCKET = 20
SEED = 20260611
MAX_SNIPPET_LINES = 60
MAX_CANDIDATES = 12


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


def prep() -> None:
    covered = {
        r["file_path"]
        for r in rows(S_DB, """
            SELECT DISTINCT n.file_path AS file_path
            FROM edges e JOIN nodes n ON e.source_id = n.id
            WHERE e.type='CALLS'
              AND json_extract(e.properties,'$.resolver_rule')='scip-ingest'
        """)
    }
    print(f"SCIP-covered files: {len(covered)}")

    h_edges = rows(H_DB, """
        SELECT c.qualified_name AS caller_qn, c.file_path AS caller_file,
               c.start_line AS caller_start, c.end_line AS caller_end,
               t.qualified_name AS h_target_qn, t.name AS callee_name,
               json_extract(e.properties,'$.resolution_strategy') AS strategy
        FROM edges e
        JOIN nodes c ON e.source_id = c.id
        JOIN nodes t ON e.target_id = t.id
        WHERE e.type='CALLS'
          AND json_extract(e.properties,'$.resolution_strategy') IN ('fuzzy','suffix_match','unique_name')
    """)
    h_edges = [e for e in h_edges if e["caller_file"] in covered]
    print(f"ambiguous-bucket heuristic edges in covered files: {len(h_edges)}")

    # SCIP truth: caller_qn -> callee_name -> set of target QNs
    scip = {}
    for r in rows(S_DB, """
        SELECT c.qualified_name AS caller_qn, t.name AS callee_name,
               t.qualified_name AS target_qn
        FROM edges e
        JOIN nodes c ON e.source_id = c.id
        JOIN nodes t ON e.target_id = t.id
        WHERE e.type='CALLS'
          AND json_extract(e.properties,'$.resolver_rule')='scip-ingest'
    """):
        scip.setdefault((r["caller_qn"], r["callee_name"]), set()).add(r["target_qn"])

    # Candidate pool per callee name (from heuristic DB = all in-repo defs)
    sites, skipped_multi = [], 0
    seen = set()
    for e in h_edges:
        key = (e["caller_qn"], e["callee_name"])
        if key in seen:
            continue
        seen.add(key)
        truth_set = scip.get(key, set())
        if len(truth_set) > 1:
            skipped_multi += 1
            continue
        truth = next(iter(truth_set)) if truth_set else "NONE"
        sites.append({**e, "truth": truth})

    print(f"joinable sites: {len(sites)} (skipped multi-target joins: {skipped_multi})")

    random.seed(SEED)
    sample = []
    for b in BUCKETS:
        pool = [s for s in sites if s["strategy"] == b]
        random.shuffle(pool)
        take = pool[:PER_BUCKET]
        print(f"  {b}: pool={len(pool)} sampled={len(take)}")
        sample.extend(take)

    # Attach snippets + candidates
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
    none_truths = sum(1 for s in sample if s["truth"] == "NONE")
    print(f"dataset written: {len(sample)} sites ({none_truths} with truth=NONE) -> {DATASET}")


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


def run() -> None:
    import os
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
            # The reply may wrap the JSON in prose or code fences that
            # themselves contain Go braces — match the answer object
            # directly instead of the first/last brace span.
            import re
            m = re.search(r'\{\s*"answer"\s*:\s*"([^"]*)"\s*\}', text)
            answer = m.group(1) if m else "PARSE_ERROR"
            if answer == "PARSE_ERROR":
                print(f"  RAW RESPONSE: {text[:300]!r}", flush=True)
            usage = {"in": msg.usage.input_tokens, "out": msg.usage.output_tokens}
        except Exception as exc:  # noqa: BLE001 — record and continue
            answer, usage = f"ERROR: {exc}", {}
        results.append({
            "caller_qn": s["caller_qn"], "callee_name": s["callee_name"],
            "strategy": s["strategy"], "truth": s["truth"],
            "heuristic": s["h_target_qn"], "llm": answer, "usage": usage,
        })
        print(f"[{i+1}/{len(sample)}] {s['strategy']:12s} {s['callee_name']:30s} "
              f"truth={'NONE' if s['truth'] == 'NONE' else 'in-repo'} llm={'NONE' if answer == 'NONE' else 'pick'}",
              flush=True)

    RESULTS.write_text(json.dumps(results, indent=1), encoding="utf-8")

    def acc(rows_):
        ok_h = sum(1 for r in rows_ if r["heuristic"] == r["truth"])
        ok_l = sum(1 for r in rows_ if r["llm"] == r["truth"])
        n = len(rows_)
        return n, ok_h, ok_l

    print("\n=== ACCURACY vs SCIP truth ===")
    print(f"{'bucket':14s} {'n':>3s} {'heuristic':>10s} {'llm':>10s}")
    for b in BUCKETS:
        n, h, l = acc([r for r in results if r["strategy"] == b])
        if n:
            print(f"{b:14s} {n:3d} {h:6d} ({h/n:4.0%}) {l:6d} ({l/n:4.0%})")
    n, h, l = acc(results)
    print(f"{'TOTAL':14s} {n:3d} {h:6d} ({h/n:4.0%}) {l:6d} ({l/n:4.0%})")
    tok_in = sum(r["usage"].get("in", 0) for r in results)
    tok_out = sum(r["usage"].get("out", 0) for r in results)
    print(f"tokens: {tok_in} in / {tok_out} out "
          f"(~${tok_in/1e6*1.0 + tok_out/1e6*5.0:.3f} at Haiku pricing)")


if __name__ == "__main__":
    {"--prep": prep, "--run": run}[sys.argv[1]]()
