"""
Phase 0b: transcript replay corpus extractor.

Walks ~/.claude/projects/ for session transcripts (.jsonl), filters to
sessions that invoked code-search or code-graph MCP tools within the last
N days (default 14), and emits one JSONL record per qualifying session
capturing the tool-call sequence, token totals, and the headline
"pre-graph-grep" metric used by PR 3 (PreToolUse hook effectiveness).

Usage
-----
    # default: last 14 days, write to bench/transcripts_YYYY-MM-DD.jsonl
    python bench/transcripts.py

    # custom window + output
    python bench/transcripts.py --days 30 --output bench/scan.jsonl

    # scan only, don't write
    python bench/transcripts.py --count-only

    # print summary stats after writing
    python bench/transcripts.py --stats

    # sample N random qualifying sessions for hand-labeling
    python bench/transcripts.py --sample 30 --output bench/sample.jsonl

Output schema (one JSON object per line)
----------------------------------------
    {
      "session_id":     "<uuid>",
      "transcript":     "<absolute path>",
      "is_subagent":    bool,
      "started_at":     "<iso>",
      "ended_at":       "<iso>",
      "duration_s":     <int>,
      "cwd":            "<first observed>",
      "version":        "<first observed>",
      "tool_call_count": <int>,
      "tool_counts":    {"Glob": 3, "mcp__code-graph__query_graph": 5, ...},
      "tool_sequence":  ["Glob", "Read", "mcp__code-graph__query_graph", ...],  (truncated to first 50)
      "first_5_tools":  ["Glob", "Glob", "Read", "Bash", "mcp__code-graph__query_graph"],
      "uses_code_search": bool,
      "uses_code_graph":  bool,
      "pre_graph_grep":   bool,        # True if any Glob/Grep/Read fired BEFORE the first code-graph|code-search call
      "pre_graph_grep_n": <int>,       # how many such calls before the first graph query
      "tokens": {
        "input":              <int>,
        "output":             <int>,
        "cache_creation":     <int>,
        "cache_read":         <int>
      },
      "prompt_excerpts":  [<first ~3 user prompts, up to 200 chars each>]
    }

Invariants
----------
- All file reads use encoding='utf-8', errors='replace' (Windows cp1252 default
  would corrupt Unicode in transcripts — per platform-constraints.md).
- Malformed JSON lines are skipped, not fatal — transcripts sometimes have
  truncated last lines from crashed sessions.
- Subagent transcripts (under a session's subagents/ subdir) are emitted as
  their own records with is_subagent=True, so A/B testers can choose to
  filter them out or analyze separately.
"""
from __future__ import annotations

import argparse
import json
import os
import random
import sys
from collections import Counter
from datetime import datetime, timedelta, timezone
from pathlib import Path


BENCH_DIR = Path(__file__).resolve().parent

# Where Claude Code stores transcripts by default.
DEFAULT_PROJECTS_ROOT = Path.home() / ".claude" / "projects"

# Which tool names indicate this session used the two MCP servers we care about.
CODE_SEARCH_TOOL_PREFIXES = ("mcp__code-search__",)
CODE_GRAPH_TOOL_PREFIXES = ("mcp__code-graph__",)

# File-exploration tools that, if fired before any graph query, indicate the
# agent grepped/globbed without consulting the graph first — the behavior that
# PR 3's PreToolUse hook aims to reduce.
FILE_EXPLORE_TOOLS = {"Glob", "Grep", "Read"}

# Sessions with fewer tool calls than this are uninteresting for A/B replay.
MIN_TOOL_CALLS = 10


def is_subagent_transcript(path: Path) -> bool:
    return "subagents" in path.parts


def extract_session(path: Path) -> dict | None:
    """Parse one .jsonl transcript into a summary record, or None if it
    does not use code-search/code-graph or has fewer than MIN_TOOL_CALLS."""
    tool_counts: Counter[str] = Counter()
    tool_sequence: list[str] = []
    token_totals = {"input": 0, "output": 0, "cache_creation": 0, "cache_read": 0}
    first_ts = None
    last_ts = None
    cwd = None
    version = None
    session_id = None
    prompts: list[str] = []
    first_graph_idx: int | None = None
    pre_graph_grep_n = 0

    try:
        with path.open(encoding="utf-8", errors="replace") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    ev = json.loads(line)
                except json.JSONDecodeError:
                    continue

                ts = ev.get("timestamp")
                if ts:
                    first_ts = first_ts or ts
                    last_ts = ts

                session_id = session_id or ev.get("sessionId")
                cwd = cwd or ev.get("cwd")
                version = version or ev.get("version")

                msg = ev.get("message")
                if not isinstance(msg, dict):
                    continue

                role = msg.get("role")
                content = msg.get("content")

                # Capture first few user prompts (short form) — helps later
                # A/B raters decide what the session was trying to do.
                if role == "user" and len(prompts) < 3:
                    text = None
                    if isinstance(content, str):
                        text = content
                    elif isinstance(content, list):
                        for c in content:
                            if isinstance(c, dict) and c.get("type") == "text":
                                text = c.get("text")
                                break
                    if text:
                        prompts.append(text[:200])

                # Tool uses live in assistant messages as content list items.
                if role == "assistant" and isinstance(content, list):
                    for c in content:
                        if isinstance(c, dict) and c.get("type") == "tool_use":
                            name = c.get("name")
                            if not name:
                                continue
                            tool_counts[name] += 1
                            tool_sequence.append(name)
                            if first_graph_idx is None and (
                                name.startswith(CODE_GRAPH_TOOL_PREFIXES)
                                or name.startswith(CODE_SEARCH_TOOL_PREFIXES)
                            ):
                                first_graph_idx = len(tool_sequence) - 1
                                # Count file-explore tools that preceded it.
                                pre_graph_grep_n = sum(
                                    1 for t in tool_sequence[:-1] if t in FILE_EXPLORE_TOOLS
                                )

                    # Aggregate token usage from assistant messages. Each
                    # assistant turn has one `usage` block.
                    usage = msg.get("usage")
                    if isinstance(usage, dict):
                        token_totals["input"] += int(usage.get("input_tokens") or 0)
                        token_totals["output"] += int(usage.get("output_tokens") or 0)
                        token_totals["cache_creation"] += int(
                            usage.get("cache_creation_input_tokens") or 0
                        )
                        token_totals["cache_read"] += int(
                            usage.get("cache_read_input_tokens") or 0
                        )
    except OSError:
        return None

    if not tool_sequence:
        return None

    uses_cs = any(t.startswith(CODE_SEARCH_TOOL_PREFIXES) for t in tool_sequence)
    uses_cg = any(t.startswith(CODE_GRAPH_TOOL_PREFIXES) for t in tool_sequence)
    if not (uses_cs or uses_cg):
        return None

    if len(tool_sequence) < MIN_TOOL_CALLS:
        return None

    duration_s: int | None = None
    if first_ts and last_ts:
        try:
            start = datetime.fromisoformat(first_ts.replace("Z", "+00:00"))
            end = datetime.fromisoformat(last_ts.replace("Z", "+00:00"))
            duration_s = int((end - start).total_seconds())
        except ValueError:
            pass

    return {
        "session_id": session_id or path.stem,
        "transcript": str(path),
        "is_subagent": is_subagent_transcript(path),
        "started_at": first_ts,
        "ended_at": last_ts,
        "duration_s": duration_s,
        "cwd": cwd,
        "version": version,
        "tool_call_count": len(tool_sequence),
        "tool_counts": dict(tool_counts),
        "tool_sequence": tool_sequence[:50],
        "first_5_tools": tool_sequence[:5],
        "uses_code_search": uses_cs,
        "uses_code_graph": uses_cg,
        "pre_graph_grep": bool(first_graph_idx is not None and pre_graph_grep_n > 0),
        "pre_graph_grep_n": pre_graph_grep_n,
        "first_graph_call_index": first_graph_idx,
        "tokens": token_totals,
        "prompt_excerpts": prompts,
    }


def walk_transcripts(root: Path, days: int) -> list[Path]:
    """Yield .jsonl transcripts under root with mtime within the window."""
    cutoff = datetime.now(timezone.utc) - timedelta(days=days)
    cutoff_epoch = cutoff.timestamp()
    results: list[Path] = []
    for dirpath, _, filenames in os.walk(root):
        for fn in filenames:
            if not fn.endswith(".jsonl"):
                continue
            p = Path(dirpath) / fn
            try:
                if p.stat().st_mtime >= cutoff_epoch:
                    results.append(p)
            except OSError:
                continue
    return results


def summarize(records: list[dict]) -> str:
    """Human-readable rollup of the corpus."""
    n = len(records)
    if n == 0:
        return "no qualifying sessions"
    subs = sum(1 for r in records if r["is_subagent"])
    cs_only = sum(1 for r in records if r["uses_code_search"] and not r["uses_code_graph"])
    cg_only = sum(1 for r in records if r["uses_code_graph"] and not r["uses_code_search"])
    both = sum(1 for r in records if r["uses_code_search"] and r["uses_code_graph"])
    pregrep = sum(1 for r in records if r["pre_graph_grep"])
    pregrep_pct = pregrep / n * 100

    tool_calls = [r["tool_call_count"] for r in records]
    tool_calls.sort()
    median = tool_calls[len(tool_calls) // 2]

    total_tokens = sum(
        r["tokens"]["input"] + r["tokens"]["output"] + r["tokens"]["cache_creation"] + r["tokens"]["cache_read"]
        for r in records
    )

    lines = [
        f"qualifying sessions:     {n}  ({subs} subagent, {n - subs} main)",
        f"mcp usage mix:           code-search only={cs_only}  code-graph only={cg_only}  both={both}",
        f"median tool calls:       {median}",
        f"total tool calls:        {sum(tool_calls)}",
        f"pre-graph grep sessions: {pregrep}  ({pregrep_pct:.1f}%)   <-- PR 3 baseline metric",
        f"total tokens in corpus:  {total_tokens:,}",
    ]
    return "\n".join(lines)


def main() -> int:
    ap = argparse.ArgumentParser(description="Phase 0b transcript replay corpus")
    ap.add_argument("--root", default=str(DEFAULT_PROJECTS_ROOT),
                    help="root directory to walk (default: ~/.claude/projects)")
    ap.add_argument("--days", type=int, default=14,
                    help="only include transcripts modified within N days (default: 14)")
    today = datetime.now(timezone.utc).strftime("%Y-%m-%d")
    ap.add_argument("--output", default=str(BENCH_DIR / f"transcripts_{today}.jsonl"),
                    help="output JSONL path (default: bench/transcripts_YYYY-MM-DD.jsonl)")
    ap.add_argument("--count-only", action="store_true",
                    help="scan and summarize but don't write output")
    ap.add_argument("--stats", action="store_true",
                    help="print summary after writing")
    ap.add_argument("--sample", type=int,
                    help="randomly sample N qualifying sessions instead of writing all")
    args = ap.parse_args()

    root = Path(os.path.expanduser(args.root)).resolve()
    if not root.exists():
        sys.exit(f"root does not exist: {root}")

    transcripts = walk_transcripts(root, args.days)
    print(f"found {len(transcripts)} .jsonl files within {args.days} days of today under {root}",
          file=sys.stderr)

    records: list[dict] = []
    for i, p in enumerate(transcripts):
        if (i + 1) % 100 == 0:
            print(f"  ... scanned {i + 1}/{len(transcripts)}", file=sys.stderr)
        rec = extract_session(p)
        if rec is not None:
            records.append(rec)

    print(file=sys.stderr)
    print(summarize(records), file=sys.stderr)

    if args.count_only:
        return 0

    if args.sample and args.sample < len(records):
        random.seed(42)
        records = random.sample(records, args.sample)
        print(f"\nsampled {len(records)} sessions (seed=42)", file=sys.stderr)

    out_path = Path(args.output).resolve()
    out_path.parent.mkdir(parents=True, exist_ok=True)
    with out_path.open("w", encoding="utf-8") as f:
        for r in records:
            f.write(json.dumps(r, default=str) + "\n")
    print(f"\nwrote {out_path}  ({len(records)} records)", file=sys.stderr)

    if args.stats:
        print(file=sys.stderr)
        print(summarize(records), file=sys.stderr)

    return 0


if __name__ == "__main__":
    sys.exit(main())
