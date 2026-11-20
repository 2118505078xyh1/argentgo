package security

import (
	"encoding/json"
	"fmt"
	"strings"
)

// IssueKindInvalidDecision 标记因决定缺失或不是可识别的判定而被跳过的规则。
const IssueKindInvalidDecision = "invalid_decision"

// PolicyIssue 描述解析策略时发现的一个问题。格式错误的规则会被跳过 —— 未知的
// 判定永远不会执行 —— 但记录该跳过可防止拼错的 deny 在没有任何可观察信号的
// 情况下悄悄扩大实际生效的策略。
type PolicyIssue struct {
	// Kind 是稳定的机器可读类别，例如 IssueKindInvalidDecision。
	Kind string
	// Index 是违规规则在 rules 数组中从零开始的位置。
	Index int
	// Raw 是违规规则的原始 JSON，供操作员诊断。
	Raw string
	// Reason 是该问题的人类可读说明。
	Reason string
}

// String 把一个问题渲染到单行。
func (i PolicyIssue) String() string {
	return fmt.Sprintf("security: rules[%d]: %s: %s (rule %s)", i.Index, i.Kind, i.Reason, i.Raw)
}

// FormatPolicyIssues 把每个问题渲染为一行，没有问题时返回空字符串。它让调用方
// 可以在日志或启动警告中呈现被跳过的规则，而无需改变 PolicyFromJSON 的签名。
func FormatPolicyIssues(issues []PolicyIssue) string {
	if len(issues) == 0 {
		return ""
	}
	var b strings.Builder
	for i, issue := range issues {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(issue.String())
	}
	return b.String()
}

// ParsePolicyWithIssues 解析设置文件的 `permissions` 块，并连同策略一起返回所有
// 不得不跳过的规则。JSON 形状和字段处理与 PolicyFromJSON 完全相同；唯一的新增
// 是诊断切片。忽略该切片的调用方完全保持原有行为。
//
// 判定未知或缺失的规则不会被执行（失败即拒绝：无法识别的判定既不授权也不拒绝
// 任何东西）。它会作为 IssueKindInvalidDecision 问题报告，这样像 "denyy" 这样的
// 拼写错误不再被悄悄丢弃。
func ParsePolicyWithIssues(root string, data []byte) (*Policy, []PolicyIssue, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("security: parsing policy: %w", err)
	}
	var cfg PolicyConfig
	var err error
	fields := []struct {
		camel string
		snake string
		into  *[]string
	}{
		{"denyPaths", "deny_paths", &cfg.DenyPaths},
		{"allowPaths", "allow_paths", &cfg.AllowPaths},
		{"denyCommands", "deny_commands", &cfg.DenyCommands},
		{"denyCommandPaths", "deny_command_paths", &cfg.DenyCommandPaths},
		{"denyTools", "deny_tools", &cfg.DenyTools},
		{"allowTools", "allow_tools", &cfg.AllowTools},
		{"readOnlyTools", "read_only_tools", &cfg.ReadOnlyTools},
		{"mutatingTools", "mutating_tools", &cfg.MutatingTools},
		{"writeRoots", "write_roots", &cfg.WriteRoots},
	}
	for _, field := range fields {
		value, ok := raw[field.camel]
		if !ok {
			value, ok = raw[field.snake]
		}
		if ok {
			if *field.into, err = stringList(value); err != nil {
				return nil, nil, fmt.Errorf("security: policy field %q: %w", field.camel, err)
			}
		}
	}
	var issues []PolicyIssue
	if value, ok := raw["rules"]; ok {
		var ruleIssues []PolicyIssue
		if cfg.Rules, ruleIssues, err = parseRulesWithIssues(value); err != nil {
			return nil, nil, fmt.Errorf("security: policy rules: %w", err)
		}
		issues = append(issues, ruleIssues...)
	}
	return NewPolicy(root, cfg), issues, nil
}

// parseRulesWithIssues 解码 rules 数组，同时保留每个元素的原始字节，以便被跳过
// 的规则可以逐字报告。结构上格式错误的元素仍会使整个解析失败，与之前一步就
// 反序列化数组的行为一致。
func parseRulesWithIssues(raw json.RawMessage) ([]Rule, []PolicyIssue, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, nil, err
	}
	rules := make([]Rule, 0, len(items))
	var issues []PolicyIssue
	for index, item := range items {
		var parsed struct {
			Decision string `json:"decision"`
			Tool     string `json:"tool"`
			Path     string `json:"path"`
			Command  string `json:"command"`
		}
		if err := json.Unmarshal(item, &parsed); err != nil {
			return nil, nil, err
		}
		decision, ok := ParseDecision(parsed.Decision)
		if !ok {
			issues = append(issues, PolicyIssue{
				Kind:   IssueKindInvalidDecision,
				Index:  index,
				Raw:    string(item),
				Reason: fmt.Sprintf("invalid decision %q (expected allow, ask, or deny); rule skipped", parsed.Decision),
			})
			continue
		}
		rules = append(rules, Rule{
			Decision: decision,
			Tool:     strings.TrimSpace(parsed.Tool),
			Path:     strings.TrimSpace(parsed.Path),
			Command:  strings.TrimSpace(parsed.Command),
		})
	}
	return rules, issues, nil
}
