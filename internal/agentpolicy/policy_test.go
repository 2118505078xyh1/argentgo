package agentpolicy

import (
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestNormalizeArgs 锁定参数规范化的三行为：JSON 键递归排序、非 JSON 折叠空白、空串归一为 {}。
func TestNormalizeArgs(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", "{}"},
		{"whitespace only", "  \t ", "{}"},
		{"sorted keys", `{"b":1,"a":2}`, `{"a":2,"b":1}`},
		{"nested sorted keys", `{"outer":{"z":1,"a":2}}`, `{"outer":{"a":2,"z":1}}`},
		{"whitespace stripped", `{ "a" : 1 }`, `{"a":1}`},
		{"non json collapses", "hello   world\nagain", "hello world again"},
		{"invalid json stays folded", "{broken", "{broken"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeArgs(tc.in); got != tc.want {
				t.Fatalf("NormalizeArgs(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestClassifyResponseFourKinds 覆盖四种主要回复类别与降级信号，并验证三个派生谓词。
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
		{"tool parts", true, false, false, "tool_calls", ResponseToolCalls, true, false, false},
		{"visible answer", false, true, false, "stop", ResponseContent, false, false, false},
		{"reasoning only", false, false, true, "stop", ResponseReasoningOnly, false, true, false},
		{"empty", false, false, false, "stop", ResponseEmpty, false, true, false},
		{"downgrade", false, false, false, "tool_calls", ResponseToolCallsMissing, false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyResponse(tc.tools, tc.content, tc.reasoning, tc.finish)
			if got != tc.want {
				t.Fatalf("ClassifyResponse = %v, want %v", got, tc.want)
			}
			if got.IsToolTurn() != tc.wantToolTurn || got.NeedsCorrection() != tc.wantCorrect || got.IsDowngraded() != tc.wantDowngrade {
				t.Fatalf("predicates for %v: tool=%v correct=%v downgrade=%v", got,
					got.IsToolTurn(), got.NeedsCorrection(), got.IsDowngraded())
			}
		})
	}
}

// TestClassifyResponseFinishBoundaries 锁定结束原因边界：各协议写法、大小写/空白折叠、
// 真实工具优先于结束原因，以及非工具原因绝不产生降级。
func TestClassifyResponseFinishBoundaries(t *testing.T) {
	cases := []struct {
		name    string
		tools   bool
		content bool
		finish  string
		want    ResponseKind
	}{
		{"openai tool_calls", false, false, "tool_calls", ResponseToolCallsMissing},
		{"anthropic tool_use", false, false, "tool_use", ResponseToolCallsMissing},
		{"responses function_call", false, false, "function_call", ResponseToolCallsMissing},
		{"responses function_calls", false, false, "function_calls", ResponseToolCallsMissing},
		{"padded upper case", false, false, "  TOOL_CALLS \t", ResponseToolCallsMissing},
		{"mixed case", false, false, "Tool_Use", ResponseToolCallsMissing},
		{"real tools win", true, false, "tool_calls", ResponseToolCalls},
		{"real tools win over length", true, false, "length", ResponseToolCalls},
		{"content with tool finish downgrades", false, true, "tool_calls", ResponseToolCallsMissing},
		{"stop stays empty", false, false, "stop", ResponseEmpty},
		{"length stays empty", false, false, "length", ResponseEmpty},
		{"empty reason", false, false, "", ResponseEmpty},
		{"unknown reason", false, false, "content_filter", ResponseEmpty},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyResponse(tc.tools, tc.content, false, tc.finish); got != tc.want {
				t.Fatalf("finish %q => %v, want %v", tc.finish, got, tc.want)
			}
		})
	}
}

// TestClassifyReplyFromMessage 验证回复级适配器：用量元数据不算可见内容，
// 空白文本属于 EMPTY，纯推理属于 ReasoningOnly。
func TestClassifyReplyFromMessage(t *testing.T) {
	if got := ClassifyReply(llm.Message{Content: "  \t "}); got != ResponseEmpty {
		t.Fatalf("blank content => %v, want ResponseEmpty", got)
	}
	if got := ClassifyReply(llm.Message{Reasoning: "thinking"}); got != ResponseReasoningOnly {
		t.Fatalf("reasoning only => %v, want ResponseReasoningOnly", got)
	}
	if got := ClassifyReply(llm.Message{Content: "answer", Reasoning: "thinking"}); got != ResponseContent {
		t.Fatalf("content over reasoning => %v, want ResponseContent", got)
	}
	if got := ClassifyReply(llm.Message{FinishReason: "tool_calls"}); got != ResponseToolCallsMissing {
		t.Fatalf("declared tool turn => %v, want ResponseToolCallsMissing", got)
	}
}

// TestResponseKindString 锁定稳定的日志名称。
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
			t.Fatalf("ResponseKind(%d).String() = %q, want %q", kind, got, want)
		}
	}
}

// TestDecideCompactRetry 覆盖失败词汇表与重试预算边界，确保纯决策逐字节不变。
func TestDecideCompactRetry(t *testing.T) {
	cases := []struct {
		name  string
		kind  llm.FailureKind
		soFar int
		max   int
		want  CompactRetryDecision
	}{
		{"overflow first", llm.FailureContextOverflow, 0, 1, CompactRetry},
		{"overflow exhausted", llm.FailureContextOverflow, 1, 1, CompactStop},
		{"overflow with budget two", llm.FailureContextOverflow, 0, 2, CompactRetry},
		{"zero max", llm.FailureContextOverflow, 0, 0, CompactStop},
		{"negative max", llm.FailureContextOverflow, 0, -1, CompactStop},
		{"auth", llm.FailureAuth, 0, 1, CompactStop},
		{"quota", llm.FailureQuota, 0, 1, CompactStop},
		{"transient", llm.FailureTransient, 0, 1, CompactStop},
		{"unknown", llm.FailureUnknown, 0, 1, CompactStop},
		{"zero value", llm.FailureKind(""), 0, 1, CompactStop},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DecideCompactRetry(tc.kind, tc.soFar, tc.max); got != tc.want {
				t.Fatalf("DecideCompactRetry(%q,%d,%d) = %q, want %q", tc.kind, tc.soFar, tc.max, got, tc.want)
			}
		})
	}
}

// TestMaxCompactRetriesIsOne 守护循环所依赖的"仅一次重放"上限。
func TestMaxCompactRetriesIsOne(t *testing.T) {
	if MaxCompactRetries != 1 {
		t.Fatalf("MaxCompactRetries = %d, want 1", MaxCompactRetries)
	}
	if CompactRetry != "retry" || CompactStop != "stop" {
		t.Fatalf("decision wire values changed: %q / %q", CompactRetry, CompactStop)
	}
}
