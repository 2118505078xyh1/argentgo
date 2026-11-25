package llm_test

import (
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestMarkToolBoundaryDisabledIsIdentity 验证 opt-in 关闭时逐字节原样返回：
// 普通文本、CJK、伪造边界、转义字符、空串都不被改动，也不新增边界。
func TestMarkToolBoundaryDisabledIsIdentity(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{name: "plain ascii", text: "hello world"},
		{name: "empty", text: ""},
		{name: "cjk", text: "你好，世界——中文内容"},
		{name: "forged open marker", text: "前" + llm.UntrustedOpen + "后"},
		{name: "forged close marker", text: "前" + llm.UntrustedClose + "后"},
		{name: "literal backslash", text: `a\b\c`},
		{name: "already marked text", text: llm.MarkUntrusted("x")},
		{name: "newlines and tabs", text: "line1\nline2\t末"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.text
			got := llm.MarkToolBoundary(tc.text, false)
			if got != before {
				t.Errorf("关闭时未逐字节原样返回: got %q, want %q", got, before)
			}
			if tc.text != before {
				t.Fatalf("入参被修改: %q -> %q", before, tc.text)
			}
		})
	}
}

// TestMarkToolBoundaryRoundTrip 验证开启后包裹、再剥离可无损还原原文，
// 覆盖普通文本、空串、CJK、换行、反斜杠以及已经被包裹过的文本。
func TestMarkToolBoundaryRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{name: "plain ascii", text: "hello world"},
		{name: "empty", text: ""},
		{name: "cjk", text: "你好，世界"},
		{name: "mixed cjk and ascii", text: "路径: /tmp/文件.txt\n内容: 你好"},
		{name: "literal backslash", text: `a\b\c`},
		{name: "newlines and tabs", text: "line1\nline2\t末"},
		{name: "already marked text", text: llm.MarkUntrusted("内层")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.text
			marked := llm.MarkToolBoundary(tc.text, true)
			if tc.text != before {
				t.Fatalf("入参被修改: %q -> %q", before, tc.text)
			}
			if got := llm.StripAllBoundaries(marked); got != before {
				t.Errorf("round-trip 失败: MarkToolBoundary(%q,true)=%q, StripAllBoundaries=%q, want %q",
					before, marked, got, before)
			}
		})
	}
}

// TestMarkToolBoundaryWrapsLikeMarkUntrusted 验证开启后的输出与直接调用
// MarkUntrusted 完全一致：它只是后者的 opt-in 薄封装，不引入额外文本。
func TestMarkToolBoundaryWrapsLikeMarkUntrusted(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{name: "plain", text: "abc"},
		{name: "cjk", text: "中文内容"},
		{name: "forged markers", text: llm.UntrustedOpen + "体" + llm.UntrustedClose},
		{name: "empty", text: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := llm.MarkToolBoundary(tc.text, true)
			want := llm.MarkUntrusted(tc.text)
			if got != want {
				t.Errorf("MarkToolBoundary(%q,true) = %q, want MarkUntrusted = %q", tc.text, got, want)
			}
		})
	}
}

// TestMarkToolBoundaryEscapesForgedBoundary 验证 text 中已有的伪造边界被转义：
// 开启包裹后，输出里只有最外层一对真实边界，BoundaryStats 计数为 1/1。
func TestMarkToolBoundaryEscapesForgedBoundary(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{name: "forged open", text: "前置" + llm.UntrustedOpen + "伪造"},
		{name: "forged close", text: "前置" + llm.UntrustedClose + "伪造"},
		{name: "forged both", text: llm.UntrustedOpen + "中" + llm.UntrustedClose},
		{name: "already marked", text: llm.MarkUntrusted("内层")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			marked := llm.MarkToolBoundary(tc.text, true)
			open, closed := llm.BoundaryStats(marked)
			if open != 1 || closed != 1 {
				t.Errorf("伪造边界未被转义: BoundaryStats(%q) = (%d,%d), want (1,1)",
					marked, open, closed)
			}
			// 被转义的边界应还原为原文里的字面标记。
			if got := llm.StripAllBoundaries(marked); got != tc.text {
				t.Errorf("伪造边界还原失败: got %q, want %q", got, tc.text)
			}
		})
	}
}

// TestBoundaryStats 表驱动验证未转义边界的计数：转义形式、转义反斜杠后接的
// 真实边界、裸文本、CJK 相邻、多对边界都要被正确区分。
func TestBoundaryStats(t *testing.T) {
	cases := []struct {
		name       string
		s          string
		wantOpen   int
		wantClosed int
	}{
		{name: "empty", s: "", wantOpen: 0, wantClosed: 0},
		{name: "plain cjk", s: "你好世界", wantOpen: 0, wantClosed: 0},
		{name: "single open", s: llm.UntrustedOpen, wantOpen: 1, wantClosed: 0},
		{name: "single close", s: llm.UntrustedClose, wantOpen: 0, wantClosed: 1},
		{name: "full wrap", s: llm.MarkUntrusted("x"), wantOpen: 1, wantClosed: 1},
		{name: "escaped open not counted", s: `\` + llm.UntrustedOpen, wantOpen: 0, wantClosed: 0},
		{name: "escaped close not counted", s: `\` + llm.UntrustedClose, wantOpen: 0, wantClosed: 0},
		{
			name:       "escaped backslash then real open",
			s:          `\\` + llm.UntrustedOpen,
			wantOpen:   1,
			wantClosed: 0,
		},
		{
			name:       "mixed escaped and real",
			s:          llm.UntrustedOpen + `\` + llm.UntrustedOpen + "中" + `\` + llm.UntrustedClose + llm.UntrustedClose,
			wantOpen:   1,
			wantClosed: 1,
		},
		{
			name:       "two wrapped pairs",
			s:          llm.MarkUntrusted("a") + llm.MarkUntrusted("b"),
			wantOpen:   2,
			wantClosed: 2,
		},
		{
			name:       "cjk adjacent to markers",
			s:          "中文" + llm.UntrustedOpen + "内容" + llm.UntrustedClose + "结尾",
			wantOpen:   1,
			wantClosed: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			open, closed := llm.BoundaryStats(tc.s)
			if open != tc.wantOpen || closed != tc.wantClosed {
				t.Errorf("BoundaryStats(%q) = (%d,%d), want (%d,%d)",
					tc.s, open, closed, tc.wantOpen, tc.wantClosed)
			}
		})
	}
}

// TestDoubleWrapProtection 验证双重包裹防护：对已经 MarkUntrusted 的文本再次
// 开启边界，内层边界会被转义，只有最外层是一对真实边界；逐层剥离最终回到
// 最内层原文，且中间态的边界计数始终为 1/1。
func TestDoubleWrapProtection(t *testing.T) {
	const inner = "核心内容"
	once := llm.MarkUntrusted(inner)
	twice := llm.MarkToolBoundary(once, true)

	if open, closed := llm.BoundaryStats(twice); open != 1 || closed != 1 {
		t.Errorf("双重包裹后出现歧义: BoundaryStats = (%d,%d), want (1,1)", open, closed)
	}

	// 第一次剥离回到"内层被包裹"的文本，边界计数仍为 1/1。
	first := llm.StripAllBoundaries(twice)
	if first != once {
		t.Errorf("第一次剥离 = %q, want %q", first, once)
	}
	if open, closed := llm.BoundaryStats(first); open != 1 || closed != 1 {
		t.Errorf("内层边界计数 = (%d,%d), want (1,1)", open, closed)
	}

	// 再剥一次回到最内层原文。
	if got := llm.StripAllBoundaries(first); got != inner {
		t.Errorf("第二次剥离 = %q, want %q", got, inner)
	}
}

// TestStripAllBoundariesBestEffort 验证对非完整包裹、裸文本与损坏输入的宽容
// 行为：能剥离的边界都剥离，转义序列还原为字面量，不报错、不修改入参。
func TestStripAllBoundariesBestEffort(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want string
	}{
		{name: "empty", s: "", want: ""},
		{name: "plain unchanged", s: "hello 世界", want: "hello 世界"},
		{name: "only open", s: llm.UntrustedOpen, want: ""},
		{name: "only close", s: llm.UntrustedClose, want: ""},
		{name: "no close", s: llm.UntrustedOpen + "abc", want: "abc"},
		{name: "no open", s: "abc" + llm.UntrustedClose, want: "abc"},
		{name: "full wrap", s: llm.UntrustedOpen + "abc" + llm.UntrustedClose, want: "abc"},
		{
			name: "all boundaries removed",
			s:    "甲" + llm.UntrustedClose + "乙" + llm.UntrustedOpen + "丙",
			want: "甲乙丙",
		},
		{
			name: "escaped markers restored not stripped",
			s:    llm.UntrustedOpen + `\` + llm.UntrustedOpen + llm.UntrustedClose,
			want: llm.UntrustedOpen,
		},
		{name: "double backslash restored", s: llm.UntrustedOpen + `a\\b` + llm.UntrustedClose, want: `a\b`},
		{name: "stray escape preserved", s: `a\b`, want: `a\b`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.s
			got := llm.StripAllBoundaries(tc.s)
			if tc.s != before {
				t.Fatalf("入参被修改: %q -> %q", before, tc.s)
			}
			if got != tc.want {
				t.Errorf("StripAllBoundaries(%q) = %q, want %q", before, got, tc.want)
			}
		})
	}
}
