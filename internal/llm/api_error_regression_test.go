package llm_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestClassifyAPIErrorAuthSpacedPhrasing covers the real-world credential
// errors that put a few words between "api key" and the verdict. The old
// authRE only matched the adjacent forms ("invalid api key", "api_key=..."),
// so OpenAI's "Incorrect API key provided" and the equally common "Your API
// key is invalid" fell through to FamilyUnknown, whose UserAction is "none" —
// the user was never guided to re-login.
func TestClassifyAPIErrorAuthSpacedPhrasing(t *testing.T) {
	auth := []string{
		"Incorrect API key provided: sk-proj-abcdefghijklmnop",
		"Your API key is invalid",
		"api key is incorrect",
		"API key has been revoked",
		"The api-key was unknown",
		"invalid api key",
	}
	for _, msg := range auth {
		t.Run("auth: "+msg, func(t *testing.T) {
			got := llm.ClassifyAPIError(errors.New(msg))
			if got == nil || got.Family != llm.FamilyAuth {
				t.Fatalf("ClassifyAPIError(%q) = %#v, want %q", msg, got, llm.FamilyAuth)
			}
			if !strings.Contains(got.NextStep, "登录") {
				t.Errorf("NextStep %q should point the user to re-login", got.NextStep)
			}
		})
	}

	// The new alternatives must not pull config, quota or rate-limit failures
	// into the auth family just because they mention an api key.
	others := []struct {
		msg  string
		want string
	}{
		{"no api key configured", llm.FamilyNotConfigured},
		{"api key is required to use this endpoint", llm.FamilyNotConfigured},
		{"provider returned 429 Too Many Requests: rate limit exceeded for api key foo", llm.FamilyRateLimit},
		{"provider returned 402 Payment Required: insufficient quota for api key", llm.FamilyQuotaExhausted},
	}
	for _, tc := range others {
		t.Run("not auth: "+tc.msg, func(t *testing.T) {
			got := llm.ClassifyAPIError(errors.New(tc.msg))
			if got == nil || got.Family != tc.want {
				t.Fatalf("ClassifyAPIError(%q) = %#v, want %q", tc.msg, got, tc.want)
			}
		})
	}
}

// TestClassifyAPIErrorIsIdempotentThroughWrappers checks the documented
// contract that classifying an error twice yields the same result. A direct
// type assertion only recognises an unwrapped *ActionableError; a wire client
// that wraps it with %w would get a freshly derived copy whose detail is
// duplicated ("额度或配额已用尽: chat: 额度或配额已用尽: ...") and whose family
// can change when the message no longer carries the original signal.
func TestClassifyAPIErrorIsIdempotentThroughWrappers(t *testing.T) {
	for _, baseErr := range []error{
		errors.New("provider returned 402 Payment Required: insufficient quota"),
		fmt.Errorf("%w: read stream: connection reset", llm.ErrStreamError),
	} {
		first := llm.ClassifyAPIError(baseErr)
		wrapped := fmt.Errorf("agent: call failed: %w", first)
		second := llm.ClassifyAPIError(wrapped)
		if second != first {
			t.Fatalf("reclassifying wrapped %q returned %#v, want the same instance %#v", first.Family, second, first)
		}
		if second.Family != first.Family || second.NextStep != first.NextStep {
			t.Fatalf("wrapped classification changed: %#v -> %#v", first, second)
		}
	}

	// A caller-built ActionableError keeps its own family even when its
	// message has no pattern that could reproduce it from text.
	custom := &llm.ActionableError{Family: "custom_family", Message: "opaque upstream failure", NextStep: "contact support"}
	if got := llm.ClassifyAPIError(fmt.Errorf("wrapped: %w", custom)); got != custom || got.Family != "custom_family" {
		t.Fatalf("wrapped custom ActionableError = %#v, want the original", got)
	}
}
