package main

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// 审批三态。engine 的 approval.answer RPC 只接受 {id, granted}（见
// internal/server/rpc.go），没有"作用域"字段。因此这里把作用域做成 native
// 侧的决策层：允许一次 = 仅回答当前调用；始终允许此命令 = 记住命令模式，
// 后续同类请求由 native 自动回答；拒绝可附理由，理由随 RPC 透传供审计，
// engine 会忽略未知字段。文案必须据此诚实描述，不承诺 engine 不支持的语义。
type approvalDecision uint8

const (
	approvalAllowOnce   approvalDecision = iota // 仅此调用不再询问
	approvalAllowAlways                         // 记住命令模式，后续同类请求自动允许
	approvalDeny                                // 拒绝，可附理由
)

// approvalChoice 是用户在审批卡上的完整决定。Reason 仅对拒绝有意义。
type approvalChoice struct {
	Decision approvalDecision
	Reason   string
}

func allowOnceChoice() approvalChoice { return approvalChoice{Decision: approvalAllowOnce} }
func allowAlwaysChoice() approvalChoice {
	return approvalChoice{Decision: approvalAllowAlways}
}
func denyChoice(reason string) approvalChoice {
	return approvalChoice{Decision: approvalDeny, Reason: strings.TrimSpace(reason)}
}

// label 返回展示用的简短中文标签（渲染时经 tr 翻译）。
func (c approvalChoice) label() string {
	switch c.Decision {
	case approvalAllowAlways:
		return tr("始终允许此命令")
	case approvalDeny:
		return tr("拒绝")
	default:
		return tr("允许一次")
	}
}

// scopeNote 返回对该决定作用范围的诚实描述，避免把"允许一次"说成"本会话允许"。
func (c approvalChoice) scopeNote() string {
	switch c.Decision {
	case approvalAllowAlways:
		return tr("本机持续有效，可在设置中撤销")
	default:
		return tr("仅此调用不再询问")
	}
}

// approvalScopePattern 生成"此命令"的稳定标识。命令类工具用规范化后的
// 整条命令；文件类工具用工具名 + 目标路径，使文件内容的每次改动不影响
// 同一条授权。返回空串表示无法归纳（此时不提供"始终允许"）。
func approvalScopePattern(tool, args string) string {
	tool = strings.TrimSpace(tool)
	if tool == "" {
		return ""
	}
	switch tool {
	case "write", "write_file", "edit", "multi_edit", "apply_patch":
		if path := approvalArgsPath(tool, args); path != "" {
			return tool + "|" + path
		}
	}
	key := normalizeCommand(args)
	if key == "" {
		return tool
	}
	return tool + "|" + key
}

// approvalArgsPath 尽力从工具参数中取目标路径；取不到时返回空串。
func approvalArgsPath(tool, args string) string {
	var a struct {
		Path  string `json:"path"`
		Input string `json:"input"`
		Patch string `json:"patch"`
	}
	if json.Unmarshal([]byte(args), &a) == nil {
		if a.Path != "" {
			return a.Path
		}
		text := a.Input
		if text == "" {
			text = a.Patch
		}
		if tool == "apply_patch" {
			for _, line := range strings.Split(text, "\n") {
				for _, prefix := range []string{"*** Add File:", "*** Delete File:", "*** Update File:"} {
					if s, ok := cutPrefix(strings.TrimSpace(line), prefix); ok {
						if p := strings.TrimSpace(s); p != "" {
							return p
						}
					}
				}
			}
		}
	}
	return ""
}

// cutPrefix 是 strings.CutPrefix 的小包装，避免为一次前缀判断分配切片。
func cutPrefix(s, prefix string) (string, bool) {
	if !strings.HasPrefix(s, prefix) {
		return "", false
	}
	return s[len(prefix):], true
}

// normalizeCommand 折叠空白并去掉首尾空白，使多行/多空格的同一命令得到
// 同一模式。它只影响"始终允许"的归纳粒度，不改变发给 engine 的参数。
func normalizeCommand(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// approvalScopeLabel 返回"始终允许"影响范围的展示文案，供二次确认使用。
func approvalScopeLabel(pattern string) string {
	parts := strings.SplitN(pattern, "|", 2)
	key := pattern
	if len(parts) == 2 {
		key = parts[1]
	}
	if utf8.RuneCountInString(key) > 48 {
		key = truncateRunes(key, 48) + "…"
	}
	return tr("始终允许的影响范围：") + parts[0] + " " + key
}

// approvalIsDangerous 用一组保守启发式识别高危请求。它只决定默认光标落点，
// 绝不阻止用户显式允许；命中时审批卡默认高亮"拒绝"（对齐 Codex / crush）。
func approvalIsDangerous(tool, args string) bool {
	cmd := strings.ToLower(normalizeCommand(args))
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "bash", "shell", "powershell", "cmd", "exec_command":
		for _, pattern := range []string{
			"rm -rf /", "rm -fr /", "sudo ", "chmod 777", "mkfs", "dd if=",
			"| sh", "| bash", "| zsh", "curl ", "wget ", "git push --force",
			"git reset --hard", "del /f /s /q", "remove-item -recurse -force",
			"format ", "shutdown ", "reg delete",
		} {
			if strings.Contains(cmd, pattern) {
				return true
			}
		}
	case "write", "write_file", "edit", "multi_edit", "apply_patch":
		for _, pattern := range []string{
			".ssh/", ".aws/", "id_rsa", "authorized_keys", "/etc/", ".env",
			"credentials", "secrets/", "passwd",
		} {
			if strings.Contains(strings.ToLower(args), pattern) {
				return true
			}
		}
	}
	return false
}

// approvalDefaultDecision 返回审批卡的默认决定：危险请求默认拒绝，
// 其余默认允许一次。渲染器据此把默认光标/主按钮放在对应项上。
func approvalDefaultDecision(tool, args string) approvalDecision {
	if approvalIsDangerous(tool, args) {
		return approvalDeny
	}
	return approvalAllowOnce
}

// approvalDefaultIsDeny 是 approvalDefaultDecision 的便捷判定，供 main.go
// 决定是否把"拒绝"设为 PrimaryButton 并默认聚焦。
func approvalDefaultIsDeny(tool, args string) bool {
	return approvalDefaultDecision(tool, args) == approvalDeny
}

// approvalShouldAutoGrant 报告同类命令是否已被"始终允许"。main.go 在收到
// 新的审批请求时调用它；命中时可跳过询问直接以 allowOnce 回答当前调用。
func approvalShouldAutoGrant(prefs nativePrefs, tool, args string) bool {
	pattern := approvalScopePattern(tool, args)
	return pattern != "" && prefs.HasAllow(pattern)
}

// approvalAnswerParams 构造 approval.answer 的 RPC 参数。id 与 granted 是
// engine 唯一识别的字段；reason 是 engine 会忽略的附加信息，用于审计与
// 审批卡回显。允许一次与始终允许在 RPC 层一致，区别由 native 侧的记住逻辑
// 承担，因此这里不虚构 engine 不支持的 scope 字段。
func approvalAnswerParams(id string, choice approvalChoice, tool, args string) map[string]any {
	params := map[string]any{
		"id":      id,
		"granted": choice.Decision != approvalDeny,
	}
	if choice.Decision == approvalDeny && choice.Reason != "" {
		params["reason"] = choice.Reason
	}
	return params
}

// commitApprovalChoice 在做出"始终允许此命令"决定后持久化命令模式；
// 其他决定不改动允许列表。调用方负责把错误显示给用户（偏好写失败不影响
// 本次审批已发出的回答）。
func commitApprovalChoice(home, tool, args string, choice approvalChoice) error {
	if choice.Decision != approvalAllowAlways {
		return nil
	}
	pattern := approvalScopePattern(tool, args)
	if pattern == "" {
		return nil
	}
	return rememberAllowPattern(home, pattern)
}
