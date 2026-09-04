# Relationship to the upstream project

code-graph is a hard fork of
[DeusData/codebase-memory-mcp](https://github.com/DeusData/codebase-memory-mcp)
(MIT). The fork point is the upstream tree as of 2026-02-27. Since then this
repository has taken its own direction: security and compliance tools,
service and change-impact analysis, SCIP compiler-index ingestion, coherent
source identity, relationship evidence, cross-project operations, and a
different module layout (`internal/` here; `src/`, `pkg/`, and `vendored/`
upstream). Upstream commits do not apply cleanly and are not merged.

## What is still shared

The C extraction layer under `internal/cbm/` corresponds to upstream's `cbm`
directory. The files worth diffing periodically are:

- `cbm.c`, `cbm.h`, `helpers.c`, `helpers.h`, `arena.c`, `arena.h`
- `extract_defs.c`, `extract_calls.c`, `extract_imports.c`,
  `extract_usages.c`, `extract_type_refs.c`, `extract_type_assigns.c`,
  `extract_env_accesses.c`, `extract_semantic.c`, `extract_unified.c`
- `lang_specs.h`
- the vendored grammars under `vendored/grammars/` (pinned refs are listed
  in `THIRD_PARTY_NOTICES.md` and `GRAMMARS.md`)

Everything else in this repository is fork-specific.

## Porting an upstream fix

1. Fetch upstream and diff only the shared directory:

   ```bash
   git remote add upstream https://github.com/DeusData/codebase-memory-mcp.git
   git fetch upstream
   git diff <last-synced-upstream-sha> upstream/main -- cbm/ > /tmp/cbm.diff
   ```

2. Apply hunks by hand into `internal/cbm/`. Path prefixes differ and the
   fork's extractors have diverged, so `git apply` is rarely clean.
3. Run the ASan lane locally (`.github/workflows/asan.yml` documents the
   flags), then the accuracy gates in
   [CONTRIBUTING.md](../CONTRIBUTING.md#measuring-an-accuracy-change).
4. Record the upstream SHA you synced to in the commit message so the next
   diff has a base.

A quarterly diff is enough; upstream's grammar bumps and extractor fixes are
the changes worth taking, and the fork's resolver and pipeline are not
candidates for merging in either direction.

## Build size and optional grammars

The binary is dominated by tree-sitter parser tables. Default builds exclude
the largest niche grammars behind the `cbm_all` build tag; `make build-all`
compiles every grammar. Languages excluded from a build are reported as
unsupported by `index_health` rather than silently skipped.
