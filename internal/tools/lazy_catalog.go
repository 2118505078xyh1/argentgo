package tools

import (
	"os"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// 精简工具目录（默认自动启用，无需用户配置）：只向模型公布一小组核心工具 +
// tool_search，其余工具通过 tool_search 按需发现。决策交给模型自己——它需要
// 什么工具就搜什么，而不是让用户预先决定公布哪些。这把简单对话的工具 schema
// 开销从约 5800 tokens 降到约 1800 tokens，显著降低本地小模型的首 token 延迟。
//
// 设计取舍：用户零选择、随开随用。模型看到 tool_search 后会自行按名字/描述
// 发现并调用其它工具——tool_search 读取 Registry.Specs()（完整目录），
// Registry.Run 按名字查找，都不要求工具出现在已公布的列表里。
//
// 唯一的逃生舱 ARGENT_FULL_TOOLS=1：仅用于极少数需要一次性暴露全部 schema 的
// 调试/审计场景（例如检查工具描述是否合规）。这不是常规用户选项——默认行为
// 已经是最优，普通用户永远不需要设置它。
const fullToolsEnv = "ARGENT_FULL_TOOLS"

// lazyCoreTools 是始终公布的核心工具集。
// 选取标准：几乎每轮都可能用到，或需要让模型一上来就知道的基础文件/命令能力。
// 其余工具（web/git/lsp/memory/时间/仓库地图等）交给 tool_search 按需发现。
var lazyCoreTools = map[string]bool{
	"read":        true,
	"write":       true,
	"edit":        true,
	"bash":        true,
	"ls":          true,
	"grep":        true,
	"find":        true,
	"glob":        true,
	"todo":        true,
	"multi_edit":  true,
	"tool_search": true,
	// exit_plan/task 是条件启用的关键能力：它们只在 plan 模式 / 配置了子角色时
	// 才注册到注册表，且必须被模型一上来就看到——否则 plan 模式下模型看不到
	// 解锁工具会永远卡住，编排模式下看不到委派工具无法派活。平时它们不在
	// 注册表里，因此加入核心集不影响默认目录的精简。
	"exit_plan": true,
	"task":      true,
}

// fullToolsForced 报告是否强制公布全部工具（仅调试逃生舱）。每次调用读取环境。
func fullToolsForced() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(fullToolsEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// LazyFilterSpecs 收窄向模型公布的工具目录：默认只保留核心工具 + tool_search，
// 让模型自行发现其余工具；仅在显式设置 ARGENT_FULL_TOOLS 时原样返回全部。
// 授权始终发生在 Registry.Run，与公布层无关——被隐藏的工具若被模型显式调用，
// 仍会走完整的策略/审批/审计链。
func LazyFilterSpecs(specs []llm.ToolSpec) []llm.ToolSpec {
	if fullToolsForced() {
		return specs
	}
	kept := make([]llm.ToolSpec, 0, len(lazyCoreTools))
	for _, spec := range specs {
		if lazyCoreTools[spec.Function.Name] {
			kept = append(kept, spec)
		}
	}
	return kept
}
