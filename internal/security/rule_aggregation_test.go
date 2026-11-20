package security

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// TestRuleDecisionDenyNotMaskedByEarlierAllow 是核心回归测试：放在 deny 规则之前的
// allow 规则过去仅因排在前面就胜出，从而隐藏同一调用中后面的高风险操作。
// 无论行序如何，最严格的判定都必须胜出。
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

// TestRuleDecisionAskOverridesAllow 验证 ask 行无论顺序如何都会吸收匹配的 allow 行：
// allow 绝不能降低待处理的审批。
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

// TestRuleDecisionDenyBeatsAskAndAllow 检查三条规则都匹配同一调用时的完整
// deny > ask > allow 顺序。
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

// TestRuleDecisionAggregatesAcrossCommandSegments 覆盖一条链式 shell 程序，其第一段
// 被允许、后面的段被拒绝。两条规则都匹配完整命令串，因此聚合保留了 deny。
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

// TestRuleDecisionNoMatchKeepsDefault 确保没有行匹配时，聚合层报告"无匹配"，
// Evaluate 回退到其现有的工具类型默认值（只读 allow，修改类 ask）。
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

// TestRuleDecisionSingleMatchUnchanged 守护不重叠的情况：单条匹配行保留其判定和
// 原因，互不相交的行保持原有的等价于首条匹配的行为。
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

// TestChainAggregatedDenyStopsBeforeApproval 确认审批链契约完好：聚合的 deny 是硬性的
// StopError（ErrDenied，而非 ErrRefused），审批器从不被询问，且审计记录一条 deny。
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
