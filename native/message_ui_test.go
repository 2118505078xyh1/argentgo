package main

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

func TestToolCardSummary(t *testing.T) {
	cases := []struct {
		name string
		m    message
		want string
	}{
		{
			name: "first non-empty line wins",
			m:    message{text: "\n  go test ./...  \nok  github.com/x 0.2s", status: "done"},
			want: "go test ./...",
		},
		{
			name: "whitespace is folded",
			m:    message{text: "cat\t\n  a  b   c", status: "done"},
			want: "cat",
		},
		{
			name: "running with no output",
			m:    message{status: "running"},
			want: tr("等待输出…"),
		},
		{
			name: "done with no output",
			m:    message{status: "done"},
			want: tr("(无输出)"),
		},
	}
	for _, tc := range cases {
		if got := toolCardSummary(&tc.m); got != tc.want {
			t.Errorf("%s: toolCardSummary = %q, want %q", tc.name, got, tc.want)
		}
	}

	long := ""
	for i := 0; i < 120; i++ {
		long += "x"
	}
	if got := toolCardSummary(&message{text: long, status: "done"}); utf8.RuneCountInString(got) != 81 || !strings.HasSuffix(got, "…") {
		t.Errorf("long summary not bounded: runes=%d tail=%q", utf8.RuneCountInString(got), got)
	}
}

func TestToolCardExpandCollapse(t *testing.T) {
	app := &chat{instant: true, messages: []*message{
		{
			id: 1, kind: msgTool, name: "shell", status: "done", done: true,
			text: "go test ./...\nok  github.com/argentgo 0.2s\nstderr: advisory warning",
		},
	}}
	tt := ui.NewTester(app.view, 1180, 720)
	if !tt.HasText("go test ./...") || !tt.HasText("shell") {
		t.Fatalf("collapsed card missing summary: %q", tt.Texts())
	}
	if tt.HasText("stderr: advisory warning") {
		t.Fatal("collapsed card leaked the full output")
	}
	// 点击卡片标题行（命中其中的名称文字）应展开完整输出。
	if err := tt.Click("shell"); err != nil {
		t.Fatal(err)
	}
	if !app.messages[0].expanded {
		t.Fatal("clicking the header did not expand the card")
	}
	if !tt.HasText("stderr: advisory warning") {
		t.Fatalf("expanded card missing full output: %q", tt.Texts())
	}
	// 再点一次折叠回去。
	if err := tt.Click("shell"); err != nil {
		t.Fatal(err)
	}
	if app.messages[0].expanded {
		t.Fatal("clicking the header did not collapse the card")
	}
	if tt.HasText("stderr: advisory warning") {
		t.Fatal("collapsed again but full output still visible")
	}
}

func TestMessageActionsCopyAndRetry(t *testing.T) {
	// 单条用户消息：操作条中的“复制”在本帧唯一，可精确点击。
	app := &chat{instant: true, messages: []*message{
		{id: 1, kind: msgUser, text: "复制测试问题", done: true},
	}}
	tt := ui.NewTester(app.view, 1180, 720)
	if !tt.HasText("复制") || !tt.HasText("重试") {
		t.Fatalf("message action bar missing: %q", tt.Texts())
	}

	old := copyToClipboard
	copied := ""
	copyToClipboard = func(s string) { copied = s }
	defer func() { copyToClipboard = old }()
	if err := tt.Click("复制"); err != nil {
		t.Fatal(err)
	}
	if copied != "复制测试问题" {
		t.Fatalf("copied %q, want the user message", copied)
	}

	// 每消息重试走与工具栏重试相同的发送路径。
	userMsg := app.messages[0]
	app.retryMessage(userMsg)
	if app.lastPrompt != "复制测试问题" {
		t.Fatalf("retry prompt = %q", app.lastPrompt)
	}
	if len(app.messages) != 3 {
		t.Fatalf("retry left %d messages, want 3", len(app.messages))
	}
	if app.messages[0] == userMsg {
		t.Fatal("retry did not replace the original turn")
	}
}

func TestJumpToLatest(t *testing.T) {
	app := &chat{instant: true}
	for i := 0; i < 30; i++ {
		app.next++
		app.messages = append(app.messages, &message{
			id: app.next, kind: msgAssistant, done: true,
			text: fmt.Sprintf("历史消息 %02d", i),
		})
	}
	tt := ui.NewTester(app.view, 1180, 720)
	if tt.HasText("↓ 跳到最新") {
		t.Fatal("at the end: jump button should be hidden")
	}
	tt.Move(600, 300)
	tt.Scroll(600, 300, 0, -600)
	if !tt.HasText("↓ 跳到最新") {
		t.Fatalf("scrolled up: jump button missing in %q", tt.Texts())
	}
	if err := tt.Click("↓ 跳到最新"); err != nil {
		t.Fatal(err)
	}
	if !app.list.AtEnd() {
		t.Fatal("click did not scroll to the end")
	}
	if tt.HasText("↓ 跳到最新") {
		t.Fatal("back at the end: jump button should be hidden")
	}
}

func TestWelcomeExamplePrompts(t *testing.T) {
	app := newTestChat()
	tt := ui.NewTester(app.view, 1180, 720)
	for _, want := range []string{"试试这样问：", "审查这段 Go 代码的并发问题", "问点什么，或者输入 / 看命令。"} {
		if !tt.HasText(want) {
			t.Errorf("welcome missing %q in %q", want, tt.Texts())
		}
	}
	if err := tt.Click("审查这段 Go 代码的并发问题"); err != nil {
		t.Fatal(err)
	}
	if app.draft != "审查这段 Go 代码的并发问题 " {
		t.Fatalf("example prompt seeded draft %q", app.draft)
	}
}
