package llm_test

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestDiagnoseBoundaryBalanced 验证 Balanced 的严格"单层包裹"语义：完整包裹
// 为 true；只有单边、并排两对、三对及开闭数量不等均为 false。双重包裹由于
// 内层边界被转义，对外仍只呈现一对真实边界，Balanced 为 true。
func TestDiagnoseBoundaryBalanced(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want llm.BoundaryReport
	}{
		{
			name: "empty",
			s:    "",
			want: llm.BoundaryReport{},
		},
		{
			name: "plain text",
			s:    "hello 世界",
			want: llm.BoundaryReport{},
		},
		{
			name: "open marker only",
			s:    llm.UntrustedOpen,
			want: llm.BoundaryReport{Marked: true, OpenCount: 1},
		},
		{
			name: "close marker only",
			s:    llm.UntrustedClose,
			want: llm.BoundaryReport{Marked: true, CloseCount: 1},
		},
		{
			name: "single wrap",
			s:    llm.MarkUntrusted("x"),
			want: llm.BoundaryReport{Marked: true, OpenCount: 1, CloseCount: 1, Balanced: true},
		},
		{
			name: "single wrap empty content",
			s:    llm.MarkUntrusted(""),
			want: llm.BoundaryReport{Marked: true, OpenCount: 1, CloseCount: 1, Balanced: true},
		},
		{
			name: "two pairs side by side",
			s:    llm.MarkUntrusted("a") + llm.MarkUntrusted("b"),
			want: llm.BoundaryReport{Marked: true, OpenCount: 2, CloseCount: 2},
		},
		{
			name: "three pairs side by side",
			s:    llm.MarkUntrusted("a") + llm.MarkUntrusted("b") + llm.MarkUntrusted("c"),
			want: llm.BoundaryReport{Marked: true, OpenCount: 3, CloseCount: 3},
		},
		{
			name: "unequal counts two opens one close",
			s:    llm.UntrustedOpen + llm.UntrustedOpen + llm.UntrustedClose,
			want: llm.BoundaryReport{Marked: true, OpenCount: 2, CloseCount: 1},
		},
		{
			name: "double wrap inner escaped",
			s:    llm.MarkUntrusted(llm.MarkUntrusted("x")),
			want: llm.BoundaryReport{Marked: true, OpenCount: 1, CloseCount: 1, EscapedCount: 2, Balanced: true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := llm.DiagnoseBoundary(tc.s); got != tc.want {
				t.Errorf("DiagnoseBoundary(%q) = %+v, want %+v", tc.s, got, tc.want)
			}
		})
	}
}

// TestDiagnoseBoundaryEscapedForged 验证伪造边界被转义后不计为真实边界：
// 被转义的开/闭标记只累加 EscapedCount，不影响 Marked 与 Balanced；前置的
// 转义反斜杠被消费后，紧随其后的标记才是真实边界。
func TestDiagnoseBoundaryEscapedForged(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want llm.BoundaryReport
	}{
		{
			name: "escaped open",
			s:    `\` + llm.UntrustedOpen,
			want: llm.BoundaryReport{EscapedCount: 1},
		},
		{
			name: "escaped close",
			s:    `\` + llm.UntrustedClose,
			want: llm.BoundaryReport{EscapedCount: 1},
		},
		{
			name: "escaped pair",
			s:    `\` + llm.UntrustedOpen + `\` + llm.UntrustedClose,
			want: llm.BoundaryReport{EscapedCount: 2},
		},
		{
			name: "escaped backslash then real open",
			s:    `\\` + llm.UntrustedOpen,
			want: llm.BoundaryReport{Marked: true, OpenCount: 1},
		},
		{
			name: "forged boundaries escaped by MarkUntrusted",
			s:    llm.MarkUntrusted("头" + llm.UntrustedOpen + "中" + llm.UntrustedClose + "尾"),
			want: llm.BoundaryReport{Marked: true, OpenCount: 1, CloseCount: 1, EscapedCount: 2, Balanced: true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := llm.DiagnoseBoundary(tc.s); got != tc.want {
				t.Errorf("DiagnoseBoundary(%q) = %+v, want %+v", tc.s, got, tc.want)
			}
		})
	}
}

// TestDiagnoseBoundaryEscapedCount 单独锁定 EscapedCount 的边界：只数被转义的
// 边界标记，转义反斜杠、孤立反斜杠、未知转义序列都不计入。
func TestDiagnoseBoundaryEscapedCount(t *testing.T) {
	cases := []struct {
		name        string
		s           string
		wantEscaped int
	}{
		{name: "empty", s: "", wantEscaped: 0},
		{name: "stray backslash", s: `\`, wantEscaped: 0},
		{name: "unknown escape", s: `\x`, wantEscaped: 0},
		{name: "literal backslash", s: `a\\b`, wantEscaped: 0},
		{name: "escaped open", s: `\` + llm.UntrustedOpen, wantEscaped: 1},
		{name: "escaped close", s: `\` + llm.UntrustedClose, wantEscaped: 1},
		{name: "three escaped markers", s: `\` + llm.UntrustedOpen + `\` + llm.UntrustedOpen + `\` + llm.UntrustedClose, wantEscaped: 3},
		{name: "backslash content has no escaped boundary", s: llm.MarkUntrusted(`a\b`), wantEscaped: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := llm.DiagnoseBoundary(tc.s); got.EscapedCount != tc.wantEscaped {
				t.Errorf("DiagnoseBoundary(%q).EscapedCount = %d, want %d", tc.s, got.EscapedCount, tc.wantEscaped)
			}
		})
	}
}

// TestDiagnoseBoundaryCJKAndEmoji 验证 CJK / emoji 等多字节 UTF-8 文本不会被
// 按字节扫描破坏：诊断出的计数与单层包裹判定不受非 ASCII 字符影响。
func TestDiagnoseBoundaryCJKAndEmoji(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want llm.BoundaryReport
	}{
		{
			name: "cjk and emoji only",
			s:    "你好，世界😀🚀——中文内容",
			want: llm.BoundaryReport{},
		},
		{
			name: "wrapped cjk and emoji",
			s:    llm.MarkUntrusted("你好😀世界"),
			want: llm.BoundaryReport{Marked: true, OpenCount: 1, CloseCount: 1, Balanced: true},
		},
		{
			name: "markers adjacent to emoji",
			s:    "😀" + llm.UntrustedOpen + "内容🚀" + llm.UntrustedClose + "结尾",
			want: llm.BoundaryReport{Marked: true, OpenCount: 1, CloseCount: 1, Balanced: true},
		},
		{
			name: "forged marker inside cjk gets escaped",
			s:    llm.MarkUntrusted("中文😀" + llm.UntrustedOpen + "伪造🚀" + llm.UntrustedClose),
			want: llm.BoundaryReport{Marked: true, OpenCount: 1, CloseCount: 1, EscapedCount: 2, Balanced: true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := llm.DiagnoseBoundary(tc.s); got != tc.want {
				t.Errorf("DiagnoseBoundary(%q) = %+v, want %+v", tc.s, got, tc.want)
			}
		})
	}
}

// TestLooksUntrusted 验证 LooksUntrusted 与 DiagnoseBoundary.Marked 等价：
// 只有未转义的真实边界才返回 true，纯文本与仅含转义边界的文本返回 false。
func TestLooksUntrusted(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want bool
	}{
		{name: "empty", s: "", want: false},
		{name: "plain cjk", s: "你好世界", want: false},
		{name: "escaped open only", s: `\` + llm.UntrustedOpen, want: false},
		{name: "escaped pair only", s: `\` + llm.UntrustedOpen + `\` + llm.UntrustedClose, want: false},
		{name: "literal backslash", s: `a\\b`, want: false},
		{name: "open marker only", s: llm.UntrustedOpen, want: true},
		{name: "close marker only", s: llm.UntrustedClose, want: true},
		{name: "single wrap", s: llm.MarkUntrusted("x"), want: true},
		{name: "two pairs", s: llm.MarkUntrusted("a") + llm.MarkUntrusted("b"), want: true},
		{name: "double wrap", s: llm.MarkUntrusted(llm.MarkUntrusted("x")), want: true},
		{name: "wrapped emoji", s: llm.MarkUntrusted("😀"), want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := llm.LooksUntrusted(tc.s)
			if got != tc.want {
				t.Errorf("LooksUntrusted(%q) = %v, want %v", tc.s, got, tc.want)
			}
			if marked := llm.DiagnoseBoundary(tc.s).Marked; marked != got {
				t.Errorf("LooksUntrusted(%q)=%v 与 DiagnoseBoundary.Marked=%v 不一致", tc.s, got, marked)
			}
		})
	}
}

// TestDiagnoseBoundaryDoesNotModifyInput 验证诊断是纯函数：多次调用前后入参
// 逐字节不变，且返回值一致（诊断稳定性）。
func TestDiagnoseBoundaryDoesNotModifyInput(t *testing.T) {
	cases := []string{
		"",
		"hello 世界😀",
		llm.UntrustedOpen + "内容" + llm.UntrustedClose,
		llm.MarkUntrusted("头" + llm.UntrustedOpen + "伪造" + llm.UntrustedClose),
		`\` + llm.UntrustedOpen + `\\` + llm.UntrustedClose + `\`,
	}

	for _, s := range cases {
		before := s
		first := llm.DiagnoseBoundary(s)
		if s != before {
			t.Fatalf("入参被修改: %q -> %q", before, s)
		}
		for i := 0; i < 3; i++ {
			again := llm.DiagnoseBoundary(s)
			if again != first {
				t.Errorf("诊断不稳定: 第 %d 次 = %+v, want %+v", i+2, again, first)
			}
			_ = llm.LooksUntrusted(s)
		}
		if s != before {
			t.Fatalf("多次诊断后入参被修改: %q -> %q", before, s)
		}
	}
}

// TestDiagnoseBoundaryConsistentWithBoundaryStats 锁定新旧原语的一致性：
// DiagnoseBoundary 的开/闭计数必须与既有 BoundaryStats 在各类输入上完全相同。
func TestDiagnoseBoundaryConsistentWithBoundaryStats(t *testing.T) {
	cases := []string{
		"",
		"你好😀世界",
		llm.UntrustedOpen,
		llm.UntrustedClose,
		llm.MarkUntrusted("x"),
		llm.MarkUntrusted("a") + llm.MarkUntrusted("b"),
		llm.MarkUntrusted(llm.MarkUntrusted("x")),
		`\` + llm.UntrustedOpen + `\` + llm.UntrustedClose,
		`\\` + llm.UntrustedOpen,
		"中文" + llm.UntrustedOpen + "🚀" + llm.UntrustedClose,
	}

	for _, s := range cases {
		report := llm.DiagnoseBoundary(s)
		open, closed := llm.BoundaryStats(s)
		if report.OpenCount != open || report.CloseCount != closed {
			t.Errorf("DiagnoseBoundary(%q) 计数 (%d,%d) 与 BoundaryStats (%d,%d) 不一致",
				s, report.OpenCount, report.CloseCount, open, closed)
		}
	}
}
