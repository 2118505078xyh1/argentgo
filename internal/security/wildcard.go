package security

import (
	"path"
	"strings"
)

// WildcardMatch 报告 subject 是否匹配某个权限规则模式。
//
// 匹配采用 path.Match 语义：'*' 匹配任意一串非分隔符字符，'?' 匹配一个
// 非分隔符字符，'[abc]' 是字符类。为方便起见，以 '*' 结尾的模式还会通过
// 前缀比较跨分隔符匹配，因此 "read*" 同时允许 "read" 和 "read/file.txt"。
// 格式错误的模式永远不会匹配。
func WildcardMatch(pattern, subject string) bool {
	// path.Match 总是用 '/' 作为分隔符，并把 '\' 当作转义符，即使在 '\' 正是
	// 分隔符的 Windows 上也是如此。把两侧都规范化为 '/'
	// （与 GlobMatch 使用的跨平台规范化相同），使规则和用 Windows 分隔符书写的
	// 路径能够按结构比较，而不是把其中的 '\' 误读为转义符。
	p := strings.ReplaceAll(pattern, `\`, "/")
	s := strings.ReplaceAll(subject, `\`, "/")
	matched, err := path.Match(p, s)
	if err != nil {
		// 格式错误的模式永远不会匹配——即使通过下面的前缀回退也不行。
		// 否则像 "[*" 这样的拼写错误仍会匹配 "[anything]"，
		// 悄悄地把一条规则变成意料之外的允许。
		return false
	}
	if matched {
		return true
	}
	if strings.HasSuffix(p, "*") {
		prefix := strings.TrimRight(p, "*")
		if prefix == "" {
			return true // an all-star pattern permits any subject
		}
		return strings.HasPrefix(s, prefix)
	}
	return false
}

// WildcardRule 是有序权限列表中的一项。Effect 通常是 Allow 或 Deny；
// 任何其他值（包括 Ask）都被当作 Ask。
type WildcardRule struct {
	Pattern string
	Effect  Decision
}

// LastMatchDecision 按顺序评估规则，返回最后一条匹配规则的 effect。
// 没有规则匹配时返回 Ask，使未列出的主体绝不会悄悄获得访问权限。
func LastMatchDecision(rules []WildcardRule, subject string) Decision {
	result := Ask
	for _, rule := range rules {
		if WildcardMatch(rule.Pattern, subject) {
			result = rule.Effect
		}
	}
	return result
}

// DenyWinsDecision 的行为与 LastMatchDecision 类似，但匹配的 deny 规则
// 无论出现顺序如何都优先于匹配的 allow 规则。对于 deny 具有绝对性的权限列表，
// 这是安全的默认选择。
func DenyWinsDecision(rules []WildcardRule, subject string) Decision {
	result := Ask
	denied := false
	for _, rule := range rules {
		if !WildcardMatch(rule.Pattern, subject) {
			continue
		}
		if rule.Effect == Deny {
			denied = true
		}
		result = rule.Effect
	}
	if denied {
		return Deny
	}
	return result
}
