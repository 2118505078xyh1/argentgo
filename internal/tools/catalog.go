package tools

import (
	"encoding/json"

	"example.com/argentgo/internal/llm"
	"example.com/argentgo/internal/security"
)

// CatalogPolicy 是安全策略中用于判断工具是否可被公布所需的最小切片。
// *security.Policy 满足它。nil 的
// CatalogPolicy 表示“没有拒绝项”。
//
// 可见性只是对模型的礼貌提示：Run 仍会走完整的
// 守卫链，因此一个被过滤掉却仍被调用的工具会在那里被
// 拒绝。检查使用空参数：依赖特定
// 路径或命令的拒绝规则，无法隐藏用其他参数调用时合法的工具。
type CatalogPolicy interface {
	Check(name string, args json.RawMessage) security.Decision
}

// SpecsFor 返回模型在策略与沙箱模式下应看到的工具 schema，
// 按稳定的注册顺序。只读模式会丢弃具有变更性的
// 工具；策略会丢弃被无条件拒绝的工具。
func (r *Registry) SpecsFor(policy CatalogPolicy, mode security.SandboxMode) []llm.ToolSpec {
	specs := make([]llm.ToolSpec, 0, len(r.order))
	for _, name := range r.order {
		if !r.ToolAllowed(policy, mode, name) {
			continue
		}
		tool := r.tools[name]
		spec := llm.ToolSpec{Type: "function"}
		spec.Function.Name = tool.Name()
		// 与 Specs() 保持一致：每一条跨越传输层的目录路径都必须截断
		// 过长的描述，并公布 `i` 意图字段。仅依赖这里的
		// wrapSchemas 装饰器是不够的 —— 由 Set* 助手重建的工具
		// （usage/history/checkpoint_diff）不会被包装，因此
		// 过滤目录曾悄悄地为它们丢掉这两项保证。
		// advertiseIntentSchema 是幂等的，所以已被包装的工具会
		// 原样返回。
		spec.Function.Description = clipDescription(tool.Description())
		spec.Function.Parameters = advertiseIntentSchema(json.RawMessage(tool.Schema()))
		specs = append(specs, spec)
	}
	return specs
}

// ToolAllowed 报告在给定策略和模式下 name 是否可被公布。
// 未知名称不是注册表中的工具，因此 SpecsFor 绝不公布它们；
// FilterSpecs 对调用方提供的 spec 采用更宽松的规则。
func (r *Registry) ToolAllowed(policy CatalogPolicy, mode security.SandboxMode, name string) bool {
	if _, known := r.tools[name]; !known {
		return false
	}
	return visibilityAllowed(policy, mode, name, func() bool {
		return r.tools[name].Mutating()
	})
}

// FilterSpecs 按与 SpecsFor 相同的策略和沙箱规则收窄任意 spec 列表
// （内置工具、MCP 工具、注入的技能）。注册表不认识的
// 名称会依据策略和内置的变更性名称表进行检查，
// 因此一个名为 "bash" 的 MCP 工具无法在只读模式下蒙混过关。
func (r *Registry) FilterSpecs(policy CatalogPolicy, mode security.SandboxMode, specs []llm.ToolSpec) []llm.ToolSpec {
	out := make([]llm.ToolSpec, 0, len(specs))
	for _, spec := range specs {
		name := spec.Function.Name
		mutating := func() bool {
			if tool, known := r.tools[name]; known {
				return tool.Mutating()
			}
			return security.MutatingTool(name)
		}
		if visibilityAllowed(policy, mode, name, mutating) {
			out = append(out, spec)
		}
	}
	return out
}

// visibilityAllowed 是共用的判定：只读模式拒绝一切已知
// 具有变更性的操作，而无参数的策略拒绝会直接否决该工具。
func visibilityAllowed(policy CatalogPolicy, mode security.SandboxMode, name string, mutating func() bool) bool {
	if mode == security.SandboxReadOnly && mutating() {
		return false
	}
	if policy != nil && policy.Check(name, nil) == security.Deny {
		return false
	}
	return true
}
