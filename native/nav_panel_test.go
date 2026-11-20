package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestEveryNonChatNavItemHasPlaceholderPanel(t *testing.T) {
	for _, name := range navItems {
		if name == "聊天" {
			continue
		}
		p := navPanelFor(name)
		if p.title != name || p.desc == "" || len(p.rows) == 0 {
			t.Errorf("navPanelFor(%q) = %+v; want a titled panel with content", name, p)
		}
	}
}

func TestNavSwitchRendersDifferentSidebarPanels(t *testing.T) {
	app := newTestChat()
	tt := ui.NewTester(app.view, 1180, 720)

	const (
		cronDesc = "查看并按计划运行自动化任务。"
		gitDesc  = "查看仓库状态与最近提交。"
	)

	// Default / 聊天 shows the live session rail, not a placeholder panel.
	if !tt.HasText("重构聊天界面") {
		t.Fatalf("chat view has no session rail: %q", tt.Texts())
	}
	if tt.HasText(cronDesc) {
		t.Fatalf("chat view shows the cron panel: %q", tt.Texts())
	}

	// Switching to a non-chat item replaces the session rail with its panel.
	if err := tt.Click("定时任务"); err != nil {
		t.Fatalf("click 定时任务: %v (texts %q)", err, tt.Texts())
	}
	if app.nav != "定时任务" {
		t.Errorf("nav = %q, want 定时任务", app.nav)
	}
	if !tt.HasText(cronDesc) {
		t.Errorf("cron panel description missing: %q", tt.Texts())
	}
	if tt.HasText("重构聊天界面") {
		t.Errorf("session rail still rendered after nav switch: %q", tt.Texts())
	}

	// Switching to another item replaces the previous panel's content.
	if err := tt.Click("Git"); err != nil {
		t.Fatalf("click Git: %v (texts %q)", err, tt.Texts())
	}
	if !tt.HasText(gitDesc) {
		t.Errorf("Git panel description missing: %q", tt.Texts())
	}
	if tt.HasText(cronDesc) {
		t.Errorf("previous cron panel still rendered: %q", tt.Texts())
	}

	// 聊天 restores the session rail.
	if err := tt.Click("聊天"); err != nil {
		t.Fatalf("click 聊天: %v (texts %q)", err, tt.Texts())
	}
	if !tt.HasText("重构聊天界面") || tt.HasText(gitDesc) {
		t.Errorf("聊天 did not restore the session rail: %q", tt.Texts())
	}
}
