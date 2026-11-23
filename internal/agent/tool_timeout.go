package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// ErrToolTimeout 是工具超时的哨兵错误。重试/审计/护栏层用 errors.Is 匹配它，
// 而不是去匹配自由格式的文案。它与 tools 包的表保持同语义，但 agent 不依赖 tools。
var ErrToolTimeout = errors.New("tool timeout")

// toolTimeouts 镜像 internal/tools/tool-timeouts.go 的默认值。
// 它是有界的小表；agent 不 import tools，保持低层依赖方向不变。
var toolTimeouts = map[string]time.Duration{
	"bash":       120 * time.Second,
	"fetch":      20 * time.Second,
	"web_search": 20 * time.Second,
	"git_status": 20 * time.Second,
	"worktree":   30 * time.Second,
}

// toolTimeoutFor 返回工具的默认超时上限；未知工具返回 0（不包裹）。
func toolTimeoutFor(name string) time.Duration {
	return toolTimeouts[strings.ToLower(strings.TrimSpace(name))]
}

// timeoutOff 报告是否用环境变量禁用 dispatch 边界的超时包裹。
func timeoutOff() bool { return featureDisabled("ARGENT_TOOL_TIMEOUT") }

// withToolTimeout 为一次工具调用派生带截止时间的 ctx，并返回还原函数。
// 调用方必须在工具返回后、进入 PostToolUse/审计路径之前调用还原函数：
// 派生 ctx 只用于真正的工具执行，后续钩子与记录看到的仍是原始 ctx，
// 因此不会因为派生信号已取消而写出误导性的审计记录。
func withToolTimeout(ctx context.Context, name string) (context.Context, context.CancelFunc, time.Duration) {
	limit := toolTimeoutFor(name)
	if limit <= 0 || timeoutOff() {
		return ctx, func() {}, 0
	}
	callCtx, cancel := context.WithTimeout(ctx, limit)
	return callCtx, cancel, limit
}

// normaliseTimeoutError 把"我们自己的截止时间胜出"转换为结构化错误；
// 嵌套的外层超时（调用方 ctx 先取消）保持原样，避免错误归因。
func normaliseTimeoutError(err error, callCtx, outerCtx context.Context, name string, limit time.Duration) (error, bool) {
	if err == nil || limit <= 0 {
		return err, false
	}
	if !errors.Is(callCtx.Err(), context.DeadlineExceeded) || outerCtx.Err() != nil {
		return err, false
	}
	if errors.Is(err, ErrToolTimeout) {
		return err, false
	}
	// 机器可读错误码固定在文案中，errors.Is 由 %w 保证；
	// 用 fmt.Errorf 包装而不是自定义 error 类型，避免为同一语义引入多余方法。
	return fmt.Errorf("%w: %q timed out after %s (code: TOOL_TIMEOUT)", ErrToolTimeout, name, limit), true
}

// timeoutDisabledByEnv 供测试锁定"仅 =0 回退"的环境语义。
func timeoutDisabledByEnv() bool { return strings.TrimSpace(os.Getenv("ARGENT_TOOL_TIMEOUT")) == "0" }
