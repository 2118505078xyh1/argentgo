package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestNavImplementedFlagsAndBadges 验证只有文档/记忆标记为已实现，
// 其余导航项都带"即将推出"角标，不再伪装成可用功能。
func TestNavImplementedFlagsAndBadges(t *testing.T) {
	for _, name := range navItems {
		wantImplemented := name == "聊天" || name == "文档" || name == "记忆"
		if got := navItemImplemented(name); got != wantImplemented {
			t.Errorf("navItemImplemented(%q) = %v, want %v", name, got, wantImplemented)
		}
		wantBadge := ""
		if !wantImplemented {
			wantBadge = "即将推出"
		}
		if got := navItemBadge(name); got != wantBadge {
			t.Errorf("navItemBadge(%q) = %q, want %q", name, got, wantBadge)
		}
	}
}

// TestNavPlaceholderPanelIsHonest 验证未实现面板显示角标与说明文字，
// 已实现面板不显示它们。
func TestNavPlaceholderPanelIsHonest(t *testing.T) {
	app := newTestChat()
	tt := ui.NewTester(app.view, 1180, 720)

	if err := tt.Click("定时任务"); err != nil {
		t.Fatalf("click 定时任务: %v", err)
	}
	for _, want := range []string{"即将推出", "需要任务调度 RPC 支持，后续接入"} {
		if !tt.HasText(want) {
			t.Fatalf("placeholder panel missing %q: %q", want, tt.Texts())
		}
	}

	if err := tt.Click("文档"); err != nil {
		t.Fatalf("click 文档: %v", err)
	}
	if tt.HasText("需要任务调度 RPC 支持，后续接入") {
		t.Fatalf("implemented panel shows placeholder note: %q", tt.Texts())
	}
}

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

	// 切换到非聊天项用其面板替换会话栏。
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

	// 切换到另一项替换之前面板的内容。
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
