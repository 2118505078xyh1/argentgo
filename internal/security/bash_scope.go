package security

import (
	"encoding/json"
	"strings"
)

// 本文件实现「始终允许」的 bash 分层通配作用域（#27）。沿用本包既有的自研
// shell 词法风格（strings.Fields 按空白切词），不引入任何外部依赖。

// maxBashScopeTokens 限制参与作用域生成的 token 数，避免超长命令把规则集
// 撑大；超出部分直接截断，仍只影响持久化 scope 文本，不影响命令执行。
const maxBashScopeTokens = 32

// bashHierarchicalScopes 按 token 数逐级生成通配前缀 + 精确命令：
// ["npm","install","lodash"] → ["npm *", "npm install *", "npm install lodash"]。
// BashArity 前缀与精确文本重合时（例如命令本身以 * 结尾）由 seen 去重。
func bashHierarchicalScopes(command string) []string {
	tokens := strings.Fields(command)
	if len(tokens) == 0 {
		return nil
	}
	if len(tokens) > maxBashScopeTokens {
		tokens = tokens[:maxBashScopeTokens]
	}
	seen := make(map[string]bool, len(tokens))
	out := make([]string, 0, len(tokens))
	add := func(scope string) {
		scope = strings.TrimSpace(scope)
		if scope == "" || seen[scope] {
			return
		}
		seen[scope] = true
		out = append(out, scope)
	}
	for i := 1; i < len(tokens); i++ {
		add(strings.Join(tokens[:i], " ") + " *")
	}
	add(strings.Join(tokens, " "))
	return out
}

// BashScopeFor 是 FileBackedScopedApprover 的 scopesFor 回调：bash/powershell
// 命令生成分层通配作用域；其他工具返回 nil，由审批器回落到「工具名」单值。
// 它只决定持久化的 scope 文本，不改变策略判定与审批流程。
func BashScopeFor(name, args string) []string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name != "bash" && name != "powershell" {
		return nil
	}
	command := chainShellCommand(name, json.RawMessage(args))
	if strings.TrimSpace(command) == "" {
		return nil
	}
	return bashHierarchicalScopes(command)
}
