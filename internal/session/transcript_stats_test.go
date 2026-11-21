package session

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

// statEntry 构造只关心 Role 与 Data 的转录条目，避免测试被 ID/TS 等无关字段干扰。
func statEntry(role, data string) Entry {
	return Entry{Role: role, Data: json.RawMessage(data)}
}

// TestSummarizeTranscriptCategories 覆盖各类记录：user/assistant/tool/system、成功与
// 失败工具、assistant 自带 tool_calls、以及两种 token 字段来源。
func TestSummarizeTranscriptCategories(t *testing.T) {
	entries := []Entry{
		statEntry("user", `{"content":"你好"}`),
		statEntry("assistant", `{"content":"收到","usage":{"input_tokens":10,"output_tokens":5}}`),
		// assistant 的 tool_calls 不重复计入 ToolCalls；顶层 stats 字段计入 token。
		statEntry("assistant", `{"content":"","tool_calls":[{"name":"bash"}],"prompt_tokens":7,"completion_tokens":3}`),
		statEntry("tool", `{"name":"bash","output":"ok","is_error":false}`),
		statEntry("tool", `{"name":"edit","output":"done","is_error":true}`),
		// 中文失败提示也应被计入错误。
		statEntry("tool", `{"name":"compile","output":"编译失败"}`),
		statEntry("system", `{"text":"压缩摘要"}`),
		// 非工具记录上的显式 is_error 也算一条错误记录。
		statEntry("assistant", `{"content":"oops","is_error":true}`),
	}

	got := SummarizeTranscript(entries)
	want := TranscriptStats{
		Messages:         8,
		UserTurns:        1,
		AssistantTurns:   3,
		ToolCalls:        3,
		Errors:           3,
		PromptTokens:     17,
		CompletionTokens: 8,
	}
	if got != want {
		t.Fatalf("SummarizeTranscript = %+v, want %+v", got, want)
	}
}

// TestSummarizeTranscriptEmpty 验证 nil 与空切片都得到零值摘要。
func TestSummarizeTranscriptEmpty(t *testing.T) {
	want := TranscriptStats{}
	for name, entries := range map[string][]Entry{
		"nil":   nil,
		"empty": {},
	} {
		if got := SummarizeTranscript(entries); got != want {
			t.Errorf("%s 输入 = %+v, want 零值", name, got)
		}
	}
}

// TestSummarizeTranscriptTokenAccumulation 验证多条记录的 token 累加、stats 与
// usage 两种载荷、Claude 风格字段名，以及负数/坏值的忽略。
func TestSummarizeTranscriptTokenAccumulation(t *testing.T) {
	entries := []Entry{
		statEntry("assistant", `{"prompt_tokens":100,"completion_tokens":20}`),
		statEntry("assistant", `{"usage":{"prompt_tokens":50,"completion_tokens":10}}`),
		statEntry("assistant", `{"usage":{"input_tokens":7,"output_tokens":3}}`),
		// 负数与无法解析的数字按 0 处理。
		statEntry("assistant", `{"prompt_tokens":-5,"completion_tokens":-2}`),
		statEntry("assistant", `{"prompt_tokens":"abc","completion_tokens":true}`),
	}

	got := SummarizeTranscript(entries)
	if got.PromptTokens != 157 || got.CompletionTokens != 33 {
		t.Fatalf("token 累加 = (%d, %d), want (157, 33)", got.PromptTokens, got.CompletionTokens)
	}
	if got.Messages != 5 || got.AssistantTurns != 5 {
		t.Fatalf("计数异常：%+v", got)
	}
}

// TestSummarizeTranscriptUsageOverridesTopLevelTokens 固定 usage 子对象优先于顶层
// 汇总字段的约定，避免同一条记录被双计。
func TestSummarizeTranscriptUsageOverridesTopLevelTokens(t *testing.T) {
	got := SummarizeTranscript([]Entry{
		statEntry("assistant", `{"prompt_tokens":999,"completion_tokens":888,"usage":{"input_tokens":1,"output_tokens":2}}`),
	})
	if got.PromptTokens != 1 || got.CompletionTokens != 2 {
		t.Fatalf("usage 优先级 = (%d, %d), want (1, 2)", got.PromptTokens, got.CompletionTokens)
	}
}

// TestSummarizeTranscriptOverflowClamp 验证累加逼近 int64 上限时钳制，且不会环绕为负。
func TestSummarizeTranscriptOverflowClamp(t *testing.T) {
	entries := []Entry{
		statEntry("assistant", `{"prompt_tokens":9223372036854775807,"completion_tokens":9223372036854775807}`),
		statEntry("assistant", `{"prompt_tokens":10,"completion_tokens":10}`),
		// 超出 int64 的字面量与负数：前者钳到上限，后者按 0 处理。
		statEntry("assistant", `{"prompt_tokens":1e30,"completion_tokens":-1}`),
		statEntry("assistant", `{"prompt_tokens":18446744073709551616,"completion_tokens":0}`),
	}

	got := SummarizeTranscript(entries)
	if got.PromptTokens != math.MaxInt64 || got.CompletionTokens != math.MaxInt64 {
		t.Fatalf("溢出钳制 = (%d, %d), want (MaxInt64, MaxInt64)", got.PromptTokens, got.CompletionTokens)
	}
	if got.PromptTokens < 0 || got.CompletionTokens < 0 {
		t.Fatalf("钳制后不应为负：%+v", got)
	}
}

// TestSummarizeTranscriptCJK 验证中日韩文本与 emoji 出现在角色载荷、工具输出与
// 工具名中时统计依旧正确，且中性文本不会被误判为错误。
func TestSummarizeTranscriptCJK(t *testing.T) {
	entries := []Entry{
		statEntry("user", `{"content":"你好，世界 🌏"}`),
		statEntry("user", `{"content":"教师 emulation で終了"}`),
		statEntry("assistant", `{"content":"收到 ✅ こんにちは"}`),
		statEntry("tool", `{"name":"读取文件","output":"文件已读 📄"}`),
	}

	got := SummarizeTranscript(entries)
	want := TranscriptStats{Messages: 4, UserTurns: 2, AssistantTurns: 1, ToolCalls: 1}
	if got != want {
		t.Fatalf("CJK 统计 = %+v, want %+v", got, want)
	}
}

// TestSummarizeTranscriptDoesNotMutateInput 验证纯函数性质：入参切片、元素与
// Data 字节在调用前后逐字段一致。
func TestSummarizeTranscriptDoesNotMutateInput(t *testing.T) {
	entries := []Entry{
		statEntry("user", `{"content":"原样"}`),
		statEntry("assistant", `{"content":"回复","usage":{"input_tokens":3,"output_tokens":4}}`),
		statEntry("tool", `{"output":"失败：保留原文","is_error":true}`),
	}

	before, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal before: %v", err)
	}
	dataBefore := make([][]byte, len(entries))
	for i := range entries {
		dataBefore[i] = append([]byte(nil), entries[i].Data...)
	}

	_ = SummarizeTranscript(entries)
	_ = SummarizeTranscript(entries)

	after, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal after: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("入参被修改：\nbefore: %s\nafter:  %s", before, after)
	}
	for i := range entries {
		if !reflect.DeepEqual(dataBefore[i], []byte(entries[i].Data)) {
			t.Errorf("entry[%d].Data 被修改", i)
		}
	}
}

// TestSummarizeTranscriptDeterministic 验证同一输入多次调用结果一致，且用相同
// 数据重新构造的切片也得到相同摘要（不依赖 map 顺序或内部缓存）。
func TestSummarizeTranscriptDeterministic(t *testing.T) {
	build := func() []Entry {
		return []Entry{
			statEntry("user", `{"content":"一"}`),
			statEntry("assistant", `{"usage":{"prompt_tokens":11,"completion_tokens":2}}`),
			statEntry("tool", `{"output":"failed: boom"}`),
			statEntry("assistant", `{"prompt_tokens":5,"completion_tokens":1}`),
		}
	}

	first := SummarizeTranscript(build())
	second := SummarizeTranscript(build())
	third := SummarizeTranscript(build())
	if first != second || first != third {
		t.Fatalf("结果不确定：%+v / %+v / %+v", first, second, third)
	}
}
