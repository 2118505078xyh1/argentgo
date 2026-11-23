package tui

import (
	"strings"
	"testing"
)

// TestNewShowsWelcomeCard 锁定首屏行为：只显示一行，介绍 argentgo 的版本与定位，
// 并给出 /help 入口；文案按 locale 切换。
func TestNewShowsWelcomeCard(t *testing.T) {
	cases := []struct {
		locale string
		want   []string
	}{
		{"zh", []string{"argentgo 0.8.2", "通用 AI 助手", "/help"}},
		{"en", []string{"argentgo 0.8.2", "general-purpose", "/help"}},
		{"", []string{"argentgo 0.8.2", "general-purpose", "/help"}}, // 默认 en
	}
	for _, tc := range cases {
		t.Run(tc.locale, func(t *testing.T) {
			m := New(Config{Locale: tc.locale})
			frame := frameText(m, 100, 24)
			for _, want := range tc.want {
				if !strings.Contains(frame, want) {
					t.Fatalf("welcome card missing %q in locale %q:\n%s", want, tc.locale, frame)
				}
			}
			for _, gone := range []string{"试试：", "Alt+U", "coding agent"} {
				if strings.Contains(frame, gone) {
					t.Fatalf("welcome card must not show %q anymore:\n%s", gone, frame)
				}
			}
		})
	}
}

// TestWelcomeCardStaysOneLine 即使终端很窄，欢迎卡也只占一行（超宽时截断）。
func TestWelcomeCardStaysOneLine(t *testing.T) {
	for _, width := range []int{24, 60, 100} {
		frame := frameText(New(Config{Locale: "zh"}), width, 12)
		lines := strings.Split(strings.TrimRight(frame, "\n"), "\n")
		count := 0
		for _, line := range lines {
			if strings.Contains(line, welcomeMark) {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("width %d: welcome mark appears on %d lines, want 1:\n%s", width, count, frame)
		}
	}
}

// TestWelcomeCardYieldsToTranscript 保证欢迎卡只在空 transcript 时绘制：
// 用户提交首条消息后它必须让位，不把示例 prompt 永久钉在屏幕上。
func TestWelcomeCardYieldsToTranscript(t *testing.T) {
	m := New(Config{Locale: "zh"})
	if before := frameText(m, 100, 24); !strings.Contains(before, "argentgo 0.8.2") {
		t.Fatal("welcome card missing on empty transcript")
	}
	m.AppendUser("read main.go")
	after := frameText(m, 100, 24)
	if strings.Contains(after, "argentgo 0.8.2") || strings.Contains(after, "通用 AI 助手") {
		t.Fatalf("welcome card must disappear after first message:\n%s", after)
	}
	if !strings.Contains(after, "read main.go") {
		t.Fatalf("user message missing from transcript:\n%s", after)
	}
}

// TestVisibleNoticeTextChinese 锁定压缩通知的中文呈现，格式漂移时原样透传而不丢事件。
func TestVisibleNoticeTextChinese(t *testing.T) {
	got := visibleNoticeText("zh", "compacted context: ~9000 -> ~3000 tokens (target 4000)")
	if !strings.Contains(got, "上下文已压缩") || !strings.Contains(got, "9000→3000") {
		t.Fatalf("visibleNoticeText = %q, want Chinese compact hint", got)
	}
	const raw = "上下文溢出：已将记录从 ~9 压缩到 ~3 tokens"
	if got := visibleNoticeText("zh", raw); got != raw {
		t.Fatalf("Chinese notice should pass through, got %q", got)
	}
}

// TestTuiEventNoticeAppendsEntry 证明专用通知事件复用通知条目路径，出现在 transcript。
func TestTuiEventNoticeAppendsEntry(t *testing.T) {
	m := New(Config{Locale: "zh"})
	m.HandleEvent(Event{Kind: TuiEventNotice, Detail: "compacted context: ~9000 -> ~3000 tokens (target 4000)"})
	entries := m.Transcript()
	if len(entries) != 1 || entries[0].Kind != EntryNotice {
		t.Fatalf("entries = %+v, want one notice entry", entries)
	}
	if !strings.Contains(entries[0].Text, "上下文已压缩") {
		t.Fatalf("notice text = %q, want localized compaction hint", entries[0].Text)
	}
}
