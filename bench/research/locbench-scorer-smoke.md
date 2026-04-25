# Loc-Bench multi-mode comparison — 2026-04-25 12:07

**Binary:** structured scorer smoke
**Modes compared:** substring-primitives, hybrid-primitives, hybrid-agent
**Repo size cap:** 1000 MB

## Aggregate

Instances attempted: 2 | Indexed: 2

| Mode | Attempted | File hits | Class hits | Func hits | Total $ |
|---|---|---|---|---|---|
| substring-primitives | 2 | 1/2 (50%) | 0/2 (0%) | 0/2 (0%) | $0.00 |
| hybrid-primitives | 2 | 1/2 (50%) | 0/2 (0%) | 0/2 (0%) | $0.00 |
| hybrid-agent | 2 | 2/2 (100%) | 1/2 (50%) | 1/2 (50%) | $0.10 |

## Per-instance details

| instance | category | size (MB) | indexed | substring-primitives F/C/Fn | hybrid-primitives F/C/Fn | hybrid-agent F/C/Fn | note |
|---|---|---|---|---|---|---|---|
| Chainlit__chainlit-1441 | Security Vulnerability | 7 | Y | Y/N/N | Y/N/N | Y/N/Y |  |
| aio-libs__aiohttp-7829 | Performance Issue | 4 | Y | N/N/N | N/N/N | Y/Y/N |  |

