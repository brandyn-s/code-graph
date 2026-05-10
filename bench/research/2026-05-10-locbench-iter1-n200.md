# Loc-Bench multi-mode comparison — 2026-05-10 13:45

**Binary:** current main
**Modes compared:** hybrid-agent
**Repo size cap:** 200 MB

## Provenance manifest

| field | value |
|---|---|
| harness_sha | `1dab656f8413` |
| scorer_schema | `2` |
| eval_bin_sha | `1f4eb8795802` |
| index_bin_sha | `0c694e998f8f` |
| dataset_sha | `8df0833c2c12` |
| agent_iterations | `1` |
| modes | `hybrid-agent` |
| max_mb | `200` |
| n_attempted | `4` |
| n_indexed | `4` |
| timestamp_utc | `2026-05-10T18:45:08Z` |

## Aggregate

Instances attempted: 4 | Indexed: 4

| Mode | Attempted | File hits | Class hits | Func hits | Total $ |
|---|---|---|---|---|---|
| hybrid-agent | 4 | 4/4 (100%) | 4/4 (100%) | 3/4 (75%) | $0.20 |

## Per-instance details

| instance | category | size (MB) | indexed | hybrid-agent F/C/Fn | note |
|---|---|---|---|---|---|
| bridgecrewio__checkov-6909 | Bug Report | 95 | Y | Y/Y/Y |  |
| PrefectHQ__prefect-16117 | Bug Report | 74 | Y | Y/Y/Y |  |
| scipy__scipy-22106 | Bug Report | 103 | Y | Y/Y/Y |  |
| flet-dev__flet-4384 | Bug Report | 30 | Y | Y/Y/N |  |

