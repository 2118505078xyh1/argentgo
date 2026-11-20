package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestParallelBatchTracksBreakerInCallOrder 检查无效参数熔断器的最终状态由工具调用的顺序决定，
// 而非由 worker 完成的顺序决定。
// 成功的调用在批次中排在最前，却刻意最后完成：按完成顺序重放结果，
// 会让它清除三个尾部无效参数失败建立的连续序列，
// 使运行错过同一批次按顺序执行时总会产生的停止。
// （接上行）
// （接上行）
func TestParallelBatchTracksBreakerInCallOrder(t *testing.T) {
	tools := &fakeTools{
		specs: []llm.ToolSpec{toolSpec("read")},
		run: func(_ context.Context, _ string, args string) (string, error) {
			if strings.Contains(args, `"n":0`) {
				// 第一个调用、最后完成：成功会清除连续序列。
				time.Sleep(80 * time.Millisecond)
				return "ok", nil
			}
			return "", errors.New("bad arguments")
		},
	}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(
				toolCall("c0", "read", `{"n":0}`),
				toolCall("c1", "read", `{"n":1}`),
				toolCall("c2", "read", `{"n":2}`),
				toolCall("c3", "read", `{"n":3}`),
			), nil
		}
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{
		Model:       "m",
		Chatter:     chat,
		Tools:       tools,
		MaxTurns:    3,
		MaxParallel: 4,
	}}

	res, err := runner.Run(context.Background(), "inspect")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Stopped != stopInvalidArguments {
		t.Fatalf("stopped = %q, want %q (call-order outcomes must trip the breaker)", res.Stopped, stopInvalidArguments)
	}
}

// TestParallelBatchTracksMistakesInCallOrder 是针对连续错误追踪器的同样契约：
// 在三次失败之后完成的成功调用，不得抹掉调用顺序建立的连续序列。
// （接上行）
func TestParallelBatchTracksMistakesInCallOrder(t *testing.T) {
	tools := &fakeTools{
		specs: []llm.ToolSpec{toolSpec("read")},
		run: func(_ context.Context, _ string, args string) (string, error) {
			if strings.Contains(args, `"n":0`) {
				time.Sleep(80 * time.Millisecond)
				return "ok", nil
			}
			return "", errors.New("boom")
		},
	}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(
				toolCall("c0", "read", `{"n":0}`),
				toolCall("c1", "read", `{"n":1}`),
				toolCall("c2", "read", `{"n":2}`),
				toolCall("c3", "read", `{"n":3}`),
			), nil
		}
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{
		Model:       "m",
		Chatter:     chat,
		Tools:       tools,
		MaxTurns:    3,
		MaxParallel: 4,
	}}

	res, err := runner.Run(context.Background(), "inspect")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// 错误追踪器有恢复指引，因此绝不硬停止；但该连续序列仍必须达到上限并注入一条指引轮次，
	// 第二次请求会把它看作一条用户消息。
	// （接上行）
	if res.Stopped != StopComplete {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopComplete)
	}
	seen := chat.requestsSeen()
	if len(seen) < 2 {
		t.Fatalf("requests = %d, want a second turn with mistake guidance", len(seen))
	}
	found := false
	for _, msg := range seen[1] {
		if msg.Role == "user" && strings.Contains(msg.Content, "连续出错") {
			found = true
		}
	}
	if !found {
		t.Fatalf("second request lacks mistake guidance; call-order outcomes were lost")
	}
}
