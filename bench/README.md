# bench/ — baseline harness for feature PR validation

Repo-level benchmark harness that captures measured pre/post metrics for incoming feature PRs (graphify-inspired improvements: confidence tags, orientation report, rationale extraction, graph diff, similarity edges).

**Scope**: this directory is *not* the language coverage benchmark in [BENCHMARK.md](../BENCHMARK.md). That file tests language parity across 63 languages. This directory tests repo-level metrics against 4 pinned redacted fixtures, and is designed to be re-run after each feature PR with [compare.py](compare.py) diffing before/after.

## Quick start

```bash
# Rebuild the binary first (required — deployed binary may lag)
cd ~/Documents/GitHub/code-graph
CGO_ENABLED=1 go build -o bin/codebase-memory-mcp.exe ./cmd/codebase-memory-mcp/

# One-time fixture setup: create the main-pinned worktree for the self-hosting fixture
git worktree add "$HOME/worktrees/code-graph-main" main
./bin/codebase-memory-mcp.exe cli --raw index_repository \
  '{"repo_path":"'$HOME'/worktrees/code-graph-main"}'

# Full baseline across all 4 fixtures
python bench/harness.py --output bench/baseline_$(date +%F).json \
  --binary ./bin/codebase-memory-mcp.exe

# Skip re-index (use whatever's cached in ~/.cache/codebase-memory-mcp/)
python bench/harness.py --output bench/baseline_$(date +%F).json \
  --binary ./bin/codebase-memory-mcp.exe --skip-index

# Smoke test (Q1+Q2 only on one repo, fast feedback)
python bench/harness.py --output bench/smoke.json --smoke --repo mcp-servers \
  --binary ./bin/codebase-memory-mcp.exe --skip-index

# Diff two baselines (before vs after a feature PR)
python bench/compare.py bench/baseline_2026-04-22.json bench/after_pr1.json
```

### Opt-in: Voyage embeddings during indexing

By default the harness sets `VOYAGE_API_KEY=""` during indexing to skip the
embeddings pass. This avoids a known indefinite stall in the embeddings HTTP
loop (see *Known issues* below). Semantic-search (`search_code_semantic`)
will return empty until a fresh index with embeddings runs.

```bash
# Opt in — only when semantic_search data is needed (still subject to stall risk)
python bench/harness.py --output bench/baseline.json \
  --binary ./bin/codebase-memory-mcp.exe --with-embeddings
```

## Files

| File | Purpose |
|------|---------|
| `PLAN.md` | Full design: why this exists, the 20-question suite, stop-ship criteria |
| `fixtures.json` | Local, not committed: pinned benchmark repos (paths + SHAs). Create it from the schema described below before running the harness |
| `questions.json` | 20-question standard suite (Q1-Q12 baseline, Q13-Q20 feature probes) |
| `harness.py` | Runs the suite against a binary, emits JSON |
| `compare.py` | Diff two JSON baselines |
| `transcripts.py` | **Phase 0b** — scans `~/.claude/projects/` for Claude Code sessions that used `mcp__code-search__*` or `mcp__code-graph__*` tools, writes one JSONL record per session with tool-call sequence and token totals. Used as the A/B population for PR 3 (PreToolUse hook effectiveness) and PR 4/7 agent-answer quality. |
| `baseline_YYYY-MM-DD.json`, `transcripts_YYYY-MM-DD.jsonl` | Captured outputs (gitignored; regenerate on demand) |

## What the harness captures per question

- Wall-clock latency (ms)
- Grade: `PASS` / `PARTIAL` / `FAIL` / `N/A` / `ERROR`
- Result hash (SHA256 prefix) — detects silent behavior changes between runs
- Short preview of the raw result

## Grading notes

- `N/A` is used when:
  - A feature PR hasn't merged yet (for Q13-Q20)
  - The installed binary doesn't contain a tool the source does (binary-lag case)
  - The question doesn't apply to the repo
- `ERROR` is reserved for unexpected tool errors on tools that should work
- `PASS` requires the question's `expect_contains` / `expect_min_results` / `expect_non_empty` predicate to hold

## Known issues + workarounds

### 1. Embeddings-pass indefinite stall (`VOYAGE_API_KEY` set)

**Symptom**: on larger repos (~4k+ nodes), the indexer logs `phase=embeddings pct=97 detail="generating embeddings"` and produces no further output for many minutes. The process is alive but no `pass.embeddings.progress` log fires. Observed 2026-04-22 on rmf-corsair at 8+ minutes.

**Root cause** (not confirmed — deferred to a separate investigation):
- Not DNS/IPv6 — reproduced with `GODEBUG=netdns=go+v4`.
- The pass has no overall timeout; only a per-request 120s `http.Client.Timeout` in `voyage_client.go`. If requests return slowly but successfully, the pass can run for an unbounded time on a large batch queue with no user-visible progress.

**Harness workaround**: default `VOYAGE_API_KEY=""` during `index_repository` calls (`Harness.with_embeddings=False` by default). Semantic-search index is not populated; everything else indexes fine.

**Opt-in**: `--with-embeddings` flag. Still subject to stall risk.

**Proper fix** (not in this PR): add a bounded `context.WithTimeout` around the embeddings pass in `pass_embeddings.go`; emit progress log per inner batch (currently only per outer batch of 128); expose a `CODE_GRAPH_SKIP_EMBEDDINGS` env var for unambiguous opt-out.

### 2. Binary version lag

The harness runs against whatever `--binary` path points to. If you pass `~/bin/codebase-memory-mcp.exe`, it may be stale. **Always build fresh from the current `code-graph` source** before capturing a baseline (see Quick Start above). Stale binary produces false `N/A`s for tools that exist in source (`explain_symbol`, `get_review_context`, etc.) and polluted comparisons.

### 3. Git worktree path mangling on Windows Git Bash

`git worktree add ~/tmp/foo main` produces a worktree at `C:/c/Users/user/tmp/foo` (the `~` expansion confuses git's path handling on Windows). **Use explicit Windows-style absolute paths**: `git worktree add "$HOME/worktrees/code-graph-main" main` (the `$HOME` env var expands correctly because it's shell-expanded before git sees it).

### 4. Fixture paths are portable

`fixtures.json` uses `$HOME`-prefixed paths (`$HOME/Documents/GitHub/...`). The harness expands both `~` and `$VAR` via `os.path.expandvars(os.path.expanduser(...))` so the same fixture file works on any machine with the expected directory layout. Don't hard-code absolute paths.

### 5. Stale codebase-memory-mcp processes after a stall kill

If a stalled index is killed via `taskkill /F`, orphan processes can linger. Clean with:

```bash
MSYS_NO_PATHCONV=1 taskkill /F /IM codebase-memory-mcp.exe
```

Then `delete_project` to clear any 0-node stub left in the registry:

```bash
./bin/codebase-memory-mcp.exe cli --raw delete_project \
  '{"project_name":"c-Users-user-Documents-GitHub-<repo>"}'
```

The harness itself has a stall watchdog (`--index-timeout`, default 600s) that surfaces a clean error instead of orphaning the subprocess.

## Fixture repo SHAs

Captured in `fixtures.json`. A baseline run prints a WARN if any fixture's HEAD SHA has drifted from the pinned value, and a WARN if any fixture has uncommitted changes. Baselines from drifted/dirty repos are still written but flagged — do not use them as reference points for PR diffing.

## Question ID coverage matrix

| ID | Tool | Baseline | PR 1 | PR 2 | PR 3 | PR 4 | PR 5 | PR 7 |
|----|------|:-:|:-:|:-:|:-:|:-:|:-:|:-:|
| Q1-Q12 | Core existing tools | ✅ | - | - | - | - | - | - |
| Q13 | `query_graph` w/ confidence filter | - | ✅ | - | - | - | - | - |
| Q14 | `get_architecture` w/ communities+cohesion | - | - | ✅ | - | - | - | - |
| Q15 | `ARCHITECTURE_REPORT.md` file check | - | - | - | ✅ | - | - | - |
| Q16 | `find_rationale` | - | - | - | - | ✅ | - | - |
| Q17 | `diff_graph` | - | - | - | - | - | ✅ | - |
| Q18 | `find_similar_functions` | - | - | - | - | - | - | ✅ |
| Q19 | `explain_symbol` w/ rationale | - | - | - | - | ✅ | - | - |
| Q20 | `get_review_context` w/ diff | - | - | - | - | - | ✅ | - |

## Baseline findings (2026-04-22, freshly-built binary from main + feat/phase0)

**Clean baselines captured:**
- `mcp-servers` @ 81fa7d5 (2,920 nodes / 7,318 edges): **15 PASS / 5 N/A** (N/As are the 5 feature-PR probes)
- `mcp-infra` @ 8173017 (668 nodes / 1,096 edges): **15 PASS / 5 N/A**

**Pending baselines** (needs `index_repository` first — harness was run with `--skip-index`):
- `rmf-corsair` @ f545976: not yet indexed
- `code-graph` @ c9b1195: on this feature branch (dirty) — baseline should be captured from `main`

### Findings that reshape the feature PR plan

1. **PR 2 scope is substantially smaller than planned.** Current `get_architecture` already returns `clusters` with per-cluster `cohesion` scores. Sample from mcp-servers: `{id:1, label:"msgraph_mcp", members:94, cohesion:0.96875}`. 157 Community nodes exist in the graph. Remaining PR 2 work: verify the oversized-community split threshold (>25% of graph) actually fires. May drop from M effort to XS, or be removed entirely if split logic is already present.

2. **Tool surface has shifted since plan was written.**
   - `list_directory` removed — replaced by `search_graph(label="File")` in Q12.
   - Schema key `edge_types` renamed to `relationship_types` in `get_graph_schema` output.
   - `get_architecture` output shape changed — no top-level `node_labels`; see `clusters`, `boundaries`, `hotspots`, `packages` keys instead.
   - Cypher parser rejects `<>` (not-equal) — Q10 adjusted to not use it.

3. **Q19 and Q20 already pass on current source.** `explain_symbol` and `get_review_context` exist. PR 4 and PR 5's scope is additive enhancement to these existing tools (rationale extraction; graph-diff inclusion) rather than building from scratch.

## Phase 0b — transcript replay corpus

Reads Claude Code session transcripts from `~/.claude/projects/` and
summarizes every session that invoked `mcp__code-search__*` or
`mcp__code-graph__*` tools. One JSONL record per qualifying session.

### Usage

```bash
# default: last 14 days, write bench/transcripts_YYYY-MM-DD.jsonl, print stats
python bench/transcripts.py --stats

# dry-run — count without writing
python bench/transcripts.py --count-only

# custom window + output
python bench/transcripts.py --days 30 --output bench/scan.jsonl

# sample N random sessions (seed=42) for hand-labeling outcomes
python bench/transcripts.py --sample 30 --output bench/sample_hand-label.jsonl
```

### Baseline captured 2026-04-22 (14-day window, 299 transcripts scanned)

| Metric | Value |
|---|---:|
| Qualifying sessions (≥10 tool calls + uses code-search or code-graph) | **30** (25 main + 5 subagent) |
| MCP usage mix | 19 use both, 9 code-search only, 2 code-graph only |
| Median tool calls per session | 295 |
| Total tool calls in corpus | 12,171 |
| **Pre-graph grep rate** | **23/30 = 76.7%** |
| Total tokens in corpus | 7.85 billion (input + output + cache) |

### What `pre_graph_grep` measures

For each session we compute `first_graph_call_index` = the position of the
first `mcp__code-search__*` or `mcp__code-graph__*` tool call. If *any*
`Glob`/`Grep`/`Read` call precedes that index, the session counts as
"pre-graph grep" (the behavior PR 3's PreToolUse hook aims to reduce). On
the current main-branch baseline, **76.7% of sessions** exhibit this.

### A/B usage (post-PR-3)

After PR 3 ships, re-run `transcripts.py` over the next 14-day window and
compare `pre_graph_grep` rate. Target delta: ≥40 pp reduction (baseline
76.7% → target ≤ 37%). Stop-ship criterion for PR 3 is met only if the
delta is observable across a meaningfully-sized sample (≥30 sessions).

## Measuring PR 3 (PreToolUse orientation hook) — A/B protocol

Script: `bench/compare_pregrep.py` — compares the `pre_graph_grep` rate
between a baseline corpus (pre-install) and a post-install corpus.

### Protocol

1. Capture a baseline (already done in PR #51 — **76.7%** on 14-day
   window, 30 qualifying sessions):
   ```bash
   python bench/transcripts.py --output bench/transcripts_pre.jsonl
   ```
2. Install the hook in your live Claude Code environment:
   ```bash
   codebase-memory-mcp install
   # writes ~/.claude/hooks/codebase-memory-orientation.sh and
   # registers a PreToolUse matcher Glob|Grep in ~/.claude/settings.json
   ```
3. Restart Claude Code.
4. Use Claude Code normally for ≥14 days (comparable sample to
   baseline).
5. Capture the post-install corpus:
   ```bash
   python bench/transcripts.py --output bench/transcripts_post.jsonl
   ```
6. Run the comparison:
   ```bash
   python bench/compare_pregrep.py \
     --baseline bench/transcripts_pre.jsonl \
     --post     bench/transcripts_post.jsonl
   ```
   Or without a pre-corpus file, against the flat baseline rate:
   ```bash
   python bench/compare_pregrep.py \
     --post bench/transcripts_post.jsonl \
     --baseline-rate 0.767
   ```

### Stop-ship criteria (PR 3)

- Post `pre_graph_grep` rate ≤ **37%** AND
- Delta ≥ **40 percentage points** AND
- Post corpus size ≥ **30** qualifying sessions

Script exits 0 on PASS, 1 on FAIL, and prints a per-session-length
breakdown (short / medium / long) so you can see whether the hook
helps long sessions more than short ones — an expected pattern,
since long sessions have more Glob/Grep opportunities for the hook
to fire on.

## Follow-up PRs

- Phase 0c: PR ground-truth set (separate PR)
- CI integration: gate feature PRs on `compare.py` output (future)
