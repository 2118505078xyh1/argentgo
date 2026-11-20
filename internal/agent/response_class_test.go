package agent

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestClassifyResponseFourKinds covers the four primary response classes and
// the three derived predicates. The function is pure, so the cases are plain
// argument tuples with no fakes or I/O.
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

// TestClassifyResponseFinishBoundaries pins the finish-reason edge cases:
// protocol spellings, case/whitespace folding, real tools overriding the
// finish reason, and non-tool reasons never producing a downgrade.
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

// TestClassifyReplyEmptyAndReasoningOnly checks the reply-level adapter: usage
// metadata must never make a reply look visible, while reasoning-only and
// genuinely empty replies are the two classes a caller may correct.
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

// TestClassifyReplyToolCallsEmpty verifies the safe downgrade at the reply
// level: a finish reason that announces tools without any tool parts is not a
// tool turn, must not be corrected into another nudge, and downgrades to stop.
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

	// A genuine tool turn with the same finish reason is never downgraded.
	reply := toolReply(toolCall("c1", "read", `{}`))
	reply.FinishReason = "tool_calls"
	if got := ClassifyReply(reply); !got.IsToolTurn() || got.IsDowngraded() {
		t.Fatalf("genuine tool turn classified as %v", got)
	}
}

// TestClassifyReplyCJK checks that multi-byte text and Unicode whitespace are
// handled by rune-aware TrimSpace: CJK content is visible, a full-width-space
// only reply is not, and CJK reasoning alone stays reasoning-only.
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

// TestResponseKindString pins the diagnostic names so logs stay stable.
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
