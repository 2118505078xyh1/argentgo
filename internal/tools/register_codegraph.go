package tools

// 本文件负责 codegraph_query 的 opt-in 注册与索引注入。
//
// 与 grep_ex 一样，codegraph_query 刻意不进入 builtin_register.go 的
// 静态注册表，也不被 New/NewWithLimit 调用，因此默认 Specs()/Names()
// 与 golden 快照逐字节不变。只有显式调用 RegisterCodeGraph 才会把它
// 加入某个 Registry；随后由 SetCodeGraph 注入具体的代码索引适配器。

// RegisterCodeGraph 把 codegraph_query 显式追加到 r 的末尾（opt-in）。
// 它是幂等的：重复调用或已存在同名工具时不再插入；既有工具顺序不变。
// 传入 nil 是空操作。本函数绝不被 New/NewWithLimit 调用。
func RegisterCodeGraph(r *Registry) {
	if r == nil {
		return
	}
	const name = codegraphToolName
	if _, exists := r.tools[name]; exists {
		return
	}
	r.tools[name] = codegraphTool{}
	r.order = append(r.order, name)
}

// SetCodeGraph 安装 codegraph_query 工具背后的索引适配器。
// 遵循 SetLspIde 的原位置重建模式：注册顺序与公布的 spec 保持稳定。
// 若尚未调用 RegisterCodeGraph，这里会先完成注册，使注入即可用。
// nil 适配器让工具报告 "codegraph is not available" 而非返回错误。
func (r *Registry) SetCodeGraph(cg CodeGraph) {
	if r == nil {
		return
	}
	const name = codegraphToolName
	if _, exists := r.tools[name]; !exists {
		r.tools[name] = codegraphTool{}
		r.order = append(r.order, name)
	}
	r.tools[name] = codegraphTool{cg: cg}
	r.wrapSchemas()
}
