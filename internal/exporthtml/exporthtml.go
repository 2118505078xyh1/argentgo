// Package exporthtml 把会话转录渲染为**单文件自包含 HTML**（内联 CSS/JS，
// 零外链），用于离线分享、复盘与 bug 报告。
//
// 与 session.RenderHTML 的简易导出不同，本包提供：按角色的消息气泡、工具调用
// 卡片默认折叠（点击展开完整输出）、推理块折叠、时间戳、以及严格的 HTML 转义
// 防 XSS。输出是单个 html 文档，可在离线浏览器打开，无网络请求。
//
// 纯标准库（html/template + io），无 cgo、无新依赖。调用方负责把 session.Entry
// 转成 Message（保持本包与 session 解耦，便于独立测试）。
package exporthtml

import (
	"html/template"
	"io"
	"strings"
	"time"
)

// Role 是消息角色。
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
	RoleSystem    Role = "system"
)

// Message 是一条可导出的消息。Content 可以是纯文本或 Markdown 子集（本包做
// 轻量渲染，不引入 Markdown 库）。
type Message struct {
	ID        string
	Role      Role
	Content   string
	ToolName  string // Role==tool 时的工具名
	Reasoning string // 可选思考文本，折叠显示
	TS        time.Time
}

// Options 控制渲染。
type Options struct {
	Title         string
	ShowReasoning bool
	FoldTools     bool // 默认 true：工具卡片折叠
}

// pageData 是模板数据。
type pageData struct {
	Title    string
	Messages []viewMessage
}

// viewMessage 是模板可见的消息视图（Content 已做 HTML 转义或安全渲染）。
type viewMessage struct {
	ID           string
	RoleClass    string
	RoleLabel    string
	Content      template.HTML // 已经过 renderContent 安全处理
	ToolName     string
	HasTool      bool
	Reasoning    string
	HasReasoning bool
	Time         string
	FoldTools    bool
}

// Render 把消息列表渲染为单文件 HTML，写入 w（流式，内存有界）。
func Render(w io.Writer, messages []Message, opts Options) error {
	if opts.Title == "" {
		opts.Title = "argentgo session"
	}
	fold := opts.FoldTools // 注意：false 是调用方显式要求展开
	data := pageData{Title: opts.Title}
	for _, m := range messages {
		vm := viewMessage{
			ID:           m.ID,
			RoleClass:    roleClass(m.Role),
			RoleLabel:    roleLabel(m.Role),
			Content:      renderContent(m.Content),
			ToolName:     m.ToolName,
			HasTool:      m.Role == RoleTool && m.ToolName != "",
			Reasoning:    m.Reasoning,
			HasReasoning: opts.ShowReasoning && m.Reasoning != "",
			Time:         formatTime(m.TS),
			FoldTools:    fold,
		}
		data.Messages = append(data.Messages, vm)
	}
	return pageTemplate.Execute(w, data)
}

func roleClass(r Role) string {
	switch r {
	case RoleUser:
		return "user"
	case RoleAssistant:
		return "assistant"
	case RoleTool:
		return "tool"
	default:
		return "system"
	}
}

func roleLabel(r Role) string {
	switch r {
	case RoleUser:
		return "你"
	case RoleAssistant:
		return "argentgo"
	case RoleTool:
		return "工具"
	default:
		return "系统"
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("01-02 15:04:05")
}

// renderContent 对内容做安全渲染：先 HTML 转义，再把换行转成 <br>，
// 并把 ``` 围栏转成 <pre>（内容仍已转义）。不引入 Markdown 库。
func renderContent(s string) template.HTML {
	// html/template 的 template.HTMLEscapeString 做严格转义。
	escaped := template.HTMLEscapeString(s)
	var b strings.Builder
	inFence := false
	for _, line := range strings.Split(escaped, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if inFence {
				b.WriteString("</pre>")
				inFence = false
			} else {
				b.WriteString("<pre>")
				inFence = true
			}
			b.WriteString("\n")
			continue
		}
		b.WriteString(line)
		if inFence {
			b.WriteString("\n")
		} else {
			b.WriteString("<br>\n")
		}
	}
	if inFence {
		b.WriteString("</pre>")
	}
	return template.HTML(b.String())
}

var pageTemplate = template.Must(template.New("page").Parse(pageHTML))

const pageHTML = `<!doctype html>
<html lang="zh"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}}</title>
<style>
:root{--bg:#fafafa;--card:#fff;--ink:#1c1c1e;--muted:#8e8e93;--line:#e5e5ea;--purple:#7c5cfc;--pbg:#f0ecff;--ok:#34a853;--warn:#f9ab00}
@media(prefers-color-scheme:dark){:root{--bg:#18181b;--card:#232327;--ink:#f2f2f5;--muted:#9a9aa2;--line:#34343a;--purple:#9b8aff;--pbg:#2e2642}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}
.wrap{max-width:860px;margin:0 auto;padding:24px 16px 64px}h1{font-size:20px;margin:0 0 20px}
.msg{margin:12px 0;border:1px solid var(--line);border-radius:12px;padding:12px 14px;background:var(--card)}
.msg.user{background:var(--pbg);border-color:var(--purple);margin-left:12%}
.msg.assistant{margin-right:12%}.msg.tool{border-style:dashed}.msg.system{opacity:.8}
.meta{display:flex;justify-content:space-between;font-size:12px;color:var(--muted);margin-bottom:6px}
.role{font-weight:600;color:var(--purple)}.time{font-variant-numeric:tabular-nums}
pre{white-space:pre-wrap;word-break:break-word;background:var(--bg);border:1px solid var(--line);border-radius:8px;padding:8px;overflow:auto}
details{margin-top:8px}summary{cursor:pointer;color:var(--purple);font-size:12px}
.reason{color:var(--muted);font-size:12px;font-style:italic}
.toolname{color:var(--ok);font-family:ui-monospace,monospace;font-size:12px}
</style></head><body><div class="wrap"><h1>{{.Title}}</h1>
{{range .Messages}}<div class="msg {{.RoleClass}}">
<div class="meta"><span class="role">{{.RoleLabel}}{{if .HasTool}} · <span class="toolname">{{.ToolName}}</span>{{end}}</span><span class="time">{{.Time}}</span></div>
{{if .HasReasoning}}<details><summary>思考过程</summary><div class="reason">{{.Reasoning}}</div></details>{{end}}
{{if and .HasTool .FoldTools}}<details><summary>展开工具输出</summary><div class="content">{{.Content}}</div></details>
{{else}}<div class="content">{{.Content}}</div>{{end}}
</div>{{end}}</div>
<script>document.querySelectorAll('details').forEach(function(d){d.addEventListener('toggle',function(){});});</script>
</body></html>`
