package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/security"
)

// TestAutoApproverGrantsReadOnlyTools pins the --yolo convenience half of the
// gate: ordinary tools and read-only shell commands are granted without ever
// consulting the confirmation fallback, so auto mode stays fast off the
// command path.
func TestAutoApproverGrantsReadOnlyTools(t *testing.T) {
	fallback := &countingApprover{grant: false} // refuse everything if asked
	a := autoApprover{fallback: fallback}
	ctx := context.Background()
	for _, tc := range []struct{ name, args string }{
		{"read", `{"path":"a.txt"}`},
		{"glob", `{"pattern":"*.go"}`},
		{"bash", `{"command":"echo hi && git status"}`},
		{"powershell", `{"script":"Get-Content a.txt"}`},
	} {
		ok, err := a.Ask(ctx, tc.name, tc.args)
		if err != nil || !ok {
			t.Fatalf("Ask(%q, %q) = (%v, %v); want (true, nil)", tc.name, tc.args, ok, err)
		}
	}
	if fallback.calls != 0 {
		t.Fatalf("confirmation fallback called %d times for read-only tools", fallback.calls)
	}
}

// TestAutoApproverRefusesMutatingShellWithoutFallback is the core P1 fix: a
// zero-value autoApprover (used by callers that inject it directly) must not
// silently grant a destructive command. Fail closed. A non-shell tool carrying
// a "command" field must be unaffected.
func TestAutoApproverRefusesMutatingShellWithoutFallback(t *testing.T) {
	a := autoApprover{}
	ctx := context.Background()
	for _, tc := range []struct{ name, args string }{
		{"bash", `{"command":"rm -rf /tmp/x"}`},
		{"bash", `{"command":"git push --force"}`},
		{"powershell", `{"script":"Remove-Item -Recurse C:\\tmp\\x"}`},
	} {
		ok, err := a.Ask(ctx, tc.name, tc.args)
		if err != nil || ok {
			t.Fatalf("Ask(%q, %q) = (%v, %v); want (false, nil)", tc.name, tc.args, ok, err)
		}
	}
	if ok, err := a.Ask(ctx, "read", `{"command":"rm -rf /tmp/x"}`); err != nil || !ok {
		t.Fatalf("read with a command field = (%v, %v); want granted", ok, err)
	}
}

// TestAutoApproverDelegatesMutatingShellToFallback proves the approval chain is
// still usable for explicit authorization: a mutating shell command consults
// the fallback exactly once and follows its decision.
func TestAutoApproverDelegatesMutatingShellToFallback(t *testing.T) {
	ctx := context.Background()
	for _, grant := range []bool{true, false} {
		fallback := &countingApprover{grant: grant}
		a := autoApprover{fallback: fallback}
		ok, err := a.Ask(ctx, "bash", `{"command":"rm -rf /tmp/x"}`)
		if err != nil || ok != grant {
			t.Fatalf("grant=%v: Ask = (%v, %v); want (%v, nil)", grant, ok, err, grant)
		}
		if fallback.calls != 1 {
			t.Fatalf("fallback called %d times, want exactly 1", fallback.calls)
		}
	}
}

// TestAutoChainDoesNotSilentlyRunMutatingBash is the wiring-level regression
// test: with policy asking for both tools, the same auto approver still grants
// read but produces a refusal for a mutating bash command through Chain.Check.
func TestAutoChainDoesNotSilentlyRunMutatingBash(t *testing.T) {
	policy := security.NewPolicy(t.TempDir(), security.PolicyConfig{
		Rules: []security.Rule{
			{Decision: security.Ask, Tool: "bash"},
			{Decision: security.Ask, Tool: "read"},
		},
	})
	chain := security.NewChain(policy, autoApprover{}, nil, "s1")
	ctx := context.Background()

	if err := chain.Check(ctx, "read", json.RawMessage(`{"path":"a.txt"}`)); err != nil {
		t.Fatalf("read Check = %v; want allowed under auto", err)
	}
	err := chain.Check(ctx, "bash", json.RawMessage(`{"command":"rm -rf /tmp/x"}`))
	var stop *security.StopError
	if !errors.As(err, &stop) || !stop.Refused {
		t.Fatalf("mutating bash Check = %v; want a refused *security.StopError", err)
	}
}

// TestApproverForAutoWiresHumanFallback checks that --yolo / approval=auto get
// a real confirmation fallback, and that it only accepts y/yes — an "A"
// (approve all) answer must not authorize a destructive command, matching
// aider's explicit_yes_required.
func TestApproverForAutoWiresHumanFallback(t *testing.T) {
	for _, tc := range []struct {
		answer string
		want   bool
	}{
		{"y\n", true},
		{"yes\n", true},
		{"A\n", false},
		{"n\n", false},
		{"\n", false},
	} {
		stdio := cli.IO{In: strings.NewReader(tc.answer), Err: &bytes.Buffer{}}
		approver := approverFor(stdio, Resolved{Approval: "auto", Storage: t.TempDir()})
		auto, ok := approver.(autoApprover)
		if !ok {
			t.Fatalf("approverFor(auto) = %T; want autoApprover", approver)
		}
		if auto.fallback == nil {
			t.Fatal("auto mode must carry a confirmation fallback")
		}
		got, err := auto.Ask(context.Background(), "bash", `{"command":"rm -rf /tmp/x"}`)
		if err != nil || got != tc.want {
			t.Fatalf("answer %q = (%v, %v); want (%v, nil)", tc.answer, got, err, tc.want)
		}
	}
}

// TestYoloAndAutoShareTheSamePath guards against the two flags diverging:
// --yolo resolves to approval=auto, so both spellings must route through the
// same gated approver.
func TestYoloAndAutoShareTheSamePath(t *testing.T) {
	for _, mode := range []string{"auto", "yolo"} {
		stdio := cli.IO{In: strings.NewReader("n\n"), Err: &bytes.Buffer{}}
		approver := approverFor(stdio, Resolved{Approval: mode, Storage: t.TempDir()})
		ok, err := approver.Ask(context.Background(), "bash", `{"command":"rm -rf /tmp/x"}`)
		if err != nil || ok {
			t.Fatalf("mode %q: Ask = (%v, %v); want (false, nil) on an 'n' answer", mode, ok, err)
		}
	}
}
