package extension

import (
	"errors"
	"testing"

	"example.com/argentgo/internal/hooks"
)

func TestAuthorizerFailClosed(t *testing.T) {
	trust := hooks.NewTrustStore()
	data := []byte("extension document")
	digest := hooks.Digest(data)
	key := "/workspace/.argent/hooks/x.json"

	a := NewAuthorizer(trust)
	if !a.Enabled() {
		t.Fatal("real authorizer must be enabled")
	}
	// 未授信：拒绝。
	if err := a.Authorize(key, digest); !errors.Is(err, hooks.ErrUntrusted) {
		t.Fatalf("untrusted err = %v, want ErrUntrusted", err)
	}
	// 授信后放行。
	trust.Trust(key, digest)
	if err := a.Authorize(key, digest); err != nil {
		t.Fatalf("trusted err = %v, want nil", err)
	}
	// key 不匹配仍拒绝（精确路径/摘要对）。
	if err := a.Authorize(key+".other", digest); !errors.Is(err, hooks.ErrUntrusted) {
		t.Fatalf("key mismatch err = %v, want ErrUntrusted", err)
	}
}

func TestNoopAuthorizerDisabled(t *testing.T) {
	a := NewAuthorizer(nil)
	if a.Enabled() {
		t.Fatal("nil trust must yield disabled authorizer")
	}
	if err := a.Authorize("any", "any"); err != nil {
		t.Fatalf("noop authorize = %v, want nil", err)
	}
}

func TestKindString(t *testing.T) {
	cases := map[Kind]string{
		KindSkill: "skill", KindHook: "hook", KindCommand: "command", KindPlugin: "plugin", Kind(99): "unknown",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("Kind(%d).String() = %q, want %q", k, got, want)
		}
	}
}

func TestDiagnosticKindString(t *testing.T) {
	cases := map[DiagnosticKind]string{
		DiagnosticSkipped: "skipped", DiagnosticInvalid: "invalid",
		DiagnosticUntrusted: "untrusted", DiagnosticConflict: "conflict",
		DiagnosticKind(99): "unknown",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("DiagnosticKind(%d).String() = %q, want %q", k, got, want)
		}
	}
}
