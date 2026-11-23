package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"
)

// navPanel 是当非聊天导航项活动时侧边栏显示的内容。
// 它刻意数据驱动：接入新导航项只需一个 map 条目，而非
// sidebar() 中的新渲染分支。
type navPanel struct {
	title string
	desc  string
	rows  []string
	// implemented 为 true 表示该面板有真实内容（当前仅文档/记忆的只读预览）。
	// 其余面板只是规划占位，渲染为置灰的纯文本并带"即将推出"角标，
	// 不套用按钮/卡片样式，避免让用户误以为可以点击使用。
	implemented bool
}

// navPanels holds one placeholder panel per nav item. "聊天" is intentionally
// absent：其面板是现有侧边栏渲染的活动会话列表。
// code, so switching back to 聊天 restores the current session view.
var navPanels = map[string]navPanel{
	"定时任务": {
		title: "定时任务",
		desc:  "查看并按计划运行自动化任务。",
		rows:  []string{"暂无已配置的任务", "可用 CLI 添加 cron 任务"},
	},
	"文档": {
		title:       "文档",
		desc:        "浏览工作区内的项目文档与说明。",
		rows:        []string{"README", "AGENTS.md", "docs/"},
		implemented: true,
	},
	"记忆": {
		title:       "记忆",
		desc:        "管理跨会话注入的项目记忆。",
		rows:        []string{"AGENTS.md", "CLAUDE.md"},
		implemented: true,
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

// navPanelFor 把导航项解析到其占位面板。未知项获得
// 通用面板，使框架永不回退到会话列表。
// accident.
func navPanelFor(name string) navPanel {
	if p, ok := navPanels[name]; ok {
		return p
	}
	return navPanel{title: name, desc: "该面板尚未实现。", rows: []string{"敬请期待"}}
}

// navItemImplemented 报告导航项是否有真实内容。main.go 的导航网格据此把
// 未实现项置灰并去掉可点击暗示；聊天由会话栏承载，视为已实现。
func navItemImplemented(name string) bool {
	return name == "" || name == "聊天" || navPanelFor(name).implemented
}

// navItemBadge 返回导航项的诚实角标：未实现项显示"即将推出"，其余为空。
func navItemBadge(name string) string {
	if navItemImplemented(name) {
		return ""
	}
	return "即将推出"
}

// navDocPreview 为"文档/记忆"面板提供只读预览：读取工作区内的真实文件，
// 按行截断到有限条数。文件缺失或读取失败时回退到面板声明的静态行，
// 因此离线/无文件的单元测试仍稳定。路径由调用方注入（工作区根目录）。
func navDocPreview(name, root string, readFile func(string) ([]byte, error)) []string {
	var files []string
	switch name {
	case "文档":
		files = []string{"README.md", "README.zh-CN.md", "AGENTS.md"}
	case "记忆":
		files = []string{"AGENTS.md", "CLAUDE.md"}
	default:
		return nil
	}
	if readFile == nil {
		readFile = os.ReadFile
	}
	var rows []string
	for _, file := range files {
		data, err := readFile(filepath.Join(root, filepath.FromSlash(file)))
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(data), "\n") {
			if i >= 6 { // 只读预览最多 6 行，避免撑满侧栏
				break
			}
			if line = strings.TrimSpace(line); line != "" {
				rows = append(rows, file+": "+line)
			}
		}
	}
	if len(rows) == 0 {
		return navPanels[name].rows // 回退静态提示
	}
	return rows
}

// 说明和几行简单内容。
func (app *chat) renderNavPanel(c *ui.Context, p palette, name string) {
	panel := navPanelFor(name)
	// 文档/记忆面板读取工作区真实文件作为只读预览；取不到时回退静态行。
	if preview := navDocPreview(name, app.configInfo.workspace, nil); len(preview) > 0 {
		panel.rows = preview
	}
	ui.Column(c).Gap(10).Grow(1).Children(func() {
		// 标题行：未实现项带"即将推出"角标，不伪装成可用功能。
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, tr(panel.title)).FontSize(16).Bold().TextColor(p.strong)
			if badge := navItemBadge(name); badge != "" {
				ui.Text(c, tr(badge)).FontSize(10).TextColor(p.dim).
					Padding(2, 6).Radius(999).Background(p.element)
			}
		})
		ui.Text(c, tr(panel.desc)).FontSize(12).TextColor(p.dim)
		if !panel.implemented {
			ui.Text(c, tr("规划中的能力，暂不可点击")).FontSize(11).TextColor(p.dim)
		}
		for _, row := range panel.rows {
			label := tr(row)
			if panel.implemented {
				// 只读预览卡片：有背景但不可点击，内容来自真实文件。
				ui.Row(c).Padding(8, 10).Radius(8).Background(p.element).Children(func() {
					ui.Text(c, label).FontSize(12).TextColor(p.text).SingleLine()
				})
				continue
			}
			// 未实现项：纯置灰文本，无背景/圆角/按钮样式，不制造虚假可点击暗示。
			ui.Row(c).Padding(6, 10).Children(func() {
				ui.Text(c, label).FontSize(12).TextColor(p.dim).SingleLine()
			})
		}
	})
}
