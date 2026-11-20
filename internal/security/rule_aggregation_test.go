package security

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// TestRuleDecisionDenyNotMaskedByEarlierAllow is the core regression: an allow
// rule placed before a deny rule used to win simply because it came first,
// hiding a high-risk operation later in the same call. The strictest decision
// must win regardless of row order.
func TestRuleDecisionDenyNotMaskedByEarlierAllow(t *testing.T) {
	command := mustArgs(t, `{"command":"echo hi && rm -rf /"}`)
	cases := []struct {
		name  string
		rules []Rule
	}{
		{
			name: "allow first, deny second",
			rules: []Rule{
				{Decision: Allow, Tool: "bash", Command: "echo"},
				{Decision: Deny, Tool: "bash", Command: "rm -rf"},
			},
		},
		{
			name: "deny first, allow second",
			rules: []Rule{
				{Decision: Deny, Tool: "bash", Command: "rm -rf"},
				{Decision: Allow, Tool: "bash", Command: "echo"},
			},
		},
	}
	for _, tc := range cases {
		policy := NewPolicy(t.TempDir(), PolicyConfig{Rules: tc.rules})
		if got := policy.Check("bash", command); got != Deny {
			t.Fatalf("%s: got %v, want deny", tc.name, got)
		}
	}
}

// TestRuleDecisionAskOverridesAllow verifies that an ask row absorbs a matching
// allow row in either order: allow must never downgrade a pending approval.
func TestRuleDecisionAskOverridesAllow(t *testing.T) {
	command := mustArgs(t, `{"command":"git push origin main"}`)
	cases := []struct {
		name  string
		rules []Rule
	}{
		{
			name: "allow first, ask second",
			rules: []Rule{
				{Decision: Allow, Tool: "bash", Command: "push"},
				{Decision: Ask, Tool: "bash", Command: "git push"},
			},
		},
		{
			name: "ask first, allow second",
			rules: []Rule{
				{Decision: Ask, Tool: "bash", Command: "git push"},
				{Decision: Allow, Tool: "bash", Command: "push"},
			},
		},
	}
	for _, tc := range cases {
		policy := NewPolicy(t.TempDir(), PolicyConfig{Rules: tc.rules})
		if got := policy.Check("bash", command); got != Ask {
			t.Fatalf("%s: got %v, want ask", tc.name, got)
		}
	}
}

// TestRuleDecisionDenyBeatsAskAndAllow checks the full deny > ask > allow
// ordering when all three rules match the same call.
func TestRuleDecisionDenyBeatsAskAndAllow(t *testing.T) {
	policy := NewPolicy(t.TempDir(), PolicyConfig{Rules: []Rule{
		{Decision: Allow, Command: "push"},
		{Decision: Ask, Command: "git push"},
		{Decision: Deny, Command: "--force"},
	}})
	if got := policy.Check("bash", mustArgs(t, `{"command":"git push --force origin main"}`)); got != Deny {
		t.Fatalf("got %v, want deny (deny must beat ask and allow)", got)
	}
}

// TestRuleDecisionAggregatesAcrossCommandSegments covers a chained shell
// program whose first segment is allowed but whose later segment is denied.
// Both rules match the full command string, so aggregation keeps the deny.
func TestRuleDecisionAggregatesAcrossCommandSegments(t *testing.T) {
	policy := NewPolicy(t.TempDir(), PolicyConfig{Rules: []Rule{
		{Decision: Allow, Command: "ls"},
		{Decision: Deny, Command: "rm -rf"},
	}})
	cases := []struct {
		name    string
		command string
		want    Decision
	}{
		{"allowed chain", "ls -la && echo ok", Allow},
		{"later high-risk segment", "ls -la && rm -rf /", Deny},
		{"semicolon chain", "echo start; rm -rf /tmp/x", Deny},
	}
	for _, tc := range cases {
		if got := policy.Check("bash", mustArgs(t, `{"command":`+quote(tc.command)+`}`)); got != tc.want {
			t.Fatalf("%s (%q): got %v, want %v", tc.name, tc.command, got, tc.want)
		}
	}
}

// TestRuleDecisionNoMatchKeepsDefault ensures that when no row matches the
// aggregation layer reports "no match" and Evaluate falls back to its existing
// tool-kind defaults (read-only allow, mutating ask).
func TestRuleDecisionNoMatchKeepsDefault(t *testing.T) {
	policy := NewPolicy(t.TempDir(), PolicyConfig{Rules: []Rule{
		{Decision: Allow, Tool: "read", Path: "src/**"},
	}})

	fields, err := parseArgs(mustArgs(t, `{"command":"echo hi"}`))
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if res, ok := policy.ruleDecision("bash", fields); ok {
		t.Fatalf("unmatched rule must report no match, got %+v", res)
	}

	if got := policy.Check("read", mustArgs(t, `{"path":"docs/a.md"}`)); got != Allow {
		t.Fatalf("unmatched read: got %v, want the default allow", got)
	}
	if got := policy.Check("bash", mustArgs(t, `{"command":"echo hi"}`)); got != Ask {
		t.Fatalf("unmatched mutating tool: got %v, want the default ask", got)
	}
}

// TestRuleDecisionSingleMatchUnchanged guards the non-overlapping cases: a
// single matching row keeps its decision and reason, and disjoint rows keep
// their original first-match-equivalent behaviour.
func TestRuleDecisionSingleMatchUnchanged(t *testing.T) {
	denyPolicy := NewPolicy(t.TempDir(), PolicyConfig{Rules: []Rule{
		{Decision: Deny, Tool: "read", Path: "**/.env"},
		{Decision: Allow, Tool: "read", Path: "src/**"},
	}})
	res := denyPolicy.Evaluate("read", mustArgs(t, `{"path":".env"}`))
	if res.Decision != Deny || !strings.Contains(res.Reason, "denied by the permissions table") {
		t.Fatalf("single deny: got %+v", res)
	}
	res = denyPolicy.Evaluate("read", mustArgs(t, `{"path":"src/main.rs"}`))
	if res.Decision != Allow || res.Reason != "" {
		t.Fatalf("single allow: got %+v", res)
	}

	askPolicy := NewPolicy(t.TempDir(), PolicyConfig{Rules: []Rule{
		{Decision: Ask, Tool: "bash", Command: "git push*"},
	}})
	res = askPolicy.Evaluate("bash", mustArgs(t, `{"command":"git push origin main"}`))
	if res.Decision != Ask || !strings.Contains(res.Reason, "requires approval per the permissions table") {
		t.Fatalf("single ask: got %+v", res)
	}
}

// TestChainAggregatedDenyStopsBeforeApproval confirms the approval-chain
// contract is intact: an aggregated deny is a hard StopError (ErrDenied, not
// ErrRefused), the approver is never consulted, and the audit records a deny.
func TestChainAggregatedDenyStopsBeforeApproval(t *testing.T) {
	dir := t.TempDir()
	approver := &countingApprover{allow: true}
	chain := NewChain(
		NewPolicy(dir, PolicyConfig{Rules: []Rule{
			{Decision: Allow, Tool: "bash", Command: "echo"},
			{Decision: Deny, Tool: "bash", Command: "rm -rf"},
		}}),
		approver,
		NewAuditLog(filepath.Join(dir, "audit")),
		"s1",
	)
	err := chain.Check(context.Background(), "bash", mustArgs(t, `{"command":"echo hi && rm -rf /"}`))
	if !errors.Is(err, ErrDenied) || errors.Is(err, ErrRefused) {
		t.Fatalf("got %v, want ErrDenied", err)
	}
	var stop *StopError
	if !errors.As(err, &stop) || stop.Refused {
		t.Fatalf("got %#v, want a non-refused StopError", err)
	}
	if approver.calls != 0 {
		t.Fatalf("an aggregated deny must not prompt, prompted %d times", approver.calls)
	}
	entries, readErr := ReadAudit(chain.Audit.Path())
	if readErr != nil || len(entries) != 1 || entries[0].Decision != "deny" {
		t.Fatalf("audit: %v entries=%+v", readErr, entries)
	}
}
