package archguard

// 本文件是「原语清单」门禁的数据侧，只被 primitive_guard_test.go 消费。
// 它刻意不放任何导出的函数/类型/变量：本门禁本身以 HEAD 为存量基线扫描
// 新增导出函数，若这里出现导出符号就会自我命中（self-flag）。

// primitiveAllowEntry 登记一个「保留但暂未接线」的原语。
//
// Symbol 支持两种写法：
//   - "NewThing"：按函数名匹配（跨包同名时适用范围较宽，仅用于确实无需区分包的场景）；
//   - "internal/foo/bar.go:NewThing"：按「相对仓库根路径:函数名」精确匹配（推荐）。
//
// Reason 必须非空，且必须说明「为什么保留、计划如何接线」。
type primitiveAllowEntry struct {
	Symbol string
	Reason string
}

// primitiveAllowlist 是「保留未启用」原语的登记簿。
//
// 规则：
//  1. 只有相对 primitiveBaseline 新增的导出函数才需要登记；HEAD 处已存在的
//     存量一律视为豁免（不回溯），因此当前列表为空是正常的。
//  2. 登记处必须写清理由：留待哪个阶段接线、将以何种方式接线（直接调用 /
//     CLI / MCP / RPC 反射等）。理由为空的条目不会生效，并由门禁单测报错。
//  3. 不要把用户可见的 opt-in 环境门控（optin_metadata 之类）当作内部原语
//     的逃生舱：那是给用户开关功能用的，往里塞内部原语会伪造 opt-in 语义。
//     内部原语「暂未接线」请登记在这里。
//  4. 接口方法、别名声明、链式调用等本身不是 FuncDecl，不会被判为新增原语；
//     RPC/反射按名字字符串注册的原语若被误报，可用本清单豁免，并在 Reason
//     中注明其反射注册点。
//
// 新增条目示例（取消注释并替换即可）：
//
//	var primitiveAllowlist = []primitiveAllowEntry{
//		{
//			Symbol: "internal/foo/future.go:NewFutureThing",
//			Reason: "为下一阶段插件 API 预留；将通过 internal/app 的 RPC 注册表按名字反射接入",
//		},
//	}
var primitiveAllowlist = []primitiveAllowEntry{
	// DAP 高层结构化 API：当前 debug_* 工具用裸 Call 转发以保留原始 DAP body 给
	// 模型；这些类型化方法供下一阶段"结构化调试工具"直接消费（并返回整理后的
	// Go 结构），握手方法（Initialize/Launch/ConfigurationDone）已生产接线。
	{Symbol: "internal/dap/client.go:Alive", Reason: "会话存活探测辅助方法；下一阶段 debug_start 复用会话时用于失效检测"},
	{Symbol: "internal/dap/session.go:Client", Reason: "会话底层 client 访问器；工具层当前用裸 Call 保留原始 DAP body，结构化阶段调用"},
	{Symbol: "internal/dap/session.go:SupportsConfigurationDone", Reason: "握手能力查询；工具层用原始命令序列，结构化握手阶段调用"},
	{Symbol: "internal/dap/session.go:Launch", Reason: "类型化 launch；工具层当前用裸 launch 命令，结构化阶段调用"},
	{Symbol: "internal/dap/session.go:ConfigurationDone", Reason: "类型化 configurationDone；同上，结构化阶段调用"},
	{Symbol: "internal/dap/session.go:Attach", Reason: "attach 握手分支；当前仅 launch 场景接线，下一阶段暴露 attach 调试工具时调用"},
	{Symbol: "internal/dap/session.go:SetBreakpoints", Reason: "类型化断点 API；工具层暂用裸 setBreakpoints 保留原始 body，下一阶段结构化工具调用"},
	{Symbol: "internal/dap/session.go:SetExceptionBreakpoints", Reason: "异常断点 API；下一阶段暴露异常断点工具时调用"},
	{Symbol: "internal/dap/session.go:Continue", Reason: "类型化 continue；工具层用裸命令，结构化工具阶段调用"},
	{Symbol: "internal/dap/session.go:StepIn", Reason: "类型化 stepIn；工具层用裸命令，结构化工具阶段调用"},
	{Symbol: "internal/dap/session.go:StepOut", Reason: "类型化 stepOut；工具层用裸命令，结构化工具阶段调用"},
	{Symbol: "internal/dap/session.go:Pause", Reason: "暂停 API；下一阶段暴露暂停工具时调用"},
	{Symbol: "internal/dap/session.go:Threads", Reason: "类型化 threads；工具层用裸命令保留原始 body，结构化阶段调用"},
	{Symbol: "internal/dap/session.go:StackTrace", Reason: "类型化 stackTrace；同上，结构化阶段调用"},
	{Symbol: "internal/dap/session.go:Scopes", Reason: "类型化 scopes；同上，结构化阶段调用"},
	{Symbol: "internal/dap/session.go:Variables", Reason: "类型化 variables；同上，结构化阶段调用"},
	{Symbol: "internal/dap/session.go:Disconnect", Reason: "类型化断开；当前 debug_stop 用裸 disconnect 命令，结构化阶段改用此方法"},
	{Symbol: "internal/dap/session.go:WaitStopped", Reason: "停止事件等待；下一阶段自动断点续跑流程调用"},
	{Symbol: "internal/dap/session.go:WaitTerminated", Reason: "终止事件等待；下一阶段调试生命周期编排调用"},
	// predict n-gram 补全原语：已完成纯模型层，ghost-text 接线需改 TUI 输入环热路径，
	// 按"外置优先/热路径谨慎"原则留到专门阶段，经补全 provider 注入。
	{Symbol: "internal/predict/predict.go:Order", Reason: "n-gram 阶数访问器；补全接线阶段用于诊断展示"},
	{Symbol: "internal/predict/predict.go:Predict", Reason: "补全预测主入口；ghost-text 接线阶段经补全 provider 调用"},
	{Symbol: "internal/predict/predict.go:SetMaxEntries", Reason: "模型容量旋钮；补全接线阶段由配置注入"},
	{Symbol: "internal/predict/predict.go:Train", Reason: "Learn 别名；补全接线阶段训练时调用"},
	// astedit 辅助：FindCalls 是调用点查询，ast_edit 工具当前用 RewriteSymbol；
	// 下一阶段"查找调用点的结构化搜索"工具消费。
	{Symbol: "internal/astedit/astedit.go:FindCalls", Reason: "调用点查询；下一阶段结构化搜索工具调用"},
	{Symbol: "internal/tools/ast_edit.go:SetAstEditWriter", Reason: "写盘助手注入点；下一阶段由 apply_patch 审批链注入真实 writer"},
}
