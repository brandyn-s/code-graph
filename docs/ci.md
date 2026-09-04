# CI workflows

Every workflow under `.github/workflows/`, what triggers it, and whether it
needs repository secrets. Workflows whose display name starts with `research:`
are manual experiments that call paid providers; they never run on pull
requests or pushes, and they fail fast with a one-line message when a required
secret is missing.

| Workflow | File | Trigger | Secrets | Purpose |
|---|---|---|---|---|
| Core CI | `core-ci.yml` | pull_request, push to `main` | none | Lint against the reviewed baseline, `go test` on Linux and macOS (required) and Windows (advisory until the lane is green), production build and smoke test, shell lint, schema drift check, zero-cost agent contract battery, workflow structure tests |
| accuracy-regression | `accuracy-regression.yml` | pull_request and push touching extraction or `bench/accuracy` | none | Oracle comparisons on synthetic fixtures, phantom-edge negative fixtures, Cypher semantics, adversarial F1 floors |
| ASan | `asan.yml` | pull_request, push to `main` | none | C extractors under AddressSanitizer |
| fuzz | `fuzz.yml` | nightly schedule, workflow_dispatch | none | Native Go fuzzing of the C extractor entry points; crashers uploaded as artifacts |
| drift-checks | `drift-checks.yml` | weekly schedule, pull_request touching grammars or canaries, workflow_dispatch | none | Grammar drift canaries and confidence-band distribution drift |
| Release | `release.yml` | workflow_dispatch | none (uses the workflow token and OIDC for attestations) | Cross-compiled binaries, checksums, build provenance, immutable GitHub release |
| Dry Run | `dry-run.yml` | workflow_dispatch | none | Release lint and test rehearsal without publishing |
| dependabot-auto-merge | `dependabot-auto-merge.yml` | pull_request from Dependabot | none (workflow token) | Auto-merge passing dependency bumps |
| research: agent-effectiveness | `agent-effectiveness.yml` | workflow_dispatch | `ANTHROPIC_API_KEY` (required) | Paid agent battery categories on the ripgrep fixture with a cost cap |
| research: locbench-reachability | `locbench-reachability.yml` | workflow_dispatch | none (workflow token for GitHub API rate limits) | Pin the reachable Loc-Bench subset |
| research: locbench-rebaseline | `locbench-rebaseline.yml` | workflow_dispatch | `ANTHROPIC_API_KEY` (required), `VOYAGE_API_KEY` (optional) | Re-baseline the localizer on the pinned Loc-Bench subset |
| research: matched-depth-retrieval-only-vs-graph | `matched-depth.yml` | workflow_dispatch | `ANTHROPIC_API_KEY`, `VOYAGE_API_KEY` (both required; fails closed with a zero-cost diagnostic) | Matched-depth comparison of retrieval-only versus graph arms. Still pinned to a pre-public code-search wheel; see the TODO in the file |

## Conventions

- Every action is pinned by commit SHA. Dependabot keeps the pins current.
- Jobs declare the minimum `permissions`; the default is `contents: read`.
- No workflow uses `pull_request_target`, `workflow_run`, or self-hosted runners.
- Secrets are referenced only in `research:` workflows and only in the steps
  that need them. `scripts/test_core_ci_workflow.py` and
  `scripts/test_matched_depth_workflow.py` pin these properties.
- Configure secrets under Settings → Secrets and variables → Actions. Without
  them, the research workflows exit early and the rest of CI is unaffected.
