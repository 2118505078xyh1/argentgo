package tools

import (
	"github.com/2118505078xyh1/argentgo/internal/memory"
	"github.com/2118505078xyh1/argentgo/internal/pluginapi"
)

// builtinFactory 构造一个内置工具实例。bank 是该次运行的初始记忆库，
// 仅记忆类工具使用；其余构造器忽略它。
//
// 注册的是构造器而非实例：每次 New/NewWithLimit 都会重新实例化一份，
// 保持"每运行一份注册表"的安全语义，也让工具实现不持有跨运行的共享状态。
type builtinFactory func(bank *memory.Bank) Tool

// builtins 保存内置工具的编译期静态注册。
//
// order 显式给出且按 10 递增：Specs() 的顺序被 golden 快照、eval 场景与
// 多个测试逐字节锁定，绝不能依赖 Go 跨包 init 执行顺序来决定注册次序。
// 需要尾部插入时利用 10 的间隔，无需重排已有条目。
var builtins = pluginapi.Registry[builtinFactory]{}

// registerBuiltin 把构造器包成 pluginapi 要求的 func() 工厂后注册。
// 构造器本身无状态，共享同一个值安全；真正的实例化发生在
// builtinTools 中每次调用构造器时。
func registerBuiltin(name string, order int, factory builtinFactory) {
	builtins.MustRegister(name, order, func() builtinFactory { return factory })
}

func init() {
	// 顺序与旧版 NewWithLimit 中的 []Tool 字面量逐项对应，
	// 确保 Names()/Specs() 输出与引入注册表之前完全一致。
	registerBuiltin("read", 10, func(*memory.Bank) Tool { return readTool{} })
	registerBuiltin("write", 20, func(*memory.Bank) Tool { return writeTool{} })
	registerBuiltin("edit", 30, func(*memory.Bank) Tool { return editTool{} })
	registerBuiltin("bash", 40, func(*memory.Bank) Tool { return bashTool{} })
	registerBuiltin("ls", 50, func(*memory.Bank) Tool { return lsTool{} })
	registerBuiltin("grep", 60, func(*memory.Bank) Tool { return grepTool{} })
	registerBuiltin("find", 70, func(*memory.Bank) Tool { return findTool{} })
	registerBuiltin("powershell", 80, func(*memory.Bank) Tool { return powershellTool{} })
	registerBuiltin("glob", 90, func(*memory.Bank) Tool { return globTool{} })
	registerBuiltin("todo", 100, func(*memory.Bank) Tool { return todoTool{} })
	registerBuiltin("multi_edit", 110, func(*memory.Bank) Tool { return multiEditTool{} })
	registerBuiltin("web_fetch", 120, func(*memory.Bank) Tool { return webFetchTool{} })
	registerBuiltin("git_status", 130, func(*memory.Bank) Tool { return gitStatusTool{} })
	registerBuiltin("current_time", 140, func(*memory.Bank) Tool { return currentTimeTool{} })
	registerBuiltin("web_search", 150, func(*memory.Bank) Tool { return webSearchTool{} })
	registerBuiltin("repo_map", 160, func(*memory.Bank) Tool { return repoMapTool{} })
	registerBuiltin("submit_plan", 170, func(*memory.Bank) Tool { return submitPlanTool{} })

	// LSP IDE 工具始终被公布。在 app 接线适配器之前它们报告"不可用"。
	// 它们位于记忆工具之前，使 memory_* 尾部仍与末尾 tool_search 相邻。
	registerBuiltin("lsp_symbols", 180, func(*memory.Bank) Tool { return newLspSymbolsTool(nil) })
	registerBuiltin("lsp_references", 190, func(*memory.Bank) Tool { return newLspReferencesTool(nil) })
	registerBuiltin("lsp_code_actions", 200, func(*memory.Bank) Tool { return newLspCodeActionsTool(nil) })
	registerBuiltin("lsp_rename_preview", 210, func(*memory.Bank) Tool { return newLspRenamePreviewTool(nil) })
	registerBuiltin("lsp_format", 220, func(*memory.Bank) Tool { return newLspFormatTool(nil) })
	registerBuiltin("lsp_workspace_symbols", 230, func(*memory.Bank) Tool { return newLspWorkspaceSymbolsTool(nil) })

	registerBuiltin("security_scan", 240, func(*memory.Bank) Tool { return securityScanTool{} })

	// 可移植的效率工具。usage/history 以 nil 数据源开始，
	// 在 app 调用 SetUsageSource/SetHistorySource 之前报告不可用。
	registerBuiltin("diff", 250, func(*memory.Bank) Tool { return &diffTool{} })
	registerBuiltin("usage", 260, func(*memory.Bank) Tool { return &usageTool{} })
	registerBuiltin("history", 270, func(*memory.Bank) Tool { return &historyTool{} })

	registerBuiltin("memory_retain", 280, func(b *memory.Bank) Tool { return newMemoryRetainTool(b) })
	registerBuiltin("memory_recall", 290, func(b *memory.Bank) Tool { return newMemoryRecallTool(b) })
	registerBuiltin("memory_forget", 300, func(b *memory.Bank) Tool { return newMemoryForgetTool(b) })
}

// builtinTools 按注册顺序实例化所有基础内置工具。
// context_pack、高级记忆工具与 tool_search 因依赖 Registry 本身，
// 仍由 NewWithLimit 在其后按既有逻辑安装。
func builtinTools(bank *memory.Bank) []Tool {
	out := make([]Tool, 0, builtins.Len())
	for _, factory := range builtins.Factories() {
		out = append(out, factory()(bank))
	}
	return out
}
