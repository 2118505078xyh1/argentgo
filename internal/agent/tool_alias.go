package agent

import (
	"fmt"
	"os"
	"strings"

	"example.com/argentgo/internal/llm"
)

// toolAliases 是跨 harness 的工具名别名表。内置 shell→bash、Bash→bash：
// 从 opencode/Claude Code 迁移来的模型常发 shell，而 argentgo 的注册名是 bash。
// 只有别名本身未注册且目标已注册时才替换，绝不覆盖真实工具。
var toolAliases = map[string]string{
	"shell": "bash",
	"Bash":  "bash",
}

// aliasOff 报告是否通过环境变量禁用别名回退（默认开启）。
func aliasOff() bool { return featureDisabled("ARGENT_TOOL_ALIAS") }

// aliasNote 返回一次性更正提示。它追加在工具输出末尾，告诉模型下次直接调用真实名字。
func aliasNote(alias, target string) string {
	return fmt.Sprintf("注意：没有 %q 工具，本次已按 %q 执行；后续请直接调用 %q。", alias, target, target)
}

// resolveToolAliases 在派发前改写调用名，返回改写后的调用副本与
// (callID → 原始别名) 映射，供执行结果的更正提示使用。
// available 是本次运行公布的真实工具名列表；没有目标工具时不做任何改写。
func resolveToolAliases(calls []llm.ToolCall, available []string) ([]llm.ToolCall, map[string]string) {
	if len(calls) == 0 || len(available) == 0 || aliasOff() {
		return calls, nil
	}
	registered := make(map[string]bool, len(available))
	for _, name := range available {
		registered[name] = true
	}
	var out []llm.ToolCall
	var aliased map[string]string
	for i, call := range calls {
		name := call.Function.Name
		target, ok := toolAliases[name]
		if !ok || registered[name] || !registered[target] {
			continue
		}
		if out == nil {
			out = make([]llm.ToolCall, len(calls))
			copy(out, calls)
		}
		if aliased == nil {
			aliased = make(map[string]string)
		}
		out[i].Function.Name = target
		aliased[call.ID] = name
	}
	if out == nil {
		return calls, nil
	}
	return out, aliased
}

// aliasEnabled 供诊断读取当前是否被环境变量关闭。
func aliasEnabled() bool {
	return strings.TrimSpace(os.Getenv("ARGENT_TOOL_ALIAS")) != "0"
}

// appendAliasNote 把一次性更正提示追加到工具输出末尾。
// 同一别名在一次运行里只提示一次；并行批次通过 aliasMu 串行检查。
func (s *runState) appendAliasNote(callID, output string) string {
	s.aliasMu.Lock()
	alias := s.aliasTargets[callID]
	if alias == "" || s.aliasNoted[alias] {
		s.aliasMu.Unlock()
		return output
	}
	s.aliasNoted[alias] = true
	s.aliasMu.Unlock()
	return output + "\n" + aliasNote(alias, toolAliases[alias])
}
