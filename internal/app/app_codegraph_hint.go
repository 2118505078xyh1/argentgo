package app

// 本文件提供 codegraph 能力提示与集中 opt-in 判定原语。
// 提示是选择性注入的：默认关闭时完全不追加，系统提示逐字节不变。

// codeGraphCapabilityHint 返回一行中文提示，告知模型可用 codegraph_query 查询符号的
// 定义与引用位置，并说明该能力是本地静态分析、只读、结果附源码位置与提取规则。
// 它是纯函数，返回值稳定，便于在字节断言中复用。
func codeGraphCapabilityHint() string {
	return "可用 codegraph_query(name) 查询符号定义与引用位置（本地静态分析、只读，结果附源码位置与提取规则）。"
}

// codeGraphHintEnabled 判定 codegraph opt-in 是否开启。它从统一的 OptInRegistry
// 读取状态——该注册表由 mergeOptIns(ParseOptInEnv(env), configOptIns(cfg)) 构造，
// 因此环境变量与配置文件的激活路径完全一致（逻辑或）。reg 为 nil 或键未设置时
// Enabled 返回 false（fail-closed），使未显式开启的系统提示与接线前逐字节相同。
func codeGraphHintEnabled(reg *OptInRegistry) bool {
	return reg != nil && reg.Enabled(OptInCodeGraph)
}
