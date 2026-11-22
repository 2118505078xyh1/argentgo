package tools

import (
	"context"
	"time"
)

// toolTimeouts 保存每个有运行时预算的工具的默认值、下限和上限。
// 默认值是本表引入之前工具所用的值，因此省略 timeout
// 会保持原有行为。"fetch" 是 web_fetch 工具的
// 历史名称。
var toolTimeouts = map[string]struct{ Def, Min, Max time.Duration }{
	"bash":       {120 * time.Second, time.Second, 3600 * time.Second},
	"fetch":      {20 * time.Second, time.Second, 45 * time.Second},
	"web_search": {20 * time.Second, time.Second, 45 * time.Second},
	"git_status": {20 * time.Second, time.Second, 60 * time.Second},
	"worktree":   {30 * time.Second, time.Second, 120 * time.Second},
}

// maxTimeoutKey 把注册表级别的超时上限带入工具调用。
type maxTimeoutKey struct{}

// maxTimeout 返回注册表上安装的上限，未设限时返回 0。
func maxTimeout(ctx context.Context) time.Duration {
	if d, ok := ctx.Value(maxTimeoutKey{}).(time.Duration); ok && d > 0 {
		return d
	}
	return 0
}

// clampTimeout 解析工具的运行时预算。非正的原始值
// 回退到工具的默认值；正的全局上限随后钳制解析后的
// 值（包括默认值），最后应用每个工具的下限和上限
// 应用。此顺序与上游 clampTimeout 契约一致：全局上限
// 同时约束省略 timeout 和显式指定 timeout 的调用。
func clampTimeout(tool string, raw, globalMax time.Duration) time.Duration {
	limits, ok := toolTimeouts[tool]
	if ok && raw <= 0 {
		raw = limits.Def
	}
	if globalMax > 0 && raw > globalMax {
		raw = globalMax
	}
	if !ok {
		return raw
	}
	if raw < limits.Min {
		raw = limits.Min
	}
	if raw > limits.Max {
		raw = limits.Max
	}
	return raw
}
