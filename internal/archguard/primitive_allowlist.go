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
var primitiveAllowlist = []primitiveAllowEntry{}
