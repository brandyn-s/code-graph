package anthropic

import (
	"errors"
	"fmt"
	"testing"
)

// Tests for the typed sentinel errors introduced by Phase B5. Verifies
// that errors.Is unwraps correctly through fmt.Errorf wrapping so callers
// can switch on failure class.

func TestErrAuthFailed_Wrapping(t *testing.T) {
	wrapped := fmt.Errorf("%w (status 401): mock body", ErrAuthFailed)
	if !errors.Is(wrapped, ErrAuthFailed) {
		t.Errorf("errors.Is should match ErrAuthFailed through fmt.Errorf wrap")
	}
	if errors.Is(wrapped, ErrRateLimitExhausted) {
		t.Errorf("errors.Is should NOT match a different sentinel")
	}
}

func TestErrRateLimitExhausted_Wrapping(t *testing.T) {
	wrapped := fmt.Errorf("%w (status 429): mock body", ErrRateLimitExhausted)
	if !errors.Is(wrapped, ErrRateLimitExhausted) {
		t.Errorf("errors.Is should match ErrRateLimitExhausted")
	}
}

func TestErrServerError_Wrapping(t *testing.T) {
	wrapped := fmt.Errorf("%w (status 500): mock body", ErrServerError)
	if !errors.Is(wrapped, ErrServerError) {
		t.Errorf("errors.Is should match ErrServerError")
	}
}

func TestErrTimeoutExhausted_Wrapping(t *testing.T) {
	innerErr := fmt.Errorf("dial tcp: connect: connection refused")
	wrapped := fmt.Errorf("%w: %v", ErrTimeoutExhausted, innerErr)
	if !errors.Is(wrapped, ErrTimeoutExhausted) {
		t.Errorf("errors.Is should match ErrTimeoutExhausted")
	}
}

func TestSentinels_Distinct(t *testing.T) {
	// Each sentinel is a distinct value; one should not match another.
	if errors.Is(ErrAuthFailed, ErrRateLimitExhausted) {
		t.Errorf("ErrAuthFailed and ErrRateLimitExhausted must be distinct")
	}
	if errors.Is(ErrServerError, ErrTimeoutExhausted) {
		t.Errorf("ErrServerError and ErrTimeoutExhausted must be distinct")
	}
}
