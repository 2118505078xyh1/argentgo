package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// This file records performance baselines for the agent loop's pure hot paths:
// token estimation, context trimming/reclamation, argument normalization and
// event emission. Fixtures are fixed in memory; no network, model or real
// wall-clock dependency is involved. No wall-clock thresholds are asserted.

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

// benchConversation builds n assistant/tool pairs around a fixed head and tail,
// matching the shape the trim and reclaim paths actually see.
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

// BenchmarkEstimateTokens covers the per-rune scan that every token estimate
// goes through, across Latin, CJK, emoji and mixed scripts.
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

// BenchmarkEstimateMessages covers the whole-conversation accounting used by
// the trim budget check on every request.
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

// BenchmarkTrim measures the budget-driven stubbing path. Trim copies its
// input, so the fixture stays pristine between iterations.
func BenchmarkTrim(b *testing.B) {
	const protect = 1 << 30 // let every older tool result be a candidate
	for _, n := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			messages := benchConversation(n, strings.Repeat("x", 512))
			opts := TrimOptions{
				MaxTokens:                1000,
				KeepTurns:                2,
				KeepToolTokens:           100,
				CacheProtectSuffixTokens: protect,
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchAgentInt = Trim(messages, opts).Pruned
			}
		})
	}
}

// BenchmarkReclaimByMeta isolates the metadata-driven reclamation pass from
// the budget loop. Each iteration copies the fixture exactly as Trim does,
// because reclaimByMeta replaces tool bodies in place.
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

// benchJSONArgs builds a flat JSON object with n string properties, used as a
// fixed normalization fixture.
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

// BenchmarkNormalizeArgs measures the JSON round-trip used by the dedupe guard
// to canonicalize tool call arguments.
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

// BenchmarkEventEmit measures the recorded + ordered-delivery emit path with a
// no-op consumer. The recorded slice is trimmed occasionally so a long full run
// cannot grow without bound; that bookkeeping sits outside the timed region.
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

// BenchmarkProjectSpecsForModel covers the schema projection applied to every
// advertised tool list for model families with JSON Schema quirks.
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
