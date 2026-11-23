package agent

import (
	"strings"
	"testing"
)

func TestUnfinishedNudge(t *testing.T) {
	t.Setenv("ARGENT_UNFINISHED_NUDGE", "1")
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"half width colon", "正在排查:", true},
		{"full width colon", "我接下来检查：", true},
		{"intent sentence", "我先查一下相关文件", true},
		{"question not triggered", "你要我继续吗？", false},
		{"english question", "Should I continue?", false},
		{"normal answer", "已经修好了。", false},
		{"colon inside code block", "```\nkey:\n```\n完成。", false},
		{"empty", "  ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksUnfinished(tc.text); got != tc.want {
				t.Fatalf("looksUnfinished(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}

	s := newRunState(Options{Model: "m", System: "s"}, "go", nil)
	if !s.maybeUnfinishedNudge("正在排查：") {
		t.Fatal("应注入续做提醒")
	}
	last := s.messages[len(s.messages)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "还没做完") {
		t.Fatalf("提醒内容错误: %#v", last)
	}
	if s.maybeUnfinishedNudge("接下来：") {
		t.Fatal("每次运行只应提醒一次")
	}
}
