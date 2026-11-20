package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// newTestChat 构建一个即时收到预设回复的 app：无计时器、
// 无窗口，因此测试中无挂钟依赖。
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
	// 预设回复是 Markdown，由 ui/markdown 原生渲染。
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
	// 把焦点移出自动聚焦的输入框，使窗口快捷键生效。
	if err := tt.Click("文档"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyZ)
	if app.status != "已撤销上一轮" || !tt.HasText("已撤销上一轮") {
		t.Errorf("undo status %q, texts %q", app.status, tt.Texts())
	}
	// Undo 移除整个回合，而非仅末尾回复：不得留下孤立的用户
	// 消息，审批卡片也被清除。
	if len(app.messages) != 0 || app.approval != nil {
		t.Errorf("undo left %d messages, approval=%v", len(app.messages), app.approval)
	}
	// Undo 移除审批卡片，它把焦点送回输入框。
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
	// 当输入框没有焦点时 Y 允许待处理的审批。
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
