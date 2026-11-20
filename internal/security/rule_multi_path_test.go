package security

import "testing"

// TestRuleDenyOnPathsArrayNotMaskedByAskOnScalarPath 复现"ask 掩盖 deny"症状背后的
// 多资源聚合漏洞：针对 checkpoint `paths` 数组某一条目的基于路径的 deny 规则过去
// 对 ruleDecision 不可见，因为它只扫描标量路径参数。于是匹配另一个标量路径的 ask
// 规则胜出，尽管 deny 规则覆盖了同一次调用的另一个资源。无论哪个参数携带该资源，
// deny 都必须是绝对的。
func TestRuleDenyOnPathsArrayNotMaskedByAskOnScalarPath(t *testing.T) {
	policy := NewPolicy(t.TempDir(), PolicyConfig{Rules: []Rule{
		{Decision: Ask, Tool: "write", Path: "safe.txt"},
		{Decision: Deny, Tool: "write", Path: "**/secret/**"},
	}})
	// "safe.txt" 是标量 `path`；"secret/key" 是 `paths` 数组的一个条目。
	// 两者都是同一次调用的资源，因此 deny 必须胜出。
	args := mustArgs(t, `{"path":"safe.txt","paths":["secret/key"]}`)
	if got := policy.Check("write", args); got != Deny {
		t.Fatalf("got %v, want deny: a deny rule on a paths-array entry must not be masked by an ask on a scalar path", got)
	}
}

// TestRuleDenyPathMatchesPathsArrayEntry 覆盖较简单的情况：路径规则必须应用到每个
// 路径资源，包括 checkpoint 恢复使用的 `paths` 数组。修复前，模式只匹配数组条目的
// deny 规则会被跳过，修改类工具便回退到它的 Ask。
func TestRuleDenyPathMatchesPathsArrayEntry(t *testing.T) {
	policy := NewPolicy(t.TempDir(), PolicyConfig{Rules: []Rule{
		{Decision: Deny, Tool: "write", Path: "**/secret/**"},
	}})
	args := mustArgs(t, `{"paths":["docs/secret/key.txt"]}`)
	if got := policy.Check("write", args); got != Deny {
		t.Fatalf("got %v, want deny: path rules must match entries of the paths array too", got)
	}
}

// TestRuleAskPathMatchesPathsArrayEntry 守护非 deny 方向，使修复不会意外地只对 deny
// 规则特殊处理：当没有标量路径匹配时，针对数组条目的 ask 规则也必须被遵守。
func TestRuleAskPathMatchesPathsArrayEntry(t *testing.T) {
	policy := NewPolicy(t.TempDir(), PolicyConfig{Rules: []Rule{
		{Decision: Ask, Tool: "read", Path: "**/secret/**"},
	}})
	// 只读工具默认 Allow，因此这里的 Ask 只能来自匹配数组条目的规则
	// （而不是工具类型的回退）。
	args := mustArgs(t, `{"paths":["docs/secret/key.txt"]}`)
	if got := policy.Check("read", args); got != Ask {
		t.Fatalf("got %v, want ask: path rules must match entries of the paths array too", got)
	}
}
