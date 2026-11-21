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
