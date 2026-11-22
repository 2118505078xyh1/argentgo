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
