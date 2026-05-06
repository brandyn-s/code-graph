package anthropic

// Typed sentinel errors for the four documented failure modes.
// See FAILURE_MODES.md for the audit motivation.
//
// Callers gain the ability to distinguish failure classes via
// errors.Is(err, anthropic.ErrXxx) without parsing error message text.

import "errors"

var (
	// ErrAuthFailed wraps 401 / 403 responses from the Anthropic API.
	// Typical cause: ANTHROPIC_API_KEY missing, rotated, or scope-revoked.
	// Not retried — auth failures are deterministic until the key changes.
	ErrAuthFailed = errors.New("anthropic: authentication failed")

	// ErrRateLimitExhausted wraps 429 responses after all retry attempts
	// have been exhausted. Typical cause: high-volume batch run on a
	// rate-limited key. Caller should back off significantly or use a
	// different key tier.
	ErrRateLimitExhausted = errors.New("anthropic: rate limit exhausted after retries")

	// ErrServerError wraps 5xx responses after all retry attempts have
	// been exhausted. Typical cause: transient Anthropic-side issue.
	// Caller may retry after a longer pause.
	ErrServerError = errors.New("anthropic: server error after retries")

	// ErrTimeoutExhausted wraps connection / timeout errors after all
	// retry attempts. Typical cause: operator network issue, DNS, or
	// firewall. Caller should investigate environment, not retry.
	ErrTimeoutExhausted = errors.New("anthropic: timeout exhausted after retries")
)
