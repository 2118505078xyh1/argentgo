package security

import (
	"strings"
	"testing"
)

func TestParsePolicyWithIssuesInvalidDecisionIsReportedAndSkipped(t *testing.T) {
	root := t.TempDir()
	data := []byte(`{"rules":[{"decision":"denyy","tool":"read","path":"secret.txt"}]}`)
	policy, issues, err := ParsePolicyWithIssues(root, data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if policy == nil {
		t.Fatal("a policy must still be returned when a rule is skipped")
	}
	if len(issues) != 1 {
		t.Fatalf("got %d issues, want 1: %#v", len(issues), issues)
	}
	got := issues[0]
	if got.Kind != IssueKindInvalidDecision {
		t.Errorf("kind = %q, want %q", got.Kind, IssueKindInvalidDecision)
	}
	if got.Index != 0 {
		t.Errorf("index = %d, want 0", got.Index)
	}
	if !strings.Contains(got.Raw, `"denyy"`) || !strings.Contains(got.Raw, "secret.txt") {
		t.Errorf("raw = %q, want the offending rule verbatim", got.Raw)
	}
	if !strings.Contains(got.Reason, "invalid decision") || !strings.Contains(got.Reason, "denyy") {
		t.Errorf("reason = %q, want it to name the bad decision", got.Reason)
	}
	// 失败即拒绝的跳过：无法识别的判定永远不会执行，因此保留原有行为（不拒绝）。
	if decision := policy.Check("read", mustArgs(t, `{"path":"secret.txt"}`)); decision != Allow {
		t.Fatalf("skipped deny rule must not block the call: got %v, want allow", decision)
	}
}

func TestParsePolicyWithIssuesValidRulesProduceNoIssues(t *testing.T) {
	root := t.TempDir()
	data := []byte(`{"rules":[
		{"decision":"allow","tool":"read","path":"public/**"},
		{"decision":"ask","tool":"write","path":"src/**"},
		{"decision":"deny","tool":"bash","command":"git push*"},
		{"decision":"Deny","tool":"ls"},
		{"decision":" allowed ","tool":"grep"},
		{"decision":"denied","tool":"find"}
	]}`)
	policy, issues, err := ParsePolicyWithIssues(root, data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("valid rules must not produce issues: %#v", issues)
	}
	if policy.IsEmpty() {
		t.Fatal("a populated policy must not be empty")
	}
	if got := policy.Check("write", mustArgs(t, `{"path":"src/a.go"}`)); got != Ask {
		t.Fatalf("ask rule: got %v, want ask", got)
	}
	if got := policy.Check("bash", mustArgs(t, `{"command":"git push origin"}`)); got != Deny {
		t.Fatalf("deny rule: got %v, want deny", got)
	}
	if got := policy.Check("read", mustArgs(t, `{"path":"public/a.txt"}`)); got != Allow {
		t.Fatalf("allow rule: got %v, want allow", got)
	}
}

func TestParsePolicyWithIssuesMixedRulesKeepIndicesAndOrder(t *testing.T) {
	root := t.TempDir()
	data := []byte(`{"rules":[
		{"decision":"deny","tool":"bash","command":"rm -rf *"},
		{"decision":"bogus","tool":"read","path":"one.txt"},
		{"decision":"ask","tool":"write","path":"src/**"},
		{"decision":"","tool":"ls","path":"two.txt"},
		{"decision":"allow","tool":"grep"}
	]}`)
	policy, issues, err := ParsePolicyWithIssues(root, data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("got %d issues, want 2: %#v", len(issues), issues)
	}
	for i, want := range []struct {
		index    int
		decision string
	}{
		{1, "bogus"},
		{3, ""},
	} {
		if issues[i].Index != want.index {
			t.Errorf("issue %d: index = %d, want %d", i, issues[i].Index, want.index)
		}
		if issues[i].Kind != IssueKindInvalidDecision {
			t.Errorf("issue %d: kind = %q, want %q", i, issues[i].Kind, IssueKindInvalidDecision)
		}
		if !strings.Contains(issues[i].Raw, `"`+want.decision+`"`) {
			t.Errorf("issue %d: raw = %q, want decision %q", i, issues[i].Raw, want.decision)
		}
	}
	// 被跳过规则周围的有效规则仍会被执行。
	if got := policy.Check("bash", mustArgs(t, `{"command":"rm -rf /tmp/x"}`)); got != Deny {
		t.Fatalf("first deny rule: got %v, want deny", got)
	}
	if got := policy.Check("write", mustArgs(t, `{"path":"src/a.go"}`)); got != Ask {
		t.Fatalf("ask rule after a skipped rule: got %v, want ask", got)
	}
}

func TestParsePolicyWithIssuesPreservesCJKInRaw(t *testing.T) {
	root := t.TempDir()
	data := []byte(`{"rules":[
		{"decision":"拒绝","tool":"读取","path":"密钥/证书.pem","command":"删除 文件"},
		{"decision":"deny","tool":"bash","command":"删除 机密.txt"}
	]}`)
	policy, issues, err := ParsePolicyWithIssues(root, data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("got %d issues, want 1: %#v", len(issues), issues)
	}
	if issues[0].Index != 0 {
		t.Errorf("index = %d, want 0", issues[0].Index)
	}
	for _, fragment := range []string{"拒绝", "读取", "密钥/证书.pem", "删除 文件"} {
		if !strings.Contains(issues[0].Raw, fragment) {
			t.Errorf("raw %q lost CJK fragment %q", issues[0].Raw, fragment)
		}
	}
	if !strings.Contains(issues[0].Reason, "拒绝") {
		t.Errorf("reason %q should name the CJK decision", issues[0].Reason)
	}
	// 携带中日韩文本的有效规则仍会应用，且不会产生问题。
	if got := policy.Check("bash", mustArgs(t, `{"command":"删除 机密.txt"}`)); got != Deny {
		t.Fatalf("CJK deny rule: got %v, want deny", got)
	}
}

func TestPolicyFromJSONCompatibilityWithSkippedRules(t *testing.T) {
	root := t.TempDir()
	data := []byte(`{"rules":[
		{"decision":"denyy","tool":"read","path":"secret.txt"},
		{"decision":"ask","tool":"write","path":"src/**"}
	]}`)
	policy, err := PolicyFromJSON(root, data)
	if err != nil {
		t.Fatalf("PolicyFromJSON must not error on a skipped rule: %v", err)
	}
	if got := policy.Check("read", mustArgs(t, `{"path":"secret.txt"}`)); got != Allow {
		t.Fatalf("skipped rule: got %v, want allow", got)
	}
	if got := policy.Check("write", mustArgs(t, `{"path":"src/a.go"}`)); got != Ask {
		t.Fatalf("valid rule: got %v, want ask", got)
	}
	withIssues, issues, err := ParsePolicyWithIssues(root, data)
	if err != nil {
		t.Fatalf("parse with issues: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("got %d issues, want 1", len(issues))
	}
	for _, call := range []struct {
		name string
		args string
	}{
		{"read", `{"path":"secret.txt"}`},
		{"write", `{"path":"src/a.go"}`},
	} {
		if got, want := withIssues.Check(call.name, mustArgs(t, call.args)), policy.Check(call.name, mustArgs(t, call.args)); got != want {
			t.Fatalf("%s: ParsePolicyWithIssues = %v, PolicyFromJSON = %v", call.name, got, want)
		}
	}
}

func TestParsePolicyWithIssuesPreservesHardErrors(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name string
		data string
	}{
		{"malformed root", `{`},
		{"bad field type", `{"denyPaths":{"x":1}}`},
		{"malformed rules element", `{"rules":[123]}`},
		{"rules wrong shape", `{"rules":{}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy, issues, err := ParsePolicyWithIssues(root, []byte(tc.data))
			if err == nil {
				t.Fatal("malformed input must fail")
			}
			if policy != nil || issues != nil {
				t.Fatalf("a hard error must not return a policy or issues: policy=%v issues=%#v", policy, issues)
			}
		})
	}
}

func TestPolicyIssueFormatting(t *testing.T) {
	issue := PolicyIssue{
		Kind:   IssueKindInvalidDecision,
		Index:  2,
		Raw:    `{"decision":"x"}`,
		Reason: `invalid decision "x"`,
	}
	line := issue.String()
	if !strings.Contains(line, "rules[2]") || !strings.Contains(line, issue.Raw) {
		t.Fatalf("String() = %q, want index and raw rule", line)
	}
	all := FormatPolicyIssues([]PolicyIssue{issue})
	if all != line {
		t.Fatalf("FormatPolicyIssues() = %q, want %q", all, line)
	}
	if got := FormatPolicyIssues(nil); got != "" {
		t.Fatalf("FormatPolicyIssues(nil) = %q, want empty", got)
	}
}
