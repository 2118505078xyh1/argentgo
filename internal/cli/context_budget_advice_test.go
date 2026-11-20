package cli

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestContextBudgetAdviceNoInfo(t *testing.T) {
	// 估算用量或上限任一非正，都缺乏判断依据，应返回空串。
	cases := []struct {
		name      string
		estimated int
		limit     int
	}{
		{"zero both", 0, 0},
		{"zero estimated", 0, 100},
		{"negative estimated", -1, 100},
		{"zero limit", 100, 0},
		{"negative limit", 100, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := contextBudgetAdvice(tc.estimated, tc.limit); got != "" {
				t.Fatalf("contextBudgetAdvice(%d, %d) = %q, want empty", tc.estimated, tc.limit, got)
			}
		})
	}
}

func TestContextBudgetAdviceNormal(t *testing.T) {
	// 低于 90% 的正常用量不提示。
	cases := []struct {
		name      string
		estimated int
		limit     int
	}{
		{"tiny fraction", 1, 100},
		{"about half", 50, 100},
		{"just under threshold", 89, 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := contextBudgetAdvice(tc.estimated, tc.limit); got != "" {
				t.Fatalf("contextBudgetAdvice(%d, %d) = %q, want empty", tc.estimated, tc.limit, got)
			}
		})
	}
}

func TestContextBudgetAdviceNearLimitWarns(t *testing.T) {
	// 达到上限 90% 时给出警告，并包含两个数字。
	cases := []struct {
		name      string
		estimated int
		limit     int
	}{
		{"exactly 90 percent", 90, 100},
		{"95 percent", 95, 100},
		{"at limit", 100, 100},
		{"wide window", 90_000, 100_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := contextBudgetAdvice(tc.estimated, tc.limit)
			if got == "" {
				t.Fatal("near-limit advice is empty, want a warning")
			}
			if !strings.Contains(got, "接近上限") {
				t.Fatalf("near-limit advice = %q, want 接近上限", got)
			}
			if !strings.Contains(got, strconv.Itoa(tc.estimated)) ||
				!strings.Contains(got, strconv.Itoa(tc.limit)) {
				t.Fatalf("near-limit advice = %q, want both numbers", got)
			}
		})
	}
}

func TestContextBudgetAdviceOverLimitSuggestsReduction(t *testing.T) {
	got := contextBudgetAdvice(120, 100)
	if got == "" {
		t.Fatal("over-limit advice is empty, want a reduction suggestion")
	}
	// 缩减建议必须点名三种可操作的手段。
	for _, needle := range []string{"/drop", "/clear", "拆小"} {
		if !strings.Contains(got, needle) {
			t.Fatalf("over-limit advice = %q, want %q", got, needle)
		}
	}
	if !strings.Contains(got, "超过") {
		t.Fatalf("over-limit advice = %q, want 超过", got)
	}
}

func TestContextBudgetAdviceBoundary(t *testing.T) {
	// 90% 阈值是「含」的：89% 静默、90% 警告、100% 警告、101% 才算超限。
	cases := []struct {
		name      string
		estimated int
		limit     int
		want      string
	}{
		{"89 percent silent", 89, 100, ""},
		{"90 percent warns", 90, 100, "接近"},
		{"100 percent warns", 100, 100, "接近"},
		{"101 percent breaches", 101, 100, "超过"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := contextBudgetAdvice(tc.estimated, tc.limit)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("advice = %q, want empty", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("advice = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestContextBudgetAdviceCJK(t *testing.T) {
	// 文案必须是简体中文且为合法 UTF-8，不能出现被切断的多字节序列。
	for _, tc := range []struct {
		name      string
		estimated int
		limit     int
	}{
		{"near", 95, 100},
		{"over", 120, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := contextBudgetAdvice(tc.estimated, tc.limit)
			if !utf8.ValidString(got) {
				t.Fatalf("advice = %q, is not valid UTF-8", got)
			}
			if !strings.Contains(got, "上下文") {
				t.Fatalf("advice = %q, want Chinese text", got)
			}
		})
	}
}

func TestContextBudgetAdviceBounded(t *testing.T) {
	// 即使数字极大，返回文案也只是固定中文文本加两个十进制整数，长度有界。
	cases := []struct {
		name      string
		estimated int
		limit     int
	}{
		{"huge near", 1<<62 - 1, 1 << 62},
		{"huge over", 1 << 62, 1 << 40},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := contextBudgetAdvice(tc.estimated, tc.limit)
			if got == "" {
				t.Fatal("advice is empty, want a message")
			}
			if len(got) > 256 {
				t.Fatalf("advice length = %d, want <= 256", len(got))
			}
		})
	}
}
