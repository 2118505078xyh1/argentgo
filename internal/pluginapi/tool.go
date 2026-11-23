package pluginapi

import (
	"context"
	"encoding/json"
)

// Tool 是一项可调用的能力。
//
// 它是插件契约的一部分，定义在零依赖的 pluginapi 包，使工具实现包与注册方
// 可以各自独立 import，而不必互相依赖（避免 import 环）。tools 包用类型别名
// `type Tool = pluginapi.Tool` 复用此契约，现有工具实现文件无需改动。
type Tool interface {
	Name() string
	Description() string
	Schema() string
	Mutating() bool
	Run(ctx context.Context, root string, args json.RawMessage) (string, error)
}

// OutputSchemaTool 是可选的加法接口：实现它的插件工具声明一个 JSON Schema，
// 其输出会在工具边界被校验。核心内置工具不实现它，因此默认路径不受影响。
type OutputSchemaTool interface {
	OutputSchema() json.RawMessage
}

// Exposure 是工具向模型公布的级别。未实现 ExposureTool 的工具默认是 direct，
// 因此现有工具与默认 golden 目录逐字节不变。
type Exposure string

const (
	// ExposureDirect：始终出现在公布目录里（默认）。
	ExposureDirect Exposure = "direct"
	// ExposureDeferred：默认不公布，仅当用户经 tool_search 命中后才并入该轮。
	ExposureDeferred Exposure = "deferred"
	// ExposureHidden：永不公布，但仍可通过工具名显式调用（走完整安全链）。
	ExposureHidden Exposure = "hidden"
)

// ExposureTool 是可选接口。实现它即可声明非默认暴露级别；核心内置工具不实现它。
type ExposureTool interface {
	Exposure() Exposure
}

// ToolExposure 返回工具的暴露级别；未实现或返回非法值时视为 direct。
func ToolExposure(t any) Exposure {
	if et, ok := t.(ExposureTool); ok {
		switch et.Exposure() {
		case ExposureDeferred:
			return ExposureDeferred
		case ExposureHidden:
			return ExposureHidden
		}
	}
	return ExposureDirect
}
