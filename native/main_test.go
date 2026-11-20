package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// newTestChat builds an app whose canned reply arrives at once: no timers,
// no window and therefore no wall-clock dependencies in the tests.
func newTestChat() *chat {
	return &chat{instant: true}
}

func TestWelcomeVisible(t *testing.T) {
	app := newTestChat()
	tt := ui.NewTester(app.view, 1180, 720)
	for _, want := range []string{"argentgo", "AGENT RUNTIME", "有什么可以帮你？", "问点什么，或者输入 / 看命令。", "就绪"} {
		if !tt.HasText(want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
}

func TestSendAddsUserMessageAndReply(t *testing.T) {
	app := newTestChat()
	tt := ui.NewTester(app.view, 1180, 720)
	tt.Type("帮我写个函数")
	if err := tt.Click("发送"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("帮我写个函数") {
		t.Errorf("user message missing in %q", tt.Texts())
	}
	// The canned reply is Markdown, rendered natively by ui/markdown.
	if !tt.HasText("原生 Markdown") || !tt.HasText("fmt.Println") {
		t.Errorf("markdown reply missing in %q", tt.Texts())
	}
	if app.sent != 1 || len(app.messages) != 3 {
		t.Errorf("sent %d, %d messages; want 1 and 3", app.sent, len(app.messages))
	}
	if app.approval == nil {
		t.Error("first send should raise the fake approval")
	}
}

func TestStatusLineShortcuts(t *testing.T) {
	app := newTestChat()
	tt := ui.NewTester(app.view, 1180, 720)
	tt.Type("测试撤销与重试")
	if err := tt.Click("发送"); err != nil {
		t.Fatal(err)
	}
	// Move focus out of the auto-focused composer so window shortcuts fire.
	if err := tt.Click("文档"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyZ)
	if app.status != "已撤销上一轮" || !tt.HasText("已撤销上一轮") {
		t.Errorf("undo status %q, texts %q", app.status, tt.Texts())
	}
	// Undo removes the whole turn, not just the trailing reply: no orphan user
	// message may remain, and the approval card is cleared too.
	if len(app.messages) != 0 || app.approval != nil {
		t.Errorf("undo left %d messages, approval=%v", len(app.messages), app.approval)
	}
	// Undo removes the approval card, which sends focus back to the composer.
	if err := tt.Click("文档"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyY)
	if len(app.messages) != 3 {
		t.Errorf("retry left %d messages, want 3", len(app.messages))
	}
	if !tt.HasText("测试撤销与重试") {
		t.Errorf("retry did not replay the prompt: %q", tt.Texts())
	}
}

func TestNewSessionClearsRetryPrompt(t *testing.T) {
	app := newTestChat()
	tt := ui.NewTester(app.view, 1180, 720)
	tt.Type("上一会话的问题")
	if err := tt.Click("发送"); err != nil {
		t.Fatal(err)
	}
	if app.lastPrompt != "上一会话的问题" {
		t.Fatalf("lastPrompt = %q", app.lastPrompt)
	}
	app.newSession()
	if app.lastPrompt != "" || len(app.messages) != 0 {
		t.Fatalf("newSession left lastPrompt=%q messages=%d", app.lastPrompt, len(app.messages))
	}
	app.retry()
	if len(app.messages) != 0 || app.status != "没有可重试的消息" {
		t.Errorf("retry after newSession sent %d messages, status %q", len(app.messages), app.status)
	}
}

func TestThemeToggle(t *testing.T) {
	app := newTestChat()
	tt := ui.NewTester(app.view, 1180, 720)
	if app.light {
		t.Fatal("the initial palette should be dark")
	}
	if err := tt.Click("主题"); err != nil {
		t.Fatal(err)
	}
	if !app.light || !tt.HasText("已切换到浅色主题") {
		t.Errorf("light=%v, status=%q, texts %q", app.light, app.status, tt.Texts())
	}
	if err := tt.Click("主题"); err != nil {
		t.Fatal(err)
	}
	if app.light || !tt.HasText("已切换到深色主题") {
		t.Errorf("light=%v, status=%q, texts %q", app.light, app.status, tt.Texts())
	}
}

func TestApprovalKeyboardAndButtons(t *testing.T) {
	app := newTestChat()
	tt := ui.NewTester(app.view, 1180, 720)
	tt.Type("写文件")
	if err := tt.Click("发送"); err != nil {
		t.Fatal(err)
	}
	if app.approval == nil {
		t.Fatal("no pending approval after the first send")
	}
	if !tt.HasText("需要批准") || !tt.HasText("write_file") {
		t.Errorf("approval card missing in %q", tt.Texts())
	}
	// Y allows the pending approval when the composer does not have focus.
	if err := tt.Click("文件"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyY)
	if app.approval != nil {
		t.Fatal("Y did not resolve the approval")
	}
	if !tt.HasText("✓ 已允许 write_file") {
		t.Errorf("allow result missing in %q", tt.Texts())
	}
}
