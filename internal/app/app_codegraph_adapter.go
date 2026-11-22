package app

import (
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/codegraph"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// 本文件把自研 codegraph 索引接到 app 装配面。它刻意与 app_jobs_wire.go 同构：
// 默认（opt-in 关闭）时零副作用——不构建索引、不调用 SetCodeGraph、不触碰任何
// 工具注册表，系统提示、Specs() 与事件流逐字节不变；只有集中键
// ARGENT_OPT_INS=codegraph 显式开启时才构建并注入。

// codeGraphBuildHook 是可替换的索引构建钩子，测试可注入失败构建器以验证降级路径。
// 生产环境它保持 nil，构建走下方 wireCodeGraph 中对 codegraph.Build 的直接调用。
var codeGraphBuildHook func(string) (*codegraph.Index, error)

// wireCodeGraph 在 opt-in 开启时为本次运行构建代码索引并注入查询工具。
//
// set 为 nil、reg 为 nil 或 opt-in 关闭时直接返回，零开销。reg 来自
// mergeOptIns(env, config) 构造的统一注册表，因此仅通过 config 的 optins 块
// 开启 codegraph 时同样会建索引并注入——与 mcp.go 的工具注册口径一致。
// 构建失败只写一行 warn 到 stderr，工具保持在"不可用"状态（codegraph_query
// 返回可读提示而非错误），绝不中断运行。索引是不可变内存快照，无需 Close；
// 重复调用是幂等的（Registry.SetCodeGraph 原位置换适配器且自动完成注册）。
func wireCodeGraph(stdio cli.IO, root string, set *toolset, reg *OptInRegistry) {
	if set == nil || !codeGraphHintEnabled(reg) {
		return
	}
	var ix *codegraph.Index
	var err error
	if codeGraphBuildHook != nil {
		ix, err = codeGraphBuildHook(root)
	} else {
		ix, err = codegraph.Build(root)
	}
	if err != nil {
		note(stdio, "codegraph", err)
		return
	}
	set.SetCodeGraph(ix)
}

// SetCodeGraph 把索引注入本次运行的全部内置工具注册表。
// 它是幂等的：每个注册表都按工具名原位置换，不重复插入工具，顺序保持不变。
// nil 工具集、nil base 或空注册表列表都是空操作，使手工构造的测试工具集保持安全。
func (t *toolset) SetCodeGraph(ix tools.CodeGraph) {
	if t == nil || t.base == nil {
		return
	}
	for _, reg := range t.base.regs {
		if reg != nil {
			reg.SetCodeGraph(ix)
		}
	}
}
