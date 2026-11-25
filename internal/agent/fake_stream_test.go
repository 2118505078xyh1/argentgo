package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"example.com/argentgo/internal/llm"
)

// 本文件提供一个脚本化的流式 Chatter 基座，补齐 fakeChatter 只能"一次性回调"
// 的短板。fakeChatter 把流形状交给每个测试自己手写 fn，容易在分帧、错误、
// 取消这些边界上写出互相矛盾的假行为；这里把"模型每一帧说什么"变成数据，
// 让 agent 循环在流中途取消、分帧到达、错误返回时的行为可被精确断言。
//
// 与 internal/llm 的同名 conformance 套件不同，这里观察的是 agent 如何消费流，
// 而不是 Chatter 实现本身是否满足协议契约。

// scriptedReply 是一次模型请求的脚本：chunks 按顺序交给 onText，text 是最终
// 消息的正文（为空时使用 chunks 的拼接结果），err 非 nil 时直接返回错误。
type scriptedReply struct {
	chunks []string
	text   string
	err    error
}

// scriptedChatter 按请求序号回放 scriptedReply。超出脚本长度时重复最后一项，
// 便于"先成功 N 次、之后一直失败"这类场景；所有观测字段都受 mu 保护。
type scriptedChatter struct {
	mu sync.Mutex

	replies []scriptedReply

	requests     int
	models       []string
	cancelChecks int
}

func (s *scriptedChatter) Chat(
	ctx context.Context,
	model string,
	_ []llm.Message,
	_ []llm.ToolSpec,
	onText func(string),
) (llm.Message, error) {
	s.mu.Lock()
	s.requests++
	n := s.requests
	s.models = append(s.models, model)
	s.mu.Unlock()

	if len(s.replies) == 0 {
		return llm.Message{}, nil
	}
	idx := n - 1
	if idx >= len(s.replies) {
		idx = len(s.replies) - 1
	}
	reply := s.replies[idx]

	// 每一帧前都观察取消：agent 的中途取消必须立刻停止播放剩余帧，
	// 否则测试里会出现"已取消却还在往外吐文本"的假行为。
	s.mu.Lock()
	s.cancelChecks++
	s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return llm.Message{}, err
	}
	if reply.err != nil {
		return llm.Message{}, reply.err
	}

	var joined strings.Builder
	for _, chunk := range reply.chunks {
		if err := ctx.Err(); err != nil {
			return llm.Message{}, err
		}
		joined.WriteString(chunk)
		if onText != nil {
			onText(chunk)
		}
	}

	text := reply.text
	if text == "" {
		text = joined.String()
	}
	return llm.Message{Role: "assistant", Content: text}, nil
}

func (s *scriptedChatter) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

func (s *scriptedChatter) modelsSeen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.models...)
}

func (s *scriptedChatter) cancelCheckCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelChecks
}

// TestScriptedChatterReplaysChunksAndErrors 演示基座的三种核心能力：逐帧回放、
// 最终消息拼接，以及按请求序号注入错误。它同时守护基座自身不回归。
func TestScriptedChatterReplaysChunksAndErrors(t *testing.T) {
	wantErr := context.DeadlineExceeded
	chat := &scriptedChatter{replies: []scriptedReply{
		{chunks: []string{"Hel", "lo ", "world"}},
		{err: wantErr},
	}}

	var frames []string
	first, err := chat.Chat(context.Background(), "m1", nil, nil, func(s string) {
		frames = append(frames, s)
	})
	if err != nil {
		t.Fatalf("first Chat: %v", err)
	}
	if first.Role != "assistant" || first.Content != "Hello world" {
		t.Fatalf("first reply = %+v", first)
	}
	if len(frames) != 3 || strings.Join(frames, "") != first.Content {
		t.Fatalf("frames = %q, want three frames joining to %q", frames, first.Content)
	}

	if _, err := chat.Chat(context.Background(), "m2", nil, nil, nil); err != wantErr {
		t.Fatalf("second Chat err = %v, want %v", err, wantErr)
	}
	if got := chat.count(); got != 2 {
		t.Errorf("requests = %d, want 2", got)
	}
	if got := chat.modelsSeen(); len(got) != 2 || got[0] != "m1" || got[1] != "m2" {
		t.Errorf("models = %q, want [m1 m2]", got)
	}
	if got := chat.cancelCheckCount(); got < 2 {
		t.Errorf("cancel checks = %d, want at least 2", got)
	}
}

// TestScriptedChatterStopsOnCancellation 确认已取消的 context 会在第一帧之前
// 终止回放，剩余帧不会被投递。agent 的取消语义依赖这个基座行为可信。
func TestScriptedChatterStopsOnCancellation(t *testing.T) {
	chat := &scriptedChatter{replies: []scriptedReply{{chunks: []string{"a", "b", "c"}}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var frames []string
	_, err := chat.Chat(ctx, "m", nil, nil, func(s string) {
		frames = append(frames, s)
	})
	if err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(frames) != 0 {
		t.Fatalf("frames = %q, want none after cancellation", frames)
	}
}
