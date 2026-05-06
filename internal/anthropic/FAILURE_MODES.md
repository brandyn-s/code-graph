# `internal/anthropic` failure-mode audit

Phase B5 of the post-roundtable plan. The roundtable flagged
`code_localize_agent` failure-mode behavior on API-key rotation /
rate-limits / timeouts as "single-source, needs verification." This
document is the verification.

**Verdict**: No silent-degradation paths exist in the current code.
All three audited failure modes return structured errors that the
caller surfaces. The improvement is **error specificity**, not
behavior change — consumers can't programmatically distinguish auth
errors from rate-limit-exhausted from transient-network-error.

## Failure mode matrix

| Failure mode | Trigger | Current behavior | Improvement landed in B5 |
|---|---|---|---|
| ANTHROPIC_API_KEY missing | `os.Getenv("ANTHROPIC_API_KEY")` returns "" at `NewClient()` time | `NewClient()` returns `nil`; callers (agent.go:291-294) check and surface "ANTHROPIC_API_KEY not set" | Already fail-closed ✓; documented |
| API key rotated mid-session | Client holds stale key; next call returns 401 | Returns `fmt.Errorf("anthropic API error 401: ...")`. Not retried (only 429+5xx retried) | New `ErrAuthFailed` typed sentinel; caller can distinguish via `errors.Is` |
| Rate-limit 429 exhausted | 4 attempts (initial + 3 retries) all return 429 | Returns `fmt.Errorf("anthropic API error 429: ...")` | New `ErrRateLimitExhausted` sentinel |
| Connection timeout | HTTP client 120s timeout on every attempt; 4 attempts | Returns the underlying network error (e.g., `context.DeadlineExceeded`) | New `ErrTimeoutExhausted` sentinel; explicit wrapping |
| Server-side 5xx | 4 attempts with backoff (1s, 2s, 4s) | Returns `fmt.Errorf("anthropic API error 5xx: ...")` | New `ErrServerError` sentinel |
| Malformed response body | JSON unmarshal failure | Returns `fmt.Errorf("parse response: %w (body: %s)", err, truncated)` | Unchanged; intentional inclusion of body excerpt for debugging |
| `context.Canceled` propagation | Caller cancels mid-retry | Returns `context.Canceled` immediately, no further retries | Already correct ✓ |

## What was checked

1. **Read `internal/anthropic/client.go` end-to-end** — 200 lines, retry
   loop at lines 134-193.
2. **Searched callers** via `grep -rn 'anthropic.NewClient\|anthropic\.Client'`:
   - `internal/locagent/agent.go:291-294` — checks nil-client correctly.
   - `internal/locagent/rewriter.go:59` — receives client from caller; doesn't construct itself.
3. **Verified retry behavior** by reading the retry loop:
   - 429 / 5xx: 4 attempts total; backoff = `1<<attempt` for 5xx, `5*(attempt+1)` for 429.
   - Connection error: 4 attempts; exponential backoff.
   - Context cancellation: returns immediately, never retries.

## What was NOT changed

- Retry counts and backoff schedules. The roundtable flagged these as
  reasonable; this audit confirms them. 4 attempts at 5s/10s/15s for
  429 is appropriate for our typical Loc-Bench batch scale (~$10/n=200
  run); raising would mask rate-limit-exhaustion as success.
- The nil-on-missing-key constructor pattern. This matches
  `internal/pipeline/voyage_client.go` and is intentional.
- Error message content. The new sentinel-based wrapping preserves the
  human-readable message; the sentinels are additive type information
  for `errors.Is` checks.

## Improvement: typed sentinel errors

The fix is in `internal/anthropic/errors.go` (added in B5). Four
exported sentinels:

- `ErrAuthFailed` — wraps 401 / 403 responses
- `ErrRateLimitExhausted` — wraps 429 after all retries
- `ErrServerError` — wraps 5xx after all retries
- `ErrTimeoutExhausted` — wraps connection / timeout errors after all retries

Callers gain the ability to distinguish:

```go
resp, err := client.CreateMessage(ctx, req)
if err != nil {
    switch {
    case errors.Is(err, anthropic.ErrAuthFailed):
        // Surface to operator as "rotate key" — not a retry signal
    case errors.Is(err, anthropic.ErrRateLimitExhausted):
        // Surface as "back off significantly or use a different key"
    case errors.Is(err, anthropic.ErrServerError):
        // Transient — operator may retry
    case errors.Is(err, anthropic.ErrTimeoutExhausted):
        // Network issue — likely operator-environment problem
    default:
        // Unknown — surface verbatim
    }
}
```

The `code_localize_agent` doesn't currently switch on error types
(it surfaces all errors uniformly to its tool-result), but having
the sentinels in place enables future error-aware handling without
another B5-style audit.

## Cross-references

- Plan: `~/Documents/knowledge-base/plans/2026-05-05-codegraph-and-cross-tool-recommendations.md` Phase B5
- Roundtable single-source finding: `~/Documents/roundtables/2026-05-05-code-graph/results/META_SYNTHESIS.md` (Opus's "code_localize_agent failure-mode behavior on API-key rotation/rate-limits unclear")
- Caller: `internal/locagent/agent.go:291`
