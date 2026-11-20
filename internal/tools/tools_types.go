package tools

import (
	"context"
	"encoding/json"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/jobs"
)

const (
	maxToolOutput = 40000
	maxMatches    = 200
)

// Tool 是一项可调用的能力。
type Tool interface {
	Name() string
	Description() string
	Schema() string
	Mutating() bool
	Run(ctx context.Context, root string, args json.RawMessage) (string, error)
}

// ToolMeta 是工具附加到其结果上的进程内回收提示。
// 它从不跨越传输层：agent 循环在运行时活着时用它把已被取代的读取
// 和空搜索替换为存根。
type ToolMeta struct {
	SupersedeKey string
	Useless      bool
}

// metaTool 由能够描述自身结果以供上下文回收的工具实现。
// 传入输出和错误，使失败调用永远不会
// 宣称取代键或"空结果"标记。
type metaTool interface {
	Meta(args json.RawMessage, output string, err error) ToolMeta
}

// Guard 在每次工具调用前被咨询。非 nil 错误会作为工具错误返回给
// 模型；工具绝不运行。
type Guard interface {
	Check(ctx context.Context, name string, args json.RawMessage) error
}

// Registry 拥有工具集并强制工作区边界。
type Registry struct {
	root       string
	yolo       bool
	maxOutput  int
	maxTimeout time.Duration
	sessionDir string
	jobs       *jobs.Registry
	guard      Guard
	tools      map[string]Tool
	order      []string
}
