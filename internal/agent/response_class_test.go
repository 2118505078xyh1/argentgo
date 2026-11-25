package agent

import (
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestClassifyResponseFourKinds 覆盖四种主要回复类别和三个派生谓词。该函数是纯函数，因此用例只是普通的参数元组，没有伪实现或 I/O。
// （接上行）
// （接上行）
func TestClassifyResponseFourKinds(t *testing.T) {
	cases := []struct {
		name          string
		tools         bool
		content       bool
		reasoning     bool
		finish        string
		want          ResponseKind
		wantToolTurn  bool
		wantCorrect   bool
		wantDowngrade bool
	}{
		{
			name: "tool parts make a tool turn", tools: true, finish: "tool_calls",
			want: ResponseToolCalls, wantToolTurn: true,
		},
		{
			name: "visible answer without tools is content", content: true, finish: "stop",
			want: ResponseContent,
		},
		{
			name: "reasoning only is its own class", reasoning: true, finish: "stop",
			want: ResponseReasoningOnly, wantCorrect: true,
		},
		{
			name: "no signal at all is empty", finish: "stop",
			want: ResponseEmpty, wantCorrect: true,
		},
		{
			name: "tool finish without parts is the downgrade", finish: "tool_calls",
			want: ResponseToolCallsMissing, wantDowngrade: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyResponse(tc.tools, tc.content, tc.reasoning, tc.finish)
			if got != tc.want {
				t.Fatalf("ClassifyResponse(%v,%v,%v,%q) = %v, want %v",
					tc.tools, tc.content, tc.reasoning, tc.finish, got, tc.want)
			}
			if got.IsToolTurn() != tc.wantToolTurn {
				t.Fatalf("%v.IsToolTurn() = %v, want %v", got, got.IsToolTurn(), tc.wantToolTurn)
			}
			if got.NeedsCorrection() != tc.wantCorrect {
				t.Fatalf("%v.NeedsCorrection() = %v, want %v", got, got.NeedsCorrection(), tc.wantCorrect)
			}
			if got.IsDowngraded() != tc.wantDowngrade {
				t.Fatalf("%v.IsDowngraded() = %v, want %v", got, got.IsDowngraded(), tc.wantDowngrade)
			}
		})
	}
}

// TestClassifyResponseFinishBoundaries 锁定结束原因的边界情况：协议写法、大小写/空白折叠、真实工具覆盖结束原因，以及非工具原因绝不产生降级。
// （接上行）
// （接上行）
func TestClassifyResponseFinishBoundaries(t *testing.T) {
	cases := []struct {
		name    string
		tools   bool
		content bool
		finish  string
		want    ResponseKind
	}{
		{name: "openai tool_calls", finish: "tool_calls", want: ResponseToolCallsMissing},
		{name: "anthropic tool_use", finish: "tool_use", want: ResponseToolCallsMissing},
		{name: "responses function_call", finish: "function_call", want: ResponseToolCallsMissing},
		{name: "responses function_calls", finish: "function_calls", want: ResponseToolCallsMissing},
		{name: "upper case and padding", finish: "  TOOL_CALLS \t", want: ResponseToolCallsMissing},
		{name: "mixed case tool_use", finish: "Tool_Use", want: ResponseToolCallsMissing},
		{
			name: "real tools win over tool finish", tools: true, finish: "tool_calls",
			want: ResponseToolCalls,
		},
		{
			name: "real tools win over length finish", tools: true, finish: "length",
			want: ResponseToolCalls,
		},
		{
			name: "content with tool finish is still downgraded", content: true, finish: "tool_calls",
			want: ResponseToolCallsMissing,
		},
		{name: "stop stays empty", finish: "stop", want: ResponseEmpty},
		{name: "length stays empty", finish: "length", want: ResponseEmpty},
		{name: "empty reason stays empty", finish: "", want: ResponseEmpty},
		{name: "unknown reason stays empty", finish: "content_filter", want: ResponseEmpty},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyResponse(tc.tools, tc.content, false, tc.finish); got != tc.want {
				t.Fatalf("finish %q (tools=%v, content=%v) => %v, want %v",
					tc.finish, tc.tools, tc.content, got, tc.want)
			}
		})
	}
}

// TestClassifyReplyEmptyAndReasoningOnly 检查回复级适配器：用量元数据绝不能让回复看起来可见，而纯推理和真正的空回复是调用方可以纠正的两类。
// （接上行）
// （接上行）
func TestClassifyReplyEmptyAndReasoningOnly(t *testing.T) {
	usage := &llm.Usage{PromptTokens: 10, CompletionTokens: 2}

	cases := []struct {
		name  string
		reply llm.Message
		want  ResponseKind
	}{
		{
			name:  "empty reply with usage is still empty",
			reply: llm.Message{Role: "assistant", Usage: usage},
			want:  ResponseEmpty,
		},
		{
			name:  "blank text with usage is still empty",
			reply: llm.Message{Role: "assistant", Content: "  \t\n", Usage: usage},
			want:  ResponseEmpty,
		},
		{
			name:  "reasoning only with usage",
			reply: llm.Message{Role: "assistant", Reasoning: "thinking", Usage: usage},
			want:  ResponseReasoningOnly,
		},
		{
			name:  "reasoning plus text is content",
			reply: llm.Message{Role: "assistant", Reasoning: "thinking", Content: "answer"},
			want:  ResponseContent,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyReply(tc.reply)
			if got != tc.want {
				t.Fatalf("ClassifyReply(%+v) = %v, want %v", tc.reply, got, tc.want)
			}
			if !got.NeedsCorrection() && (tc.want == ResponseEmpty || tc.want == ResponseReasoningOnly) {
				t.Fatalf("%v should be correctable", got)
			}
		})
	}
}

// TestClassifyReplyToolCallsEmpty 验证回复级的安全降级：宣告工具但没有任何工具部分的结束原因不是工具轮次，不得被纠正为又一次提示，而是降级为停止。
// （接上行）
// （接上行）
func TestClassifyReplyToolCallsEmpty(t *testing.T) {
	cases := []struct {
		name   string
		finish string
	}{
		{name: "openai", finish: "tool_calls"},
		{name: "anthropic", finish: "tool_use"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply := textReply("")
			reply.FinishReason = tc.finish
			got := ClassifyReply(reply)
			if got != ResponseToolCallsMissing {
				t.Fatalf("ClassifyReply = %v, want tool_calls_missing", got)
			}
			if got.IsToolTurn() {
				t.Fatal("a malformed tool turn must not be dispatched as a tool turn")
			}
			if got.NeedsCorrection() {
				t.Fatal("a malformed tool turn must not be nudged; downgrade it to a stop")
			}
			if !got.IsDowngraded() {
				t.Fatal("expected the downgrade marker")
			}
		})
	}

	// 具有相同结束原因的真正工具轮次绝不会被降级。
	reply := toolReply(toolCall("c1", "read", `{}`))
	reply.FinishReason = "tool_calls"
	if got := ClassifyReply(reply); !got.IsToolTurn() || got.IsDowngraded() {
		t.Fatalf("genuine tool turn classified as %v", got)
	}
}

// TestClassifyReplyCJK 检查多字节文本和 Unicode 空白由感知 rune 的 TrimSpace 处理：CJK 内容可见，仅全角空格的回复不可见，仅有 CJK 推理仍属纯推理。
// （接上行）
// （接上行）
func TestClassifyReplyCJK(t *testing.T) {
	cases := []struct {
		name  string
		reply llm.Message
		want  ResponseKind
	}{
		{
			name:  "CJK answer is visible content",
			reply: llm.Message{Content: "这是中文回答。", FinishReason: "stop"},
			want:  ResponseContent,
		},
		{
			name:  "CJK reasoning only",
			reply: llm.Message{Reasoning: "正在思考问题。", FinishReason: "stop"},
			want:  ResponseReasoningOnly,
		},
		{
			name:  "full width space only is empty",
			reply: llm.Message{Content: "\u3000\u3000", FinishReason: "stop"},
			want:  ResponseEmpty,
		},
		{
			name:  "full width spaces around CJK still count as content",
			reply: llm.Message{Content: "\u3000中文\u3000"},
			want:  ResponseContent,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyReply(tc.reply); got != tc.want {
				t.Fatalf("ClassifyReply(%+v) = %v, want %v", tc.reply, got, tc.want)
			}
		})
	}
}

// TestResponseKindString 固定诊断名称，使日志保持稳定。
func TestResponseKindString(t *testing.T) {
	cases := map[ResponseKind]string{
		ResponseToolCalls:        "tool_calls",
		ResponseContent:          "content",
		ResponseReasoningOnly:    "reasoning_only",
		ResponseEmpty:            "empty",
		ResponseToolCallsMissing: "tool_calls_missing",
		ResponseKind(0):          "unknown",
	}
	for kind, want := range cases {
		if got := kind.String(); got != want {
			t.Fatalf("%d.String() = %q, want %q", kind, got, want)
		}
	}
}
