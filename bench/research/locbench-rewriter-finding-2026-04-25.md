# Loc-Bench: question rewriter ablation — 2026-04-25

**Hypothesis tested**: extracting focused search terms from issue prose
before passing to the agent should help cases where the issue's first
paragraph lacks specific symbols (the openlibrary case from the recall
ceiling).

**Result**: **rewriter regresses results.** Default off; opt-in via
`LOCAGENT_REWRITE=1`.

## Numbers (n=16, structured scorer, hybrid-agent)

| Config | File | Class | Func |
|---|---|---|---|
| B3 baseline (read_file + open + depth 4, no rewriter) | 14/16 (88%) | 7/16 (44%) | 13/16 (81%) |
| **+ rewriter** | 13/16 (81%) | 6/16 (38%) | 11/16 (69%) |
| Δ | -7pp file | -6pp class | **-12pp func** |

## What changed per instance

| instance | B3 baseline | + rewriter | direction |
|---|---|---|---|
| ranaroussi__yfinance-2122 | Y/Y/Y | Y/N/N | regressed |
| kornia__kornia-3084 | Y/Y/Y | N/N/N | regressed |
| openlibrary | N/N/N | N/N/N | unchanged |
| pandas-dev | N/N/N | N/N/N | unchanged |
| Innopoints__backend-124 | Y/N/Y | Y/N/N | partial regression |
| (others) | matched | matched | no change |

## Why it likely regresses

Three plausible mechanisms, ranked by my confidence:

1. **Loss of contextual signal.** The rewriter replaces the issue's prose
   (which often contains hints about what's broken — "we're hitting
   timeouts", "after upgrade", "in the legacy code path") with a flat
   keyword list. The agent's downstream reasoning loses that signal.
   The keywords are necessary but not sufficient.

2. **Dual-context confusion.** The agent now sees BOTH the original
   issue and the rewriter's keyword list in its first user message. On
   ambiguous instances this may push the agent toward the rewriter's
   interpretation when the original prose contained the right cue.

3. **Keyword extraction is generic.** The rewriter pulls obvious nouns
   ("plugin", "handler", "register") that are correct but too common.
   The right keyword for openlibrary's case is `setup` itself — but
   "setup" appears in 31 different functions across the openlibrary
   graph. The rewriter doesn't know which "setup" to anchor on.

## What this means for the openlibrary case specifically

The recall-ceiling diagnosis (Phase A) said openlibrary was an
AGENT_BOTTLENECK — entity present, agent didn't surface it. The
rewriter doesn't fix this because:

- The keyword list still surfaces too many candidates
- The agent doesn't read utils.py specifically; it reads code.py and
  models.py instead (more "obvious" plugin files)

A more targeted fix would be: pass the AGENT a tool to grep file
contents for an exact symbol. Or augment the existing graph with
SYMBOL_DEFINED_IN edges so substring-on-name returns specific files.
Either is a separate piece of work.

## Decision

Ship the rewriter as opt-in code (`LOCAGENT_REWRITE=1`) so the
experiment is preserved and reproducible. Default OFF.

Keep B3's config (`LOCAGENT_PROMPT_VARIANT=open
LOCAGENT_BFS_DEPTH=4`) as the recommended high-accuracy mode. That
config is the genuine win:

| Mode | File | Class | Func |
|---|---|---|---|
| Original internal-release default | 81% | 31% | 44% |
| **B3 high-accuracy (read_file + open + depth 4)** | **88%** | **44%** | **81%** |
| LocAgent published | 92.7% | n/a | ~80% |

## What I'd test next on the rewriter

Not in this session, but for future reference:

1. **Replace rewriter output with structured rewrite.** Instead of "comma
   separated keywords", have the rewriter output JSON: `{"specific_symbols":
   [...], "domain_terms": [...], "error_messages": [...]}`. Lets the
   agent prioritize specific symbols.

2. **Use rewriter only when issue has no obvious symbols.** Heuristic:
   if the first paragraph contains a CamelCase or snake_case identifier,
   skip the rewriter. Otherwise rewrite. Avoids regressing the cases
   where the original prose was already specific enough.

3. **Measure on a larger n.** n=16 with this small a delta is consistent
   with noise plus a couple of regressions from poor keyword extraction.
   The rewriter's signal-to-noise on a broader sample might differ.
