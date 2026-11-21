package eval

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestMeasurePressureEmptyInput 覆盖 nil 与空切片：所有计数为零，等级健康，
// 最老消息年龄为 0（没有"最老消息"）。
func TestMeasurePressureEmptyInput(t *testing.T) {
	want := ContextPressure{
		Messages: 0, EstTokens: 0, ToolResultBytes: 0, OldestTokenAge: 0, Verdict: verdictHealthy,
	}
	for _, tc := range []struct {
		name string
		msgs []MessageLike
	}{
		{"nil slice", nil},
		{"empty slice", []MessageLike{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := MeasurePressure(tc.msgs, nil)
			if got != want {
				t.Fatalf("MeasurePressure(%v) = %+v, want %+v", tc.msgs, got, want)
			}
		})
	}
}

// TestMeasurePressureTokenThresholds 用固定 est 精确命中 token 阈值边界，
// 锁定 healthy/warning/critical 的稳定分级规则（工具字节与消息数均为健康值）。
func TestMeasurePressureTokenThresholds(t *testing.T) {
	tests := []struct {
		name        string
		tokens      int
		wantVerdict string
	}{
		{"below warning", pressureWarningTokens - 1, verdictHealthy},
		{"at warning", pressureWarningTokens, verdictWarning},
		{"below critical", pressureCriticalTokens - 1, verdictWarning},
		{"at critical", pressureCriticalTokens, verdictCritical},
		{"above critical", pressureCriticalTokens + 123, verdictCritical},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgs := []MessageLike{{Role: "user", Content: "x"}}
			got := MeasurePressure(msgs, func(string) int { return tt.tokens })
			if got.EstTokens != tt.tokens {
				t.Fatalf("EstTokens = %d, want %d", got.EstTokens, tt.tokens)
			}
			if got.Verdict != tt.wantVerdict {
				t.Fatalf("Verdict = %q, want %q (tokens=%d)", got.Verdict, tt.wantVerdict, tt.tokens)
			}
		})
	}
}

// TestMeasurePressureOtherSignalsEscalate 证明工具结果字节数与消息数也能独立升级等级，
// 且最终等级取三个信号中的最高级；同时锁定消息数对应的最老消息年龄。
func TestMeasurePressureOtherSignalsEscalate(t *testing.T) {
	zeroEst := func(string) int { return 0 }
	tests := []struct {
		name        string
		msgs        []MessageLike
		wantVerdict string
		wantAge     int
	}{
		{
			name:        "tool bytes at warning",
			msgs:        []MessageLike{{Role: "tool", Content: strings.Repeat("x", pressureWarningToolBytes), ToolResult: true}},
			wantVerdict: verdictWarning, wantAge: 0,
		},
		{
			name:        "tool bytes at critical",
			msgs:        []MessageLike{{Role: "tool", Content: strings.Repeat("x", pressureCriticalToolBytes), ToolResult: true}},
			wantVerdict: verdictCritical, wantAge: 0,
		},
		{
			name:        "message count at warning",
			msgs:        make([]MessageLike, pressureWarningMessages),
			wantVerdict: verdictWarning,
			wantAge:     pressureWarningMessages - 1,
		},
		{
			name:        "message count at critical",
			msgs:        make([]MessageLike, pressureCriticalMessages),
			wantVerdict: verdictCritical,
			wantAge:     pressureCriticalMessages - 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MeasurePressure(tt.msgs, zeroEst)
			if got.Verdict != tt.wantVerdict {
				t.Fatalf("Verdict = %q, want %q", got.Verdict, tt.wantVerdict)
			}
			if got.OldestTokenAge != tt.wantAge {
				t.Fatalf("OldestTokenAge = %d, want %d", got.OldestTokenAge, tt.wantAge)
			}
			if got.EstTokens != 0 {
				t.Fatalf("EstTokens = %d, want 0 with zero est", got.EstTokens)
			}
		})
	}
}

// TestMeasurePressureMaxLevelWins 覆盖混合信号：token 仅警告、工具字节已危急，
// 最终等级必须是危急（取最高级），且各诊断量都被正确累加。
func TestMeasurePressureMaxLevelWins(t *testing.T) {
	msgs := []MessageLike{
		{Role: "user", Content: "abc"},
		{Role: "tool", Content: strings.Repeat("t", pressureCriticalToolBytes), ToolResult: true},
	}
	got := MeasurePressure(msgs, func(string) int { return pressureWarningTokens })
	if got.Verdict != verdictCritical {
		t.Fatalf("Verdict = %q, want critical", got.Verdict)
	}
	if got.Messages != 2 || got.OldestTokenAge != 1 {
		t.Fatalf("Messages/OldestTokenAge = %d/%d, want 2/1", got.Messages, got.OldestTokenAge)
	}
	if got.EstTokens != 2*pressureWarningTokens {
		t.Fatalf("EstTokens = %d, want %d", got.EstTokens, 2*pressureWarningTokens)
	}
	if got.ToolResultBytes != pressureCriticalToolBytes {
		t.Fatalf("ToolResultBytes = %d, want %d", got.ToolResultBytes, pressureCriticalToolBytes)
	}
}

// TestMeasurePressureCJKContent 覆盖中文/emoji：默认 est 按 rune 计数而非字节，
// 工具结果字节数仍是字节数；测量过程不切分、不破坏 UTF-8，内容保持有效。
func TestMeasurePressureCJKContent(t *testing.T) {
	const userText = "你好世界"
	const toolText = "工具结果🙂"
	msgs := []MessageLike{
		{Role: "user", Content: userText},
		{Role: "tool", Content: toolText, ToolResult: true},
	}
	got := MeasurePressure(msgs, nil)

	wantTokens := utf8.RuneCountInString(userText) + utf8.RuneCountInString(toolText)
	if got.EstTokens != wantTokens {
		t.Fatalf("EstTokens = %d, want %d runes (not %d bytes)",
			got.EstTokens, wantTokens, len(userText)+len(toolText))
	}
	if got.ToolResultBytes != len(toolText) {
		t.Fatalf("ToolResultBytes = %d, want %d bytes", got.ToolResultBytes, len(toolText))
	}
	//前提校验：CJK/emoji 的 rune 数必须小于字节数，否则本测试无法区分两种计数口径。
	if utf8.RuneCountInString(toolText) >= len(toolText) {
		t.Fatalf("test premise broken: CJK/emoji text is ASCII-sized")
	}
	for _, m := range msgs {
		if !utf8.ValidString(m.Content) {
			t.Fatalf("content became invalid UTF-8: %q", m.Content)
		}
	}
}

// TestMeasurePressureEstInjection 证明注入的 est 被逐条以正确的 Content 调用，
// 其返回值被如实累加；est 的副作用不触碰入参，测量仍为纯函数。
func TestMeasurePressureEstInjection(t *testing.T) {
	var called []string
	est := func(s string) int {
		called = append(called, s)
		return 7
	}
	msgs := []MessageLike{
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "中文"},
		{Role: "tool", Content: "c", ToolResult: true},
	}
	got := MeasurePressure(msgs, est)

	if want := []string{"a", "中文", "c"}; !reflect.DeepEqual(called, want) {
		t.Fatalf("est called with %v, want %v", called, want)
	}
	if got.EstTokens != 21 {
		t.Fatalf("EstTokens = %d, want 21 (3 messages x 7)", got.EstTokens)
	}
	if got.ToolResultBytes != 1 {
		t.Fatalf("ToolResultBytes = %d, want 1", got.ToolResultBytes)
	}
	if got.Messages != 3 || got.OldestTokenAge != 2 {
		t.Fatalf("Messages/OldestTokenAge = %d/%d, want 3/2", got.Messages, got.OldestTokenAge)
	}
}

// TestMeasurePressureDoesNotMutateInput 用 reflect.DeepEqual 锁定纯函数语义：
// 测量前后 messages 完全相等，字符串内容不变。
func TestMeasurePressureDoesNotMutateInput(t *testing.T) {
	msgs := []MessageLike{
		{Role: "user", Content: "abc"},
		{Role: "tool", Content: "你好🙂", ToolResult: true},
	}
	before := append([]MessageLike(nil), msgs...)
	MeasurePressure(msgs, func(s string) int { return len(s) })
	if !reflect.DeepEqual(msgs, before) {
		t.Fatalf("MeasurePressure mutated its input:\n got %+v\nwant %+v", msgs, before)
	}
}

// TestContextPressureSummary 锁定稳定中文摘要：等级标签被翻译，
// 各数值按固定格式呈现；未知等级原样保留且摘要可重复调用。
func TestContextPressureSummary(t *testing.T) {
	tests := []struct {
		name string
		p    ContextPressure
		want string
	}{
		{
			name: "healthy",
			p:    ContextPressure{Messages: 1, EstTokens: 10, ToolResultBytes: 0, OldestTokenAge: 0, Verdict: verdictHealthy},
			want: "上下文压力[健康]：消息 1 条，估算 token 10，工具结果 0 字节，最老消息年龄 0",
		},
		{
			name: "warning",
			p:    ContextPressure{Messages: 3, EstTokens: 42, ToolResultBytes: 120, OldestTokenAge: 2, Verdict: verdictWarning},
			want: "上下文压力[警告]：消息 3 条，估算 token 42，工具结果 120 字节，最老消息年龄 2",
		},
		{
			name: "critical",
			p:    ContextPressure{Messages: 9, EstTokens: 9000, ToolResultBytes: 50000, OldestTokenAge: 8, Verdict: verdictCritical},
			want: "上下文压力[危急]：消息 9 条，估算 token 9000，工具结果 50000 字节，最老消息年龄 8",
		},
		{
			name: "unknown verdict is preserved",
			p:    ContextPressure{Messages: 0, Verdict: "custom"},
			want: "上下文压力[custom]：消息 0 条，估算 token 0，工具结果 0 字节，最老消息年龄 0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.Summary(); got != tt.want {
				t.Fatalf("Summary() = %q, want %q", got, tt.want)
			}
			if got := tt.p.Summary(); got != tt.p.Summary() {
				t.Fatalf("Summary() is not stable: %q vs %q", got, tt.p.Summary())
			}
		})
	}
}

// TestMeasurePressureSummaryRoundTrip 验证从测量得到的压力值可直接产出稳定摘要。
func TestMeasurePressureSummaryRoundTrip(t *testing.T) {
	msgs := []MessageLike{{Role: "user", Content: "问题"}}
	p := MeasurePressure(msgs, nil)
	want := "上下文压力[健康]：消息 1 条，估算 token 2，工具结果 0 字节，最老消息年龄 0"
	if got := p.Summary(); got != want {
		t.Fatalf("Summary() = %q, want %q", got, want)
	}
}
