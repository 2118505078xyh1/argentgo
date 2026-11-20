package app

import (
	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/config"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/security"
)

// Resolved 是合并命令行标志、配置和提供方模板之后，一次运行所需的一切。
type Resolved struct {
	Options  cli.Options
	Cfg      config.Config
	Root     string
	Home     string
	Storage  string
	Model    string
	Provider string
	Profile  string
	BaseURL  string
	APIKey   string
	Protocol string
	Locale   string
	Sandbox  security.SandboxMode
	Approval string
	MaxTurns int
	Mutating bool
	System   string

	// MaxParallel 是只读工具批次的 worker 数量。<= 1 的值使批次串行执行；
	// 零表示"未设置"，在 Resolve 中解析为 defaultMaxParallel。
	MaxParallel int

	// 预算与定价。
	MaxTokens  int
	MaxCostUSD float64
	MaxSeconds int
	Prices     map[string]agent.Price

	// 额外的工作区目录、输出 schema 请求以及推理强度提示。
	AddDirs      []string
	WriteRoots   []string
	OutputSchema string
	Thinking     string
	TrustProject bool
	Plugins      []string

	// 运行后压缩。零表示选用默认阈值；负值会禁用基于估算的触发。
	AutoCompactTokens int
	NoAutoCompact     bool

	// Approve 覆盖交互式审批器；TUI 在这里安装一个模态审批器。
	Approve security.Approver

	// HookDispatcher 覆盖 hooks 桥接器使用的 shell 分发器；测试会注入伪实现，
	// 这样就不会派生任何钩子进程。
	HookDispatcher HookDispatcher

	Chatter llm.Chatter
}

// providerChoice 是已解析的提供方端点，不含模板类型。
type providerChoice struct {
	Name         string
	Protocol     string
	BaseURL      string
	DefaultModel string
	APIKeyEnv    string
}
