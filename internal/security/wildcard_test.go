package security

import "testing"

func TestWildcardMatch(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		subject string
		want    bool
	}{
		{"exact", "read", "read", true},
		{"exact mismatch", "read", "readme", false},
		{"star within segment", "read*", "readme", true},
		{"star suffix crosses separator", "read*", "read/file.txt", true},
		{"path match star", "src/*.go", "src/main.go", true},
		{"path match star does not cross separator", "src/*.go", "src/sub/main.go", false},
		{"globstar suffix", "**", "a/b/c", true},
		{"question mark", "file?.txt", "file1.txt", true},
		{"character class", "file[12].txt", "file2.txt", true},
		{"command prefix", "git push*", "git push --force", true},
		{"malformed pattern never matches", "[", "anything", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WildcardMatch(tc.pattern, tc.subject); got != tc.want {
				t.Errorf("WildcardMatch(%q, %q) = %v, want %v", tc.pattern, tc.subject, got, tc.want)
			}
		})
	}
}

func TestLastMatchDecision(t *testing.T) {
	rules := []WildcardRule{
		{Pattern: "*", Effect: Allow},
		{Pattern: "bash", Effect: Deny},
		{Pattern: "bash", Effect: Allow}, // 后面的 allow 取代 deny
	}
	if got := LastMatchDecision(rules, "bash"); got != Allow {
		t.Errorf("bash = %v, want Allow (last match wins)", got)
	}
	if got := LastMatchDecision(rules, "write"); got != Allow {
		t.Errorf("write = %v, want Allow from the wildcard", got)
	}
	if got := LastMatchDecision(rules, "grep"); got != Allow {
		t.Errorf("grep = %v, want Allow from the wildcard", got)
	}
	if got := LastMatchDecision(nil, "anything"); got != Ask {
		t.Errorf("no rules = %v, want Ask", got)
	}
}

func TestDenyWinsDecision(t *testing.T) {
	rules := []WildcardRule{
		{Pattern: "*", Effect: Allow},
		{Pattern: "bash", Effect: Deny},
		{Pattern: "bash", Effect: Allow}, // 尽管后面有 allow，deny 仍然胜出
	}
	if got := DenyWinsDecision(rules, "bash"); got != Deny {
		t.Errorf("bash = %v, want Deny (deny wins)", got)
	}
	if got := DenyWinsDecision(rules, "write"); got != Allow {
		t.Errorf("write = %v, want Allow", got)
	}

	// 没有匹配的 deny 时，最后一条匹配的规则仍然决定结果。
	allows := []WildcardRule{
		{Pattern: "*", Effect: Allow},
		{Pattern: "read", Effect: Ask},
		{Pattern: "read", Effect: Allow},
	}
	if got := DenyWinsDecision(allows, "read"); got != Allow {
		t.Errorf("read = %v, want Allow when no deny matches", got)
	}
}
