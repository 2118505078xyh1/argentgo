package mcp

// assignUniqueToolNames 为一批工具分配唯一且符合长度约束的面向模型名字。
//
// 返回值以工具的原始带命名空间名字（即 server__tool，与 app 当前使用的可见名
// 形式一致）为键，映射到归一化后的唯一名字。实现复用 NormalizeToolName 的
// 字符转义、按 rune 边界截断与稳定哈希后缀逻辑，并在整批工具间共享 used
// 集合来消解重名：先到者保留基名，后到者获得由原始 server/tool 计算出的稳定后缀。
//
// 名字按 tools 中的顺序分配，因此相同输入总得到相同结果（确定性）；多字节字符
// （含 CJK）不会被截断到半个 rune。原始名字完全重复时只保留首次分配的结果——
// 这种工具靠名字本就无法区分，且调用方仍可通过 RawName 发起协议调用。
//
// 本函数是纯能力：它不修改任何 Tool，也不触碰传输或缓存。把它接入 app 层
// 可见名需要在 internal/app 的工具绑定处显式调用，且会改变模型看到的名字，
// 属于破坏字节兼容的行为，因此默认不接线。
func assignUniqueToolNames(tools []Tool) map[string]string {
	used := make(map[string]bool, len(tools))
	assigned := make(map[string]string, len(tools))
	for _, tool := range tools {
		name := tool.Name
		if name == "" {
			name = tool.RawName
		}
		original := Namespaced(tool.Server, name)
		if _, exists := assigned[original]; exists {
			continue
		}
		assigned[original] = normalizeToolName(tool.Server, name, used)
	}
	return assigned
}
