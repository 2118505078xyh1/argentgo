package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// 重试前持久上下文省略 + 可中止退避（TODO #106，学自 pi-mono）的独立门控。
// 只有把值修剪空白后精确等于 "1" 才开启；默认（未设置/空/其它值）关闭。
// 这里直接读取字面量，使 internal/cli/optin_docs_guard_test.go 能识别真实源码读点。
func retryOmitEnabled() bool {
	return strings.TrimSpace(os.Getenv("ARGENT_RETRY_OMIT")) == "1"
}

// 退避参数。指数退避按失败序号增长并被 max 钳制，避免瞬时限流时立刻切换模型。
// 声明为变量而非常量，便于测试用极小值加速，而生产默认保持不变。
var (
	retryOmitBaseDelay = 200 * time.Millisecond
	retryOmitMaxDelay  = 2 * time.Second
)

// abortableBackoff 在重试前等待 delay，且可被 ctx 取消。
// 它是 ctx 感知的定时器：ctx 先结束时立即返回 ctx.Err()，不留下悬挂的计时器。
// delay <= 0 时不等待，只返回 ctx 当前状态（默认路径从不进入这里）。
func abortableBackoff(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// retryOmitDelay 返回第 attempt 次失败（从 1 开始）后、下一次尝试前的退避时长。
// 指数增长并以 retryOmitMaxDelay 封顶，防止长回退链把等待时间放大到不可接受。
func retryOmitDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := retryOmitBaseDelay
	for i := 1; i < attempt && delay < retryOmitMaxDelay; i++ {
		delay *= 2
	}
	if delay > retryOmitMaxDelay {
		delay = retryOmitMaxDelay
	}
	return delay
}

// omitFailedAttempt 在重试前通过注入的回调永久省略失败尝试的模型可见上下文。
// 回调为 nil（未接线）时整段是 no-op，行为与接线前逐字节一致。
// 回调自身报错只记一条 Notice 供观察，绝不改变原有的模型切换/重试语义：
// 带着未省略的上下文重试只会退回旧行为，不会比原实现更糟。
func (s *runState) omitFailedAttempt(turn, attempt int) {
	hook := currentRetryOmitHook()
	if hook == nil {
		return
	}
	if err := hook(turn); err != nil {
		s.emit(Event{Kind: EventNotice, Text: fmt.Sprintf(
			"重试前省略失败上下文失败（turn %d, attempt %d）: %v", turn, attempt, err,
		)})
	}
}
