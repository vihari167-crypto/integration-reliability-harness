package tools

import (
	"errors"
	"testing"

	"integration-harness/tool-server/internal/linear"
)

// classifyError is the single place the hardened tools turn a raw API failure
// into the typed, actionable error vocabulary the agent was told to expect.
// Getting this mapping right is what lets the agent tell "retry me" from
// "don't bother" — so it's worth testing directly.
func TestClassifyError(t *testing.T) {
	cases := []struct {
		name          string
		err           error
		wantCode      string
		wantRetryable bool
	}{
		{"429 is a retryable rate limit", &linear.APIError{StatusCode: 429, RetryAfter: "2"}, "rate_limited", true},
		{"500 is a retryable server error", &linear.APIError{StatusCode: 500}, "server_error", true},
		{"404 is a non-retryable not-found", &linear.APIError{StatusCode: 404}, "not_found", false},
		{"401 is a non-retryable permission error", &linear.APIError{StatusCode: 401}, "permission_denied", false},
		{"403 is a non-retryable permission error", &linear.APIError{StatusCode: 403}, "permission_denied", false},
		{"422 is a non-retryable validation error", &linear.APIError{StatusCode: 422, Body: "bad input"}, "validation_error", false},
		{"transport error is treated as retryable", errors.New("connection reset"), "unknown", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyError(c.err)
			if got.Code != c.wantCode {
				t.Errorf("code: want %q, got %q", c.wantCode, got.Code)
			}
			if got.Retryable != c.wantRetryable {
				t.Errorf("retryable: want %v, got %v", c.wantRetryable, got.Retryable)
			}
		})
	}
}

func TestRateLimitCarriesRetryAfter(t *testing.T) {
	got := classifyError(&linear.APIError{StatusCode: 429, RetryAfter: "5"})
	if got.RetryAfter != "5" {
		t.Fatalf("want retry_after 5, got %q", got.RetryAfter)
	}
}
