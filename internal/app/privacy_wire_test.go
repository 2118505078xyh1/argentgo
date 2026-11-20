package app

import (
	"strings"
	"testing"
)

// TestApplyPrivacyPostureAnyIsNoOp pins the default posture: "any" (and an
// unset/empty mode) leaves web tools available and accepts a remote base URL.
func TestApplyPrivacyPostureAnyIsNoOp(t *testing.T) {
	webTools := []string{"web_search", "web_fetch"}
	for _, mode := range []string{"any", "", "  "} {
		blocked, err := ApplyPrivacyPosture(mode, "https://api.openai.com/v1", webTools)
		if err != nil {
			t.Fatalf("mode %q: unexpected error: %v", mode, err)
		}
		if len(blocked) != 0 {
			t.Fatalf("mode %q: blocked = %v, want none", mode, blocked)
		}
	}
}

// TestApplyPrivacyPostureLocalOnlyBlocksWebTools verifies that local-only
// withholds every web tool while still allowing a loopback chat endpoint.
func TestApplyPrivacyPostureLocalOnlyBlocksWebTools(t *testing.T) {
	for _, baseURL := range []string{
		"http://localhost:11434",
		"http://127.0.0.1:8080/v1",
		"http://[::1]:9000",
	} {
		blocked, err := ApplyPrivacyPosture("local-only", baseURL, []string{"web_search", "web_fetch"})
		if err != nil {
			t.Fatalf("baseURL %q: unexpected error: %v", baseURL, err)
		}
		if strings.Join(blocked, ",") != "web_search,web_fetch" {
			t.Fatalf("baseURL %q: blocked = %v", baseURL, blocked)
		}
	}
}

// TestApplyPrivacyPostureLocalOnlyRejectsRemoteBaseURL verifies the fail-closed
// endpoint check: a remote base URL is an error, not a silent downgrade.
func TestApplyPrivacyPostureLocalOnlyRejectsRemoteBaseURL(t *testing.T) {
	blocked, err := ApplyPrivacyPosture("local-only", "https://api.openai.com/v1", []string{"web_search"})
	if err == nil {
		t.Fatal("expected an error for a remote base URL in local-only mode")
	}
	if len(blocked) != 0 {
		t.Fatalf("blocked = %v, want none when the endpoint is rejected", blocked)
	}
	if !strings.Contains(err.Error(), "remote endpoint blocked") {
		t.Fatalf("error does not carry the audit reason: %v", err)
	}
}

// TestApplyPrivacyPostureUnknownModeRejected pins the fail-closed behaviour for
// an unrecognized mode, matching security.AllowRemote.
func TestApplyPrivacyPostureUnknownModeRejected(t *testing.T) {
	if _, err := ApplyPrivacyPosture("bogus", "http://localhost:8080", nil); err == nil {
		t.Fatal("expected an error for an unknown privacy mode")
	}
}
