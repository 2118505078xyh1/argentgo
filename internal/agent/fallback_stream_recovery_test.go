package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// fakeStreamOnceChatter 在 fakeChatter 之上实现 streamOnceChatter，
// 用于断言回补请求与失败请求携带同一 model/messages/tools，且回补调用没有 onText。
type fakeStreamOnceChatter struct {
	*fakeChatter
	mu         sync.Mutex
	onceFn     func(n int, model string, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error)
	onceCalls  int
	onceModels []string
	onceSeen   [][]llm.Message
	onceTools  [][]llm.ToolSpec
}

func newFakeStreamOnceChatter(
	chatFn func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error),
	onceFn func(int, string, []llm.Message, []llm.ToolSpec) (llm.Message, error),
) *fakeStreamOnceChatter {
	return &fakeStreamOnceChatter{fakeChatter: &fakeChatter{fn: chatFn}, onceFn: onceFn}
}

func (f *fakeStreamOnceChatter) ChatOnce(ctx context.Context, model string, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
	f.mu.Lock()
	f.onceCalls++
	n := f.onceCalls
	f.onceModels = append(f.onceModels, model)
	f.onceSeen = append(f.onceSeen, append([]llm.Message(nil), messages...))
	f.onceTools = append(f.onceTools, append([]llm.ToolSpec(nil), tools...))
	f.mu.Unlock()
	return f.onceFn(n, model, messages, tools)
}

func (f *fakeStreamOnceChatter) onceCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.onceCalls
}

func (f *fakeStreamOnceChatter) onceDetails() ([]string, [][]llm.Message, [][]llm.ToolSpec) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.onceModels...),
		append([][]llm.Message(nil), f.onceSeen...),
		append([][]llm.ToolSpec(nil), f.onceTools...)
}

// testStreamErr 构造满足 errors.Is(err, llm.ErrStreamError) 的提供方流错误。
func testStreamErr(text string) error {
	return fmt.Errorf("%w: %s", llm.ErrStreamError, text)
}

func sinkTextDeltas(s *eventSink) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, e := range s.events {
		if e.Kind == EventTextDelta {
			out = append(out, e.Text)
		}
	}
	return out
}

func sinkRetryReasons(s *eventSink) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, e := range s.events {
		if e.Kind == EventRetry && e.Retry != nil {
			out = append(out, e.Retry.Reason)
		}
	}
	return out
}

func assertNoRecoveryRequest(t *testing.T, chat *fakeStreamOnceChatter, sink *eventSink) {
	t.Helper()
	if got := chat.count(); got != 1 {
		t.Fatalf("stream requests = %d, want 1 (recovery gate must stay closed)", got)
	}
	if got := chat.onceCount(); got != 0 {
		t.Fatalf("ChatOnce requests = %d, want 0", got)
	}
	if reasons := sinkRetryReasons(sink); len(reasons) != 0 {
		t.Fatalf("retry reasons = %q, want none", reasons)
	}
}

// TestStreamRecoverySucceedsWithExactSuffixDelta 锁定成功回补的全程：恰好两个请求、
// 失败前缀只出现一次、仅发布精确后缀、完整内容作为结果返回，并带上 stream_recovery 重试事件。
func TestStreamRecoverySucceedsWithExactSuffixDelta(t *testing.T) {
	const partial = "Hello "
	const full = "Hello world"
	chat := newFakeStreamOnceChatter(
		func(_ int, _ []llm.Message, _ []llm.ToolSpec, onText func(string)) (llm.Message, error) {
			onText(partial)
			return llm.Message{}, testStreamErr("connection reset")
		},
		func(_ int, _ string, _ []llm.Message, _ []llm.ToolSpec) (llm.Message, error) {
			return textReply(full), nil
		},
	)
	sink := &eventSink{}
	runner := &Runner{Opts: Options{
		Model:                "primary",
		EnableStreamRecovery: true,
		Chatter:              chat,
		Emit:                 sink.emit,
	}}

	res, err := runner.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Text != full || res.Stopped != StopComplete {
		t.Fatalf("result = %+v, want text %q and complete", res, full)
	}
	if got := chat.count(); got != 1 {
		t.Fatalf("stream requests = %d, want 1", got)
	}
	if got := chat.onceCount(); got != 1 {
		t.Fatalf("ChatOnce requests = %d, want 1", got)
	}
	if got, want := sinkTextDeltas(sink), []string{partial, "world"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("text deltas = %q, want %q (prefix must not be replayed)", got, want)
	}
	if sink.has(EventPartial) {
		t.Fatal("successful recovery must not emit EventPartial")
	}
	if got, want := sinkRetryReasons(sink), []string{streamRecoveryReason}; !reflect.DeepEqual(got, want) {
		t.Fatalf("retry reasons = %q, want %q", got, want)
	}

	// 回补必须使用与失败请求完全相同的 model/messages/tools。
	models, seen, tools := chat.onceDetails()
	if len(models) != 1 || models[0] != "primary" {
		t.Fatalf("ChatOnce models = %q, want [primary]", models)
	}
	streamSeen := chat.requestsSeen()
	if len(seen) != 1 || len(streamSeen) != 1 || !reflect.DeepEqual(seen[0], streamSeen[0]) {
		t.Fatalf("ChatOnce messages = %+v, want the failed request's %+v", seen, streamSeen)
	}
	if len(tools) != 1 || len(chat.toolSets) != 1 || !reflect.DeepEqual(tools[0], chat.toolSets[0]) {
		t.Fatalf("ChatOnce tools = %+v, want %+v", tools, chat.toolSets)
	}
}

// TestStreamRecoveryGateKeepsSingleStreamRequest 验证所有回补前置条件：
// 无文本、非流错误、取消、未 opt-in、Chatter 未实现接口时一律只发一次流式请求。
func TestStreamRecoveryGateKeepsSingleStreamRequest(t *testing.T) {
	t.Run("no partial text", func(t *testing.T) {
		chat := newFakeStreamOnceChatter(
			func(_ int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
				return llm.Message{}, testStreamErr("connection reset")
			},
			func(_ int, _ string, _ []llm.Message, _ []llm.ToolSpec) (llm.Message, error) {
				return textReply("recovered"), nil
			},
		)
		sink := &eventSink{}
		runner := &Runner{Opts: Options{
			Model: "primary", EnableStreamRecovery: true, Chatter: chat, Emit: sink.emit,
		}}
		if _, err := runner.Run(context.Background(), "hi"); err == nil {
			t.Fatal("stream error with no partial text unexpectedly succeeded")
		}
		assertNoRecoveryRequest(t, chat, sink)
		if sink.has(EventPartial) {
			t.Fatal("no streamed text must not produce EventPartial")
		}
	})

	t.Run("not a stream error", func(t *testing.T) {
		chat := newFakeStreamOnceChatter(
			func(_ int, _ []llm.Message, _ []llm.ToolSpec, onText func(string)) (llm.Message, error) {
				onText("half")
				return llm.Message{}, errors.New("HTTP 503: service unavailable")
			},
			func(_ int, _ string, _ []llm.Message, _ []llm.ToolSpec) (llm.Message, error) {
				return textReply("recovered"), nil
			},
		)
		sink := &eventSink{}
		runner := &Runner{Opts: Options{
			Model: "primary", EnableStreamRecovery: true, Chatter: chat, Emit: sink.emit,
		}}
		if _, err := runner.Run(context.Background(), "hi"); err == nil {
			t.Fatal("provider error unexpectedly succeeded")
		}
		assertNoRecoveryRequest(t, chat, sink)
		if got, want := sinkTextDeltas(sink), []string{"half"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("text deltas = %q, want %q", got, want)
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		chat := newFakeStreamOnceChatter(
			func(_ int, _ []llm.Message, _ []llm.ToolSpec, onText func(string)) (llm.Message, error) {
				onText("half")
				cancel()
				return llm.Message{}, testStreamErr("connection reset")
			},
			func(_ int, _ string, _ []llm.Message, _ []llm.ToolSpec) (llm.Message, error) {
				return textReply("recovered"), nil
			},
		)
		sink := &eventSink{}
		runner := &Runner{Opts: Options{
			Model: "primary", EnableStreamRecovery: true, Chatter: chat, Emit: sink.emit,
		}}
		if _, err := runner.Run(ctx, "hi"); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		assertNoRecoveryRequest(t, chat, sink)
	})

	t.Run("not opted in", func(t *testing.T) {
		chat := newFakeStreamOnceChatter(
			func(_ int, _ []llm.Message, _ []llm.ToolSpec, onText func(string)) (llm.Message, error) {
				onText("half")
				return llm.Message{}, testStreamErr("connection reset")
			},
			func(_ int, _ string, _ []llm.Message, _ []llm.ToolSpec) (llm.Message, error) {
				return textReply("recovered"), nil
			},
		)
		sink := &eventSink{}
		runner := &Runner{Opts: Options{
			Model: "primary", Chatter: chat, Emit: sink.emit,
		}}
		if _, err := runner.Run(context.Background(), "hi"); err == nil {
			t.Fatal("stream error unexpectedly succeeded without opt-in")
		}
		assertNoRecoveryRequest(t, chat, sink)
	})

	t.Run("chatter lacks ChatOnce", func(t *testing.T) {
		chat := &fakeChatter{fn: func(_ int, _ []llm.Message, _ []llm.ToolSpec, onText func(string)) (llm.Message, error) {
			onText("half")
			return llm.Message{}, testStreamErr("connection reset")
		}}
		sink := &eventSink{}
		runner := &Runner{Opts: Options{
			Model: "primary", EnableStreamRecovery: true, Chatter: chat, Emit: sink.emit,
		}}
		if _, err := runner.Run(context.Background(), "hi"); err == nil {
			t.Fatal("stream error unexpectedly succeeded without ChatOnce")
		}
		if got := chat.count(); got != 1 {
			t.Fatalf("stream requests = %d, want 1", got)
		}
		if reasons := sinkRetryReasons(sink); len(reasons) != 0 {
			t.Fatalf("retry reasons = %q, want none", reasons)
		}
	})
}

// TestStreamRecoveryFailureKeepsOriginalFailureEvents 证明回补失败时原失败路径逐字节保留：
// EventPartial 仍在、原错误与停止原因不变、不会发出 stream_recovery 事件。
func TestStreamRecoveryFailureKeepsOriginalFailureEvents(t *testing.T) {
	const partial = "half"
	chat := newFakeStreamOnceChatter(
		func(_ int, _ []llm.Message, _ []llm.ToolSpec, onText func(string)) (llm.Message, error) {
			onText(partial)
			return llm.Message{}, testStreamErr("connection reset")
		},
		func(_ int, _ string, _ []llm.Message, _ []llm.ToolSpec) (llm.Message, error) {
			return llm.Message{}, errors.New("HTTP 500: recovery failed")
		},
	)
	sink := &eventSink{}
	runner := &Runner{Opts: Options{
		Model: "primary", EnableStreamRecovery: true, Chatter: chat, Emit: sink.emit,
	}}

	res, err := runner.Run(context.Background(), "hi")
	if err == nil {
		t.Fatal("recovery failed but Run returned nil error")
	}
	if res.Stopped != StopProviderError {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopProviderError)
	}
	if got, want := sinkTextDeltas(sink), []string{partial}; !reflect.DeepEqual(got, want) {
		t.Fatalf("text deltas = %q, want %q", got, want)
	}
	var found bool
	for _, e := range sink.events {
		if e.Kind == EventPartial {
			found = true
			if e.Text != partial || !e.Partial {
				t.Fatalf("partial event = %+v, want text %q and Partial=true", e, partial)
			}
		}
	}
	if !found {
		t.Fatal("failed recovery dropped the original EventPartial")
	}
	if reasons := sinkRetryReasons(sink); len(reasons) != 0 {
		t.Fatalf("retry reasons = %q, want none", reasons)
	}
	if chat.count() != 1 || chat.onceCount() != 1 {
		t.Fatalf("requests = %d stream / %d once, want 1 / 1", chat.count(), chat.onceCount())
	}
}

// TestStreamRecoveryPrefixMismatchEmitsNoDuplicate 验证"宁缺勿重"：
// 回补内容与已推送前缀不匹配时，整段都不发增量，但完整答复仍作为结果返回。
func TestStreamRecoveryPrefixMismatchEmitsNoDuplicate(t *testing.T) {
	const partial = "Hello "
	const full = "A completely different final answer"
	chat := newFakeStreamOnceChatter(
		func(_ int, _ []llm.Message, _ []llm.ToolSpec, onText func(string)) (llm.Message, error) {
			onText(partial)
			return llm.Message{}, testStreamErr("connection reset")
		},
		func(_ int, _ string, _ []llm.Message, _ []llm.ToolSpec) (llm.Message, error) {
			return textReply(full), nil
		},
	)
	sink := &eventSink{}
	runner := &Runner{Opts: Options{
		Model: "primary", EnableStreamRecovery: true, Chatter: chat, Emit: sink.emit,
	}}

	res, err := runner.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Text != full {
		t.Fatalf("text = %q, want %q", res.Text, full)
	}
	if got, want := sinkTextDeltas(sink), []string{partial}; !reflect.DeepEqual(got, want) {
		t.Fatalf("text deltas = %q, want only %q (mismatched prefix must not be replayed)", got, want)
	}
	if got, want := sinkRetryReasons(sink), []string{streamRecoveryReason}; !reflect.DeepEqual(got, want) {
		t.Fatalf("retry reasons = %q, want %q", got, want)
	}
}

// TestStreamRecoveryAtMostOncePerTurn 验证回退链上多个模型都以流错误失败时，
// 每轮也只尝试一次非流式回补，其余尝试沿用原有的换模型/部分事件路径。
func TestStreamRecoveryAtMostOncePerTurn(t *testing.T) {
	chat := newFakeStreamOnceChatter(
		func(_ int, _ []llm.Message, _ []llm.ToolSpec, onText func(string)) (llm.Message, error) {
			onText("partial ")
			return llm.Message{}, testStreamErr("connection reset")
		},
		func(_ int, _ string, _ []llm.Message, _ []llm.ToolSpec) (llm.Message, error) {
			return llm.Message{}, errors.New("HTTP 500: recovery failed")
		},
	)
	sink := &eventSink{}
	runner := &Runner{Opts: Options{
		FallbackModels:       []string{"primary", "backup"},
		EnableStreamRecovery: true,
		Chatter:              chat,
		Emit:                 sink.emit,
	}}

	if _, err := runner.Run(context.Background(), "hi"); err == nil {
		t.Fatal("all attempts failed but Run returned nil error")
	}
	if got := chat.count(); got != 2 {
		t.Fatalf("stream requests = %d, want 2 models", got)
	}
	if got := chat.onceCount(); got != 1 {
		t.Fatalf("ChatOnce requests = %d, want exactly 1 per turn", got)
	}
	if !sink.has(EventPartial) {
		t.Fatal("original failure path lost EventPartial")
	}
	for _, reason := range sinkRetryReasons(sink) {
		if reason == streamRecoveryReason {
			t.Fatal("failed recovery emitted a stream_recovery retry event")
		}
	}
}

// TestStreamRecoveryBufferedModeEmitsFullContent 验证多模型缓冲模式：失败尝试的缓冲增量
// 被丢弃，回补成功后只发布一次完整答复，绝不重放未发布过的半截文本。
func TestStreamRecoveryBufferedModeEmitsFullContent(t *testing.T) {
	const partial = "Hello "
	const full = "Hello world"
	chat := newFakeStreamOnceChatter(
		func(_ int, _ []llm.Message, _ []llm.ToolSpec, onText func(string)) (llm.Message, error) {
			onText(partial)
			return llm.Message{}, testStreamErr("connection reset")
		},
		func(_ int, _ string, _ []llm.Message, _ []llm.ToolSpec) (llm.Message, error) {
			return textReply(full), nil
		},
	)
	sink := &eventSink{}
	runner := &Runner{Opts: Options{
		FallbackModels:       []string{"primary", "backup"},
		EnableStreamRecovery: true,
		Chatter:              chat,
		Emit:                 sink.emit,
	}}

	res, err := runner.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Text != full {
		t.Fatalf("text = %q, want %q", res.Text, full)
	}
	if got := chat.count(); got != 1 {
		t.Fatalf("stream requests = %d, want 1 (recovery precedes model switch)", got)
	}
	if got := chat.onceCount(); got != 1 {
		t.Fatalf("ChatOnce requests = %d, want 1", got)
	}
	if got, want := sinkTextDeltas(sink), []string{full}; !reflect.DeepEqual(got, want) {
		t.Fatalf("text deltas = %q, want only the full answer %q", got, want)
	}
	if got, want := sinkRetryReasons(sink), []string{streamRecoveryReason}; !reflect.DeepEqual(got, want) {
		t.Fatalf("retry reasons = %q, want %q", got, want)
	}
	if sink.has(EventPartial) {
		t.Fatal("successful recovery must not emit EventPartial")
	}
}

// TestChildOptionsInheritStreamRecovery 验证子 agent 按值继承父级 opt-in；
// 父级零值时子级保持关闭，默认运行不变。
func TestChildOptionsInheritStreamRecovery(t *testing.T) {
	child := (&taskTools{
		inner:  nopTools{},
		cfg:    &SubagentConfig{},
		parent: Options{EnableStreamRecovery: true},
	}).childOptions("child", "")
	if !child.EnableStreamRecovery {
		t.Fatal("childOptions dropped the parent's EnableStreamRecovery")
	}

	zero := (&taskTools{inner: nopTools{}, cfg: &SubagentConfig{}}).childOptions("child", "")
	if zero.EnableStreamRecovery {
		t.Fatal("childOptions enabled stream recovery without parent opt-in")
	}
}
