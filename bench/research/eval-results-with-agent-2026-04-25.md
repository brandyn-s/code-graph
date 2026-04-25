# Loc-Bench Validation — B-2 Embeddings + LLM Agent Loop

**Date:** 2026-04-25
**Loc-Bench instance:** `pypa__pip-13085` (security vulnerability — "Lazy import allows wheel to execute code on install")
**Ground truth:** `src/pip/_internal/commands/install.py:InstallCommand.run`

This PR ships the two paths that PR #81 flagged as deferred: B-2 (semantic embedding seeds) and the LLM agent loop. Same Loc-Bench instance, three configurations measured side-by-side.

## Results

| Configuration | Ground truth in top-K? | Notes |
|---|---|---|
| Substring seeds only (PR #79 baseline) | ❌ Not in top-20 | Token "install" matched dozens of unrelated symbols; BFS amplified noise |
| Hybrid seeds (substring + Voyage embedding) | ❌ Not in top-20 | Embedding ranked Project / `__init__.py` / `Optional` higher than `InstallCommand` |
| **LLM agent loop (`code_localize_agent`)** | ✅ **#3 in top-10** | 6 turns, ~50K input tokens, finalized cleanly |

## What the agent returned

```
1. src/pip/_internal/self_outdated_check  (the lazily-loaded vulnerable module)
2. src/pip/_internal/self_outdated_check.pip_self_version_check  (the lazy-import trigger)
3. src/pip/_internal/commands/install.InstallCommand  (THE GROUND-TRUTH ENTRY POINT)
```

The agent's reasoning chain is coherent: it identified the immediately-vulnerable module (#1), the trigger function (#2), and the entry-point class containing the ground-truth method (#3). At file-level granularity, this is a Loc-Bench hit.

## Why each configuration succeeded or failed

### Substring + hybrid seeds: failed for the same root cause

The Loc-Bench issue is a 1000+ char security vulnerability description. After tokenization+stopword filtering, the surviving tokens are common code/English words (install, wheel, lazy, import, run, code, execute). These substring-match against thousands of symbols across pip's source + vendored libraries.

Embedding the full issue helps somewhat — Voyage clusters the description with auth/install code — but the cosine-top-10 doesn't include `InstallCommand` because the issue talks about `self_outdated_check`, not the install command. The class containing the fix isn't semantically central to the issue's text.

### LLM agent loop: succeeded by reasoning, not by retrieval

Transcript pattern:

- Turn 1: `rank_by_query("self_outdated_check")` — finds the vulnerable module
- Turn 2-3: refines with `code_localize` for structural context
- Turn 4: `rank_by_query("InstallCommand main entry point")` — finds the install command
- Turn 6: finalizes with three entities ordered by relevance

The agent solves the gap that pure retrieval can't: **"the issue talks about A, but the fix happens in B"**. Substring/embedding return what the issue talks about; the agent reasons about call paths and entry points.

This matches LocAgent's published architecture (ACL 2025, arXiv 2503.09089). LocAgent's 92.7% file-level Loc-Bench accuracy comes from the LLM-in-the-loop variant, not from BFS alone.

## Cost / latency

- 6 turns, 49,482 input tokens, 1,380 output tokens
- ~$0.04-0.05 per issue at Claude Haiku 4.5 prices
- ~30-60 seconds end-to-end (HTTP latency dominated)

For comparison: primitives-only `code_localize` runs in ~50ms with zero LLM cost. The agent is ~1000x slower per query but produces a usable localization where the primitives missed.

## What was shipped

### New packages

- `internal/anthropic/client.go` — minimal HTTP client for Anthropic Messages API (raw HTTP, retry, env-var auth — modeled on existing `voyage_client.go`)
- `internal/locagent/agent.go` — multi-turn agent loop with `rank_by_query` / `code_localize` / `finalize` tools exposed to the LLM
- `internal/ranking/embedding_seeds.go` — `MatchSeedNodesByEmbedding` and `MatchSeedNodesHybrid` (B-2)

### Modified packages

- `internal/ranking/pagerank.go` — added `RankByQueryWithStrategy(ctx, ..., strategy)` accepting `SeedStrategy`
- `internal/localize/agent.go` — added `CodeLocalizeWithStrategy(ctx, ..., strategy)`
- `internal/tools/rank.go` + `localize.go` + `tools.go` — `seed_strategy` arg plumbed through MCP schemas
- `internal/tools/localize_agent.go` — new `code_localize_agent` MCP tool

### Backward compatibility

`RankByQuery(st, project, query, topK)` and `CodeLocalize(st, project, issue, depth, topK)` retain their original signatures (substring-only). Existing callers see no behavior change. New callers should use `*WithStrategy` or pass `seed_strategy="hybrid"` via MCP — defaults to hybrid in the MCP tool schemas.

### What this does NOT do

- **Does NOT validate the agent on the full 560-instance Loc-Bench.** N=1 is one data point, not a benchmark. Claiming parity with LocAgent's 92.7% requires the full set (multi-hour per-issue indexing).
- **Does NOT optimize cost.** Uses Haiku 4.5 by default; larger models hit answers faster but cost more per token.
- **Does NOT cache agent results.** Re-running the same issue produces a fresh LLM call.

## Honest framing

The LLM agent loop **demonstrably bridges the substring/embedding gap on this one Loc-Bench instance**. Whether that generalizes to the full benchmark is unmeasured. A proper N=20 subset eval is the appropriate next step.

The B-2 hybrid-seeds path remains valuable for queries that DO have clean identifier match — it adds embedding-similar nodes alongside substring matches without the LLM cost. But on this issue, embedding alone wasn't enough.

**Architectural takeaway:** substring + BFS gets you token-cost reduction; the LLM-in-the-loop gets you accuracy. Both paths are now shipped and selectable via `seed_strategy` (for primitives) or by choosing `code_localize` vs `code_localize_agent`.
