// Command mygo-native is a native MyGo UI replica of the argentgo chat
// frontend. It uses only package ui (no WebView, HTML or JS); assistant
// replies are rendered with ui/markdown so streaming and Markdown work
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
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// palette mirrors the warm charcoal/coral design tokens of
// argent-go/ui/src/styles.css. The app keeps its own palette so the theme
// toggle can switch the whole window without relying on the desktop.
type palette struct {
	bg, surface, element, border, borderSubtle ui.Color
	text, dim, accent, ok, danger, warn        ui.Color
	strong, onAccent, sidebar                  ui.Color
	accentBG, accentLine                       ui.Color
}

var (
	darkPalette = palette{
		bg:           ui.Hex("#171716"),
		surface:      ui.Hex("#20201e"),
		element:      ui.Hex("#2b2b28"),
		border:       ui.Hex("#3b3a35"),
		borderSubtle: ui.Hex("#2e2e2a"),
		text:         ui.Hex("#e8e6df"),
		dim:          ui.Hex("#9b9991"),
		accent:       ui.Hex("#d97757"),
		ok:           ui.Hex("#8fae72"),
		danger:       ui.Hex("#e0737b"),
		warn:         ui.Hex("#d3a35c"),
		strong:       ui.Hex("#faf8f3"),
		onAccent:     ui.Hex("#1b1310"),
		sidebar:      ui.Hex("#131312"),
		accentBG:     ui.RGBA(217, 119, 87, 0.13),
		accentLine:   ui.RGBA(217, 119, 87, 0.34),
	}
	lightPalette = palette{
		bg:           ui.Hex("#f5f4ef"),
		surface:      ui.Hex("#ffffff"),
		element:      ui.Hex("#ebe9e1"),
		border:       ui.Hex("#dcd9cf"),
		borderSubtle: ui.Hex("#e7e4db"),
		text:         ui.Hex("#2f2e29"),
		dim:          ui.Hex("#7d7b72"),
		accent:       ui.Hex("#b8522c"),
		ok:           ui.Hex("#4d7938"),
		danger:       ui.Hex("#b23b48"),
		warn:         ui.Hex("#9a6b18"),
		strong:       ui.Hex("#1b1a17"),
		onAccent:     ui.Hex("#ffffff"),
		sidebar:      ui.Hex("#efeee8"),
		accentBG:     ui.RGBA(184, 82, 44, 0.08),
		accentLine:   ui.RGBA(184, 82, 44, 0.27),
	}
)

type msgKind int

const (
	msgUser msgKind = iota
	msgAssistant
	msgTool
)

// message is one row of the transcript. For tools, name/text/status describe
// the compact card; for assistant messages, text is Markdown source.
type message struct {
	id     int
	kind   msgKind
	text   string
	name   string
	status string // "running", "done" or "failed"
	done   bool   // assistant reply finished streaming
}

type approval struct {
	id   string // engine tool-call id from the approval:request notice; empty in demo mode
	name string
	args string
}

// session is one row of the session rail. id is the real engine session id;
// it is empty for the demo rows in fakeSessions. openSession uses id when set
// (so session.load gets a real id) and falls back to title in demo mode.
type session struct {
	id    string
	title string
	meta  string
	group string
}

var navItems = []string{
	"聊天", "定时任务", "文档", "记忆", "扩展", "用量",
	"Git", "任务", "工作流", "终端", "文件", "计划",
}

var fakeSessions = []session{
	{title: "重构聊天界面", meta: "2 分钟前", group: "今天"},
	{title: "修复工具调用状态", meta: "昨天 18:20", group: "昨天"},
	{title: "整理记忆索引", meta: "3 天前", group: "更早"},
}

var quickActions = []string{"解释这段代码", "写单元测试", "检查命令", "/ 命令"}

// chat is the single app state object. The view is rebuilt from it every
// frame; background work changes it only through Window.Update.
type chat struct {
	win     *mygo.Window
	engine  *bridge // nil in tests / when the engine binary is unavailable
	instant bool    // tests set this so replies appear at once, with no timers

	light           bool
	draft           string
	status          string
	nav             string
	sessionQuery    string
	selectedSession string
	messages        []*message
	tools           map[string]*message // real tool cards keyed by tool-call id, upserted from events
	list            ui.ListState
	sessionList     ui.ListState
	md              markdown.Options

	// sessions holds the real session rail fetched over session.list. It is
	// nil until a successful fetch, in which case sessionPanel keeps showing
	// fakeSessions (demo mode, or a failed RPC that must degrade gracefully).
	sessions          []session
	sessionListLoaded bool // session.list is fetched at most once per chat

	next       int
	sent       int
	lastPrompt string // most recent user text; backed up by undo, replayed by retry
	streaming  bool
	reply      *message
	tool       *message
	approval   *approval

	// sessionListRPC is the test seam for loading the session rail. When nil,
	// fetchSessions calls engine.call directly; tests inject a recorder to
	// verify the session.list RPC without a live engine process.
	sessionListRPC func(method string, params any) (json.RawMessage, error)

	// sessionLoadRPC is the test seam for opening a session. When nil,
	// openSession calls engine.call directly; tests inject a recorder to
	// verify the session.load RPC without a live engine process.
	sessionLoadRPC func(method string, params any) (json.RawMessage, error)

	// approvalAnswerRPC is the test seam for answering an approval. When nil,
	// answerApproval calls engine.call directly; tests inject a recorder to
	// verify the approval.answer RPC without a live engine process.
	approvalAnswerRPC func(method string, params any) (json.RawMessage, error)
}

func (app *chat) palette() palette {
	if app.light {
		return lightPalette
	}
	return darkPalette
}

// applyTheme installs the app's palette as the window theme so that widgets
// (buttons, inputs, scroll bars) follow the same design as our own elements.
func (app *chat) applyTheme(c *ui.Context) palette {
	p := app.palette()
	t := *c.Theme()
	t.Background, t.Surface, t.SurfaceHover, t.SurfacePressed = p.bg, p.surface, p.element, p.element
	t.Border, t.Text, t.TextMuted = p.border, p.text, p.dim
	t.Accent, t.AccentText, t.InactiveAccent = p.accent, p.onAccent, p.accent
	t.Success, t.Danger, t.Warning = p.ok, p.danger, p.warn
	t.Focus, t.Font, t.FontSize, t.Radius = p.accent, "Segoe UI", 14, 8
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

	// Undo / retry / approval keys. A focused text input takes plain keys
	// first, so Y/N only act on an approval while the composer is not typing.
	if c.Shortcut(ui.Cmd, ui.KeyZ) {
		app.undo()
	}
	if c.Shortcut(ui.Cmd, ui.KeyY) {
		app.retry()
	}
	if app.approval != nil && !c.FocusTakesText() {
		if c.Shortcut(0, ui.KeyY) {
			app.answerApproval(true)
		} else if c.Shortcut(0, ui.KeyN) || c.Shortcut(0, ui.KeyEscape) {
			app.answerApproval(false)
		}
	}

	ui.Row(c).Fill().Children(func() {
		app.sidebar(c, p)
		app.main(c, p, t)
	})
}

func (app *chat) sidebar(c *ui.Context, p palette) {
	ui.Column(c).Width(240).FillHeight().Background(p.sidebar).
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

		// Navigation grid.
		ui.Grid(c).Columns(3).Gap(4).Children(func() {
			for _, name := range navItems {
				active := app.nav == name
				btn := ui.Button(c, name).Padding(8, 2).Radius(6)
				if active {
					btn.Background(p.accentBG).TextColor(p.accent)
				} else {
					btn.Background(ui.Transparent).TextColor(p.dim)
				}
				if btn.Clicked() {
					app.nav = name
					app.status = "已切换到" + name
				}
			}
		})

		// The active nav item decides what the lower sidebar shows. 聊天 (and the
		// zero value used by tests/demo) keeps the live session list; every other
		// item renders its own placeholder sub-panel instead of a session rail.
		if app.nav == "" || app.nav == "聊天" {
			app.sessionPanel(c, p)
		} else {
			app.renderNavPanel(c, p, app.nav)
		}

		// Footer status dot.
		ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(8, 6, 0, 6).
			BorderWidth(1, 0, 0, 0).BorderColor(p.borderSubtle).Children(func() {
			dot := p.ok
			label := "就绪"
			if app.streaming {
				dot, label = p.accent, "运行中"
			}
			ui.Box(c).Size(7, 7).Radius(4).Background(dot)
			ui.Text(c, label).FontSize(12).TextColor(p.dim)
		})
	})
}

// sessionPanel renders the chat nav item's sidebar: session search, the
// session rail and the new-session button. The first time a real engine is
// attached it kicks off a session.list fetch; until that succeeds (or in demo
// mode) the existing fakeSessions remain on screen, so a failed RPC leaves a
// usable rail instead of a blank one.
func (app *chat) sessionPanel(c *ui.Context, p palette) {
	app.ensureSessions()

	// Session header and search.
	ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(4, 6, 0, 6).Children(func() {
		ui.Text(c, "会话").FontSize(12).Bold().TextColor(p.dim).Grow(1)
		if ui.Button(c, "＋").Clicked() {
			app.newSession()
		}
	})
	ui.TextInput(c, &app.sessionQuery).Placeholder("搜索会话").
		Padding(8, 10).Radius(8).Background(p.element).FontSize(12)

	// Session list. A successful fetch replaces the demo rows; a failure (or
	// demo mode, where sessions stays nil) keeps them.
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
	ui.List(c, &app.sessionList, len(filtered), func(i int) {
		s := filtered[i]
		row := ui.Row(c).Padding(8, 10).Radius(8)
		if app.isSessionSelected(s) {
			row.Background(p.accentBG)
		}
		row.Children(func() {
			ui.Column(c).Gap(2).Children(func() {
				ui.Text(c, s.title).FontSize(13).TextColor(p.text).SingleLine()
				ui.Text(c, s.group+" · "+s.meta).FontSize(11).TextColor(p.dim).SingleLine()
			})
		})
		if row.Clicked() {
			app.openSession(s)
		}
	}).Grow(1)
}

// isSessionSelected compares by real id when the row has one, so two sessions
// sharing a generated title cannot both look selected. Demo rows fall back to
// the title, which is their identity there.
func (app *chat) isSessionSelected(s session) bool {
	if s.id != "" {
		return app.selectedSession == s.id
	}
	return app.selectedSession == s.title
}

// ensureSessions starts the one-time session.list fetch when a real engine is
// attached. With a window it runs off-thread and applies the result on the UI
// thread (like prompt replies do); without one (tests) it applies inline so
// the very first frame already shows the real rail.
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

// fetchSessions calls the session.list RPC and decodes its {"sessions":[...]}
// payload. An RPC error or an unparseable payload is returned to the caller,
// which degrades to the existing rail rather than clearing it.
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

// engineSession mirrors server.SessionInfo (internal/server/sessions.go) as
// it arrives on the wire. Keeping the type here avoids importing internal/
// into native/, which stays a dependency-light frontend.
type engineSession struct {
	ID      string    `json:"id"`
	Title   string    `json:"title,omitempty"`
	Updated time.Time `json:"updated"`
}

// parseSessionList maps the RPC payload into rail rows. A session with no
// title is shown by its id; the timestamp supplies the group/meta labels.
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

// applySessions installs a successful fetch. A failure leaves sessions as it
// was (so the current rail survives) and reports a redacted, single-line
// notice; it never crashes the view.
func (app *chat) applySessions(sessions []session, err error) {
	if err != nil {
		app.status = "会话列表加载失败：" + engineErrorNotice(err)
		return
	}
	app.sessions = sessions
}

// sessionGroup buckets a session by the day it was updated, matching the
// 今天/昨天/更早 groups of the demo rail.
func sessionGroup(updated time.Time) string {
	if updated.IsZero() {
		return "更早"
	}
	now := time.Now()
	if sameDay(now, updated) {
		return "今天"
	}
	if sameDay(now.Add(-24*time.Hour), updated) {
		return "昨天"
	}
	return "更早"
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// sessionMeta renders a short relative time for a session row.
func sessionMeta(updated time.Time) string {
	if updated.IsZero() {
		return ""
	}
	d := time.Since(updated)
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%d 天前", int(d.Hours()/24))
	default:
		return updated.Format("01-02")
	}
}

// openSession selects a session row and asks a real engine to load it through
// the existing session.load RPC. The id comes from session.list when available;
// demo/degraded rows have none and fall back to the title (their identity in
// that mode). With no bridge attached it keeps the original local-only switch.
func (app *chat) openSession(s session) {
	id := s.id
	if id == "" {
		id = s.title
	}
	app.selectedSession = id
	app.status = "已打开会话：" + s.title
	if app.engine == nil {
		return
	}
	call := app.sessionLoadRPC
	if call == nil {
		call = app.engine.call
	}
	if _, err := call("session.load", map[string]any{"id": id}); err != nil {
		app.status = "打开会话失败：" + engineErrorNotice(err)
	}
}

func (app *chat) main(c *ui.Context, p palette, t *ui.Theme) {
	_ = t // theme colors already installed through c.SetTheme
	ui.Column(c).Grow(1).FillHeight().Children(func() {
		// Top bar.
		ui.Row(c).Height(64).Padding(10, 20).Gap(12).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(p.borderSubtle).Children(func() {
			ui.Column(c).Gap(2).Children(func() {
				ui.Text(c, "argentgo").FontSize(15).Bold().TextColor(p.strong)
				ui.Text(c, "~/my-project").FontSize(12).TextColor(p.dim)
			})
			ui.Spacer(c)
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "模型").FontSize(12).TextColor(p.dim)
				ui.Text(c, "gpt-4o-mini").FontSize(13).TextColor(p.text).
					Padding(6, 10).Radius(999).Background(p.element)
				if ui.Button(c, "撤销").Clicked() {
					app.undo()
				}
				if ui.Button(c, "重试").Clicked() {
					app.retry()
				}
				if ui.Button(c, "主题").Clicked() {
					app.toggleTheme()
				}
			})
		})

		// Approval bar.
		if app.approval != nil {
			app.approvalView(c, p)
		}

		// Transcript.
		app.list.Key = func(i int) any { return app.messages[i].id }
		app.list.FollowEnd = true
		ui.List(c, &app.list, len(app.messages), func(i int) {
			app.message(c, app.messages[i], p)
		}).Grow(1).Padding(16, 20).Gap(14).Justify(ui.End).Children(func() {
			if len(app.messages) == 0 {
				ui.Column(c).MaxWidth(560).Margin(ui.Auto).Gap(12).
					AlignItems(ui.Center).Children(func() {
					ui.Text(c, "◆").FontSize(22).TextColor(p.accent)
					ui.Text(c, "有什么可以帮你？").FontSize(28).Bold().TextColor(p.strong)
					ui.Text(c, "问点什么，或者输入 / 看命令。").FontSize(14).TextColor(p.dim)
					ui.Text(c, "首次使用请在终端运行 argent login 配置模型凭据。").
						FontSize(12).TextColor(p.dim)
					ui.Row(c).Gap(8).Wrap().Justify(ui.Center).Children(func() {
						for _, action := range quickActions {
							a := action
							if ui.Button(c, a).Padding(6, 12).Clicked() {
								if strings.HasPrefix(a, "/ ") {
									// A slash-command hint seeds the trigger, not its label.
									app.draft = "/ "
								} else {
									app.draft = a + " "
								}
							}
						}
					})
				})
			}
		})

		// Status line.
		ui.Row(c).Height(30).Padding(0, 20).Gap(8).AlignItems(ui.Center).Children(func() {
			dot := p.ok
			if app.streaming {
				dot = p.accent
			}
			ui.Box(c).Size(6, 6).Radius(3).Background(dot)
			ui.Text(c, app.status).FontSize(12).TextColor(p.dim)
			ui.Spacer(c)
			ui.Text(c, "gpt-4o-mini · ~/my-project").FontSize(11).TextColor(p.dim)
		})

		// Composer.
		ui.Column(c).Padding(10, 20, 14, 20).Gap(8).Background(p.surface).
			BorderWidth(1, 0, 0, 0).BorderColor(p.border).Children(func() {
			in := ui.TextArea(c, &app.draft).Lines(1, 5).AutoFocus().
				Placeholder("问点什么，或者输入 / 看命令。").Grow(1)
			in.OnTextKey(func(k ui.TextKey) bool {
				if k.Key == ui.KeyEnter && k.Mods == 0 {
					app.send(k.Text())
					k.SetText("")
					return true
				}
				return false // Shift+Enter inserts a newline
			})
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Enter 发送 · Shift+Enter 换行").FontSize(11).TextColor(p.dim).Grow(1)
				if app.streaming {
					if ui.Button(c, "停止").Clicked() {
						app.stop()
					}
				} else if ui.PrimaryButton(c, "发送").Clicked() {
					app.send(app.draft)
				}
			})
		})
	})
}

func (app *chat) approvalView(c *ui.Context, p palette) {
	ap := app.approval
	ui.Column(c).Padding(10, 20).Gap(8).Background(p.accentBG).
		BorderWidth(1, 0, 0, 0).BorderColor(p.accentLine).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(18, 18).Radius(9).Background(p.warn)
			ui.Text(c, "需要批准").Bold().TextColor(p.strong)
			ui.Text(c, ap.name).FontSize(12).TextColor(p.accent)
			ui.Spacer(c)
			ui.Text(c, "Y 允许 · N 拒绝").FontSize(11).TextColor(p.dim)
		})
		ui.Text(c, ap.args).FontSize(12).TextColor(p.dim).
			Padding(8, 10).Radius(6).Background(p.surface).Selectable()
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "拒绝").Clicked() {
				app.answerApproval(false)
			}
			if ui.PrimaryButton(c, "允许").Clicked() {
				app.answerApproval(true)
			}
		})
	})
}

// message builds one transcript row: a right-aligned user bubble, a compact
// tool card with a status glyph, or an assistant Markdown view.
func (app *chat) message(c *ui.Context, m *message, p palette) {
	switch m.kind {
	case msgUser:
		ui.Row(c).Children(func() {
			ui.Spacer(c)
			ui.Column(c).MaxWidth(520).Radius(14).Background(p.surface).
				Padding(10, 14).Gap(4).Children(func() {
				ui.Text(c, "你").FontSize(11).Bold().TextColor(p.dim)
				ui.Text(c, m.text).TextColor(p.text).Selectable()
			})
		})
	case msgTool:
		glyph, color := "⋯", p.warn
		switch m.status {
		case "done":
			glyph, color = "✓", p.ok
		case "failed":
			glyph, color = "✗", p.danger
		}
		ui.Row(c).Gap(10).AlignItems(ui.Center).Padding(8, 12).Radius(8).
			Background(p.element).Children(func() {
			ui.Text(c, glyph).Bold().TextColor(color)
			ui.Text(c, m.name).FontSize(12).Bold().TextColor(p.text)
			ui.Text(c, m.text).FontSize(12).TextColor(p.dim).SingleLine()
		})
	default:
		ui.Column(c).MaxWidth(720).Gap(4).Children(func() {
			ui.Text(c, "argentgo").FontSize(11).Bold().TextColor(p.dim)
			if m.done {
				markdown.View(c, m.text, &app.md)
			} else {
				markdown.Stream(c, m.text, &app.md)
			}
		})
	}
}

// send appends the user's message and drives the reply. When a real engine
// bridge is attached it calls prompt over JSON-RPC and streams the deltas;
// otherwise it keeps the canned Markdown fallback (tests, or no engine).
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

	// A tool row is only shown in the fallback demo: the stdio RPC protocol
	// carries prompt text and deltas, not tool events.
	var tool *message
	if app.engine == nil {
		app.next++
		tool = &message{id: app.next, kind: msgTool, name: "read_file", text: "~/my-project/src/main.go", status: "running"}
		app.messages = append(app.messages, tool)
	}

	app.draft = ""
	app.sent++
	app.tool, app.reply = tool, reply
	app.streaming = true
	app.status = "正在生成…"

	if app.engine != nil {
		app.sendReal(text, reply)
		return
	}

	src := cannedReplies[(app.sent-1)%len(cannedReplies)]
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

// isCancelError reports whether an engine error is a user abort rather than a
// real failure. After the server-side unification cancellation arrives as a
// normal stopReason (so err is nil); the string check keeps the UI correct if
// an older engine still sends "context canceled" as a JSON-RPC error.
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

// finishText picks the text passed to finish() for a non-failing prompt. A
// cancelled run can come back with an empty result; wiping the answer the user
// already watched stream in would be worse than showing nothing at all.
func finishText(full string, err error, streamed string) string {
	if err != nil && !isCancelError(err) {
		return full
	}
	if streamed != "" && (full == "" || isCancelError(err)) {
		return streamed
	}
	return full
}

// completeFromPrompt finalizes a real-engine reply. It is the single place that
// decides which text and status survive a prompt result, so a genuine engine
// error cannot be silently replaced by finish()'s (usually empty) full result.
// Keeping the decision here makes it testable without a window or live engine.
func (app *chat) completeFromPrompt(streamed, full string, err error) {
	if err != nil && !isCancelError(err) {
		// Route through finishEngineError so provider errors are redacted,
		// newline-collapsed and rune-bounded before entering the transcript.
		app.finishEngineError(err)
		return
	}
	// Cancellation is not an error: keep the streamed answer and never show
	// the "引擎错误" banner for it.
	app.finish(finishText(full, err, streamed))
}

// sendReal calls the real engine. Deltas arrive on the bridge read loop and
// are marshalled back onto the UI thread through Window.Update.
func (app *chat) sendReal(text string, reply *message) {
	eng := app.engine
	// Register the delta callback through the bridge's lock and capture the
	// round's generation. readLoop snapshots the callback under the same lock,
	// so swapping callbacks across rounds cannot race with a live delta. Only
	// the completion goroutine reads gen; it is launched after the assignment
	// below, so that read is race-free.
	gen := eng.setDeltaHandler(func(chunk string) {
		if app.win == nil {
			return
		}
		app.win.Update(func() {
			if !reply.done {
				reply.text += chunk
			}
		})
	})
	go func() {
		full, err := eng.prompt(text)
		// reply.text is only ever written on the UI thread by onDelta, so
		// reading the streamed text inside the Update callback is safe; with
		// no window no deltas are applied at all.
		complete := func() { app.completeRealReply(eng, gen, reply, full, err) }
		if app.win == nil {
			// Tests never attach a real bridge, but stay safe anyway.
			complete()
			return
		}
		app.win.Update(complete)
	}()
}

// completeRealReply finalizes a real-engine prompt only if gen is still the
// active streaming generation. A round that was superseded (the user hit 停止
// and sent a new message before the aborted prompt returned) must not run
// finish(): finish writes app.reply, which by then belongs to the newer round.
func (app *chat) completeRealReply(eng *bridge, gen uint64, reply *message, full string, err error) {
	if eng != nil && !eng.isCurrentDeltaGen(gen) {
		return
	}
	app.completeFromPrompt(reply.text, full, err)
}

// ---- Real agent events ----------------------------------------------------

// Approval notices ride the ordinary event stream: their Text carries a stable
// prefix followed by a small JSON object (see internal/agent/approval_events).
// Decoding the prefix locally keeps native/ free of any internal/ dependency.
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

// parseEngineApproval decodes an approval notice. ok is false for unrelated
// notices, unknown prefixes and malformed payloads, so every other item on
// the event stream can be ignored safely.
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

// onEngineEvent is the bridge callback. readLoop calls it off the UI thread,
// so state changes are marshalled through Window.Update. Tests (no window)
// apply events directly so the mapping stays unit-testable.
func (app *chat) onEngineEvent(kind string, raw json.RawMessage) {
	if app.win == nil {
		app.applyEngineEvent(kind, raw)
		return
	}
	app.win.Update(func() { app.applyEngineEvent(kind, raw) })
}

// applyEngineEvent maps one real agent event onto the existing transcript
// state. Only tool and approval events are rendered: assistant text already
// arrives over prompt/delta, and every other event kind is ignored.
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
		app.upsertTool(ev.ToolCallID, ev.ToolName, ev.Args, "running")
	case "tool_end":
		var ev struct {
			ToolCallID string `json:"ToolCallID"`
			ToolName   string `json:"ToolName"`
			Output     string `json:"Output"`
			IsError    bool   `json:"IsError"`
		}
		if json.Unmarshal(raw, &ev) != nil {
			return
		}
		status := "done"
		if ev.IsError {
			status = "failed"
		}
		app.upsertTool(ev.ToolCallID, ev.ToolName, ev.Output, status)
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

// upsertTool creates or updates one tool card keyed by its engine tool-call id.
// Reusing the row keeps parallel calls distinct and lets tool_end update the
// card that tool_start opened instead of adding a second one.
func (app *chat) upsertTool(id, name, detail, status string) {
	if id == "" {
		id = name // defensive: an older engine may omit the correlation id
	}
	if app.tools == nil {
		app.tools = make(map[string]*message)
	}
	if m := app.tools[id]; m != nil {
		if name != "" {
			m.name = name
		}
		if detail != "" {
			m.text = clipToolText(detail, 80)
		}
		m.status = status
		return
	}
	app.next++
	m := &message{id: app.next, kind: msgTool, name: name, text: clipToolText(detail, 80), status: status}
	app.tools[id] = m
	app.messages = append(app.messages, m)
}

// applyApprovalNotice turns a real approval:request / approval:resolved notice
// into the existing approval card, replacing the canned demo approval.
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
		app.approval = &approval{id: id, name: tool, args: args}
		app.status = "需要批准: " + tool
	case "resolved":
		if app.approval != nil && (app.approval.id == id || app.approval.name == tool) {
			app.approval = nil
		}
		status, note := "done", "✓ 已允许 "+tool
		if !granted {
			status, note = "failed", "✗ 已拒绝 "+tool
		}
		app.next++
		app.messages = append(app.messages, &message{
			id: app.next, kind: msgTool, name: tool, text: note, status: status, done: true,
		})
		app.status = note
	}
}

// clipToolText keeps a tool card to one readable line without splitting UTF-8.
func clipToolText(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// finish completes the current reply; it always runs on the main thread.
func (app *chat) finish(src string) {
	if app.reply != nil {
		app.reply.text, app.reply.done = src, true
	}
	if app.tool != nil {
		app.tool.status = "done"
	}
	app.streaming = false
	app.reply, app.tool = nil, nil
	app.status = "已完成"
	// The demo approval only belongs to the canned fallback. A real engine
	// handles approvals over its own transport, not through a fake prompt.
	if app.engine == nil && app.sent == 1 {
		app.approval = &approval{
			name: "write_file",
			args: "{\"path\": \"~/my-project/src/main.go\", \"lines\": 12}",
		}
	}
}

// retireStream invalidates the active streaming round so a late delta or prompt
// result cannot resurrect UI state after stop/undo/newSession. It goes through
// the bridge's locked setDeltaHandler API, so bridge.go's race handling is not
// modified: readLoop snapshots the nil handler and completeRealReply's generation
// check rejects the superseded completion.
func (app *chat) retireStream() {
	if app.engine != nil {
		app.engine.setDeltaHandler(nil)
	}
}

func (app *chat) stop() {
	if app.engine != nil {
		_ = app.engine.abort()
	}
	// Abort alone is not enough: the in-flight prompt's completion still carries
	// the old generation. Retire it before clearing reply state so a late result
	// cannot run finish()/finishEngineError() and undo the user's stop/undo.
	app.retireStream()
	if app.reply != nil {
		app.reply.done = true
	}
	if app.tool != nil {
		app.tool.status = "failed"
	}
	app.streaming = false
	app.reply, app.tool = nil, nil
	app.status = "已停止"
}

// undo removes the most recent turn as a unit: the user message, its assistant
// reply and any trailing tool cards. Deleting only the last row used to leave an
// orphan user message whose reply had vanished. The prompt text is kept in
// lastPrompt so a following retry can replay it.
func (app *chat) undo() {
	if app.streaming {
		app.stop()
	}
	// Find the last user message; everything from there to the end is one turn.
	start := -1
	for i := len(app.messages) - 1; i >= 0; i-- {
		if app.messages[i].kind == msgUser {
			start = i
			break
		}
	}
	if start < 0 {
		app.status = "没有可撤销的消息"
		return
	}
	app.messages = app.messages[:start]
	app.approval = nil
	app.status = "已撤销上一轮"
}

// retry re-sends the most recent user prompt after dropping its turn, so the
// model gets another chance at the same question. It is a no-op while streaming
// or when nothing has been asked yet.
func (app *chat) retry() {
	if app.streaming {
		app.status = "正在生成，无法重试"
		return
	}
	prompt := app.lastPrompt
	if prompt == "" {
		app.status = "没有可重试的消息"
		return
	}
	app.undo()
	app.status = "已重试上一轮"
	app.send(prompt)
}

func (app *chat) toggleTheme() {
	app.light = !app.light
	if app.light {
		app.status = "已切换到浅色主题"
	} else {
		app.status = "已切换到深色主题"
	}
}

func (app *chat) answerApproval(allow bool) {
	if app.approval == nil {
		return
	}
	ap := app.approval
	if app.engine == nil {
		// Demo approval: no engine to answer, keep the original local-only
		// behavior (record the decision as a tool result row).
		app.approval = nil
		app.recordApprovalResult(ap.name, allow)
		return
	}
	// Real engine approval: send the decision back over RPC. Keep the card
	// open until the engine accepts it, so a failed call can be retried from
	// the same UI. The engine reports the true outcome via an
	// approval:resolved notice, which appends the result row.
	call := app.approvalAnswerRPC
	if call == nil {
		call = app.engine.call
	}
	if _, err := call("approval.answer", map[string]any{"id": ap.id, "granted": allow}); err != nil {
		app.status = "审批回应失败：" + engineErrorNotice(err)
		return
	}
	app.approval = nil
	app.status = "已响应，等待引擎确认"
}

// recordApprovalResult appends the local tool result row for a demo approval,
// where no engine is involved and the decision is final.
func (app *chat) recordApprovalResult(name string, allow bool) {
	app.next++
	status, text := "done", "✓ 已允许 "+name
	if !allow {
		status, text = "failed", "✗ 已拒绝 "+name
	}
	app.messages = append(app.messages, &message{
		id: app.next, kind: msgTool, name: name, text: text, status: status, done: true,
	})
	app.status = text
}

func (app *chat) newSession() {
	// A previous round may still be in flight; drop its late result so it cannot
	// overwrite the "已新建会话" status or re-add a stale reply.
	app.retireStream()
	app.messages = nil
	app.tools = nil
	app.approval = nil
	app.streaming = false
	app.reply, app.tool = nil, nil
	app.lastPrompt = "" // a fresh session must not replay the previous one's prompt
	app.selectedSession = ""
	app.status = "已新建会话"
}

var cannedReplies = []string{
	"## 已经处理好了\n\n我读懂了你的问题。这是一条 **原生 Markdown** 回复：\n\n" +
		"- 支持列表\n- 支持 `代码` 与[链接](https://example.com)\n\n" +
		"```go\nfmt.Println(\"hello, argentgo\")\n```\n\n" +
		"> 流式输出与 Markdown 渲染都由 ui/markdown 完成。\n",
	"## 分析完成\n\n| 项目 | 状态 |\n|---|---|\n| 会话 | 就绪 |\n| 工具 | 已批准 |\n\n" +
		"1. 读取上下文\n2. 生成回复\n\n*继续提问吧。*\n",
}

func main() {
	app := &chat{status: "就绪", nav: "聊天"}

	// ARGENT_EXE points at the engine binary. When set, the native window
	// drives the real agent over --rpc; otherwise it runs the canned demo.
	if exe := os.Getenv("ARGENT_EXE"); exe != "" {
		cwd := os.Getenv("ARGENT_CWD")
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		b, err := startBridge(exe, cwd, nil, app.onEngineEvent)
		if err != nil {
			log.Printf("native: engine bridge unavailable: %v (using demo mode)", err)
		} else {
			app.engine = b
			app.status = "已连接引擎"
			defer b.close()
			// Consume real agent events so tool cards and approvals reflect the
			// engine instead of canned demo data. If the engine has no event
			// hub, subscription fails and the demo fallback stays intact.
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
