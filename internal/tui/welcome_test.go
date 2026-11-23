package tui

import (
	"strings"
	"testing"
)

// TestNewShowsWelcomeCard 锁定首屏行为：新用户不再面对空白屏，欢迎卡同时给出
// 一句话定位、可直接照抄的示例 prompt、/help 入口与 Alt+U 撤销提示，并按 locale 切换。
func TestNewShowsWelcomeCard(t *testing.T) {
	cases := []struct {
		locale string
		want   []string
	}{
		{"zh", []string{"会读代码", "试试：", "/help", "Alt+U 撤销"}},
		{"en", []string{"coding agent", "Try:", "/help", "Alt+U"}},
		{"", []string{"coding agent", "/help", "Alt+U"}}, // 默认 en
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
		})
	}
}

// TestWelcomeCardYieldsToTranscript 保证欢迎卡只在空 transcript 时绘制：
// 用户提交首条消息后它必须让位，不把示例 prompt 永久钉在屏幕上。
func TestWelcomeCardYieldsToTranscript(t *testing.T) {
	m := New(Config{Locale: "zh"})
	if before := frameText(m, 100, 24); !strings.Contains(before, "会读代码") {
		t.Fatal("welcome card missing on empty transcript")
	}
	m.AppendUser("read main.go")
	after := frameText(m, 100, 24)
	if strings.Contains(after, "会读代码") || strings.Contains(after, "试试：") {
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
