package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// 本文件记录 agent 循环纯热路径的性能基线：
// token 估算、上下文裁剪/回收、参数规范化和
// 事件发出。固定样本存于内存中；不涉及网络、模型或真实
// 墙上时钟依赖。不断言任何墙上时钟阈值。

var (
	benchAgentInt    int
	benchAgentString string
)

func benchToolCall(id, name, args string) llm.ToolCall {
	return llm.ToolCall{
		ID:   id,
		Type: "function",
		Function: llm.ToolCallFunction{
			Name:      name,
			Arguments: args,
		},
	}
}

// benchConversation 在固定的头和尾之间构建 n 对助手/工具消息，
// 匹配裁剪和回收路径实际看到的形状。
func benchConversation(n int, body string) []llm.Message {
	messages := make([]llm.Message, 0, 2*n+3)
	messages = append(messages,
		llm.Message{Role: "system", Content: "rules"},
		llm.Message{Role: "user", Content: "inspect"},
	)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("c%d", i)
		messages = append(messages,
			llm.Message{Role: "assistant", Content: "step", ToolCalls: []llm.ToolCall{benchToolCall(id, "read", `{}`)}},
			llm.Message{Role: "tool", ToolCallID: id, Content: body},
		)
	}
	messages = append(messages, llm.Message{Role: "assistant", Content: "done"})
	return messages
}

// BenchmarkEstimateTokens 覆盖每次 token 估算都要经过的逐 rune 扫描，
// 涵盖拉丁、CJK、emoji 和混合文字。
func BenchmarkEstimateTokens(b *testing.B) {
	cases := []struct {
		name string
		text string
	}{
		{"ascii-1KB", strings.Repeat("abcdefghij", 100)},
		{"cjk-768B", strings.Repeat("中文测试", 64)},
		{"emoji-1KB", strings.Repeat("😀🚀", 128)},
		{"mixed-640B", strings.Repeat("ab中😀 ", 64)},
	}
	for _, bc := range cases {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchAgentInt = EstimateTokens(bc.text)
			}
		})
	}
}

// BenchmarkEstimateMessages 覆盖每次请求时裁剪预算检查所用的整段对话计数。
// （接上行）
func BenchmarkEstimateMessages(b *testing.B) {
	for _, n := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			messages := benchConversation(n, strings.Repeat("x", 64))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchAgentInt = EstimateMessages(messages)
			}
		})
	}
}

// BenchmarkTrim 测量预算驱动的占位替换路径。Trim 会复制输入，
// 因此样本在迭代之间保持原样。缓存保护
// 防护保持为一个较小的显式值：禁用它时，Trim 会一直替换占位，
// 直到整个后缀都被保护，因此几百次迭代会耗时数分钟——
// 调用方并不是这样使用它的。
func BenchmarkTrim(b *testing.B) {
	for _, n := range []int{100, 500, 1000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			messages := benchConversation(n, strings.Repeat("x", 512))
			opts := TrimOptions{
				MaxTokens:                1000,
				KeepTurns:                2,
				KeepToolTokens:           100,
				CacheProtectSuffixTokens: 2000,
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchAgentInt = Trim(messages, opts).Pruned
			}
		})
	}
}

// BenchmarkReclaimByMeta 把元数据驱动的回收过程与预算循环隔离开。
// 每次迭代都像 Trim 一样精确复制样本，
// 因为 reclaimByMeta 是就地替换工具主体。
func BenchmarkReclaimByMeta(b *testing.B) {
	const protect = 1 << 30
	for _, n := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			base := benchConversation(n, strings.Repeat("x", 64))
			meta := make(map[string]ToolMeta, n)
			for i := 0; i < n; i++ {
				id := fmt.Sprintf("c%d", i)
				if i%2 == 0 {
					meta[id] = ToolMeta{SupersedeKey: "read|shared"}
				} else {
					meta[id] = ToolMeta{Useless: true}
				}
			}
			opts := TrimOptions{ToolMeta: meta, CacheProtectSuffixTokens: protect}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				work := append([]llm.Message(nil), base...)
				res := &TrimResult{}
				reclaimByMeta(work, opts, res)
				benchAgentInt += res.Pruned
			}
		})
	}
}

// benchJSONArgs 构建一个包含 n 个字符串属性的扁平 JSON 对象，用作固定的规范化样本。
// （接上行）
func benchJSONArgs(n int) string {
	var sb strings.Builder
	sb.WriteByte('{')
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, "\"key%03d\":\"value%03d\"", i, i)
	}
	sb.WriteByte('}')
	return sb.String()
}

// BenchmarkNormalizeArgs 测量重复防护器用来规范化工具调用参数的 JSON 往返。
// （接上行）
func BenchmarkNormalizeArgs(b *testing.B) {
	cases := []struct {
		name string
		args string
	}{
		{"flat-small", `{"path":"a.go","line":1}`},
		{"flat-16", benchJSONArgs(16)},
		{"flat-128", benchJSONArgs(128)},
		{"non-json-1KB", strings.Repeat("  alpha  beta  ", 64)},
	}
	for _, bc := range cases {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchAgentString = NormalizeArgs(bc.args)
			}
		})
	}
}

// BenchmarkEventEmit 用空消费者测量记录 + 有序投递的发出路径。
// 记录切片会被偶尔裁剪，使长时间的完整运行不会无界增长；
// 这部分记账位于计时区域之外。
func BenchmarkEventEmit(b *testing.B) {
	cases := []struct {
		name  string
		event Event
	}{
		{"text-delta", Event{Kind: EventTextDelta, Text: strings.Repeat("x", 64)}},
		{"tool-start", Event{Kind: EventToolStart, ToolName: "read", Args: `{"path":"a.go"}`}},
		{"turn-boundary", Event{Kind: EventTurnStart, Turn: 1}},
	}
	for _, bc := range cases {
		b.Run(bc.name, func(b *testing.B) {
			s := &runState{emitFn: func(Event) {}}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if len(s.events) > 4096 {
					b.StopTimer()
					s.events = s.events[:0]
					b.StartTimer()
				}
				s.emit(bc.event)
			}
		})
	}
}

// BenchmarkProjectSpecsForModel 覆盖对存在 JSON Schema 特性的模型系列，
// 每次公布工具列表时所做的 schema 投影。
func BenchmarkProjectSpecsForModel(b *testing.B) {
	specs := make([]llm.ToolSpec, 32)
	for i := range specs {
		specs[i].Type = "function"
		specs[i].Function.Name = fmt.Sprintf("tool%d", i)
		specs[i].Function.Parameters = json.RawMessage(benchJSONArgs(8))
	}
	for _, model := range []string{"gemini-2.0-flash", "moonshot-v1-8k", "gpt-4o"} {
		b.Run(model, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchAgentInt += len(projectSpecsForModel(specs, model))
			}
		})
	}
}
