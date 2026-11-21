package llm_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestCollectUsageAccumulatesMultipleMessages 验证跨消息的四个字段被逐条累加。
func TestCollectUsageAccumulatesMultipleMessages(t *testing.T) {
	msgs := []llm.Message{
		{Role: "assistant", Usage: &llm.Usage{PromptTokens: 10, CompletionTokens: 2, CachedTokens: 4}},
		{Role: "assistant", Usage: &llm.Usage{PromptTokens: 7, CompletionTokens: 5, CachedTokens: 3}},
		{Role: "assistant", Usage: &llm.Usage{PromptTokens: 1, CompletionTokens: 1}},
	}

	got := llm.CollectUsage(msgs)
	want := llm.UsageTotals{
		PromptTokens:     18,
		CompletionTokens: 8,
		CachedTokens:     7,
		TotalTokens:      26, // (10+2)+(7+5)+(1+1)
	}
	if got != want {
		t.Fatalf("汇总不符\n got: %#v\nwant: %#v", got, want)
	}
}

// TestCollectUsageSkipsNilUsage 验证 Usage 为 nil 的消息（用户消息、工具结果等）被跳过。
func TestCollectUsageSkipsNilUsage(t *testing.T) {
	msgs := []llm.Message{
		{Role: "user", Content: "你好"},
		{Role: "assistant", Usage: &llm.Usage{PromptTokens: 9, CompletionTokens: 4, CachedTokens: 2}},
		{Role: "tool", Content: "result"},
		{Role: "assistant"},
		{Role: "assistant", Usage: &llm.Usage{PromptTokens: 3, CompletionTokens: 1}},
	}

	got := llm.CollectUsage(msgs)
	want := llm.UsageTotals{
		PromptTokens:     12,
		CompletionTokens: 5,
		CachedTokens:     2,
		TotalTokens:      17,
	}
	if got != want {
		t.Fatalf("nil Usage 应被跳过\n got: %#v\nwant: %#v", got, want)
	}
}

// TestCollectUsageCachedIndependent 验证 CachedTokens 独立累计，
// 既不改变 PromptTokens/CompletionTokens 的口径，也不重复计入 TotalTokens。
func TestCollectUsageCachedIndependent(t *testing.T) {
	withCache := llm.CollectUsage([]llm.Message{
		{Usage: &llm.Usage{PromptTokens: 100, CompletionTokens: 20, CachedTokens: 60}},
	})
	withoutCache := llm.CollectUsage([]llm.Message{
		{Usage: &llm.Usage{PromptTokens: 100, CompletionTokens: 20}},
	})

	if withCache.PromptTokens != withoutCache.PromptTokens ||
		withCache.CompletionTokens != withoutCache.CompletionTokens ||
		withCache.TotalTokens != withoutCache.TotalTokens {
		t.Fatalf("缓存细分不得影响其它口径：with=%#v without=%#v", withCache, withoutCache)
	}
	if withCache.CachedTokens != 60 || withoutCache.CachedTokens != 0 {
		t.Fatalf("CachedTokens 应独立累计：with=%d without=%d", withCache.CachedTokens, withoutCache.CachedTokens)
	}
}

// TestCollectUsageTotalTokensDefinition 固定 TotalTokens 口径：
// 等于各消息 Usage.TotalTokens() 之和（提示 + 补全），
// 而不是把 CachedTokens 也当作独立计数重复相加。
func TestCollectUsageTotalTokensDefinition(t *testing.T) {
	msgs := []llm.Message{
		{Usage: &llm.Usage{PromptTokens: 10, CompletionTokens: 3, CachedTokens: 6}},
		{Usage: &llm.Usage{PromptTokens: 20, CompletionTokens: 7, CachedTokens: 12}},
	}
	got := llm.CollectUsage(msgs)

	// 期望值取自每条消息的 Usage.TotalTokens()，而非所有字段的裸和。
	var sumOfTotalTokensMethod int
	for _, m := range msgs {
		sumOfTotalTokensMethod += m.Usage.TotalTokens()
	}
	if got.TotalTokens != sumOfTotalTokensMethod {
		t.Errorf("TotalTokens = %d, want Usage.TotalTokens() 之和 %d", got.TotalTokens, sumOfTotalTokensMethod)
	}
	if want := got.PromptTokens + got.CompletionTokens; got.TotalTokens != want {
		t.Errorf("TotalTokens = %d, want PromptTokens+CompletionTokens = %d", got.TotalTokens, want)
	}
	// 缓存是提示的子集，重复累加会多出 CachedTokens。
	naiveSum := got.PromptTokens + got.CompletionTokens + got.CachedTokens
	if got.TotalTokens == naiveSum && got.CachedTokens != 0 {
		t.Errorf("TotalTokens 不应重复计入 CachedTokens：got=%d naive=%d", got.TotalTokens, naiveSum)
	}
}

// TestCollectUsageDoesNotModifyInput 验证汇总不改写消息切片及其 Usage 内容。
func TestCollectUsageDoesNotModifyInput(t *testing.T) {
	first := &llm.Usage{PromptTokens: 10, CompletionTokens: 2, CachedTokens: 4}
	second := &llm.Usage{PromptTokens: 7, CompletionTokens: 5, CachedTokens: 3}
	msgs := []llm.Message{
		{Role: "assistant", Usage: first},
		{Role: "user", Content: "原始内容"},
		{Role: "assistant", Usage: second},
	}

	before := make([]llm.Usage, len(msgs))
	for i, m := range msgs {
		if m.Usage != nil {
			before[i] = *m.Usage
		}
	}
	_ = llm.CollectUsage(msgs)

	if len(msgs) != 3 || msgs[0].Usage != first || msgs[2].Usage != second {
		t.Fatal("汇总不得改变消息切片结构或 Usage 指针")
	}
	for i, m := range msgs {
		if m.Usage == nil {
			continue
		}
		if *m.Usage != before[i] {
			t.Errorf("消息 %d 的 Usage 被修改：got %#v, want %#v", i, *m.Usage, before[i])
		}
	}
	if msgs[1].Content != "原始内容" {
		t.Errorf("消息内容被修改：%q", msgs[1].Content)
	}
}

// TestCollectUsageEmptyInput 验证空输入与 nil 输入都返回零值汇总。
func TestCollectUsageEmptyInput(t *testing.T) {
	for name, msgs := range map[string][]llm.Message{
		"nil":   nil,
		"empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			got := llm.CollectUsage(msgs)
			want := llm.UsageTotals{}
			if got != want {
				t.Fatalf("空输入应返回零值：got %#v, want %#v", got, want)
			}
		})
	}
}

// TestCollectUsageDeterministic 验证相同输入重复汇总得到完全相同的结果。
func TestCollectUsageDeterministic(t *testing.T) {
	msgs := []llm.Message{
		{Usage: &llm.Usage{PromptTokens: 10, CompletionTokens: 2, CachedTokens: 4}},
		{Role: "user"},
		{Usage: &llm.Usage{PromptTokens: 7, CompletionTokens: 5, CachedTokens: 3}},
	}
	first := llm.CollectUsage(msgs)
	for i := 0; i < 5; i++ {
		if got := llm.CollectUsage(msgs); got != first {
			t.Fatalf("第 %d 次汇总结果漂移：got %#v, want %#v", i, got, first)
		}
	}
}

// TestTraceFromUsageFields 验证用量汇总到 RequestTrace 的字段映射正确，
// 且 Status 在写入前脱敏、Redacted 为真、入参不被修改。
func TestTraceFromUsageFields(t *testing.T) {
	u := llm.UsageTotals{
		PromptTokens:     100,
		CompletionTokens: 20,
		CachedTokens:     60,
		TotalTokens:      120,
	}
	const secret = "sk-ant-api03-abcdefghijklmnopqrstuvwxyz"
	status := "模型-Alpha 请求失败：api_key=" + secret

	got := llm.TraceFromUsage(llm.ProtocolChatCompletions, "模型-Alpha", u, 1<<40, status)

	if got.Protocol != llm.ProtocolChatCompletions {
		t.Errorf("Protocol = %q, want %q", got.Protocol, llm.ProtocolChatCompletions)
	}
	if want := "模型-Alpha"; got.Model != want {
		t.Errorf("Model = %q, want %q", got.Model, want)
	}
	if got.PromptTokens != u.PromptTokens {
		t.Errorf("PromptTokens = %d, want %d", got.PromptTokens, u.PromptTokens)
	}
	if got.CompletionTokens != u.CompletionTokens {
		t.Errorf("CompletionTokens = %d, want %d", got.CompletionTokens, u.CompletionTokens)
	}
	if got.CachedTokens != u.CachedTokens {
		t.Errorf("CachedTokens = %d, want %d", got.CachedTokens, u.CachedTokens)
	}
	if got.LatencyMS != 1<<40 {
		t.Errorf("LatencyMS = %d, want %d", got.LatencyMS, int64(1<<40))
	}
	if !got.Redacted {
		t.Error("Redacted 应为 true")
	}
	if strings.Contains(got.Status, secret) {
		t.Errorf("凭据不得落入追踪：Status=%q", got.Status)
	}
	if !strings.Contains(got.Status, "请求失败") || !strings.Contains(got.Status, "模型-Alpha") {
		t.Errorf("普通措辞与 CJK 文本应保留：Status=%q", got.Status)
	}
	if status != "模型-Alpha 请求失败：api_key="+secret {
		t.Errorf("入参 status 被修改：%q", status)
	}
	if u != (llm.UsageTotals{PromptTokens: 100, CompletionTokens: 20, CachedTokens: 60, TotalTokens: 120}) {
		t.Errorf("入参 u 被修改：%#v", u)
	}
}

// TestTraceFromUsageDeterministic 验证相同入参产生完全相同的追踪记录。
func TestTraceFromUsageDeterministic(t *testing.T) {
	u := llm.UsageTotals{PromptTokens: 10, CompletionTokens: 20, CachedTokens: 5, TotalTokens: 30}
	first := llm.TraceFromUsage(llm.ProtocolResponses, "m", u, 123, "ok")
	if second := llm.TraceFromUsage(llm.ProtocolResponses, "m", u, 123, "ok"); !reflect.DeepEqual(first, second) {
		t.Fatalf("相同入参应得到相同记录\n first: %#v\nsecond: %#v", first, second)
	}
}
