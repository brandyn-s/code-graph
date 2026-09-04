# CI workflows

Every workflow under `.github/workflows/`, what triggers it, and whether it
needs repository secrets. Workflows whose display name starts with `research:`
are manual experiments that call paid providers; they never run on pull
requests or pushes, and they fail fast with a one-line message when a required
secret is missing.

| Workflow | File | Trigger | Secrets | Purpose |
|---|---|---|---|---|
| Core CI | `core-ci.yml` | pull_request, push to `main` | none | Lint against the reviewed baseline, `go test` on Linux and macOS (required) and Windows (required)|
| accuracy-regression | `accuracy-regression.yml` | pull_request and push touching extraction or `bench/accuracy` | none | Oracle comparisons on synthetic fixtures, phantom-edge negative fixtures, Cypher semantics, adversarial F1 floors |
| ASan | `asan.yml` | pull_request, push to `main` | none | C extractors under AddressSanitizer |
| tsan | `tsan.yml` | nightly schedule, pull_request touching the workflow file, workflow_dispatch | none | C extractors and pipeline under ThreadSanitizer (`make test-tsan`) |
| soak | `soak.yml` | nightly schedule, workflow_dispatch (iterations, fixture) | none | Index the largest synthetic fixture 50 times with an ASan build (`make soak`); fails on crash, sanitizer report, or database growth over 3x |
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

## Sanitizer lanes

`asan.yml` runs on pull requests that touch the C interop code; `tsan.yml` and
`soak.yml` run nightly because sanitizer builds of the grammar tables are slow.
All three have Makefile equivalents that need clang or gcc with the sanitizer
runtimes installed:

```bash
make test-asan   # internal/cbm + internal/pipeline under AddressSanitizer
make test-tsan   # the same packages under ThreadSanitizer
make soak        # ASan binary, 50 forced re-indexes of bench/accuracy/synthetic/post-battery
SOAK_ITERATIONS=5 SOAK_FIXTURE=bench/accuracy/synthetic/go-minimal make soak
```

`scripts/soak-index.sh` keeps per-iteration stdout/stderr under `SOAK_LOG_DIR`
(uploaded as the `soak-logs` artifact when the nightly run fails) and exits
non-zero on a crash, any `ERROR: ...Sanitizer` / `panic:` / `fatal error:`
line, or a database that ends more than `SOAK_MAX_DB_GROWTH` (default 3) times
its size after the first iteration. `scripts/test_sanitizer_workflows.py` pins
the structure of these lanes.

ThreadSanitizer with cgo is supported by the Go runtime on Linux only; on macOS
the TSan runtime kills the process during Go scheduler start-up
(`ThreadSanitizer:DEADLYSIGNAL` ... `fatal error: stoplockedm`), so run
`make test-tsan` on Linux or in a Linux container. ASan works on both.

## Reproducing CI lint locally

Core CI and the release lint action run golangci-lint 2.10.1 on Go 1.26 with
`--new-from-rev` pointed at the reviewed baseline, so only findings introduced
after that commit fail the build. A newer local Go toolchain can change the
typecheck results, and `make lint` (which lints everything, not just new code)
reports pre-baseline findings CI ignores. To see exactly what CI sees:

```bash
GOTOOLCHAIN=go1.26.1 golangci-lint run --timeout=10m \
  --new-from-rev=32fc4dd857497addff22115d6858dde2289e8e04
```

Run it from the repository's physical path. golangci-lint's `--new-from-rev`
filter matches issue paths against `git diff` output, and when the working
directory is reached through a symlink (macOS `/tmp` is one) every finding is
silently dropped and the run prints `0 issues` for code CI will reject. Use
`cd "$(pwd -P)"` first; the same applies to a checkout under `/tmp`.

Install golangci-lint 2.10.1 from its release page (or `brew install golangci-lint`
and check `golangci-lint version`). `GOTOOLCHAIN` makes the `go` command
download and use exactly 1.26.1 for that invocation.
