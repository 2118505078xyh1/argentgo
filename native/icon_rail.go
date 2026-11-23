package main

import (
	"github.com/egoist/mygo/ui"
)

// 最左图标栏：RPG Desktop 三栏布局的第一栏。用语言无关的字形作为图标，
// 点击后切换到对应导航面板（复用 sidebar 的导航状态与行为，不引入新功能）。
// 字形不进入 i18n 表，天然满足 en-US 主界面无汉字门禁。
type railIcon struct {
	glyph string
	nav   string
}

var railIcons = []railIcon{
	{glyph: "◈", nav: "聊天"},
	{glyph: "◷", nav: "定时任务"},
	{glyph: "▤", nav: "文档"},
	{glyph: "◍", nav: "记忆"},
	{glyph: "✦", nav: "扩展"},
	{glyph: "▦", nav: "用量"},
	{glyph: "⑂", nav: "Git"},
	{glyph: "☑", nav: "任务"},
	{glyph: "⇄", nav: "工作流"},
	{glyph: "▣", nav: "终端"},
	{glyph: "▥", nav: "文件"},
	{glyph: "☰", nav: "计划"},
}

// iconRail 渲染 44px 宽的图标栏：当前导航高亮为紫色，底部是主题切换
// （同顶栏“主题”按钮的同一路径，换肤即时生效且持久化）。
func (app *chat) iconRail(c *ui.Context, p palette) {
	ui.Column(c).Width(44).FillHeight().Background(p.sidebar).
		BorderWidth(0, 0, 0, 1).BorderColor(p.borderSubtle).
		AlignItems(ui.Center).Padding(10, 0, 10, 0).Gap(4).Children(func() {
		for _, item := range railIcons {
			it := item
			active := app.nav == it.nav || (it.nav == "聊天" && app.nav == "")
			btn := ui.Button(c, it.glyph).Width(34).Height(34).Radius(10).FontSize(16)
			if active {
				btn.Background(p.accentBG).TextColor(p.accent)
			} else {
				btn.Background(ui.Transparent).TextColor(p.dim)
			}
			if btn.Clicked() {
				app.nav = it.nav
				app.status = tr("已切换到") + tr(it.nav)
			}
		}
		ui.Spacer(c)
		themeGlyph := "☾"
		if app.light {
			themeGlyph = "☀"
		}
		if ui.Button(c, themeGlyph).Width(34).Height(34).Radius(10).
			FontSize(16).Background(ui.Transparent).TextColor(p.dim).Clicked() {
			app.toggleTheme()
		}
	})
}
