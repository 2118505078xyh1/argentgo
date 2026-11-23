package main

import (
	"encoding/json"
	"fmt"
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
	// gap 说明这块面板为什么还没有真实数据。它必须具体（点名缺哪类能力），
	// 不再使用"规划中的能力"这类空承诺；有真实数据时不再显示。
	gap string
}

// navPanels holds one placeholder panel per nav item. "聊天" is intentionally
// absent：其面板是现有侧边栏渲染的活动会话列表。
// code, so switching back to 聊天 restores the current session view.
var navPanels = map[string]navPanel{
	"定时任务": {
		title: "定时任务",
		desc:  "查看并按计划运行自动化任务。",
		rows:  []string{"暂无已配置的任务", "可用 CLI 添加 cron 任务"},
		gap:   "需要任务调度 RPC 支持，后续接入",
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
		gap:   "需要插件与技能目录 RPC 支持，后续接入",
	},
	"用量": {
		title: "用量",
		desc:  "统计令牌消耗与请求次数。",
		rows:  []string{"本次会话", "今日汇总"},
		gap:   "运行后可见：尚未收到用量事件",
	},
	"Git": {
		title: "Git",
		desc:  "查看仓库状态与最近提交。",
		rows:  []string{"工作区变更", "提交历史", "分支"},
		gap:   "未找到工作区的 .git 信息，暂时无法显示分支",
	},
	"任务": {
		title: "任务",
		desc:  "跟踪后台任务与长时作业。",
		rows:  []string{"正在运行", "最近完成"},
		gap:   "需要后台作业状态 RPC 支持，后续接入",
	},
	"工作流": {
		title: "工作流",
		desc:  "运行可复用的多步骤脚本。",
		rows:  []string{"可用工作流", "运行记录"},
		gap:   "需要工作流执行 RPC 支持，后续接入",
	},
	"终端": {
		title: "终端",
		desc:  "在工作区内打开一个 shell。",
		rows:  []string{"PowerShell", "Git Bash"},
		gap:   "native 不直接启动 shell，需要终端 RPC 支持",
	},
	"文件": {
		title: "文件",
		desc:  "浏览当前工作区的文件树。",
		rows:  []string{"工作区目录", "最近打开"},
		gap:   "需要文件树 RPC 支持，后续接入",
	},
	"计划": {
		title: "计划",
		desc:  "查看并整理多步骤的实施计划。",
		rows:  []string{"当前计划", "待办步骤"},
		gap:   "运行后可见：当前会话没有任务列表",
	},
}

const (
	navNoPanelGap = "没有更多可用信息，后续接入"
	navSoonBadge  = "即将推出"
)

// navUsage 保存从 engine 的 usage 事件读取的真实用量。TotalUsage 是
// 本轮运行的累计值，因此直接覆盖即可，不需要 native 自己累计。
type navUsage struct {
	input  int
	output int
	cached int
	calls  int
}

// navTodoItem 是"计划"面板展示的一条待办，来自 todo 工具写入会话的
// 结构化参数（native 只解码 JSON-RPC 数据，不复制工具实现）。
type navTodoItem struct {
	text   string
	status string
}

// navPanelFor 把导航项解析到其占位面板。未知项获得
// 通用面板，使框架永不回退到会话列表。
// accident.
func navPanelFor(name string) navPanel {
	if p, ok := navPanels[name]; ok {
		return p
	}
	return navPanel{title: name, desc: "该面板尚未实现。", rows: []string{"敬请期待"}, gap: navNoPanelGap}
}

// navItemImplemented 报告导航项的静态能力：文档/记忆有只读预览，
// 聊天由会话栏承载。动态接入的真实数据由 (*chat).navPanelReady 补充。
func navItemImplemented(name string) bool {
	return name == "" || name == "聊天" || navPanelFor(name).implemented
}

// navItemBadge 返回导航项的静态角标（无真实数据时使用）。
func navItemBadge(name string) string {
	if navItemImplemented(name) {
		return ""
	}
	return navSoonBadge
}

// navPanelLive 报告该导航项当前是否已从真实数据取得内容。它只检查
// 内存中的快照，不做磁盘 IO，因此可在每帧的导航网格渲染中调用。
func (app *chat) navPanelLive(name string) bool {
	switch name {
	case "用量":
		return app.usage.calls > 0
	case "Git":
		return app.gitBranch != ""
	case "计划":
		return len(app.planTodos) > 0
	}
	return false
}

// navPanelReady 报告导航项是否已有真实内容：静态已实现，或动态拿到了数据。
func (app *chat) navPanelReady(name string) bool {
	return navItemImplemented(name) || app.navPanelLive(name)
}

// navPanelBadge 返回与当前数据状态一致的诚实角标：有真实数据就不打标。
func (app *chat) navPanelBadge(name string) string {
	if app.navPanelReady(name) {
		return ""
	}
	return navSoonBadge
}

// parsePlanTodos 解码 todo 工具调用参数中的结构化任务列表。
// 只有显式带 todos 数组的 write 调用才算一次计划更新；read 调用
// 或畸形参数返回 nil，调用方据此保留旧列表而不是清空它。
func parsePlanTodos(args string) []navTodoItem {
	if strings.TrimSpace(args) == "" {
		return nil
	}
	var doc struct {
		Todos []struct {
			Text   string `json:"text"`
			Status string `json:"status"`
		} `json:"todos"`
	}
	if json.Unmarshal([]byte(args), &doc) != nil || doc.Todos == nil {
		return nil
	}
	items := make([]navTodoItem, 0, len(doc.Todos))
	for _, t := range doc.Todos {
		text := strings.TrimSpace(t.Text)
		if text == "" {
			continue
		}
		items = append(items, navTodoItem{text: text, status: strings.TrimSpace(t.Status)})
	}
	return items
}

// navGitBranch 读取工作区的 .git/HEAD 并返回当前分支名。readFile 可注入
// 以便离线测试；路径以 root 为工作区根。detached HEAD（直接是提交哈希）
// 不猜分支名，返回空串，由调用方继续显示诚实占位。
func navGitBranch(root string, readFile func(string) ([]byte, error)) string {
	if readFile == nil {
		readFile = os.ReadFile
	}
	data, err := readFile(filepath.Join(root, ".git", "HEAD"))
	if err != nil {
		return ""
	}
	return branchFromHEAD(string(data))
}

// branchFromHEAD 从 .git/HEAD 的内容解析分支名。
func branchFromHEAD(head string) string {
	line := strings.TrimSpace(head)
	const prefix = "ref: refs/heads/"
	if !strings.HasPrefix(line, prefix) {
		return "" // detached HEAD 或未知格式
	}
	branch := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if branch == "" || strings.ContainsAny(branch, "\r\n") {
		return ""
	}
	return branch
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

// navPanelRows 返回面板当前应显示的正文行，以及是否来自真实数据。
// 未接上的面板保持静态占位，由调用方继续显示具体 gap 文案。
func (app *chat) navPanelRows(name string) ([]string, bool) {
	panel := navPanelFor(name)
	switch name {
	case "文档", "记忆":
		if preview := navDocPreview(name, app.configInfo.workspace, nil); len(preview) > 0 {
			return preview, true
		}
	case "用量":
		if app.usage.calls > 0 {
			return []string{
				fmt.Sprintf(tr("本轮输入 token：%d"), app.usage.input),
				fmt.Sprintf(tr("本轮输出 token：%d"), app.usage.output),
				fmt.Sprintf(tr("本轮请求数：%d"), app.usage.calls),
			}, true
		}
	case "Git":
		if app.gitBranch != "" {
			return []string{tr("当前分支：") + app.gitBranch}, true
		}
	case "计划":
		if len(app.planTodos) > 0 {
			rows := make([]string, 0, len(app.planTodos))
			for _, item := range app.planTodos {
				var mark string
				switch item.status {
				case "completed":
					mark = tr("已完成")
				case "in_progress":
					mark = tr("进行中")
				default:
					mark = tr("待办")
				}
				rows = append(rows, mark+" · "+item.text)
			}
			return rows, true
		}
	}
	return panel.rows, panel.implemented
}

// renderNavPanel 用当前数据渲染侧栏面板。
func (app *chat) renderNavPanel(c *ui.Context, p palette, name string) {
	panel := navPanelFor(name)
	rows, realData := app.navPanelRows(name)
	ui.Column(c).Gap(10).Grow(1).Children(func() {
		// 标题行：未实现项带"即将推出"角标，不伪装成可用功能。
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, tr(panel.title)).FontSize(16).Bold().TextColor(p.strong)
			if badge := app.navPanelBadge(name); badge != "" {
				ui.Text(c, tr(badge)).FontSize(10).TextColor(p.dim).
					Padding(2, 6).Radius(999).Background(p.element)
			}
		})
		ui.Text(c, tr(panel.desc)).FontSize(12).TextColor(p.dim)
		if !realData {
			ui.Text(c, tr(panel.gap)).FontSize(11).TextColor(p.dim)
		}
		for _, row := range rows {
			label := tr(row)
			if realData {
				// 只读预览卡片：有背景但不可点击，内容来自真实数据。
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
