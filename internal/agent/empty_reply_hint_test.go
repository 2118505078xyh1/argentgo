package agent

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// hasHan 报告文本中是否含有汉字，用于确认提示确实是中文而不是空串或占位符。
func hasHan(text string) bool {
	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// TestEmptyReplyHintBySituation 锁定三种情形各自生成不同的、有针对性的中文提示，
// 并确认"有工具调用 + 有推理"同时出现时按工具调用情形处理（优先级最高）。
func TestEmptyReplyHintBySituation(t *testing.T) {
	tests := []struct {
		name          string
		hasReasoning  bool
		toolCalls     int
		wantSubstring string
	}{
		{
			name:          "纯空回复要求直接给出答复",
			hasReasoning:  false,
			toolCalls:     0,
			wantSubstring: "没有任何内容",
		},
		{
			name:          "有推理无答复要求把结论写出来",
			hasReasoning:  true,
			toolCalls:     0,
			wantSubstring: "只有思考过程",
		},
		{
			name:          "有工具调用无答复要求执行工具并汇报",
			hasReasoning:  false,
			toolCalls:     1,
			wantSubstring: "只声明了工具调用",
		},
		{
			name:          "工具调用优先于推理",
			hasReasoning:  true,
			toolCalls:     2,
			wantSubstring: "只声明了工具调用",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hint := emptyReplyHint(tt.hasReasoning, tt.toolCalls, 1)
			if hint == "" {
				t.Fatal("提示为空，期望生成非空纠正提示")
			}
			if !strings.Contains(hint, tt.wantSubstring) {
				t.Fatalf("提示 = %q，期望包含 %q", hint, tt.wantSubstring)
			}
			if !utf8.ValidString(hint) {
				t.Fatalf("提示不是合法 UTF-8: %q", hint)
			}
			if !hasHan(hint) {
				t.Fatalf("提示不含汉字: %q", hint)
			}
			if len(hint) > emptyReplyHintMaxBytes {
				t.Fatalf("提示字节数 = %d，超过上限 %d", len(hint), emptyReplyHintMaxBytes)
			}
		})
	}

	// 三种基本情形必须两两不同，才称得上"有针对性"。
	pure := emptyReplyHint(false, 0, 1)
	reasoning := emptyReplyHint(true, 0, 1)
	tools := emptyReplyHint(false, 1, 1)
	if pure == reasoning || pure == tools || reasoning == tools {
		t.Fatalf("三种情形的提示未彼此区分: 纯空=%q 推理=%q 工具=%q", pure, reasoning, tools)
	}
}

// TestEmptyReplyHintEscalation 验证语气随连续纠正次数逐级增强，
// 并在达到封顶后保持不变（稳定、有界）。
func TestEmptyReplyHintEscalation(t *testing.T) {
	byTurn := make(map[int]string)
	for _, turn := range []int{1, 2, 3, 4, 100} {
		byTurn[turn] = emptyReplyHint(true, 0, turn)
	}

	base := byTurn[1]
	second := byTurn[2]
	third := byTurn[3]

	if !strings.HasPrefix(second, base) {
		t.Fatalf("级别 2 提示 %q 应以级别 1 提示 %q 为前缀", second, base)
	}
	if !strings.HasPrefix(third, base) {
		t.Fatalf("级别 3 提示 %q 应以级别 1 提示 %q 为前缀", third, base)
	}
	if len(base) >= len(second) || len(second) >= len(third) {
		t.Fatalf("语气未逐级增强: 级别 1/2/3 字节数 = %d/%d/%d", len(base), len(second), len(third))
	}
	// 超过封顶的 turn 与封顶值完全一致：异常大的输入不会改变输出。
	for _, turn := range []int{4, 100} {
		if byTurn[turn] != third {
			t.Fatalf("turn=%d 的输出 = %q，期望与封顶值 %q 相同", turn, byTurn[turn], third)
		}
	}
}

// TestEmptyReplyHintNonPositiveTurn 确认非正的 turn 按 1 处理，
// 不会 panic 也不会产生空提示或越界。
func TestEmptyReplyHintNonPositiveTurn(t *testing.T) {
	want := emptyReplyHint(false, 0, 1)
	for _, turn := range []int{0, -1, -100} {
		if got := emptyReplyHint(false, 0, turn); got != want {
			t.Fatalf("turn=%d 的输出 = %q，期望与 turn=1 的 %q 相同", turn, got, want)
		}
	}
}

// TestEmptyReplyHintStable 验证纯函数性质：相同参数逐字节一致。
func TestEmptyReplyHintStable(t *testing.T) {
	cases := []struct {
		hasReasoning bool
		toolCalls    int
		turn         int
	}{
		{false, 0, 1},
		{true, 0, 2},
		{false, 3, 3},
		{true, 9, 50},
	}
	for _, c := range cases {
		first := emptyReplyHint(c.hasReasoning, c.toolCalls, c.turn)
		for i := 0; i < 5; i++ {
			if got := emptyReplyHint(c.hasReasoning, c.toolCalls, c.turn); got != first {
				t.Fatalf("相同输入产生了不同输出: %q != %q", got, first)
			}
		}
	}
}

// TestEmptyReplyHintCJKAndBounded 对所有输入组合验证三条性质：
// 输出非空、为合法 UTF-8 且含汉字（CJK 安全），并且字节数不超过上限（有界）。
func TestEmptyReplyHintCJKAndBounded(t *testing.T) {
	for _, hasReasoning := range []bool{false, true} {
		for _, toolCalls := range []int{-1, 0, 1, 7} {
			for _, turn := range []int{-5, 0, 1, 2, 3, 4, 1 << 20} {
				hint := emptyReplyHint(hasReasoning, toolCalls, turn)
				if hint == "" {
					t.Fatalf("hasReasoning=%v toolCalls=%d turn=%d 生成了空提示", hasReasoning, toolCalls, turn)
				}
				if !utf8.ValidString(hint) {
					t.Fatalf("hasReasoning=%v toolCalls=%d turn=%d 输出不是合法 UTF-8: %q", hasReasoning, toolCalls, turn, hint)
				}
				if !hasHan(hint) {
					t.Fatalf("hasReasoning=%v toolCalls=%d turn=%d 输出不含汉字: %q", hasReasoning, toolCalls, turn, hint)
				}
				if len(hint) > emptyReplyHintMaxBytes {
					t.Fatalf("hasReasoning=%v toolCalls=%d turn=%d 输出字节数 = %d，超过上限 %d", hasReasoning, toolCalls, turn, len(hint), emptyReplyHintMaxBytes)
				}
			}
		}
	}
}

// TestClipHintHeadPreservesShortText 验证未超限时文本原样返回。
func TestClipHintHeadPreservesShortText(t *testing.T) {
	const text = "这是原始提示，不应被裁剪"
	if got := clipHintHead(text, len(text)); got != text {
		t.Fatalf("刚好等于上限时被改写: %q", got)
	}
	if got := clipHintHead(text, len(text)+10); got != text {
		t.Fatalf("小于上限时被改写: %q", got)
	}
	if got := clipHintHead(text, 0); got != text {
		t.Fatalf("非正上限应原样返回: %q", got)
	}
}

// TestClipHintHeadCJKsafe 验证裁剪落在 rune 边界：即使切点位于汉字内部，
// 结果仍是合法 UTF-8，且保留原文前缀并带截断省略号。
func TestClipHintHeadCJKsafe(t *testing.T) {
	const text = "汉字汉字汉字汉字"
	for _, maxBytes := range []int{1, 2, 3, 4, 5, 7, len(text) - 1} {
		got := clipHintHead(text, maxBytes)
		if !utf8.ValidString(got) {
			t.Fatalf("maxBytes=%d 的裁剪结果不是合法 UTF-8: %q", maxBytes, got)
		}
		if strings.HasSuffix(got, "…") {
			body := strings.TrimSuffix(got, "…")
			if !strings.HasPrefix(text, body) {
				t.Fatalf("maxBytes=%d 的裁剪结果未保留原文前缀: %q", maxBytes, got)
			}
		}
		if len(got) > maxBytes {
			t.Fatalf("maxBytes=%d 的裁剪结果字节数 = %d，超过上限", maxBytes, len(got))
		}
	}
}

// TestClipHeadBytesRuneBoundary 直接验证底层裁剪函数不会切断多字节字符。
func TestClipHeadBytesRuneBoundary(t *testing.T) {
	const text = "abc汉字def"
	// "abc" 占 3 字节，随后每个汉字占 3 字节；从 4、5、6 字节截断都应停在汉字边界。
	for _, maxBytes := range []int{4, 5, 6} {
		got := clipHeadBytes(text, maxBytes)
		if !utf8.ValidString(got) {
			t.Fatalf("maxBytes=%d 的裁剪结果不是合法 UTF-8: %q", maxBytes, got)
		}
		if !strings.HasPrefix(text, got) {
			t.Fatalf("maxBytes=%d 的裁剪结果不是原文前缀: %q", maxBytes, got)
		}
	}
}
