package main

import (
	"strings"
	"testing"
	"unicode"

	"github.com/egoist/mygo/ui"
)

// TestEnUSMainViewHasNoChinese 验证 opt-in 开启后主界面无中文，对应
// TODO #52 的验收条件：ARGENT_LOCALE=en-US 时截图主界面不应出现汉字。
func TestEnUSMainViewHasNoChinese(t *testing.T) {
	configureLocale(func(k string) string {
		if k == "ARGENT_LOCALE" {
			return "en-US"
		}
		return ""
	})
	defer configureLocale(nil)

	app := newTestChat()
	tt := ui.NewTester(app.view, 1180, 720)
	for _, text := range tt.Texts() {
		for _, r := range text {
			if unicode.Is(unicode.Han, r) {
				t.Errorf("en-US 主界面出现汉字：%q（全部文本：%q）", text, tt.Texts())
				break
			}
		}
	}
	// 关键文案抽查，确保不是把整个界面渲染成空白。
	for _, want := range []string{"How can I help?", "model", "ready", "Retry", "theme"} {
		if !tt.HasText(want) && !strings.Contains(strings.Join(tt.Texts(), " "), want) {
			t.Errorf("en-US 主界面缺少 %q，实际文本：%q", want, tt.Texts())
		}
	}
}

// TestEnUSApprovalCardHasNoChinese 验证审批卡的新文案（标题/徽章/提示/理由）
// 在 en-US 下也已翻译，避免换皮改造把汉字带进英文界面。
func TestEnUSApprovalCardHasNoChinese(t *testing.T) {
	configureLocale(func(k string) string {
		if k == "ARGENT_LOCALE" {
			return "en-US"
		}
		return ""
	})
	defer configureLocale(nil)

	app := newTestChat()
	tt := ui.NewTester(app.view, 1180, 720)
	tt.Type("write file")
	if err := tt.Click("send"); err != nil {
		t.Fatal(err)
	}
	if app.approval == nil {
		t.Fatal("no pending approval after send")
	}
	for _, text := range tt.Texts() {
		for _, r := range text {
			if unicode.Is(unicode.Han, r) {
				t.Errorf("en-US 审批卡出现汉字：%q", text)
				break
			}
		}
	}
	for _, want := range []string{"Unconfirmed tool call", "approval required"} {
		if !tt.HasText(want) {
			t.Errorf("en-US 审批卡缺少 %q，实际文本：%q", want, tt.Texts())
		}
	}
}
