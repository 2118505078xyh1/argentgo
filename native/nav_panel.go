package main

import "github.com/egoist/mygo/ui"

// navPanel is the placeholder content shown in the sidebar when a non-chat
// navigation item is active. It is deliberately data-driven: wiring up a new
// nav item only needs one map entry, not a new render branch in sidebar().
type navPanel struct {
	title string
	desc  string
	rows  []string
}

// navPanels holds one placeholder panel per nav item. "聊天" is intentionally
// absent: its panel is the live session list rendered by the existing sidebar
// code, so switching back to 聊天 restores the current session view.
var navPanels = map[string]navPanel{
	"定时任务": {
		title: "定时任务",
		desc:  "查看并按计划运行自动化任务。",
		rows:  []string{"暂无已配置的任务", "可用 CLI 添加 cron 任务"},
	},
	"文档": {
		title: "文档",
		desc:  "浏览工作区内的项目文档与说明。",
		rows:  []string{"README", "AGENTS.md", "docs/"},
	},
	"记忆": {
		title: "记忆",
		desc:  "管理跨会话注入的项目记忆。",
		rows:  []string{"AGENTS.md", "CLAUDE.md"},
	},
	"扩展": {
		title: "扩展",
		desc:  "查看已安装的插件与技能。",
		rows:  []string{"插件", "技能", "MCP 服务器"},
	},
	"用量": {
		title: "用量",
		desc:  "统计令牌消耗与请求次数。",
		rows:  []string{"本次会话", "今日汇总"},
	},
	"Git": {
		title: "Git",
		desc:  "查看仓库状态与最近提交。",
		rows:  []string{"工作区变更", "提交历史", "分支"},
	},
	"任务": {
		title: "任务",
		desc:  "跟踪后台任务与长时作业。",
		rows:  []string{"正在运行", "最近完成"},
	},
	"工作流": {
		title: "工作流",
		desc:  "运行可复用的多步骤脚本。",
		rows:  []string{"可用工作流", "运行记录"},
	},
	"终端": {
		title: "终端",
		desc:  "在工作区内打开一个 shell。",
		rows:  []string{"PowerShell", "Git Bash"},
	},
	"文件": {
		title: "文件",
		desc:  "浏览当前工作区的文件树。",
		rows:  []string{"工作区目录", "最近打开"},
	},
	"计划": {
		title: "计划",
		desc:  "查看并整理多步骤的实施计划。",
		rows:  []string{"当前计划", "待办步骤"},
	},
}

// navPanelFor resolves a nav item to its placeholder panel. Unknown items get
// a generic panel so the framework never falls back to the session list by
// accident.
func navPanelFor(name string) navPanel {
	if p, ok := navPanels[name]; ok {
		return p
	}
	return navPanel{title: name, desc: "该面板尚未实现。", rows: []string{"敬请期待"}}
}

// renderNavPanel draws one placeholder panel in the sidebar: a title, a short
// explanation and a few simple content rows.
func (app *chat) renderNavPanel(c *ui.Context, p palette, name string) {
	panel := navPanelFor(name)
	ui.Column(c).Gap(10).Grow(1).Children(func() {
		ui.Text(c, panel.title).FontSize(16).Bold().TextColor(p.strong)
		ui.Text(c, panel.desc).FontSize(12).TextColor(p.dim)
		for _, row := range panel.rows {
			label := row
			ui.Row(c).Padding(8, 10).Radius(8).Background(p.element).Children(func() {
				ui.Text(c, label).FontSize(12).TextColor(p.text).SingleLine()
			})
		}
	})
}
