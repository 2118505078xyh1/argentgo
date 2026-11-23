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
	for _, want := range []string{"Chat", "How can I help?", "model", "ready", "Undo", "Retry", "theme"} {
		if !tt.HasText(want) && !strings.Contains(strings.Join(tt.Texts(), " "), want) {
			t.Errorf("en-US 主界面缺少 %q，实际文本：%q", want, tt.Texts())
		}
	}
}
