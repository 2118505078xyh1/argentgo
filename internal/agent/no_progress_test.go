package agent

import (
	"context"
	"strings"
	"testing"

	"example.com/argentgo/internal/llm"
)

// repeatedSigs 构建 n 个 name/args/result 都相同的签名。
func repeatedSigs(n int, name, args, result string) []ToolCallSignature {
	out := make([]ToolCallSignature, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, ToolCallSignature{Name: name, Args: args, Result: result})
	}
	return out
}

func TestDetectNoProgressIdenticalCallsStuck(t *testing.T) {
	calls := repeatedSigs(DefaultNoProgressThreshold, "read_file", `{"path":"a.go"}`, "same-hash")
	stuck, reason := DetectNoProgress(calls)
	if !stuck {
		t.Fatal("identical calls with identical results should be stuck")
	}
	if !strings.Contains(reason, "read_file") || !strings.Contains(reason, "no progress") {
		t.Fatalf("reason = %q, want a readable reason naming the tool", reason)
	}
}

func TestDetectNoProgressDifferentArgsNotStuck(t *testing.T) {
	calls := []ToolCallSignature{
		{Name: "read_file", Args: `{"path":"a.go"}`, Result: "r"},
		{Name: "read_file", Args: `{"path":"b.go"}`, Result: "r"},
		{Name: "read_file", Args: `{"path":"a.go"}`, Result: "r"},
	}
	if stuck, reason := DetectNoProgress(calls); stuck {
		t.Fatalf("different arguments: got stuck=%v reason=%q, want not stuck", stuck, reason)
	}
}

func TestDetectNoProgressThresholdBoundary(t *testing.T) {
	tests := []struct {
		name      string
		threshold int
		calls     int
		wantStuck bool
	}{
		{"one below default", 0, DefaultNoProgressThreshold - 1, false},
		{"exactly default", 0, DefaultNoProgressThreshold, true},
		{"one above default", 0, DefaultNoProgressThreshold + 1, true},
		{"explicit 2: one below", 2, 1, false},
		{"explicit 2: exact", 2, 2, true},
		{"explicit 4: one below", 4, 3, false},
		{"explicit 4: exact", 4, 4, true},
		{"below-one threshold falls back to default", 1 - 3, DefaultNoProgressThreshold, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := repeatedSigs(tt.calls, "read", `{}`, "r")
			stuck, _ := DetectNoProgressWithThreshold(calls, tt.threshold)
			if stuck != tt.wantStuck {
				t.Fatalf("calls=%d threshold=%d: stuck=%v, want %v", tt.calls, tt.threshold, stuck, tt.wantStuck)
			}
		})
	}
}

func TestDetectNoProgressCJKArgs(t *testing.T) {
	t.Run("identical CJK args are stuck", func(t *testing.T) {
		calls := repeatedSigs(DefaultNoProgressThreshold, "读文件", `{"路径":"项目/主.go"}`, "结果")
		if stuck, reason := DetectNoProgress(calls); !stuck || reason == "" {
			t.Fatalf("identical CJK arguments: got stuck=%v reason=%q, want stuck", stuck, reason)
		}
	})
	t.Run("different CJK args are not stuck", func(t *testing.T) {
		calls := []ToolCallSignature{
			{Name: "读文件", Args: `{"路径":"项目/甲.go"}`, Result: "结果"},
			{Name: "读文件", Args: `{"路径":"项目/乙.go"}`, Result: "结果"},
			{Name: "读文件", Args: `{"路径":"项目/甲.go"}`, Result: "结果"},
		}
		if stuck, reason := DetectNoProgress(calls); stuck {
			t.Fatalf("different CJK arguments: got stuck=%v reason=%q, want not stuck", stuck, reason)
		}
	})
}

func TestDetectNoProgressEmptyInput(t *testing.T) {
	for _, calls := range [][]ToolCallSignature{nil, {}} {
		stuck, reason := DetectNoProgress(calls)
		if stuck || reason != "" {
			t.Fatalf("empty input: got stuck=%v reason=%q, want not stuck with empty reason", stuck, reason)
		}
	}
}

func TestDetectNoProgressDifferentResultsNotStuck(t *testing.T) {
	t.Run("varying results are progress", func(t *testing.T) {
		calls := []ToolCallSignature{
			{Name: "read", Args: `{}`, Result: "a"},
			{Name: "read", Args: `{}`, Result: "b"},
			{Name: "read", Args: `{}`, Result: "a"},
		}
		if stuck, reason := DetectNoProgress(calls); stuck {
			t.Fatalf("varying results: got stuck=%v reason=%q, want not stuck", stuck, reason)
		}
	})
	t.Run("identical results are stuck", func(t *testing.T) {
		calls := repeatedSigs(DefaultNoProgressThreshold, "read", `{}`, "a")
		if stuck, _ := DetectNoProgress(calls); !stuck {
			t.Fatal("identical results should be stuck")
		}
	})
	t.Run("absent results count as a stable value", func(t *testing.T) {
		calls := repeatedSigs(DefaultNoProgressThreshold, "read", `{}`, "")
		if stuck, _ := DetectNoProgress(calls); !stuck {
			t.Fatal("calls with no recorded results should count as identical")
		}
	})
}

func TestSignatureFromToolCallNormalizesArgs(t *testing.T) {
	a := SignatureFromToolCall(toolCall("1", "read", `{"path":"a","line":1}`))
	b := SignatureFromToolCall(toolCall("2", "read", `{"line":1,"path":"a"}`))
	if a.Name != "read" || a.Args != b.Args {
		t.Fatalf("key order must not hide a repeat: a=%+v b=%+v", a, b)
	}
	if a.Result != "" {
		t.Fatalf("pre-result signature should have an empty result, got %q", a.Result)
	}
}

func TestToolCallSignaturesPairsResults(t *testing.T) {
	calls := []llm.ToolCall{
		toolCall("1", "read", `{"path":"a.go"}`),
		toolCall("2", "read", `{"path":"a.go"}`),
	}
	results := []callResult{{output: "same"}, {output: "same"}}
	sigs := ToolCallSignatures(calls, results)
	if len(sigs) != 2 {
		t.Fatalf("signatures = %d, want 2", len(sigs))
	}
	if stuck, _ := DetectNoProgress(sigs); stuck {
		t.Fatal("two identical pairs are below the default threshold of three; should not stick")
	}

	// 显式阈值为 2 时，同样的一对就是循环。
	if stuck, _ := DetectNoProgressWithThreshold(sigs, 2); !stuck {
		t.Fatal("two identical calls with identical results should be stuck at threshold 2")
	}

	// 较短的 results 切片会让尾部的调用没有结果。
	partial := ToolCallSignatures(calls, results[:1])
	if partial[0].Result == "" || partial[1].Result != "" {
		t.Fatalf("partial results pairing: %+v", partial)
	}
}

func TestSignaturesFromEvents(t *testing.T) {
	events := []Event{
		{Kind: EventTurnStart, Turn: 1},
		{Kind: EventToolStart, ToolCallID: "1", ToolName: "read", Args: `{"path":"甲.go"}`},
		{Kind: EventToolEnd, ToolCallID: "1", ToolName: "read", Args: `{"path":"甲.go"}`, Output: "内容"},
		// 只有开始没有结束的调用必须被忽略；它没有产生结果。
		{Kind: EventToolStart, ToolCallID: "2", ToolName: "read", Args: `{"path":"乙.go"}`},
	}
	sigs := SignaturesFromEvents(events)
	if len(sigs) != 1 {
		t.Fatalf("signatures = %d, want 1 (only tool_end)", len(sigs))
	}
	if sigs[0].Name != "read" || sigs[0].Args != `{"path":"甲.go"}` || sigs[0].Result == "" {
		t.Fatalf("unexpected signature: %+v", sigs[0])
	}
	// 相同输出哈希为相同的摘要；不同输出则不会。
	same := ToolCallSignatures([]llm.ToolCall{toolCall("3", "read", `{"path":"甲.go"}`)}, []callResult{{output: "内容"}})
	if sigs[0].Result != same[0].Result {
		t.Fatal("equal outputs must hash to the same result summary")
	}
	other := ToolCallSignatures([]llm.ToolCall{toolCall("4", "read", `{}`)}, []callResult{{output: "不同内容"}})
	if sigs[0].Result == other[0].Result {
		t.Fatal("different outputs must hash to different result summaries")
	}
}

// noProgressRun 驱动一次脚本化运行，返回其结果、发出的事件以及模型请求次数，
// 使接线测试能断言提前终止。
func noProgressRun(t *testing.T, cfg *NoProgressConfig, toolRun func(context.Context, string, string) (string, error), replies ...llm.Message) (Result, []Event, int, int) {
	t.Helper()
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}, run: toolRun}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n > len(replies) {
			t.Fatalf("unexpected request %d (script has %d replies)", n, len(replies))
		}
		return replies[n-1], nil
	}}
	sink := &eventSink{}
	runner := &Runner{Opts: Options{
		Model:      "m",
		Chatter:    chat,
		Tools:      tools,
		Emit:       sink.emit,
		MaxRepeats: 10, // 让独立的重复防护器不参与这些测试
		NoProgress: cfg,
	}}
	res, err := runner.Run(context.Background(), "work")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res, sink.events, chat.count(), tools.count()
}

func sameOutput(context.Context, string, string) (string, error) { return "same output", nil }

func TestNoProgressOptInWiring(t *testing.T) {
	const args = `{"path":"a.go"}`
	threeCalls := []llm.Message{
		toolReply(toolCall("c1", "read", args)),
		toolReply(toolCall("c2", "read", args)),
		toolReply(toolCall("c3", "read", args)),
	}

	t.Run("default off leaves the run unchanged", func(t *testing.T) {
		replies := append(append([]llm.Message{}, threeCalls...), textReply("done"))
		res, events, requests, calls := noProgressRun(t, nil, sameOutput, replies...)
		if res.Stopped != StopComplete || requests != 4 || calls != 3 {
			t.Fatalf("stopped=%q requests=%d calls=%d, want complete/4/3", res.Stopped, requests, calls)
		}
		for _, e := range events {
			if e.Kind == EventStop && e.Reason == StopStuck {
				t.Fatal("default-off run must not emit a stuck stop")
			}
		}
	})

	t.Run("opt-in stops after the threshold-th identical call", func(t *testing.T) {
		res, events, requests, calls := noProgressRun(t, &NoProgressConfig{Threshold: 3}, sameOutput, threeCalls...)
		if res.Stopped != StopStuck || requests != 3 || calls != 3 {
			t.Fatalf("stopped=%q requests=%d calls=%d, want stuck/3/3", res.Stopped, requests, calls)
		}
		notices := 0
		stops := 0
		for _, e := range events {
			if e.Kind == EventNotice && strings.Contains(e.Text, "no progress") {
				notices++
			}
			if e.Kind == EventStop && e.Reason == StopStuck {
				stops++
			}
		}
		if notices != 1 || stops != 1 {
			t.Fatalf("notices=%d stuck-stops=%d, want 1/1", notices, stops)
		}
	})

	t.Run("opt-in with varying arguments does not stop", func(t *testing.T) {
		replies := []llm.Message{
			toolReply(toolCall("c1", "read", `{"path":"a.go"}`)),
			toolReply(toolCall("c2", "read", `{"path":"b.go"}`)),
			toolReply(toolCall("c3", "read", `{"path":"a.go"}`)),
			textReply("done"),
		}
		res, _, requests, _ := noProgressRun(t, &NoProgressConfig{Threshold: 3}, sameOutput, replies...)
		if res.Stopped != StopComplete || requests != 4 {
			t.Fatalf("stopped=%q requests=%d, want complete/4 (varying args are progress)", res.Stopped, requests)
		}
	})

	t.Run("opt-in with varying results does not stop", func(t *testing.T) {
		varying := func(_ context.Context, _ string, args string) (string, error) {
			if strings.Contains(args, "a.go") {
				return "first", nil
			}
			return "second", nil
		}
		replies := []llm.Message{
			toolReply(toolCall("c1", "read", `{"path":"a.go"}`)),
			toolReply(toolCall("c2", "read", `{"path":"b.go"}`)),
			toolReply(toolCall("c3", "read", `{"path":"a.go"}`)),
			textReply("done"),
		}
		res, _, requests, _ := noProgressRun(t, &NoProgressConfig{Threshold: 2}, varying, replies...)
		if res.Stopped != StopComplete || requests != 4 {
			t.Fatalf("stopped=%q requests=%d, want complete/4 (varying results are progress)", res.Stopped, requests)
		}
	})
}
