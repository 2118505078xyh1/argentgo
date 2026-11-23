package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// hookChatter 在 Chat 中从 ctx 取出工具增量钩子并发布若干片段。
type hookChatter struct{}

func (hookChatter) Chat(ctx context.Context, model string, messages []llm.Message, tools []llm.ToolSpec, onText func(string)) (llm.Message, error) {
	hook := llm.HookFromContext(ctx)
	if hook != nil {
		hook(llm.ToolCallDelta{Index: 0, ID: "call-1", Name: "write", Arguments: `{"path":`})
		hook(llm.ToolCallDelta{Index: 0, Arguments: `"a.go"}`})
		hook(llm.ToolCallDelta{Index: 1, ID: "call-2", Name: "bash", Arguments: `{"cmd":`})
	}
	return llm.Message{Role: "assistant", Content: "ok"}, nil
}

func TestToolArgDeltaLiveOnly(t *testing.T) {
	t.Setenv("ARGENT_TOOL_ARG_DELTA", "1")
	var recorded []string
	s := newRunState(Options{
		Model: "m", Chatter: hookChatter{},
		Record: RecorderFunc(func(kind string, payload any) error {
			recorded = append(recorded, kind)
			return nil
		}),
	}, "go", nil)

	if _, err := s.chat(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	events := s.eventsSnapshot()
	var deltas []Event
	for _, e := range events {
		if e.Kind == EventToolArgDelta {
			deltas = append(deltas, e)
		}
	}
	if len(deltas) != 3 {
		t.Fatalf("增量事件数 = %d, want 3", len(deltas))
	}
	if deltas[0].ToolCallID != "call-1" || deltas[0].ToolName != "write" || deltas[0].ArgDelta != `{"path":` {
		t.Fatalf("首片错误: %#v", deltas[0])
	}
	if deltas[1].ToolCallID != "call-1" || deltas[1].ArgDelta != `"a.go"}` {
		t.Fatalf("后续片未沿用 callID: %#v", deltas[1])
	}
	if deltas[2].ToolCallID != "call-2" || deltas[2].ToolArgIndex != 1 {
		t.Fatalf("第二路调用错误: %#v", deltas[2])
	}
	// live-only：不写 JSONL（Recorder 只收到初始 user 记录，没有任何参数增量记录）。
	for _, kind := range recorded {
		if kind == "tool_arg_delta" {
			t.Fatal("参数增量被写入 JSONL")
		}
	}

	// 新增字段带 omitempty：既有事件的 JSON 不受影响，新事件带小写标签。
	data, err := json.Marshal(Event{Kind: EventToolArgDelta, ToolCallID: "c", ArgDelta: "{}", ToolArgIndex: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"arg_delta":"{}"`) || !strings.Contains(string(data), `"tool_arg_index":2`) {
		t.Fatalf("事件 JSON 缺少增量字段: %s", data)
	}
	zero, _ := json.Marshal(Event{})
	if strings.Contains(string(zero), "arg_delta") {
		t.Fatalf("零值事件被改变: %s", zero)
	}
}

func TestToolArgDeltaOffFallback(t *testing.T) {
	t.Setenv("ARGENT_TOOL_ARG_DELTA", "0")
	s := newRunState(Options{Model: "m", Chatter: hookChatter{}}, "go", nil)
	if _, err := s.chat(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	for _, e := range s.eventsSnapshot() {
		if e.Kind == EventToolArgDelta {
			t.Fatal("=0 时不应发布增量事件")
		}
	}
}
