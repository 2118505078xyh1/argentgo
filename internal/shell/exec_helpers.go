package shell

import (
	"context"
	"time"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/jobs"
	"github.com/2118505078xyh1/argentgo/internal/pluginapi"
)

// 本文件提供命令类工具执行所需的运行时 helper。它们原先在 internal/tools
// 包内，迁入 internal/shell 后改为从 pluginapi.ExecContext 读取注册表注入的
// 值——这样 shell 无需 import tools（避免 import 环），且与原实现逐字节等价。

// maxToolOutput 是默认输出上限，与 tools 包内同名常量保持一致。
const maxToolOutput = 40000

// toolTimeouts 保存每个有运行时预算的工具的默认值、下限和上限。
// 原样迁移自 internal/tools/tool-timeouts.go；"fetch" 是 web_fetch 的历史名。
var toolTimeouts = map[string]struct{ Def, Min, Max time.Duration }{
	"bash":       {120 * time.Second, time.Second, 3600 * time.Second},
	"fetch":      {20 * time.Second, time.Second, 45 * time.Second},
	"web_search": {20 * time.Second, time.Second, 45 * time.Second},
	"git_status": {20 * time.Second, time.Second, 60 * time.Second},
}

// execCtx 返回注册表经 pluginapi 注入的执行参数；未注入时返回零值。
func execCtx(ctx context.Context) pluginapi.ExecContext {
	ec, _ := pluginapi.ExecContextFrom(ctx)
	return ec
}

// maxTimeout 返回注册表安装的超时上限，未设限时为 0。
func maxTimeout(ctx context.Context) time.Duration { return execCtx(ctx).MaxTimeout }

// outputLimit 返回每注册表的输出上限，未设限时回退默认值。
func outputLimit(ctx context.Context) int {
	if limit := execCtx(ctx).MaxOutput; limit > 0 {
		return limit
	}
	return maxToolOutput
}

// jobRegistry 返回注册表安装的后台作业注册表，未安装时为 nil。
func jobRegistry(ctx context.Context) *jobs.Registry {
	reg, _ := execCtx(ctx).Jobs.(*jobs.Registry)
	return reg
}

// clampTimeout 解析工具的运行时预算。非正值回退默认值，全局上限随后钳制，
// 最后应用每个工具的下限与上限——顺序与原实现一致。
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

// clip 裁剪文本到输出上限。原实现含 opt-in 的 spill 落盘；spill 默认关闭，
// 且迁移后由 shell 自行管理输出治理，因此这里直接走 clipTo（默认字节一致）。
func clip(ctx context.Context, text string) string {
	return clipTo(text, outputLimit(ctx))
}

// clipTo 保留过大输出的头部和尾部，按字素簇边界切分。
func clipTo(text string, limit int) string {
	if limit <= 0 {
		limit = maxToolOutput
	}
	if len(text) <= limit {
		return text
	}
	head := limit * 2 / 3
	tail := limit - head
	head = clusterBoundaryBefore(text, head)
	tailStart := len(text) - tail
	for tailStart < len(text) && !utf8.RuneStart(text[tailStart]) {
		tailStart++
	}
	tailStart = clusterBoundaryAfter(text, tailStart)
	return text[:head] + "\n... [truncated] ...\n" + text[tailStart:]
}

// trimPartialRune 把字节偏移从 UTF-8 序列的中间回退到起始边界。
func trimPartialRune(text string, offset int) int {
	if offset <= 0 {
		return 0
	}
	for offset > 0 && offset < len(text) && !utf8.RuneStart(text[offset]) {
		offset--
	}
	return offset
}
