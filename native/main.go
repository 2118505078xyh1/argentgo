// Command mygo-native 是 argentgo 聊天前端的原生 MyGo UI 复刻。
// 它只使用 package ui（无 WebView、HTML 或 JS）；助手
// newTestChat 构建一个即时收到预设回复的 app：无计时器、无窗口，因此测试中无挂钟依赖。预设回复是 Markdown，由 ui/markdown 原生渲染。把焦点移出自动聚焦的输入框，使窗口快捷键生效。Undo 移除整个回合，而非仅末尾回复：不得留下孤立的用户消息，审批卡片也被清除。Undo 移除审批卡片，它把焦点送回输入框。当输入框没有焦点时 Y 允许待处理的审批。
// natively.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// 调色板采用 RPG Desktop 紫色系设计 token（浅/深双主题）。
// 所有颜色集中在这里，换肤只改色值，不动渲染结构与交互。
type palette struct {
	bg, surface, element, border, borderSubtle ui.Color
	text, dim, accent, ok, danger, warn        ui.Color
	strong, onAccent, sidebar                  ui.Color
	accentBG, accentLine                       ui.Color
	warnBG, warnLine, warnText                 ui.Color // 审批/提示卡黄色系
}

var (
	darkPalette = palette{
		bg:           ui.Hex("#18181B"),
		surface:      ui.Hex("#232327"),
		element:      ui.Hex("#2E2E33"),
		border:       ui.Hex("#34343A"),
		borderSubtle: ui.Hex("#2C2C31"),
		text:         ui.Hex("#F2F2F5"),
		dim:          ui.Hex("#9A9AA2"),
		accent:       ui.Hex("#9B8AFF"),
		ok:           ui.Hex("#5FCE7A"),
		danger:       ui.Hex("#FF6B6B"),
		warn:         ui.Hex("#F9AB00"),
		strong:       ui.Hex("#F2F2F5"),
		onAccent:     ui.Hex("#17132B"),
		sidebar:      ui.Hex("#1E1E22"),
		accentBG:     ui.Hex("#2E2642"),
		accentLine:   ui.RGBA(155, 138, 255, 0.34),
		warnBG:       ui.Hex("#3A2F14"),
		warnLine:     ui.Hex("#F9AB00"),
		warnText:     ui.Hex("#F9AB00"),
	}
	lightPalette = palette{
		bg:           ui.Hex("#FAFAFA"),
		surface:      ui.Hex("#FFFFFF"),
		element:      ui.Hex("#F4F4F6"),
		border:       ui.Hex("#E5E5EA"),
		borderSubtle: ui.Hex("#ECECF0"),
		text:         ui.Hex("#1C1C1E"),
		dim:          ui.Hex("#8E8E93"),
		accent:       ui.Hex("#7C5CFC"),
		ok:           ui.Hex("#34A853"),
		danger:       ui.Hex("#D93025"),
		warn:         ui.Hex("#C77700"),
		strong:       ui.Hex("#1C1C1E"),
		onAccent:     ui.Hex("#FFFFFF"),
		sidebar:      ui.Hex("#F4F4F6"),
		accentBG:     ui.Hex("#F0ECFF"),
		accentLine:   ui.RGBA(124, 92, 252, 0.27),
		warnBG:       ui.Hex("#FEF7E0"),
		warnLine:     ui.Hex("#F9AB00"),
		warnText:     ui.Hex("#8A5A00"),
	}
)

type msgKind int

const (
	msgUser msgKind = iota
	msgAssistant
	msgTool
)

// message 是 transcript 的一行。对工具，name/text/status 描述
// 紧凑卡片；对助手消息，text 是 Markdown 源。
type message struct {
	id         int
	kind       msgKind
	text       string
	name       string
	status     string // "running", "done" or "failed"
	done       bool   // assistant reply finished streaming
	expanded   bool   // tool card: show the full output instead of the summary
	durationMS int64  // tool card: engine-reported elapsed time (0 means unknown)
	argPreview bool   // tool card: built from live-only tool_arg_delta fragments
}

type approval struct {
	id            string // engine tool-call id from the approval:request notice; empty in demo mode
	name          string
	args          string
	reason        string // 拒绝理由（用户可输入，随 RPC 透传供审计）
	confirmAlways bool   // "始终允许"二次确认态
}

// session 是会话栏的一行。id 是真实的 engine 会话 id；
// 对 fakeSessions 中的 demo 行为空。openSession 在设置时使用 id
// session 是会话栏的一行。id 是真实的 engine 会话 id；对 fakeSessions 中的 demo 行为空。openSession 在设置时使用 id（使 session.load 获得真实 id）并在 demo 模式下回退到 title。chat 是唯一的 app 状态对象。视图每帧从它重建；后台工作只通过 Window.Update 改变它。
type session struct {
	id    string
	title string
	meta  string
	group string
}

// SessionBranch 是 session.tree 返回的单条分支节点。它与 server.SessionBranch
// 的 wire 形态一致，但 native 只发 JSON-RPC，不 import internal/。
type SessionBranch struct {
	ID     string `json:"id"`
	Parent string `json:"parent,omitempty"`
	Kind   string `json:"kind"`
	Depth  int    `json:"depth"`
	Active bool   `json:"active,omitempty"`
	Label  string `json:"label,omitempty"`
	Fork   bool   `json:"fork,omitempty"`
}

// sessionEvent 是 session.load 返回的单条记录，对应 internal/session 的
// id/parent/kind/ts/data 约定。data 的具体形态由 decodeSessionEventData 解析。
type sessionEvent struct {
	ID     string          `json:"id"`
	Parent string          `json:"parent"`
	Kind   string          `json:"kind"`
	TS     time.Time       `json:"ts"`
	Data   json.RawMessage `json:"data"`
}

// sessionEventData 覆盖会话记录 data 的两种线上形态：纯字符串（旧格式）
// 或带 content/text/output 等字段的对象。Arguments 是工具调用的原始参数
// JSON，供"计划"面板读取 todo 工具写入的结构化任务列表。
type sessionEventData struct {
	Content   string `json:"content"`
	Text      string `json:"text"`
	Output    string `json:"output"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	Arguments string `json:"arguments"`
	IsError   bool   `json:"is_error"`
}

// 这些 demo 数据保持中文原文作为内部身份（selectedSession/sessionRowID 依赖
// title），仅在渲染时经 tr() 翻译，使 configureLocale 在 main() 设置后生效。
var fakeSessions = []session{
	{title: "重构聊天界面", meta: "2 分钟前", group: "今天"},
	{title: "修复工具调用状态", meta: "昨天 18:20", group: "昨天"},
	{title: "整理记忆索引", meta: "3 天前", group: "更早"},
}

var quickActions = []string{"解释这段代码", "写单元测试", "检查命令", "/ 命令"}

// chat 是唯一的 app 状态对象。视图每帧从它
// 重建；后台工作只通过 Window.Update 改变它。
type chat struct {
	win     *mygo.Window
	engine  *bridge // nil in tests / when the engine binary is unavailable
	instant bool    // tests set this so replies appear at once, with no timers

	// engineDisconnected 标记 engine 连接已断开。只影响状态点颜色，
	// 常态（未断开）不参与渲染，因此常态帧字节保持不变。
	engineDisconnected bool

	light           bool
	draft           string
	status          string
	sessionQuery    string
	selectedSession string
	messages        []*message
	tools           map[string]*message // real tool cards keyed by tool-call id, upserted from events
	// argPreviewIDs 记录流式参数预览按并发槽位选用的卡片 id。片段早于 tool_start
	// 到达且 id 会在流式中从 pending-N 切换为真实调用 id，靠它把预览卡迁移到最终 id。
	argPreviewIDs map[int]string
	// replySplit 标记本轮回复被工具调用切分过：多段时每段文本由 delta
	// 各自累积，finish 不再用整段全文覆盖，避免重复。
	replySplit  bool
	list        ui.ListState
	sessionList ui.ListState
	md          markdown.Options

	// sessions 保存通过 session.list 获取的真实会话栏。在成功获取前
	// 它为 nil，此时 sessionPanel 继续显示
	// fakeSessions（demo 模式，或必须优雅降级的失败 RPC）。
	sessions          []session
	sessionListLoaded bool // session.list is fetched at most once per chat

	// 右键菜单的内联重命名状态：renamingSession 是正在改名行的 id，
	// renameDraft 是编辑中的标题。为空表示没有行处于编辑态。
	renamingSession string
	renameDraft     string

	next       int
	sent       int
	lastPrompt string // most recent user text; backed up by undo, replayed by retry
	streaming  bool
	reply      *message
	tool       *message
	approval   *approval

	// sessionListRPC 是加载会话栏的测试接缝。为 nil 时，
	// fetchSessions 直接调用 engine.call；测试注入记录器以
	// 验证 session.list RPC 而无需活 engine 进程。
	sessionListRPC func(method string, params any) (json.RawMessage, error)

	// sessionLoadRPC 是打开会话的测试接缝。为 nil 时，
	// openSession 直接调用 engine.call；测试注入记录器以
	// 验证 session.load RPC 而无需活 engine 进程。
	sessionLoadRPC func(method string, params any) (json.RawMessage, error)

	// sessionTreeRPC 是获取会话分支树的测试接缝。为 nil 时，
	// fetchSessionTree 直接调用 engine.call；测试注入记录器以
	// 验证 session.tree RPC 而无需活 engine 进程。
	sessionTreeRPC func(method string, params any) (json.RawMessage, error)

	// branches 是当前选中会话的分支树镜像，branchSession 标记它属于哪个会话。
	// 两者只在 session.tree 成功后一起更新，失败时保留旧树。
	branches      []SessionBranch
	branchSession string

	// approvalAnswerRPC 是回答审批的测试接缝。为 nil 时，
	// answerApproval 直接调用 engine.call；测试注入记录器以
	approvalAnswerRPC func(method string, params any) (json.RawMessage, error)

	// configInfo 保存通过 config.info RPC 获取的配置快照。
	// 零值表示未获取或获取失败，状态栏随后回退到 demo 占位串。
	configInfo       configInfoSnapshot
	configInfoLoaded bool // config.info is fetched at most once per chat

	// configInfoRPC 是获取配置的测试接缝。为 nil 时，
	// fetchConfigInfo 直接调用 engine.call；测试注入记录器以
	// 验证 config.info RPC 而无需活 engine 进程。
	configInfoRPC func(method string, params any) (json.RawMessage, error)

	// 以下三个字段是导航面板的真实数据来源，全部来自 engine 已暴露的
	// 数据（usage 事件、tool_end 事件、工作区 .git/HEAD），没有新增 RPC。
	usage     navUsage      // 从 usage 事件读取的本轮累计用量
	planTodos []navTodoItem // 最近的 todo 工具任务列表
	gitBranch string        // 启动时从工作区 .git/HEAD 读取的分支名
}

func (app *chat) palette() palette {
	if app.light {
		return lightPalette
	}
	return darkPalette
}

// applyTheme 把 app 的调色板安装为窗口主题，使控件
// （按钮、输入框、滚动条）遵循与我们自己的元素相同的设计。
func (app *chat) applyTheme(c *ui.Context) palette {
	p := app.palette()
	t := *c.Theme()
	t.Background, t.Surface, t.SurfaceHover, t.SurfacePressed = p.bg, p.surface, p.element, p.element
	t.Border, t.Text, t.TextMuted = p.border, p.text, p.dim
	t.Accent, t.AccentText, t.InactiveAccent = p.accent, p.onAccent, p.accent
	t.Success, t.Danger, t.Warning = p.ok, p.danger, p.warn
	t.Focus, t.Font, t.FontSize, t.Radius = p.accent, "Segoe UI", 14, 10
	c.SetTheme(&t)

	app.md.Theme = &markdown.Theme{
		Font:                "Segoe UI",
		CodeFont:            "Consolas",
		FontSize:            14,
		Link:                p.accent,
		CodeBlockBackground: p.surface,
		CodeBlockBorder:     p.border,
		QuoteBar:            p.accent,
	}
	return p
}

func (app *chat) view(c *ui.Context) {
	p := app.applyTheme(c)
	t := c.Theme()

	// Undo / retry / 审批按键。聚焦的文本输入优先接收普通按键，
	// applyTheme 把 app 的调色板安装为窗口主题，使控件（按钮、输入框、滚动条）遵循与我们自己的元素相同的设计。Undo / retry / 审批按键。聚焦的文本输入优先接收普通按键，因此 Y/N 仅在输入框未键入时才对审批起作用。导航网格。零值（测试/demo 使用）保留活动会话列表；其他每一项渲染自己的占位子面板而非会话栏。页脚状态点。
	if c.Shortcut(ui.Cmd, ui.KeyZ) {
		app.undo()
	}
	if c.Shortcut(ui.Cmd, ui.KeyY) {
		app.retry()
	}
	if app.approval != nil && !c.FocusTakesText() {
		switch {
		case c.Shortcut(0, ui.KeyY):
			app.answerApprovalChoice(allowOnceChoice())
		case c.Shortcut(0, ui.KeyA):
			app.answerApprovalChoice(allowAlwaysChoice())
		case c.Shortcut(0, ui.KeyN), c.Shortcut(0, ui.KeyEscape):
			app.answerApprovalChoice(denyChoice(app.approval.reason))
		}
	}

	ui.Row(c).Fill().Children(func() {
		app.sidebar(c, p)
		app.main(c, p, t)
	})
}

func (app *chat) sidebar(c *ui.Context, p palette) {
	ui.Column(c).Width(230).FillHeight().Background(p.sidebar).
		BorderWidth(0, 0, 0, 1).BorderColor(p.borderSubtle).
		Padding(16, 12, 12, 12).Gap(10).Children(func() {
		// Brand.
		ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(0, 6, 8, 6).Children(func() {
			ui.Text(c, "◆").FontSize(17).TextColor(p.accent)
			ui.Column(c).Gap(1).Children(func() {
				ui.Text(c, "argentgo").FontSize(19).Bold().TextColor(p.strong)
				ui.Text(c, "AGENT RUNTIME").FontSize(10).TextColor(p.accent)
			})
		})

		// 只保留聊天界面：侧边栏直接渲染会话列表，不再有导航网格与占位面板。
		app.sessionPanel(c, p)

		// 页脚状态点。
		ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(8, 6, 0, 6).
			BorderWidth(1, 0, 0, 0).BorderColor(p.borderSubtle).Children(func() {
			dot := p.ok
			label := tr("就绪")
			if app.streaming {
				dot, label = p.accent, tr("运行中")
			}
			ui.Box(c).Size(7, 7).Radius(4).Background(dot)
			ui.Text(c, label).FontSize(12).TextColor(p.dim)
		})
	})
}

// sessionPanel 渲染聊天导航项的侧边栏：会话搜索、
// 会话栏和新建会话按钮。首次附加真实 engine 时
// sessionPanel 渲染聊天导航项的侧边栏：会话搜索、会话栏和新建会话按钮。首次附加真实 engine 时它启动一次 session.list 获取；在该请求成功之前（或 demo 模式下）现有 fakeSessions 留在屏幕上，因此失败的 RPC 留下可用的会话栏而非空白。
// 模式）现有 fakeSessions 留在屏幕上，因此失败的 RPC 留下
// 可用的会话栏而非空白。
func (app *chat) sessionPanel(c *ui.Context, p palette) {
	app.ensureSessions()

	// 会话头部：标题 + 新建按钮。
	ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(4, 6, 0, 6).Children(func() {
		ui.Text(c, tr("项目与会话")).FontSize(12).Bold().TextColor(p.dim).Grow(1)
		if ui.Button(c, "＋").Clicked() {
			app.newSession()
		}
	})
	// 当前会话紫底高亮条：它是状态展示而非按钮，没有选中会话时不做假可点击。
	current := tr("当前会话")
	if app.selectedSession != "" {
		current += " · " + tr(app.selectedSession)
	}
	ui.Row(c).Padding(8, 10).Radius(10).Background(p.accentBG).Children(func() {
		ui.Text(c, current).FontSize(12).Bold().TextColor(p.accent).SingleLine()
	})
	ui.TextInput(c, &app.sessionQuery).Placeholder(tr("搜索会话")).
		Padding(8, 10).Radius(10).Background(p.element).FontSize(12)

	// 会话列表。成功获取替换 demo 行；失败（或
	// sessions 保持 nil 的 demo 模式）保留它们。
	rows := app.sessions
	if rows == nil {
		rows = fakeSessions
	}
	filtered := make([]session, 0, len(rows))
	q := strings.ToLower(strings.TrimSpace(app.sessionQuery))
	for _, s := range rows {
		if q == "" || strings.Contains(strings.ToLower(s.title+s.group), q) {
			filtered = append(filtered, s)
		}
	}
	app.sessionList.Key = func(i int) any {
		if filtered[i].id != "" {
			return filtered[i].id
		}
		return filtered[i].title
	}
	// 真实会话列表为空时给出空状态引导，避免"接上引擎反而一片空白"。
	// 仅在已取得真实列表（app.sessions != nil）且无匹配项时显示；demo 模式
	// 的 fakeSessions 永远非空，不会触发，帧字节不变。
	if app.sessions != nil && len(filtered) == 0 {
		emptyText := tr("还没有会话，点右上角 ＋ 开始对话")
		if q != "" {
			emptyText = tr("没有匹配的会话")
		}
		ui.Column(c).Grow(1).AlignItems(ui.Center).Padding(24, 12).Children(func() {
			ui.Text(c, emptyText).FontSize(12).TextColor(p.dim)
		})
		return
	}
	ui.List(c, &app.sessionList, len(filtered), func(i int) {
		s := filtered[i]
		// 内联重命名：该行处于编辑态时用输入框替换标题，Enter 提交、Esc 取消。
		if app.renamingSession != "" && s.id != "" && app.renamingSession == s.id {
			inp := ui.TextInput(c, &app.renameDraft).Padding(6, 8).Radius(6).FontSize(13)
			if inp.Submitted() {
				if strings.TrimSpace(app.renameDraft) == "" {
					app.cancelRename()
				} else {
					app.commitRename(s)
				}
			}
			app.branchNodes(c, p, s)
			return
		}
		row := ui.Row(c).Padding(8, 10).Radius(8)
		if app.isSessionSelected(s) {
			row.Background(p.accentBG)
		}
		row.Children(func() {
			ui.Column(c).Gap(2).Children(func() {
				ui.Text(c, tr(s.title)).FontSize(13).TextColor(p.text).SingleLine()
				ui.Text(c, tr(s.group)+" · "+tr(s.meta)).FontSize(11).TextColor(p.dim).SingleLine()
			})
		})
		// 右键菜单：删除 / 重命名。demo/降级行没有真实 id，不提供菜单。
		if s.id != "" {
			row.ContextMenu(func(m *ui.Menu) {
				if m.Item(tr("重命名")).Chosen() {
					app.beginRename(s)
				}
				m.Separator()
				if m.Item(tr("删除")).Danger().Chosen() {
					app.deleteSession(s)
				}
			})
		}
		if row.Clicked() {
			app.openSession(s)
		}
		// 分支节点是行的兄弟元素而非子元素：点击节点时命中按钮，
		// 不会同时触发行本身的 openSession。没有分叉时不构建任何元素，
		// 单线/无 branch 的帧与旧版逐字节一致。
		app.branchNodes(c, p, s)
	}).Grow(1)
}

// 会话头部和搜索。会话列表。成功获取替换 demo 行；失败（或 sessions 保持 nil 的 demo 模式）保留它们。isSessionSelected 在行有真实 id 时按 id 比较，使共享生成标题的两个会话不会都显得被选中。Demo 行回退到 title，那是它们在该模式下的身份。
// 共享生成标题的两个会话不会都显得被选中。Demo 行回退到
// title，那是它们在该模式下的身份。
func (app *chat) isSessionSelected(s session) bool {
	if s.id != "" {
		return app.selectedSession == s.id
	}
	return app.selectedSession == s.title
}

// sessionRowID 返回会话栏行在 RPC 中使用的 stem。demo 行没有 id，回退到 title。
func sessionRowID(s session) string {
	if s.id != "" {
		return s.id
	}
	return s.title
}

// beginRename 进入某会话行的内联改名态，预填当前标题。
func (app *chat) beginRename(s session) {
	app.renamingSession = s.id
	app.renameDraft = tr(s.title)
}

// cancelRename 退出改名态，不改动引擎或列表。
func (app *chat) cancelRename() {
	app.renamingSession = ""
	app.renameDraft = ""
}

// commitRename 把编辑后的标题提交到引擎（session.rename），成功后更新本地行。
func (app *chat) commitRename(s session) {
	title := strings.TrimSpace(app.renameDraft)
	if title == "" {
		app.cancelRename()
		return
	}
	app.renamingSession = ""
	app.renameDraft = ""
	// 先本地更新，UI 立即反馈；RPC 失败再回写错误状态。
	app.setSessionTitle(s.id, title)
	app.runSessionRPC("session.rename", map[string]any{"id": s.id, "title": title}, func(err error) {
		if err != nil {
			app.status = tr("重命名失败: ") + err.Error()
		}
	})
}

// deleteSession 删除一个会话（session.delete），成功后从列表移除。
func (app *chat) deleteSession(s session) {
	if s.id == "" {
		return
	}
	app.runSessionRPC("session.delete", map[string]any{"id": s.id}, func(err error) {
		if err != nil {
			app.status = tr("删除失败: ") + err.Error()
			return
		}
		app.removeSessionRow(s.id)
		app.status = tr("已删除会话")
	})
}

// runSessionRPC 在后台线程执行一次会话相关 RPC，并在 UI 线程回调结果。
// 无窗口（测试）时内联执行，行为可预测。
func (app *chat) runSessionRPC(method string, params map[string]any, done func(error)) {
	eng := app.engine
	if eng == nil {
		done(nil)
		return
	}
	if app.win == nil {
		_, err := eng.call(method, params)
		done(err)
		return
	}
	win := app.win
	go func() {
		_, err := eng.call(method, params)
		win.Update(func() { done(err) })
	}()
}

// setSessionTitle 就地更新本地会话行的标题。
func (app *chat) setSessionTitle(id, title string) {
	for i := range app.sessions {
		if app.sessions[i].id == id {
			app.sessions[i].title = title
			return
		}
	}
}

// removeSessionRow 从本地会话列表移除一行；若它正是当前选中的会话，清空选择。
func (app *chat) removeSessionRow(id string) {
	out := app.sessions[:0]
	for _, s := range app.sessions {
		if s.id == id {
			continue
		}
		out = append(out, s)
	}
	app.sessions = out
	if app.selectedSession == id {
		app.selectedSession = ""
	}
}

// branchNodes 在会话行下渲染分支节点。只有树里出现真正分叉（任一节点
// Fork=true）时才构建元素；单线或空树下一帧与旧版逐字节一致。
// 节点可点击：点击后用 {"id":stem,"branch":recordID} 重新加载该分支转录。
func (app *chat) branchNodes(c *ui.Context, p palette, s session) {
	if app.branchSession == "" || app.branchSession != sessionRowID(s) || !hasForkBranches(app.branches) {
		return
	}
	ui.Column(c).Gap(2).Padding(6, 10, 2, 26).Children(func() {
		for _, b := range app.branches {
			if !b.Fork && !b.Active {
				continue
			}
			label := branchNodeLabel(b)
			if ui.Button(c, label).Padding(4, 6).Radius(6).
				FontSize(11).Background(ui.Transparent).TextColor(p.dim).Clicked() {
				app.openSessionBranch(s, b.ID)
			}
		}
	})
}

// hasForkBranches 报告会话树是否存在真正的分叉点。
// session.tree 把父节点有多个孩子的所有孩子都标为 Fork，因此单线树
// （每个父节点至多一个孩子）没有任何 Fork 节点。
func hasForkBranches(branches []SessionBranch) bool {
	for _, b := range branches {
		if b.Fork {
			return true
		}
	}
	return false
}

// branchNodeLabel 生成分支节点的显示文字：按深度缩进，分叉和活动叶子
// 分别带 ⑂ / ● 字形，空标签回退到 Kind。
func branchNodeLabel(b SessionBranch) string {
	var out strings.Builder
	out.WriteString(strings.Repeat("  ", max(b.Depth, 0)))
	if b.Fork {
		out.WriteString("⑂ ")
	}
	if b.Active {
		out.WriteString("● ")
	}
	label := b.Label
	if label == "" {
		label = b.Kind
	}
	out.WriteString(label)
	return out.String()
}

// parseSessionTree 解码 session.tree 的 {"branches":[...]} payload。
func parseSessionTree(raw json.RawMessage) ([]SessionBranch, error) {
	var payload struct {
		Branches []SessionBranch `json:"branches"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	return payload.Branches, nil
}

// fetchSessionTree 请求当前会话的分支树并更新侧边栏。engine 为 nil（demo）
// 时不发 RPC；失败或无法解析时保留旧 branches/branchSession，让旧节点存活，
// 而不是把会话栏清空。
func (app *chat) fetchSessionTree(sessionID string) {
	if app.engine == nil {
		return
	}
	call := app.sessionTreeRPC
	if call == nil {
		call = app.engine.call
	}
	raw, err := call("session.tree", map[string]any{"id": sessionID})
	if err != nil {
		return
	}
	branches, err := parseSessionTree(raw)
	if err != nil {
		return
	}
	app.branches = branches
	app.branchSession = sessionID
}

// ensureSessions 在附加真实 engine 时启动一次性 session.list 获取。
// 有窗口时它在后台线程运行并在 UI 线程应用结果
// （像提示回复那样）；无窗口时（测试）它内联应用，使
// 第一帧就显示真实会话栏。
func (app *chat) ensureSessions() {
	if app.engine == nil || app.sessionListLoaded {
		return
	}
	app.sessionListLoaded = true
	if app.win == nil {
		sessions, err := app.fetchSessions()
		app.applySessions(sessions, err)
		return
	}
	win := app.win
	go func() {
		sessions, err := app.fetchSessions()
		win.Update(func() { app.applySessions(sessions, err) })
	}()
}

// ensureSessions 在附加真实 engine 时启动一次性 session.list 获取。有窗口时它在后台线程运行并在 UI 线程应用结果（像提示回复那样）；无窗口时（测试）它内联应用，使第一帧就显示真实会话栏。fetchSessions 调用 session.list RPC 并解码其 {"sessions":[...]} payload。RPC 错误或无法解析的 payload 返回给调用方，后者降级到现有会话栏而非清空它。
// payload。RPC 错误或无法解析的 payload 返回给调用方，
// 后者降级到现有会话栏而非清空它。
func (app *chat) fetchSessions() ([]session, error) {
	call := app.sessionListRPC
	if call == nil {
		call = app.engine.call
	}
	raw, err := call("session.list", nil)
	if err != nil {
		return nil, err
	}
	return parseSessionList(raw)
}

// 它在线上的形态。把类型保留在这里避免把 internal/
// 导入 native/，后者保持为依赖轻量前端。
type engineSession struct {
	ID      string    `json:"id"`
	Title   string    `json:"title,omitempty"`
	Updated time.Time `json:"updated"`
}

// parseSessionList 把 RPC payload 映射为会话栏行。没有
// 标题的会话按其 id 显示；时间戳提供分组/元信息标签。
func parseSessionList(raw json.RawMessage) ([]session, error) {
	var payload struct {
		Sessions []engineSession `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	out := make([]session, 0, len(payload.Sessions))
	for _, info := range payload.Sessions {
		title := info.Title
		if title == "" {
			title = info.ID
		}
		out = append(out, session{
			id:    info.ID,
			title: title,
			meta:  sessionMeta(info.Updated),
			group: sessionGroup(info.Updated),
		})
	}
	return out, nil
}

// applySessions 安装成功的获取。失败让 sessions 保持原样
// （使当前会话栏存活）并报告脱敏、单行的
// 通知；它从不使视图崩溃。
func (app *chat) applySessions(sessions []session, err error) {
	if err != nil {
		app.status = tr("会话列表加载失败：") + engineErrorNotice(err)
		return
	}
	app.sessions = sessions
}

// configInfoSnapshot 是状态栏所需的配置快照，只保留三个字段。
// 它对应 server 的 config.info RPC 返回，但 native 不 import internal/：
// 这里只发 JSON-RPC 请求，不编译期依赖 server 的实现（由另一变更落盘）。
type configInfoSnapshot struct {
	model     string
	provider  string
	workspace string
}

// ensureConfigInfo 在附加真实 engine 时启动一次性 config.info 获取。
// 它在启动路径（window 尚未创建）被调用，内联应用结果，使首帧
// 就能显示真实状态栏；若已有窗口则在后台线程获取并在 UI 线程应用。
// engine 为 nil（demo）时不发送任何 RPC。
func (app *chat) ensureConfigInfo() {
	if app.engine == nil || app.configInfoLoaded {
		return
	}
	app.configInfoLoaded = true
	if app.win == nil {
		info, err := app.fetchConfigInfo()
		app.applyConfigInfo(info, err)
		return
	}
	win := app.win
	go func() {
		info, err := app.fetchConfigInfo()
		win.Update(func() { app.applyConfigInfo(info, err) })
	}()
}

// fetchConfigInfo 调用 config.info RPC 并解码其
// {"model":...,"provider":...,"workspace":...} payload。RPC 由 server 侧
// 提供（另一变更落盘），native 只发 JSON-RPC 请求，没有编译期依赖。
// RPC 错误或无法解析的 payload 返回给调用方，后者回退到 demo 占位串。
func (app *chat) fetchConfigInfo() (configInfoSnapshot, error) {
	call := app.configInfoRPC
	if call == nil {
		call = app.engine.call
	}
	raw, err := call("config.info", map[string]any{})
	if err != nil {
		return configInfoSnapshot{}, err
	}
	var payload struct {
		Model     string `json:"model"`
		Provider  string `json:"provider"`
		Workspace string `json:"workspace"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return configInfoSnapshot{}, err
	}
	return configInfoSnapshot{
		model:     payload.Model,
		provider:  payload.Provider,
		workspace: payload.Workspace,
	}, nil
}

// applyConfigInfo 安装成功的获取。失败让 configInfo 保持零值
// （状态栏随后回退到 demo 占位串）；它从不使视图崩溃。
func (app *chat) applyConfigInfo(info configInfoSnapshot, err error) {
	if err != nil {
		return
	}
	app.configInfo = info
}

// demoStatusInfo 是 config.info 未加载或失败时状态栏的占位串，
// 逐字节保持与原硬编码一致。
const demoStatusInfo = "gpt-4o-mini · ~/my-project"

// configInfoLabel 返回状态栏右侧显示的模型/工作区文字。
// 未获取或获取失败（model/workspace 为空）时回退到 demo 占位串；
// workspace 可能已由 server 脱敏，这里原样显示。
func (app *chat) configInfoLabel() string {
	info := app.configInfo
	if info.model == "" || info.workspace == "" {
		return demoStatusInfo
	}
	return info.model + " · " + info.workspace
}

// headerModel / headerWorkspace 返回顶栏显示的真实模型与工作区。
// 尚未取得 config.info（demo 或未连接引擎）时回退到占位串，避免顶栏
// 一边说"未连接"一边又展示一个假模型名；拿到真实值后顶栏立即反映。
func (app *chat) headerModel() string {
	if app.configInfo.model == "" {
		return "gpt-4o-mini"
	}
	return app.configInfo.model
}

func (app *chat) headerWorkspace() string {
	if app.configInfo.workspace == "" {
		return "~/my-project"
	}
	return app.configInfo.workspace
}

// workspaceBreadcrumb 把工作区路径转成顶栏面包屑（如 "D: / CODE / MPI"）。
// 只做分隔符替换与大写，不发明路径信息；空路径返回空串。
func workspaceBreadcrumb(ws string) string {
	if ws == "" {
		return ""
	}
	parts := strings.FieldsFunc(ws, func(r rune) bool { return r == '/' || r == '\\' })
	for i, p := range parts {
		parts[i] = strings.ToUpper(p)
	}
	return strings.Join(parts, " / ")
}

// headerSessionTitle 返回顶栏的会话标题：优先真实选中的会话，
// 否则用已加载列表的第一条，最后回退到“新会话”（不冒充某条 demo 会话）。
func (app *chat) headerSessionTitle() string {
	if app.selectedSession != "" {
		return tr(app.selectedSession)
	}
	if len(app.sessions) > 0 {
		return tr(app.sessions[0].title)
	}
	return tr("新会话")
}

// sessionGroup 按会话更新的日期分组，匹配
// 今天/昨天/更早 groups of the demo rail.
func sessionGroup(updated time.Time) string {
	if updated.IsZero() {
		return tr("更早")
	}
	now := time.Now()
	if sameDay(now, updated) {
		return tr("今天")
	}
	if sameDay(now.Add(-24*time.Hour), updated) {
		return tr("昨天")
	}
	return tr("更早")
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// sessionMeta 为会话行渲染简短相对时间。
func sessionMeta(updated time.Time) string {
	if updated.IsZero() {
		return ""
	}
	d := time.Since(updated)
	switch {
	case d < time.Minute:
		return tr("刚刚")
	case d < time.Hour:
		return fmt.Sprintf(tr("%d 分钟前"), int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf(tr("%d 小时前"), int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf(tr("%d 天前"), int(d.Hours()/24))
	default:
		return updated.Format("01-02")
	}
}

// openSession 选中某会话行并通过现有 session.load RPC 请求真实 engine
// 加载它。id 在可用时来自 session.list；demo/降级行没有 id 并回退到
// title（它们在该模式下的身份）。没有附加 bridge 时它保持原有的纯本地切换。
func (app *chat) openSession(s session) {
	app.openSessionBranch(s, "")
}

// openSessionBranch 加载会话（可选地裁剪到某条分支）。branch 非空时，
// session.load 收到 {"id":stem,"branch":recordID}；返回的事件被映射为
// 现有 transcript 行，复用 ui.List + app.message，不新建视图。
// 范围仅限“查看分支转录”：不提供在分支上续跑的入口。
func (app *chat) openSessionBranch(s session, branch string) {
	id := s.id
	if id == "" {
		id = s.title
	}
	app.selectedSession = id
	app.status = tr("已打开会话：") + s.title
	if app.engine == nil {
		return
	}
	call := app.sessionLoadRPC
	if call == nil {
		call = app.engine.call
	}
	params := map[string]any{"id": id}
	if branch != "" {
		params["branch"] = branch
	}
	raw, err := call("session.load", params)
	if err != nil {
		app.status = tr("打开会话失败：") + engineErrorNotice(err)
		return
	}
	events, ok := parseSessionEvents(raw)
	if !ok {
		app.status = tr("打开会话失败：无法解析会话记录")
		return
	}
	// 清空陈旧流式/工具/审批状态（仿 newSession），再填入映射消息。
	app.loadTranscript(events)
	// 分支树跟随 transcript：仅在切换到新会话且确实加载出记录时获取，
	// 避免点击分支节点造成重复 RPC，也让空会话/降级加载不触发额外请求。
	// 失败时 fetchSessionTree 保留旧树。
	if len(events) > 0 && app.branchSession != id {
		app.fetchSessionTree(id)
	}
}

// parseSessionEvents 解码 session.load 的 {"id":...,"events":[...]} payload。
// 第二个返回值为 false 表示 payload 无法解析，此时调用方保留旧 transcript。
func parseSessionEvents(raw json.RawMessage) ([]sessionEvent, bool) {
	var payload struct {
		Events []sessionEvent `json:"events"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return nil, false
	}
	return payload.Events, true
}

// loadTranscript 用 session.load 的事件替换 transcript。它先退役可能仍在
// 进行的流式轮次并清空 reply/tools/approval，使迟到结果不会被误混入；然后
// 把每条可识别事件映射为 app.message，继续由既有 ui.List 渲染。同一批事件
// 中的 todo 工具记录也用于刷新"计划"面板（最后一次写入即当前计划）。
func (app *chat) loadTranscript(events []sessionEvent) {
	app.retireStream()
	app.messages = nil
	app.tools = nil
	app.approval = nil
	app.streaming = false
	app.reply, app.tool = nil, nil
	app.lastPrompt = ""
	app.planTodos = nil
	for _, ev := range events {
		if ev.Kind == "tool" {
			if d, ok := decodeSessionEventData(ev.Data); ok && d.Name == "todo" {
				if todos := parsePlanTodos(d.Arguments); len(todos) > 0 {
					app.planTodos = todos
				}
			}
		}
		m, ok := messageFromSessionEvent(ev)
		if !ok {
			continue
		}
		app.next++
		m.id = app.next
		app.messages = append(app.messages, m)
	}
}

// messageFromSessionEvent 按 Kind 判定角色，并从 data 的 content/text/output
// 提取正文。未知 Kind 被忽略，避免把 header/attempt 等元数据塞进消息列表。
func messageFromSessionEvent(ev sessionEvent) (*message, bool) {
	d, ok := decodeSessionEventData(ev.Data)
	if !ok {
		return nil, false
	}
	role := ev.Kind
	if role == "message" {
		role = d.Role // 旧格式把角色放在 data.role 里
	}
	switch role {
	case "user":
		return &message{kind: msgUser, text: d.Content, done: true}, true
	case "assistant":
		return &message{kind: msgAssistant, text: d.Content, done: true}, true
	case "tool":
		status := "done"
		if d.IsError {
			status = "failed"
		}
		return &message{kind: msgTool, name: d.Name, text: d.Content, status: status, done: true}, true
	default:
		return nil, false
	}
}

// decodeSessionEventData 同时接受 data 为 JSON 字符串（早期格式）和对象。
// 对象按 content → text → output 的优先级取正文，工具名单独取自 name。
func decodeSessionEventData(raw json.RawMessage) (sessionEventData, bool) {
	if len(raw) == 0 {
		return sessionEventData{}, false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return sessionEventData{Content: s}, true
	}
	var d sessionEventData
	if json.Unmarshal(raw, &d) != nil {
		return sessionEventData{}, false
	}
	if d.Content == "" {
		d.Content = d.Text
	}
	if d.Content == "" {
		d.Content = d.Output
	}
	return d, true
}

// welcome 渲染空会话的欢迎区：横向居中、固定 560 宽的列，避免内容溢出
// 到窗口之外（此前放在列表 children 里时 MaxWidth/Margin auto 不生效）。
func (app *chat) welcome(c *ui.Context, p palette) {
	// 左对齐 + 限宽换行：长文本与按钮在 760 内换行，绝不撑出窗口右边界
	// （MyGo 的 Text 默认不换行会把容器撑宽，导致内容溢出到屏幕外）。
	ui.Column(c).Grow(1).FillWidth().Padding(40, 40).Gap(12).
		Justify(ui.Center).AlignItems(ui.Start).Children(func() {
		ui.Text(c, "◆").FontSize(22).TextColor(p.accent)
		ui.Text(c, tr("有什么可以帮你？")).FontSize(28).Bold().TextColor(p.strong)
		ui.Text(c, tr("问点什么，或者输入 / 看命令。")).FontSize(14).TextColor(p.dim).
			MaxWidth(760).Wrap()
		if app.engine == nil {
			ui.Text(c, tr("演示模式：未连接到引擎，回复与会话为示例数据，不会保存。")).
				FontSize(12).TextColor(p.warn).MaxWidth(760).Wrap()
			ui.Text(c, tr("请从与引擎相同的发布包启动 argentgo-native，或设置 ARGENT_EXE。")).
				FontSize(12).TextColor(p.dim).MaxWidth(760).Wrap()
		} else {
			ui.Text(c, tr("若尚未配置模型凭据，可运行 argentgo login 或在设置中填写。")).
				FontSize(12).TextColor(p.dim).MaxWidth(760).Wrap()
		}
		ui.Text(c, tr("试试这样问：")).FontSize(12).Bold().TextColor(p.dim)
		ui.Row(c).Gap(8).Wrap().MaxWidth(760).Children(func() {
			for _, prompt := range examplePrompts {
				s := prompt
				if ui.Button(c, tr(s)).Padding(6, 12).Clicked() {
					app.draft = s + " "
				}
			}
		})
		ui.Row(c).Gap(8).Wrap().MaxWidth(760).Children(func() {
			for _, action := range quickActions {
				a := action
				if ui.Button(c, tr(a)).Padding(6, 12).Clicked() {
					if strings.HasPrefix(a, "/ ") {
						app.draft = "/ "
					} else {
						app.draft = a + " "
					}
				}
			}
		})
	})
}

func (app *chat) main(c *ui.Context, p palette, t *ui.Theme) {
	_ = t // theme colors already installed through c.SetTheme
	// 在构建列表之前读取滚动位置（读到的是上一帧的布局结果）。
	// 列表未布局时 Visible 返回 last=-1，避免首帧误显示按钮。
	_, lastVisible := app.list.Visible()
	showJump := lastVisible >= 0 && !app.list.AtEnd()
	ui.Column(c).Grow(1).FillHeight().Children(func() {
		// 顶栏：面包屑 + 会话标题 + 状态徽章 + 操作按钮。
		ui.Row(c).Height(56).Padding(8, 20).Gap(12).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(p.borderSubtle).Children(func() {
			ui.Column(c).Gap(1).Children(func() {
				ui.Text(c, workspaceBreadcrumb(app.headerWorkspace())).
					FontSize(10).TextColor(p.dim).SingleLine()
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, app.headerSessionTitle()).FontSize(16).Bold().TextColor(p.strong)
					ui.Text(c, app.headerWorkspace()).FontSize(11).TextColor(p.dim)
				})
			})
			ui.Spacer(c)
			// 状态徽章：运行中黄底，就绪紫底。
			badgeText, badgeBG, badgeLine, badgeTextColor := tr("就绪"), p.accentBG, p.accentLine, p.accent
			if app.streaming {
				count := app.usage.calls
				if count < 1 {
					count = 1
				}
				badgeText = fmt.Sprintf("%s · %d", tr("运行中"), count)
				badgeBG, badgeLine, badgeTextColor = p.warnBG, p.warnLine, p.warnText
			}
			ui.Text(c, badgeText).FontSize(11).TextColor(badgeTextColor).
				Padding(4, 10).Radius(999).Background(badgeBG).
				BorderWidth(1, 1, 1, 1).BorderColor(badgeLine)
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				if ui.Button(c, tr("重试")).Padding(5, 10).Radius(8).Clicked() {
					app.retry()
				}
				if ui.Button(c, tr("主题")).Padding(5, 10).Radius(8).Clicked() {
					app.toggleTheme()
				}
			})
		})

		// 演示模式持久横幅：未连接引擎时始终可见，避免用户对着假数据工作数分钟
		// 才发现一切不会保存。真实引擎接入后整段不渲染，帧字节不变。
		if app.engine == nil {
			ui.Row(c).Padding(8, 20).Background(p.warnBG).
				BorderWidth(0, 0, 1, 0).BorderColor(p.warnLine).Children(func() {
				ui.Text(c, tr("演示模式 · 未连接到引擎 · 回复与会话不会保存")).
					FontSize(12).TextColor(p.warnText)
			})
		}

		// 审批栏：醒目黄色卡片，左右留白使其像悬浮卡片。
		if app.approval != nil {
			ui.Column(c).Padding(0, 16, 8, 16).Children(func() {
				app.approvalView(c, p)
			})
		}

		// Transcript.
		app.list.Key = func(i int) any { return app.messages[i].id }
		app.list.FollowEnd = true
		if len(app.messages) == 0 {
			app.welcome(c, p)
		} else {
			ui.List(c, &app.list, len(app.messages), func(i int) {
				app.message(c, app.messages[i], p)
			}).Grow(1).Padding(16, 20).Gap(14).Justify(ui.End)
		}

		// 状态行。
		ui.Row(c).Height(30).Padding(0, 20).Gap(8).AlignItems(ui.Center).Children(func() {
			dot := p.ok
			if app.streaming {
				dot = p.accent
			}
			if app.engineDisconnected {
				dot = p.danger
			}
			ui.Box(c).Size(6, 6).Radius(3).Background(dot)
			ui.Text(c, app.status).FontSize(12).TextColor(p.dim)
			ui.Spacer(c)
			// 向上滚动离开末尾后给出可见的“跳到最新”操作。
			if showJump {
				if ui.Button(c, tr("↓ 跳到最新")).Padding(3, 10).Radius(999).
					Background(p.accentBG).TextColor(p.accent).FontSize(11).Clicked() {
					app.list.ScrollToEnd()
				}
			}
			ui.Text(c, app.configInfoLabel()).FontSize(11).TextColor(p.dim)
		})

		// 输入区：大圆角卡片 + 模型/上下文信息 + 紫色发送按钮。
		ui.Column(c).Padding(10, 20, 14, 20).Children(func() {
			ui.Column(c).Padding(12, 14, 10, 14).Gap(8).Background(p.surface).
				BorderWidth(1, 1, 1, 1).BorderColor(p.border).Radius(12).Children(func() {
				in := ui.TextArea(c, &app.draft).Lines(1, 5).AutoFocus().
					Placeholder(tr("输入任务或提问…")).Grow(1)
				in.OnTextKey(func(k ui.TextKey) bool {
					if k.Key == ui.KeyEnter && k.Mods == 0 {
						app.send(k.Text())
						k.SetText("")
						return true
					}
					return false // Shift+Enter inserts a newline
				})
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, tr("模型")).FontSize(11).TextColor(p.dim)
					ui.Text(c, app.headerModel()+" · "+tr("上下文")).
						FontSize(11).TextColor(p.dim).Grow(1)
					ui.Text(c, tr("Enter 发送 · Shift+Enter 换行")).FontSize(11).TextColor(p.dim)
					if app.streaming {
						if ui.Button(c, tr("停止")).Clicked() {
							app.stop()
						}
					} else if ui.PrimaryButton(c, tr("发送")).Clicked() {
						app.send(app.draft)
					}
				})
			})
		})
	})
}

func (app *chat) approvalView(c *ui.Context, p palette) {
	ap := app.approval
	dangerous := approvalDefaultIsDeny(ap.name, ap.args)
	ui.Column(c).Padding(12, 14).Gap(8).Background(p.warnBG).
		BorderWidth(1, 1, 1, 1).BorderColor(p.warnLine).Radius(12).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, tr("未确认工具调用")).Bold().TextColor(p.warnText)
			ui.Text(c, tr("需要批准")).FontSize(11).TextColor(p.onAccent).
				Padding(2, 8).Radius(999).Background(p.warnLine)
			ui.Text(c, ap.name).FontSize(12).Bold().TextColor(p.text)
			ui.Spacer(c)
			// 上下文相关的按键提示（不恢复全局底部 hint bar）。
			ui.Text(c, tr("Y 允许一次 · A 始终允许 · N 拒绝")).FontSize(11).TextColor(p.warnText)
		})
		ui.Text(c, ap.args).FontSize(12).TextColor(p.text).
			Padding(8, 10).Radius(8).Background(p.surface).Selectable()
		// diff 默认开启（=0 回退）：摘要 + 预览两行给出概览，逐行 diff 给出细节，
		// 让审批从"盲签"变成"看一眼再签"。无数据时整段跳过，帧字节不变。
		if approvalDiffVisible() {
			if summary := buildApprovalSummary(ap.name, ap.args); !summary.empty() {
				ui.Text(c, renderApprovalSummary(summary)).FontSize(11).TextColor(p.warnText)
				ui.Text(c, summary.Preview).FontSize(11).TextColor(p.dim).Selectable()
			}
			// 逐行 diff：编辑类工具展示真正改了什么（+/- 与上下文）。
			if files := buildApprovalLineDiff(ap.name, ap.args); len(files) > 0 {
				ui.Text(c, renderApprovalLineDiff(files)).FontSize(11).TextColor(p.text).
					Padding(8, 10).Radius(8).Background(p.surface).Selectable()
			}
		}
		// 拒绝理由（可选）：用户可写一句让模型改方案，随 RPC 透传供审计。
		ui.TextInput(c, &ap.reason).Placeholder(tr("拒绝理由（可选，例如：别删 dist，改删 build/）"))
		// 作用域说明独占一行（诚实描述授权范围），按钮保持简短精确文案。
		ui.Text(c, allowOnceChoice().scopeNote()+" · "+allowAlwaysChoice().scopeNote()).
			FontSize(11).TextColor(p.dim)
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			// 危险请求默认焦点落在"拒绝"（对齐 Claude Code / crush 范式）。
			if dangerous {
				if ui.PrimaryButton(c, tr("拒绝")).Clicked() {
					app.answerApprovalChoice(denyChoice(ap.reason))
				}
				if ui.Button(c, tr("允许")).Clicked() {
					app.answerApprovalChoice(allowOnceChoice())
				}
			} else {
				if ui.Button(c, tr("拒绝")).Clicked() {
					app.answerApprovalChoice(denyChoice(ap.reason))
				}
				if ui.PrimaryButton(c, tr("允许")).Clicked() {
					app.answerApprovalChoice(allowOnceChoice())
				}
			}
			// 始终允许需二次确认，文案诚实描述精确作用域。
			always := allowAlwaysChoice()
			label := tr("始终允许此命令")
			if ap.confirmAlways {
				label = tr("确认始终允许")
			}
			if ui.Button(c, label).Clicked() {
				if ap.confirmAlways {
					app.answerApprovalChoice(always)
				} else {
					ap.confirmAlways = true
				}
			}
		})
	})
}

// messageActions 渲染消息行底部的轻量操作条：复制原文、重发该回合。
// 操作项是行内兄弟元素，点击它们不会触发外层可点击的工具卡片。
func (app *chat) messageActions(c *ui.Context, m *message, p palette, end bool) {
	actions := ui.Row(c).Gap(10)
	if end {
		actions.Justify(ui.End)
	}
	actions.Children(func() {
		if ui.Button(c, tr("复制")).Padding(3, 8).Radius(6).FontSize(11).
			Background(ui.Transparent).TextColor(p.dim).Clicked() {
			app.copyText(m.text, tr("已复制消息"))
		}
		if ui.Button(c, tr("重试")).Padding(3, 8).Radius(6).FontSize(11).
			Background(ui.Transparent).TextColor(p.dim).Clicked() {
			app.retryMessage(m)
		}
	})
}

// message 构建一条 transcript 行：右对齐的用户气泡、可展开的
// 工具卡片带状态字形，或助手 Markdown 视图。
func (app *chat) message(c *ui.Context, m *message, p palette) {
	switch m.kind {
	case msgUser:
		// 左对齐紫色气泡（与助手同侧，浅紫底 + 细紫边 + 12px 圆角）。
		ui.Column(c).MaxWidth(520).Radius(12).Background(p.accentBG).
			BorderWidth(1, 1, 1, 1).BorderColor(p.accentLine).
			Padding(10, 14).Gap(4).Children(func() {
			ui.Text(c, tr("你")).FontSize(11).Bold().TextColor(p.accent)
			// 用户消息也走 Markdown：多行代码/日志在气泡里同样被格式化。
			markdown.View(c, m.text, &app.md)
			app.messageActions(c, m, p, true)
		})
	case msgTool:
		glyph, color := "⋯", p.warn
		switch m.status {
		case "done":
			glyph, color = "✓", p.ok
		case "failed":
			glyph, color = "✗", p.danger
		}
		// 紧凑单行工具行：状态图标 + 工具名 + 参数摘要 + 右侧耗时。
		// 整行可点击展开完整输出（已实现能力保留）。
		ui.Column(c).Radius(10).Background(p.element).
			BorderWidth(1, 1, 1, 1).BorderColor(p.borderSubtle).Children(func() {
			chevron := "▸"
			if m.expanded {
				chevron = "▾"
			}
			header := ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(7, 12).Children(func() {
				ui.Text(c, chevron).FontSize(10).TextColor(p.dim)
				ui.Text(c, glyph).Bold().TextColor(color)
				ui.Text(c, m.name).FontSize(12).Bold().TextColor(p.text)
				ui.Text(c, toolCardSummary(m)).FontSize(12).TextColor(p.dim).
					SingleLine().Grow(1)
				if d := formatDuration(m.durationMS); d != "" {
					ui.Text(c, d).FontSize(11).TextColor(p.dim)
				}
			})
			if header.Clicked() {
				m.expanded = !m.expanded
			}
			if m.expanded {
				ui.Column(c).Padding(0, 12, 10, 30).Gap(6).Children(func() {
					out := m.text
					if strings.TrimSpace(out) == "" {
						out = tr("(无输出)")
					}
					// 完整输出含 stderr/diff，用等宽字体保留对齐并允许选中复制。
					ui.Text(c, out).FontSize(11).Font("Consolas").
						TextColor(p.text).Selectable().Wrap()
					if ui.Button(c, tr("复制输出")).Padding(3, 8).Radius(6).FontSize(11).
						Background(ui.Transparent).TextColor(p.dim).Clicked() {
						app.copyText(m.text, tr("已复制工具输出"))
					}
				})
			}
		})
	default:
		// 助手消息：左对齐卡片 + 细边框 + 10px 圆角。
		ui.Column(c).MaxWidth(720).Radius(10).Background(p.surface).
			BorderWidth(1, 1, 1, 1).BorderColor(p.borderSubtle).
			Padding(12, 14).Gap(6).Children(func() {
			ui.Text(c, "argentgo").FontSize(11).Bold().TextColor(p.accent)
			if m.done {
				markdown.View(c, m.text, &app.md)
				app.messageActions(c, m, p, false)
			} else {
				markdown.Stream(c, m.text, &app.md)
			}
		})
	}
}

// send 追加用户消息并驱动回复。附加真实 engine 时
// 否则保持预设 Markdown 回退（测试，或无 engine）。
func (app *chat) send(text string) {
	text = strings.TrimSpace(text)
	if text == "" || app.streaming {
		return
	}
	app.lastPrompt = text
	app.next++
	app.messages = append(app.messages, &message{id: app.next, kind: msgUser, text: text, done: true})

	app.next++
	reply := &message{id: app.next, kind: msgAssistant}
	app.messages = append(app.messages, reply)

	// 工具行只在回退 demo 中显示：stdio RPC 协议
	// 携带提示文本和 deltas，而非工具事件。
	var tool *message
	if app.engine == nil {
		app.next++
		tool = &message{id: app.next, kind: msgTool, name: "read_file", text: "~/my-project/src/main.go", status: "running"}
		app.messages = append(app.messages, tool)
	}

	app.draft = ""
	app.sent++
	app.tool, app.reply = tool, reply
	app.replySplit = false
	app.streaming = true
	app.status = tr("正在生成…")

	if app.engine != nil {
		app.sendReal(text, reply)
		return
	}

	src := localizedReply((app.sent - 1) % len(cannedReplies))
	if app.win == nil || app.instant {
		app.finish(src)
		return
	}
	go func() {
		for i := 0; i < len(src); {
			n := min(len(src)-i, 2+rand.IntN(6))
			for n < len(src)-i && src[i+n]&0xC0 == 0x80 {
				n++
			}
			i += n
			chunk := src[:i]
			time.Sleep(12 * time.Millisecond)
			stopped := false
			app.win.Update(func() {
				if reply.done {
					stopped = true
					return
				}
				reply.text = chunk
			})
			if stopped {
				return
			}
		}
		app.win.Update(func() { app.finish(src) })
	}()
}

// isCancelError 报告 engine 错误是否是用户中止而非
// 真实失败。在服务端统一之后，取消作为普通 stopReason 到达
// （因此 err 为 nil）；字符串检查在较旧 engine 仍发送 "context canceled"
func isCancelError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "context canceled") || strings.Contains(msg, "cancelled")
}

// finishText 为非失败的提示挑选传给 finish() 的文本。
// 被取消的运行可能带回空结果；擦掉用户已经
// 看着流进来的答案比什么都不显示更糟。
func finishText(full string, err error, streamed string) string {
	if err != nil && !isCancelError(err) {
		return full
	}
	if streamed != "" && (full == "" || isCancelError(err)) {
		return streamed
	}
	return full
}

// completeFromPrompt 终结真实 engine 的回复。它是决定
// 哪些文本和状态在提示结果后存活的唯一位置，使真实的 engine
// 错误不会被 finish() 的（通常为空的）完整结果静默替换。
// 把决定保留在这里使其无需窗口或活 engine 即可测试。
func (app *chat) completeFromPrompt(streamed, full string, err error) {
	if err != nil && !isCancelError(err) {
		// 通过 finishEngineError 路由，使 provider 错误被脱敏、
		// 通过 finishEngineError 路由，使 provider 错误在进入 transcript 前被脱敏、折叠换行并限制 rune。取消不是错误：保留流式答案，永不显示错误通知。
		app.finishEngineError(err)
		return
	}
	// 取消不是错误：保留流式答案，永不显示错误通知。
	// the "引擎错误" banner for it.
	app.finish(finishText(full, err, streamed))
}

// 通过 Window.Update 编组回 UI 线程。
func (app *chat) sendReal(text string, reply *message) {
	eng := app.engine
	// 通过 bridge 的锁注册 delta 回调并捕获
	// 该轮的 generation。readLoop 在同一锁下快照回调，
	// 因此跨轮交换回调不会与实时 delta 竞争。只有
	// 完成 goroutine 读取 gen；它在下面的赋值之后
	// 启动，使该读取无竞态。
	gen := eng.setDeltaHandler(func(chunk string) {
		if app.win == nil {
			return
		}
		app.win.Update(func() {
			// 写到当前活动段：工具调用会把回复切分成多段，使转录按真实
			// 发生顺序交错显示（文本→工具→文本），而不是工具全堆在末尾。
			if app.reply != nil && !app.reply.done {
				app.reply.text += chunk
			}
		})
	})
	go func() {
		full, err := eng.prompt(text)
		// reply.text 只由 onDelta 在 UI 线程上写入，因此
		// 在 Update 回调中读取流式文本是安全的；无
		// 窗口时根本不应用 deltas。
		complete := func() { app.completeRealReply(eng, gen, reply, full, err) }
		if app.win == nil {
			// 测试从不附加真实 bridge，但仍保持安全。
			complete()
			return
		}
		app.win.Update(complete)
	}()
}

// completeRealReply 仅在 gen 仍是活动时终结真实 engine 提示。
// active streaming generation. A round that was superseded (the user hit 停止
// 并在被中止的提示返回前发送了新消息）绝不能运行
// finish()：finish 写 app.reply，而那时它属于更新轮。
func (app *chat) completeRealReply(eng *bridge, gen uint64, reply *message, full string, err error) {
	if eng != nil && !eng.isCurrentDeltaGen(gen) {
		return
	}
	app.completeFromPrompt(reply.text, full, err)
}

// ---- 真实 agent 事件 ----------------------------------------------------

// 审批通知搭载普通事件流：其 Text 携带稳定
// 本地解码前缀使 native/ 不含任何 internal/ 依赖。
const (
	approvalRequestPrefix  = "approval:request "
	approvalResolvedPrefix = "approval:resolved "
)

type engineApproval struct {
	V       int    `json:"v"`
	Tool    string `json:"tool"`
	Digest  string `json:"digest"`
	ID      string `json:"id"`
	Granted *bool  `json:"granted,omitempty"`
}

// parseEngineApproval 解码审批通知。对无关
// 通知、未知前缀和畸形 payload，ok 为 false，使事件流上的其他每一项
// 都能被安全忽略。
func parseEngineApproval(text string) (kind, tool, id, digest string, granted, ok bool) {
	switch {
	case strings.HasPrefix(text, approvalRequestPrefix):
		var p engineApproval
		if json.Unmarshal([]byte(strings.TrimPrefix(text, approvalRequestPrefix)), &p) != nil {
			return "", "", "", "", false, false
		}
		if p.V != 1 || p.Tool == "" || p.ID == "" {
			return "", "", "", "", false, false
		}
		return "request", p.Tool, p.ID, p.Digest, false, true
	case strings.HasPrefix(text, approvalResolvedPrefix):
		var p engineApproval
		if json.Unmarshal([]byte(strings.TrimPrefix(text, approvalResolvedPrefix)), &p) != nil {
			return "", "", "", "", false, false
		}
		if p.V != 1 || p.Tool == "" || p.ID == "" || p.Granted == nil {
			return "", "", "", "", false, false
		}
		return "resolved", p.Tool, p.ID, p.Digest, *p.Granted, true
	default:
		return "", "", "", "", false, false
	}
}

// 因此状态变更通过 Window.Update 编组。测试（无窗口）
// 直接应用事件，使映射保持可单元测试。
func (app *chat) onEngineEvent(kind string, raw json.RawMessage) {
	if app.win == nil {
		app.applyEngineEvent(kind, raw)
		return
	}
	app.win.Update(func() { app.applyEngineEvent(kind, raw) })
}

// onEngineClose 在 engine 连接断开时把状态栏切到断连态：状态文字改为
// "引擎连接已断开"，状态点转 danger 色。它由 bridge 的 readLoop
// goroutine 调用，因此状态变更必须经 Window.Update 编组到窗口线程；
// 窗口尚未就绪（engine 在 WhenReady 之前就断开）时直接应用。
func (app *chat) onEngineClose(err error) {
	apply := func() {
		app.engineDisconnected = true
		app.status = tr("引擎连接已断开")
	}
	if app.win == nil {
		apply()
		return
	}
	app.win.Update(apply)
}

// applyEngineEvent 把一个真实 agent 事件映射到现有 transcript
// 状态。工具与审批事件进入 transcript；usage 事件更新用量面板；
// todo 工具的结构化参数更新计划面板。助手文本已通过 prompt/delta 到达。
func (app *chat) applyEngineEvent(kind string, raw json.RawMessage) {
	switch kind {
	case "tool_start":
		var ev struct {
			ToolCallID string `json:"ToolCallID"`
			ToolName   string `json:"ToolName"`
			Args       string `json:"Args"`
		}
		if json.Unmarshal(raw, &ev) != nil {
			return
		}
		app.upsertTool(ev.ToolCallID, ev.ToolName, ev.Args, "running", 0)
	case "tool_end":
		var ev struct {
			ToolCallID string `json:"ToolCallID"`
			ToolName   string `json:"ToolName"`
			Output     string `json:"Output"`
			Args       string `json:"Args"`
			IsError    bool   `json:"IsError"`
			DurationMS int64  `json:"DurationMS"`
		}
		if json.Unmarshal(raw, &ev) != nil {
			return
		}
		status := "done"
		if ev.IsError {
			status = "failed"
		}
		app.upsertTool(ev.ToolCallID, ev.ToolName, ev.Output, status, ev.DurationMS)
		// todo 工具的 write 调用携带完整任务列表；用它更新"计划"面板。
		// read 调用/畸形参数返回 nil，不覆盖已有列表。
		if ev.ToolName == "todo" {
			if todos := parsePlanTodos(ev.Args); len(todos) > 0 {
				app.planTodos = todos
			}
		}
	case "usage":
		// agent.Event 的 TotalUsage 是本轮累计值（字段名为 Go 字段名，
		// 嵌套 llm.Usage 用 snake_case），直接覆盖即可反映真实消耗。
		var ev struct {
			TotalUsage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				CachedTokens     int `json:"cached_tokens"`
			} `json:"TotalUsage"`
		}
		if json.Unmarshal(raw, &ev) != nil {
			return
		}
		// TotalUsage 是单次 prompt 运行的累计值，新一轮会从零开始；
		// 检测到回落时把请求计数也归零，避免跨轮数字口径不一致。
		calls := app.usage.calls + 1
		if ev.TotalUsage.PromptTokens < app.usage.input ||
			ev.TotalUsage.CompletionTokens < app.usage.output {
			calls = 1
		}
		app.usage = navUsage{
			input:  ev.TotalUsage.PromptTokens,
			output: ev.TotalUsage.CompletionTokens,
			cached: ev.TotalUsage.CachedTokens,
			calls:  calls,
		}
	case "goal_status":
		// /goal 每轮状态此前被 native 静默丢弃。Text 是与 agent.GoalStatus
		// 同形的紧凑 JSON；这里只取封闭状态与有界计数更新状态徽章。
		var ev struct {
			Text string `json:"Text"`
		}
		if json.Unmarshal(raw, &ev) != nil {
			return
		}
		var status engineGoalStatus
		if json.Unmarshal([]byte(ev.Text), &status) != nil {
			return
		}
		app.status = goalStatusBadge(status.State, status.Iteration, status.MaxIterations)
	case "tool_arg_delta":
		// live-only 参数片段：在工具卡片上实时预览。片段早于 tool_start，
		// 后者会用完整 Args 覆盖同一张卡片，不会留下重复行。
		var ev struct {
			ToolCallID   string `json:"ToolCallID"`
			ToolName     string `json:"ToolName"`
			ArgDelta     string `json:"ArgDelta"`
			ToolArgIndex int    `json:"ToolArgIndex"`
		}
		if json.Unmarshal(raw, &ev) != nil {
			return
		}
		app.appendToolArgPreview(ev.ToolCallID, ev.ToolName, ev.ArgDelta, ev.ToolArgIndex)
	case "notice":
		var ev struct {
			Text string `json:"Text"`
		}
		if json.Unmarshal(raw, &ev) != nil {
			return
		}
		app.applyApprovalNotice(ev.Text)
	}
}

// engineGoalStatus 与 agent.GoalStatus 的线上形态一致，但 native 刻意不 import
// internal/agent；字段都带 JSON 标签，未知字段自然忽略。
type engineGoalStatus struct {
	State         string `json:"state"`
	Iteration     int    `json:"iteration"`
	MaxIterations int    `json:"max_iterations"`
}

// maxArgPreview 限制实时参数预览的可见长度，避免异常长片段撑宽工具行。
const maxArgPreview = 160

// goalStatusBadge 把封闭状态与有界计数格式化为状态栏徽章。
// 所有面向用户的文字都经 tr()，未知状态有界透传，满足 en-US 无汉字门禁。
func goalStatusBadge(state string, iteration, max int) string {
	zh, ok := map[string]string{
		"running":     "目标运行中",
		"complete":    "目标已完成",
		"capped":      "目标已达上限",
		"interrupted": "目标已中断",
	}[state]
	if !ok {
		// 未知状态一律用本地化的"未知"短句：绝不把任意负载回显进 en-US 界面。
		zh = "目标状态未知"
	}
	return tr("目标循环：") + tr(zh) + " · " + fmt.Sprintf(tr("第 %d/%d 次"), iteration, max)
}

// appendToolArgPreview 把一段实时参数追加到对应工具卡片。真实调用 id 在流式
// 过程中会从 "pending-N" 换成 engine id，argPreviewIDs 负责把卡片迁移到最终 id；
// tool_start 到达后再由 upsertTool 用完整参数覆盖同一行。
func (app *chat) appendToolArgPreview(id, name, delta string, index int) {
	if delta == "" {
		return
	}
	if app.argPreviewIDs == nil {
		app.argPreviewIDs = make(map[int]string)
	}
	key := id
	if key == "" {
		key = fmt.Sprintf("pending-%d", index) // 与 agent 侧回退 id 保持一致
	}
	if prev := app.argPreviewIDs[index]; prev != "" && prev != key {
		if m := app.tools[prev]; m != nil {
			delete(app.tools, prev)
			app.tools[key] = m
		}
	}
	app.argPreviewIDs[index] = key

	if m := app.tools[key]; m != nil {
		if name != "" {
			m.name = name
		}
		m.text = clipToolText(m.text+delta, maxArgPreview)
		m.argPreview = true
		return
	}
	// 若同名预览卡已用别的 id 建立（例如 tool_start 前 id 切换），复用它。
	for _, m := range app.tools {
		if m.argPreview && name != "" && m.name == name {
			if name != "" {
				m.name = name
			}
			m.text = clipToolText(m.text+delta, maxArgPreview)
			return
		}
	}
	app.upsertTool(key, name, clipToolText(delta, maxArgPreview), "running", 0)
	if m := app.tools[key]; m != nil {
		m.argPreview = true
	}
}

// upsertTool 按其 engine 工具调用 id 创建或更新一个工具卡片。
// 复用该行使并行调用保持区分，并让 tool_end 更新
// tool_start 打开的卡片而非新增第二张。
func (app *chat) upsertTool(id, name, detail, status string, durationMS int64) {
	if id == "" {
		id = name // defensive: an older engine may omit the correlation id
	}
	if app.tools == nil {
		app.tools = make(map[string]*message)
	}
	m := app.tools[id]
	if m == nil && name != "" {
		// tool_arg_delta 可能用 pending-N 或旧 id 建了预览卡；按工具名
		// 迁移到真实 id，使 tool_start 原地覆盖而不是新增第二行。
		for key, candidate := range app.tools {
			if candidate.argPreview && candidate.name == name {
				delete(app.tools, key)
				app.tools[id] = candidate
				m = candidate
				break
			}
		}
	}
	if m != nil {
		if name != "" {
			m.name = name
		}
		if detail != "" {
			// 保存完整输出；折叠摘要在渲染时由 toolCardSummary 有界截取，
			// 展开后仍能看到 stderr/diff 全文。
			m.text = detail
		}
		m.status = status
		m.argPreview = false
		if durationMS > 0 {
			m.durationMS = durationMS
		}
		return
	}
	app.next++
	m = &message{id: app.next, kind: msgTool, name: name, text: detail, status: status, durationMS: durationMS}
	app.tools[id] = m
	// 时间顺序：若当前流式段已有文本，先封存它，使工具卡出现在该段文本之后；
	// 工具之后再开一个新文本段容纳后续文本，避免所有工具堆在整段回复末尾。
	if app.streaming && app.reply != nil && !app.reply.done && strings.TrimSpace(app.reply.text) != "" {
		app.reply.done = true
		app.replySplit = true
		app.reply = nil
	}
	app.messages = append(app.messages, m)
	if app.streaming && app.reply == nil {
		app.next++
		next := &message{id: app.next, kind: msgAssistant}
		app.messages = append(app.messages, next)
		app.reply = next
	}
}

// applyApprovalNotice 把真实的 approval:request / approval:resolved 通知转换为现有审批卡片，替换预设的 demo 审批。clipToolText 使工具卡片保持单行可读而不拆分 UTF-8。finish 完成当前回复；它总是在主线程上运行。demo 审批只属于预设回退。真实引擎通过其自己的传输处理审批，而非通过假提示。
// 到现有审批卡片，替换预设的 demo 审批。
func (app *chat) applyApprovalNotice(text string) {
	kind, tool, id, digest, granted, ok := parseEngineApproval(text)
	if !ok {
		return
	}
	switch kind {
	case "request":
		args := digest
		if args == "" {
			args = "(no arguments)"
		}
		// "始终允许此命令"自动放行：命中持久化模式则直接回答，不弹卡打扰用户。
		if approvalShouldAutoGrant(loadNativePrefs(nativePrefsHome()), tool, args) {
			app.approval = &approval{id: id, name: tool, args: args}
			app.answerApprovalChoice(allowOnceChoice())
			return
		}
		app.approval = &approval{id: id, name: tool, args: args}
		app.status = tr("需要批准: ") + tool
	case "resolved":
		if app.approval != nil && (app.approval.id == id || app.approval.name == tool) {
			app.approval = nil
		}
		status, note := "done", tr("✓ 已允许 ")+tool
		if !granted {
			status, note = "failed", tr("✗ 已拒绝 ")+tool
		}
		app.next++
		app.messages = append(app.messages, &message{
			id: app.next, kind: msgTool, name: tool, text: note, status: status, done: true,
		})
		app.status = note
	}
}

// clipToolText 使工具卡片保持单行可读而不拆分 UTF-8。
func clipToolText(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// finish 完成当前回复；它总是在主线程上运行。
func (app *chat) finish(src string) {
	if app.reply != nil {
		// 单段时用最终文本覆盖（保持既有行为）；被工具切分成多段时，
		// 每段文本已由 delta 各自累积，不能再用整段全文覆盖最后一段。
		if !app.replySplit {
			app.reply.text = src
		}
		app.reply.done = true
	}
	if app.tool != nil {
		app.tool.status = "done"
	}
	app.streaming = false
	app.reply, app.tool = nil, nil
	app.replySplit = false
	app.status = tr("已完成")
	// demo 审批只属于预设回退。真实 engine
	// 通过其自己的传输处理审批，而非通过假提示。
	if app.engine == nil && app.sent == 1 {
		app.approval = &approval{
			name: "write_file",
			args: "{\"path\": \"~/my-project/src/main.go\", \"lines\": 12}",
		}
	}
}

// retireStream 使活动流式轮次失效，使迟到的 delta 或提示
// 结果在 stop/undo/newSession 之后无法复活 UI 状态。它走
// bridge 带锁的 setDeltaHandler API，因此 bridge.go 的竞态处理无需
// 修改：readLoop 快照 nil 处理器且 completeRealReply 的 generation
// 检查拒绝被取代的完成。
func (app *chat) retireStream() {
	if app.engine != nil {
		app.engine.setDeltaHandler(nil)
	}
}

func (app *chat) stop() {
	if app.engine != nil {
		_ = app.engine.abort()
	}
	// 仅 Abort 不够：进行中提示的完成仍携带
	// 旧 generation。在清除回复状态前退役它，使迟到结果
	app.retireStream()
	if app.reply != nil {
		app.reply.done = true
	}
	if app.tool != nil {
		app.tool.status = "failed"
	}
	app.streaming = false
	app.reply, app.tool = nil, nil
	app.status = tr("已停止")
}

// undo 把最近的回合作为一个整体移除：用户消息、其助手
// 回复和任何末尾工具卡片。过去只删最后一行会留下
// 回复已消失的孤立用户消息。提示文本保留在 lastPrompt 中，使
// undo 把最近的回合作为一个整体移除：用户消息、其助手回复和任何末尾工具卡片。过去只删最后一行会留下回复已消失的孤立用户消息。提示文本保留在 lastPrompt 中，使接下来的 retry 能重放它。找到最后一条用户消息；从那里到末尾的一切是一个回合。
func (app *chat) undo() {
	if app.streaming {
		app.stop()
	}
	// 找到最后一条用户消息；从那里到末尾的一切是一个回合。
	start := -1
	for i := len(app.messages) - 1; i >= 0; i-- {
		if app.messages[i].kind == msgUser {
			start = i
			break
		}
	}
	if start < 0 {
		app.status = tr("没有可撤销的消息")
		return
	}
	app.messages = app.messages[:start]
	app.approval = nil
	app.status = tr("已撤销上一轮")
}

// retry 在丢弃最近回合后重发最近的用户提示，使
// 模型对同一问题再获一次机会。流式处理中或
// 尚未提问任何内容时它是空操作。
func (app *chat) retry() {
	if app.streaming {
		app.status = tr("正在生成，无法重试")
		return
	}
	prompt := app.lastPrompt
	if prompt == "" {
		app.status = tr("没有可重试的消息")
		return
	}
	app.undo()
	app.status = tr("已重试上一轮")
	app.send(prompt)
}

func (app *chat) toggleTheme() {
	app.light = !app.light
	// 主题偏好持久化，重启后保持用户选择。
	_ = saveThemePref(nativePrefsHome(), app.light)
	if app.light {
		app.status = tr("已切换到浅色主题")
	} else {
		app.status = tr("已切换到深色主题")
	}
}

func (app *chat) answerApproval(allow bool) {
	if app.approval == nil {
		return
	}
	ap := app.approval
	if app.engine == nil {
		// Demo 审批：没有 engine 可回答，保持原有的纯本地
		// 行为（把决定记录为工具结果行）。
		app.approval = nil
		app.recordApprovalResult(ap.name, allow)
		return
	}
	// retry 在丢弃最近回合后重发最近的用户提示，使模型对同一问题再获一次机会。流式处理中或尚未提问任何内容时它是空操作。Demo 审批：没有 engine 可回答，保持原有的纯本地行为（把决定记录为工具结果行）。真实 engine 审批：通过 RPC 把决定发回。保持卡片打开直到 engine 接受它，使失败的调用能从同一 UI 重试。engine 通过 approval:resolved 通知报告真实结果，它追加结果行。
	// 保持卡片打开直到 engine 接受它，使失败的调用能从
	// 同一 UI 重试。engine 通过 approval:resolved 通知报告
	// 真实结果，它追加结果行。
	call := app.approvalAnswerRPC
	if call == nil {
		call = app.engine.call
	}
	if _, err := call("approval.answer", map[string]any{"id": ap.id, "granted": allow}); err != nil {
		app.status = tr("审批回应失败：") + engineErrorNotice(err)
		return
	}
	app.approval = nil
	app.status = tr("已响应，等待引擎确认")
}

// answerApprovalChoice 是三态审批的统一回答入口。allow-once 与 deny 在 RPC
// 层与旧的 answerApproval(bool) 完全一致（保持测试契约）；allow-always 额外
// 持久化命令模式，后续同类请求由 approvalShouldAutoGrant 自动放行。
func (app *chat) answerApprovalChoice(choice approvalChoice) {
	ap := app.approval
	if ap == nil {
		return
	}
	allow := choice.Decision != approvalDeny
	// 始终允许：先持久化命令模式（写失败不影响本次回答）。
	if choice.Decision == approvalAllowAlways {
		pattern := approvalScopePattern(ap.name, ap.args)
		if pattern != "" {
			_ = commitApprovalChoice(nativePrefsHome(), ap.name, ap.args, choice)
		}
	}
	// Demo 模式沿用纯本地结果行。
	if app.engine == nil {
		app.approval = nil
		app.recordApprovalResult(ap.name, allow)
		return
	}
	call := app.approvalAnswerRPC
	if call == nil {
		call = app.engine.call
	}
	params := approvalAnswerParams(ap.id, choice, ap.name, ap.args)
	if _, err := call("approval.answer", params); err != nil {
		app.status = tr("审批回应失败：") + engineErrorNotice(err)
		return
	}
	app.approval = nil
	app.status = tr("已响应，等待引擎确认")
}

// recordApprovalResult 为 demo 审批追加本地工具结果行，
// 那里没有 engine 参与且决定是最终的。
func (app *chat) recordApprovalResult(name string, allow bool) {
	app.next++
	status, text := "done", tr("✓ 已允许 ")+name
	if !allow {
		status, text = "failed", tr("✗ 已拒绝 ")+name
	}
	app.messages = append(app.messages, &message{
		id: app.next, kind: msgTool, name: name, text: text, status: status, done: true,
	})
	app.status = text
}

func (app *chat) newSession() {
	// 上一轮可能仍在进行中；丢弃其迟到结果使它无法干扰。
	// overwrite the "已新建会话" status or re-add a stale reply.
	app.retireStream()
	app.messages = nil
	app.tools = nil
	app.approval = nil
	app.streaming = false
	app.reply, app.tool = nil, nil
	app.lastPrompt = "" // a fresh session must not replay the previous one's prompt
	app.selectedSession = ""
	app.status = tr("已新建会话")
}

// demo 预设回复保持中文原文；英文版在 locale.go 中一一对应，发送时按
// configureLocale 的结果选择，避免包级变量在 init 时固化错误语言。
var cannedReplies = []string{
	"## 已经处理好了\n\n我读懂了你的问题。这是一条 **原生 Markdown** 回复：\n\n" +
		"- 支持列表\n- 支持 `代码` 与[链接](https://example.com)\n\n" +
		"```go\nfmt.Println(\"hello, argentgo\")\n```\n\n" +
		"> 流式输出与 Markdown 渲染都由 ui/markdown 完成。\n",
	"## 分析完成\n\n| 项目 | 状态 |\n|---|---|\n| 会话 | 就绪 |\n| 工具 | 已批准 |\n\n" +
		"1. 读取上下文\n2. 生成回复\n\n*继续提问吧。*\n",
}

func main() {
	app := &chat{status: tr("就绪")}

	// 偏好持久化：主题/语言/始终允许列表存于 ~/.argentgo/native-prefs.json。
	// 偏好优先于 ARGENT_LOCALE 环境变量，使设置面板的修改能跨重启生效。
	prefs := loadNativePrefs(nativePrefsHome())
	configureLocaleWithPrefs(os.Getenv, prefs)
	app.light = prefs.LightTheme()

	// 审批卡 diff 默认开启（=0 紧急回退）：审批从"盲签"变成看一眼再签。
	configureApprovalDiff(os.Getenv)

	// 工作区来自 ARGENT_CWD 或进程当前目录。即使未连接引擎，native 自身
	// 仍在这个目录里，读取 .git/HEAD 即可拿到真实分支名（无需 CLI/RPC）。
	cwd := os.Getenv("ARGENT_CWD")
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	app.gitBranch = navGitBranch(cwd, nil)

	// ARGENT_EXE 指向 engine 二进制。显式设置时优先使用；未设置时回退到
	// 与 native 二进制同目录的 argentgo.exe——安装包把两个二进制放在一起，
	// 这样用户双击原生窗口即可直接对话，无需配置环境变量。都没有才进 demo 模式。
	engineExe := os.Getenv("ARGENT_EXE")
	if engineExe == "" {
		if self, err := os.Executable(); err == nil {
			candidate := filepath.Join(filepath.Dir(self), "argentgo.exe")
			if _, statErr := os.Stat(candidate); statErr == nil {
				engineExe = candidate
			}
		}
	}
	if exe := engineExe; exe != "" {
		b, err := startBridge(exe, cwd, nil, app.onEngineEvent)
		if err != nil {
			log.Printf("native: engine bridge unavailable: %v (using demo mode)", err)
		} else {
			app.engine = b
			app.status = tr("已连接引擎")
			// engine 崩溃/EOF 时由 readLoop 通知，状态栏随之切到断连态，
			// 避免窗口仍显示绿色"已连接"而用户继续输入才收到失败。
			b.setCloseHandler(app.onEngineClose)
			// 启动时一次性获取真实配置，供状态栏显示。失败时
			// configInfo 保持零值，状态栏回退到 demo 占位串。
			app.ensureConfigInfo()
			defer b.close()
			// 消费真实 agent 事件使工具卡片和审批反映
			// engine 而非预设 demo 数据。若 engine 没有事件
			// 中心，订阅失败且 demo 回退保持完整。
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			seq, err := b.SubscribeEvents(ctx)
			cancel()
			if err != nil {
				log.Printf("native: event subscribe unavailable: %v (using demo fallback)", err)
			} else {
				log.Printf("native: subscribed to engine events at seq %d", seq)
			}
		}
	}

	mygo.App.WhenReady(func() {
		app.win = mygo.NewWindow(mygo.WindowOptions{
			Title:   "argentgo (native)",
			Width:   1180,
			Height:  760,
			Content: ui.View(app.view),
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
