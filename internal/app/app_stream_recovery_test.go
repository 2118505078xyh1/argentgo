package app

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/llm"
)

// TestStreamRecoveryEnabledDefaultOn 覆盖 env 门的所有取值：默认开启，
// 仅 ARGENT_STREAM_RECOVERY=0（允许首尾空白）时紧急关闭。
func TestStreamRecoveryEnabledDefaultOn(t *testing.T) {
	cases := []struct {
		name string
		env  func(string) string
		want bool
	}{
		{name: "nil 读取器默认开启", env: nil, want: true},
		{name: "缺失默认开启", env: envFrom(map[string]string{}), want: true},
		{name: "空值默认开启", env: envFrom(map[string]string{"ARGENT_STREAM_RECOVERY": ""}), want: true},
		{name: "0 关闭", env: envFrom(map[string]string{"ARGENT_STREAM_RECOVERY": "0"}), want: false},
		{name: "带空白的 0 关闭", env: envFrom(map[string]string{"ARGENT_STREAM_RECOVERY": "\t 0 \n"}), want: false},
		{name: "true 保持开启", env: envFrom(map[string]string{"ARGENT_STREAM_RECOVERY": "true"}), want: true},
		{name: "01 保持开启", env: envFrom(map[string]string{"ARGENT_STREAM_RECOVERY": "01"}), want: true},
		{name: "1 开启", env: envFrom(map[string]string{"ARGENT_STREAM_RECOVERY": "1"}), want: true},
		{name: "首尾空格的 1 开启", env: envFrom(map[string]string{"ARGENT_STREAM_RECOVERY": " 1 "}), want: true},
		{name: "制表换行的 1 开启", env: envFrom(map[string]string{"ARGENT_STREAM_RECOVERY": "\t1\n"}), want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := streamRecoveryEnabled(tc.env); got != tc.want {
				t.Fatalf("streamRecoveryEnabled() = %t, want %t", got, tc.want)
			}
		})
	}
}

// recoveryChatter 在首次流式请求输出部分文本后返回 ErrStreamError；ChatOnce 以
// 非流式方式返回完整答复。它同时实现 Chat 与 ChatOnce，满足 agent 的
// streamOnceChatter 断言，用于端到端验证 app→agent 的流中断回补接线。
type recoveryChatter struct {
	mu        sync.Mutex
	partial   string
	full      string
	chatCalls int
	onceCalls int
}

func (c *recoveryChatter) Chat(_ context.Context, _ string, _ []llm.Message, _ []llm.ToolSpec, onText func(string)) (llm.Message, error) {
	c.mu.Lock()
	c.chatCalls++
	partial := c.partial
	c.mu.Unlock()
	if onText != nil {
		onText(partial)
	}
	return llm.Message{}, fmt.Errorf("%w: connection reset", llm.ErrStreamError)
}

func (c *recoveryChatter) ChatOnce(_ context.Context, _ string, _ []llm.Message, _ []llm.ToolSpec) (llm.Message, error) {
	c.mu.Lock()
	c.onceCalls++
	c.mu.Unlock()
	return llm.Message{Role: "assistant", Content: c.full}, nil
}

func (c *recoveryChatter) counts() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.chatCalls, c.onceCalls
}

func hasStreamRecoveryRetry(events []agent.Event) bool {
	for _, e := range events {
		if e.Kind == agent.EventRetry && e.Retry != nil && e.Retry.Reason == "stream_recovery" {
			return true
		}
	}
	return false
}

// runWithStreamRecovery 用指定的 ARGENT_STREAM_RECOVERY 值运行一次 Execute，
// 返回探测 chatter 与本次发布的所有事件。
func runWithStreamRecovery(t *testing.T, value string) (*recoveryChatter, []agent.Event) {
	t.Helper()
	baseEnv, root, _ := wiringEnv(t)
	env := func(key string) string {
		if key == "ARGENT_STREAM_RECOVERY" {
			return value
		}
		return baseEnv(key)
	}
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	res.TrustProject = true
	res.MaxTurns = 1
	res.Approve = autoApprover{}
	chat := &recoveryChatter{partial: "Hello ", full: "Hello world"}
	res.Chatter = chat
	store := testStore(t, res)
	sess, err := store.NewSession("stream-recovery")
	if err != nil {
		t.Fatal(err)
	}
	var emitted []agent.Event
	ctx := withEventEmit(context.Background(), func(e agent.Event) { emitted = append(emitted, e) })
	// 开启时回补成功应无错误；关闭时按原失败路径返回错误，这里只关心是否触发了 ChatOnce。
	_, _ = Execute(ctx, cli.IO{Env: env}, res, sess, "hi", nil)
	return chat, emitted
}

// TestExecuteStreamRecoveryWiring 是端到端接线证明：默认开启时 app 把
// EnableStreamRecovery 注入 agent.Options，流中断后触发 ChatOnce 并发布
// stream_recovery 重试事件；=0 时恢复分支短路，ChatOnce 不被调用。
func TestExecuteStreamRecoveryWiring(t *testing.T) {
	t.Run("开启时触发非流式回补", func(t *testing.T) {
		chat, emitted := runWithStreamRecovery(t, "1")
		chatCalls, onceCalls := chat.counts()
		if chatCalls == 0 {
			t.Fatal("chatter.Chat 未被调用")
		}
		if onceCalls != 1 {
			t.Fatalf("ChatOnce 调用次数 = %d, want 1（EnableStreamRecovery 未接通）", onceCalls)
		}
		if !hasStreamRecoveryRetry(emitted) {
			t.Fatalf("未发布 stream_recovery 重试事件: %+v", emitted)
		}
	})

	t.Run("=0 关闭基线不触发回补", func(t *testing.T) {
		chat, emitted := runWithStreamRecovery(t, "0")
		_, onceCalls := chat.counts()
		if onceCalls != 0 {
			t.Fatalf("关闭时 ChatOnce 调用次数 = %d, want 0", onceCalls)
		}
		if hasStreamRecoveryRetry(emitted) {
			t.Fatalf("关闭时不应发布 stream_recovery 重试事件: %+v", emitted)
		}
	})
}
