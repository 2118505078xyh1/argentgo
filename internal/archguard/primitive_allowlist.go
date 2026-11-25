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
	// 分页 watermark（#190/#198）：声明回放进度与跨版本边界的只读辅助。
	// 当前由测试锁定形状；计划由 /v1/event 续传头与 doctor 展示回放进度时接线。
	{Symbol: "internal/session/page.go:Watermark", Reason: "分页回放位置标记 API；计划由事件续传响应头消费，当前供测试与边界声明使用"},
	// TUI 键位冲突诊断：检测用户覆盖层与默认表的重名键。当前由配置预览/测试
	// 消费，尚未接入 doctor 的诊断输出；作为键位注册表地剂的公开诊断 API 保留。
	{Symbol: "internal/tui/actions.go:Conflict", Reason: "键位覆盖冲突诊断 API；计划由 doctor/配置预览展示重绑定冲突，当前供测试与未来配置诊断调用"},
	// 网络代理沙箱的显式环境注入口：默认路径由 netproxy.DefaultEnv 懒启动并
	// 直接把 Env 交给 bash 工具；WithEnv 供 app 层注入自定义代理地址/NO_PROXY，
	// 属于跨包依赖注入 API，由接线层调用，故在清单中登记。
	{Symbol: "internal/netproxy/env.go:WithEnv", Reason: "跨包 ctx 注入 API；由 app 层把自建代理的 Env 注入 shell 子进程调用链，默认路径走 DefaultEnv"},
	// 压缩摘要 V2 投影的渲染器：生产只读路径用 ParseStructuredSummaryV2/Compaction.StructuredV2，
	// RenderStructuredSummaryV2 仅用于 Render→Parse 往返测试校验格式稳定；
	// 下一阶段若要把扩展字段结构化落盘（生成式摘要模板）时由压缩写入路径调用。
	{Symbol: "internal/session/compact_fields.go:RenderStructuredSummaryV2", Reason: "结构化摘要模板渲染器；供生成式摘要扩展字段落盘阶段调用，当前只读消费端用 Parse 投影"},
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
	// LSP writethrough 的另外两个原语：WillRenameFiles 已接到 lsp_will_rename 工具；
	// watched files 需文件系统监听集成、workspace diagnostics 需诊断面板消费。
	{Symbol: "internal/lsp/writethrough.go:DidChangeWatchedFiles", Reason: "文件变更通知；下一阶段接入 FS 监听（fsnotify 风格）或编辑后主动通知时调用"},
	{Symbol: "internal/lsp/writethrough.go:WorkspaceDiagnostics", Reason: "工作区诊断；下一阶段接入 doctor/TUI 诊断面板时调用"},
	// 对标 pi-mono 引入的可重放执行/目录/扩展基建：均为可独立单测的纯原语。
	// 接线需要改 eval 编排、provider 刷新路径与会话生命周期等热路径，
	// 按"外置优先/热路径谨慎"原则留到专门阶段，按下列理由接入。
	{Symbol: "internal/pluginapi/txn.go:NewTxRegistry", Reason: "事务注册入口；下一阶段动态扩展加载时由 internal/app 用它包装现有 Registry"},
	{Symbol: "internal/pluginapi/txn.go:Commit", Reason: "原子提交；随动态扩展加载接线，批量生效失败时整批回滚"},
	{Symbol: "internal/pluginapi/txn.go:Discard", Reason: "失败丢弃；加载失败时由调用方丢弃待注册项"},
	{Symbol: "internal/provider/catalog_source.go:WriteCatalogAtomic", Reason: "目录原子落盘；下一阶段生成式模型目录编译时替换静态表文件"},
	{Symbol: "internal/provider/catalog_source.go:LoadCatalogStrict", Reason: "严格加载；同上，从磁盘读取权威目录时拒绝可疑数据"},
	{Symbol: "internal/provider/catalog_source.go:SourceFingerprint", Reason: "目录指纹；同上，供 catalog 缓存失效判断使用"},
	{Symbol: "internal/session/replay_env.go:EnvFingerprintText", Reason: "环境指纹文本；下一阶段 eval 报告输出环境标识时调用"},
	{Symbol: "internal/session/replay_env.go:NewVirtualClock", Reason: "虚拟时钟；下一阶段可重放 eval 注入时间源时调用"},
	{Symbol: "internal/session/replay_env.go:Advance", Reason: "虚拟时钟推进；同上，由重放脚本驱动时间前进"},
	{Symbol: "internal/session/replay_env.go:NewSeededRandom", Reason: "确定性随机源；同上，注入可重放种子"},
	{Symbol: "internal/session/replay_env.go:NewSeqCollector", Reason: "序号重排器；同上，让乱序到达的流结果按序号严格投递"},
	{Symbol: "internal/session/replay_env.go:Drain", Reason: "序号重排输出；同上，消费连续序号区间"},
	// 事件钩子常驻外部进程扩展（TODO #72）：接线点在 internal/app 的钩子桥接构造处，
	// 由父任务统一处理；此处登记避免「钩子包已就绪、应用尚未接线」期间的原语门禁误报。
	{Symbol: "internal/hooks/daemon.go:AttachDaemon", Reason: "常驻扩展挂载入口；将由 internal/app 的 newHookBridge 在构造 Dispatcher 后以 os.Getenv 注入调用，opt-in 关闭时为 no-op"},
	{Symbol: "internal/hooks/daemon.go:StopDaemon", Reason: "常驻扩展回收入口；将由 internal/app 在会话结束（SessionEnd 生命周期）处调用，与 AttachDaemon 成对"},
}
