package app

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/session"
	"example.com/argentgo/internal/textutil"
)

func toolEntry(content string) session.Entry {
	return session.Entry{Role: "tool", Content: content}
}

func TestLocalTrimEstimateZeroValue(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []session.Entry
	}{
		{name: "nil", entries: nil},
		{name: "empty", entries: []session.Entry{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := LocalTrimEstimate(tc.entries, 1000)
			want := LocalTrimResult{}
			if got != want {
				t.Fatalf("LocalTrimEstimate() = %+v, want %+v", got, want)
			}
			if ShouldSkipModelSummary(got, 0) {
				t.Fatal("零值结果不应触发跳过模型摘要")
			}
		})
	}
}

func TestLocalTrimEstimateOnlyToolEntriesAreTrimmed(t *testing.T) {
	long := strings.Repeat("x", 4000)
	entries := []session.Entry{
		{Role: "system", Content: long},
		{Role: "user", Content: long},
		{Role: "assistant", Content: long},
	}
	got := LocalTrimEstimate(entries, 100)
	if got.Trimmed {
		t.Fatal("没有工具结果时不应报告已裁剪")
	}
	if got.BeforeTokens != got.AfterTokens || got.Saved != 0 {
		t.Fatalf("非工具条目不应产生节省: %+v", got)
	}
	if got.BeforeTokens <= 0 {
		t.Fatal("非工具条目的 token 估算应大于零")
	}

	withTool := append(entries, toolEntry(long))
	got2 := LocalTrimEstimate(withTool, 100)
	if !got2.Trimmed {
		t.Fatal("工具结果超限时报告已裁剪")
	}
	if got2.BeforeTokens <= got2.AfterTokens || got2.Saved <= 0 {
		t.Fatalf("超长工具结果应产生节省: %+v", got2)
	}
	if got2.BeforeTokens-got2.AfterTokens != got2.Saved {
		t.Fatalf("Saved 必须等于 Before-After: %+v", got2)
	}
}

func TestLocalTrimEstimateUnderLimitNotTrimmed(t *testing.T) {
	entries := []session.Entry{toolEntry("short tool output")}
	got := LocalTrimEstimate(entries, 1000)
	if got.Trimmed || got.Saved != 0 || got.BeforeTokens != got.AfterTokens {
		t.Fatalf("未超限的输出不应裁剪: %+v", got)
	}
}

func TestLocalTrimEstimateDoesNotMutateInput(t *testing.T) {
	entries := []session.Entry{
		{Role: "system", Content: "rules"},
		toolEntry(strings.Repeat("abc ", 2000)),
		{Role: "assistant", Content: "done"},
	}
	before := append([]session.Entry(nil), entries...)
	_ = LocalTrimEstimate(entries, 200)
	if !reflect.DeepEqual(entries, before) {
		t.Fatal("LocalTrimEstimate 修改了入参")
	}
}

func TestLocalTrimEstimateStable(t *testing.T) {
	entries := []session.Entry{
		toolEntry(strings.Repeat("日志内容 log line\n", 500)),
		toolEntry(strings.Repeat("😀中abc", 500)),
	}
	a := LocalTrimEstimate(entries, 512)
	b := LocalTrimEstimate(entries, 512)
	if a != b {
		t.Fatalf("重复调用结果不稳定: %+v vs %+v", a, b)
	}
}

func TestLocalTrimEstimateNonPositiveMaxOutputDisablesTrim(t *testing.T) {
	entries := []session.Entry{toolEntry(strings.Repeat("z", 4000))}
	for _, maxOutput := range []int{0, -1, -100} {
		got := LocalTrimEstimate(entries, maxOutput)
		if got.Trimmed || got.Saved != 0 || got.BeforeTokens != got.AfterTokens {
			t.Fatalf("maxOutput=%d 应禁用裁剪: %+v", maxOutput, got)
		}
	}
}

func TestLocalTrimEstimateTinyLimitNeverGrowsTokens(t *testing.T) {
	// 标记比原文还贵时，该条结果保持原样，Saved 不得为负。
	entries := []session.Entry{toolEntry("ab"), toolEntry("xy")}
	got := LocalTrimEstimate(entries, 1)
	if got.Trimmed {
		t.Fatal("插入标记反而变贵时不应报告已裁剪")
	}
	if got.Saved < 0 || got.AfterTokens < got.BeforeTokens {
		t.Fatalf("裁剪绝不增加 token: %+v", got)
	}
}

func TestLocalTrimEstimateLargeInput(t *testing.T) {
	// 大文本：验证累加与比值计算不会溢出或变负。
	big := strings.Repeat("0123456789abcdef", 1<<16) // ~1 MiB
	entries := []session.Entry{toolEntry(big), {Role: "user", Content: big}}
	got := LocalTrimEstimate(entries, 4096)
	if !got.Trimmed {
		t.Fatal("超长工具结果应被裁剪")
	}
	if got.BeforeTokens <= 0 || got.Saved < 0 || got.AfterTokens < 0 {
		t.Fatalf("大输入结果异常: %+v", got)
	}
	if got.BeforeTokens-got.AfterTokens != got.Saved {
		t.Fatalf("Saved 必须等于 Before-After: %+v", got)
	}
	if got.Saved > got.BeforeTokens {
		t.Fatalf("Saved 不可能超过 Before: %+v", got)
	}
}

func TestLocalHeadTailClipKeepsUTF8Intact(t *testing.T) {
	// 混合 ASCII、汉字、组合标记、ZWJ emoji 与旗帜，确保切点吸附字素簇边界。
	unit := "ab中e\u0301👨‍👩‍👧‍👦🇨🇳"
	text := strings.Repeat(unit, 40)
	for _, budget := range []int{7, 13, 64, 333, 1024} {
		clipped := localHeadTailClip(text, budget)
		if clipped == text {
			t.Fatalf("budget=%d: 超长文本应被裁剪", budget)
		}
		if !utf8.ValidString(clipped) {
			t.Fatalf("budget=%d: 裁剪结果不是合法 UTF-8", budget)
		}
		if len(clipped) >= len(text) {
			t.Fatalf("budget=%d: 裁剪结果未变短", budget)
		}
		parts := strings.Split(clipped, localTrimMarker)
		if len(parts) != 2 {
			t.Fatalf("budget=%d: 标记数量异常: %d", budget, len(parts)-1)
		}
		head, tail := parts[0], parts[1]
		// head 的末尾与 tail 的开头都不得落在字素簇内部（悬空 ZWJ / 组合标记）。
		if strings.HasSuffix(head, string(rune(textutil.ClusterZWJ))) {
			t.Fatalf("budget=%d: head 以悬空 ZWJ 结束", budget)
		}
		if strings.HasPrefix(tail, string(rune(textutil.ClusterZWJ))) {
			t.Fatalf("budget=%d: tail 以悬空 ZWJ 开始", budget)
		}
		if head != "" && !utf8.ValidString(head) || tail != "" && !utf8.ValidString(tail) {
			t.Fatalf("budget=%d: 窗口切片不是合法 UTF-8", budget)
		}
	}
}

func TestLocalHeadTailClipGraphemeBoundaryMatchesTools(t *testing.T) {
	// 与 tools.clipTo 相同的窗口比例：汉字串按字节预算裁剪后两端仍是完整汉字。
	text := strings.Repeat("汉", 100) // 每个汉字 3 字节
	clipped := localHeadTailClip(text, 30)
	parts := strings.Split(clipped, localTrimMarker)
	if len(parts) != 2 {
		t.Fatalf("标记数量异常: %d", len(parts)-1)
	}
	head, tail := parts[0], parts[1]
	if len(head)%3 != 0 || len(tail)%3 != 0 {
		t.Fatalf("汉字被切坏: head=%d tail=%d 字节", len(head), len(tail))
	}
	if head == "" || tail == "" {
		t.Fatal("head/tail 窗口不应为空")
	}
}

func TestLocalTrimEstimateCJKAndEmojiSavings(t *testing.T) {
	cjk := strings.Repeat("中", 2000)
	emoji := strings.Repeat("😀", 2000)
	for name, content := range map[string]string{"cjk": cjk, "emoji": emoji} {
		t.Run(name, func(t *testing.T) {
			got := LocalTrimEstimate([]session.Entry{toolEntry(content)}, 300)
			if !got.Trimmed || got.Saved <= 0 {
				t.Fatalf("%s 超长输出应产生节省: %+v", name, got)
			}
			// CJK/emoji 按 tokenize.IsWide 每 rune 约一个 token 估算。
			wantBefore := agent.EstimateTokens(content)
			if got.BeforeTokens != wantBefore {
				t.Fatalf("BeforeTokens=%d, want %d", got.BeforeTokens, wantBefore)
			}
		})
	}
}

func TestShouldSkipModelSummaryThresholds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result LocalTrimResult
		ratio  float64
		want   bool
	}{
		{"zero value uses default", LocalTrimResult{}, 0, false},
		{"no saving", LocalTrimResult{BeforeTokens: 1000, AfterTokens: 1000, Saved: 0}, 0, false},
		{"negative saved", LocalTrimResult{BeforeTokens: 1000, AfterTokens: 1100, Saved: -100}, 0, false},
		{"default below 0.3", LocalTrimResult{BeforeTokens: 100, AfterTokens: 71, Saved: 29}, 0, false},
		{"default exactly 0.3", LocalTrimResult{BeforeTokens: 100, AfterTokens: 70, Saved: 30}, 0, true},
		{"default above 0.3", LocalTrimResult{BeforeTokens: 100, AfterTokens: 60, Saved: 40}, 0, true},
		{"custom ratio below", LocalTrimResult{BeforeTokens: 1000, Saved: 500}, 0.6, false},
		{"custom ratio exactly", LocalTrimResult{BeforeTokens: 1000, Saved: 600}, 0.6, true},
		{"custom ratio above", LocalTrimResult{BeforeTokens: 1000, Saved: 900}, 0.6, true},
		{"explicit tiny ratio accepts small saving", LocalTrimResult{BeforeTokens: 1000, Saved: 1}, 0.001, true},
		{"ratio one requires full removal", LocalTrimResult{BeforeTokens: 1000, Saved: 999}, 1, false},
		{"ratio one full removal", LocalTrimResult{BeforeTokens: 1000, Saved: 1000}, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldSkipModelSummary(tc.result, tc.ratio); got != tc.want {
				t.Fatalf("ShouldSkipModelSummary(%+v, %v) = %v, want %v", tc.result, tc.ratio, got, tc.want)
			}
		})
	}
}

func TestShouldSkipModelSummaryLargeNumbers(t *testing.T) {
	// 大数：比值用 float64 计算，整数除法不会截断，也不会因乘法溢出。
	r := LocalTrimResult{BeforeTokens: 1 << 40, Saved: 1 << 38} // 恰好 25%
	if ShouldSkipModelSummary(r, 0.3) {
		t.Fatal("25% 不应达到默认 0.3 阈值")
	}
	if !ShouldSkipModelSummary(r, 0.25) {
		t.Fatal("25% 应达到显式 0.25 阈值")
	}
}
