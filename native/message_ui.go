package main

import (
	"strings"

	"github.com/egoist/mygo"
)

// 本文件承载消息行的交互增强：工具卡片展开、消息操作条（复制/重试）、
// 欢迎态示例 prompt。所有能力都通过 chat 的可变状态 + 每帧重建视图实现，
// 不引入任何新依赖（native 仅依赖 mygo）。

// copyToClipboard 是剪贴板写入的测试接缝。真实窗口写入系统剪贴板；
// 测试替换为记录器，避免在未 App.Run 的线程里触发 mygo 的 needsApp 断言。
var copyToClipboard = func(s string) { mygo.Clipboard.WriteText(s) }

// examplePrompts 是欢迎态的示例问题，点击后种入输入框（用户零选择即可上手）。
var examplePrompts = []string{
	"审查这段 Go 代码的并发问题",
	"解释这个报错并给出修复",
	"为当前模块补一组单元测试",
}

func init() {
	// 新文案注册进共享英文表，避免改动由另一代理负责的 locale.go。
	nativeEnglish["复制"] = "Copy"
	nativeEnglish["重试"] = "Retry"
	nativeEnglish["复制输出"] = "Copy output"
	nativeEnglish["↓ 跳到最新"] = "↓ Jump to latest"
	nativeEnglish["(无输出)"] = "(no output)"
	nativeEnglish["等待输出…"] = "waiting for output…"
	nativeEnglish["试试这样问："] = "Try asking:"
	nativeEnglish["已复制消息"] = "Message copied"
	nativeEnglish["已复制工具输出"] = "Tool output copied"
	nativeEnglish["审查这段 Go 代码的并发问题"] = "Review this Go code for concurrency issues"
	nativeEnglish["解释这个报错并给出修复"] = "Explain this error and suggest a fix"
	nativeEnglish["为当前模块补一组单元测试"] = "Write unit tests for the current module"
}

// toolCardSummary 生成工具卡片的折叠摘要：取正文首个非空行，折叠空白后
// 限制到 80 个 rune。工具输出常是多行日志/diff，首行通常是命令、路径或
// 状态，足以在单行内说明这条卡片是什么，完整内容留给展开态。
func toolCardSummary(m *message) string {
	if strings.TrimSpace(m.text) == "" {
		if m.status == "running" {
			return tr("等待输出…")
		}
		return tr("(无输出)")
	}
	summary := ""
	for _, line := range strings.Split(m.text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			summary = strings.Join(strings.Fields(line), " ")
			break
		}
	}
	return clipToolText(summary, 80)
}

// ownerUserIndex 返回消息所属回合的用户消息下标。用户消息自身即拥有者；
// 助手/工具消息向前找最近的用户消息。找不到时返回 -1。
func ownerUserIndex(app *chat, m *message) int {
	idx := -1
	for i, mm := range app.messages {
		if mm == m {
			idx = i
			break
		}
	}
	if idx < 0 {
		return -1
	}
	if m.kind == msgUser {
		return idx
	}
	for i := idx - 1; i >= 0; i-- {
		if app.messages[i].kind == msgUser {
			return i
		}
	}
	return -1
}

// retryMessage 重发指定消息所属回合的用户提示。它把该用户消息及其后的
// 回合从 transcript 移除，再走常规 send 流程，因此工具栏的全局重试与每
// 条消息的重试共用同一条发送路径。
func (app *chat) retryMessage(m *message) {
	if app.streaming {
		app.status = tr("正在生成，无法重试")
		return
	}
	idx := ownerUserIndex(app, m)
	if idx < 0 {
		app.status = tr("没有可重试的消息")
		return
	}
	prompt := app.messages[idx].text
	app.messages = app.messages[:idx]
	app.approval = nil
	app.lastPrompt = prompt
	app.status = tr("已重试上一轮")
	app.send(prompt)
}

// copyText 把文本写入剪贴板并回执到状态栏。空文本不写，避免出现
// “已复制”却剪贴板没变化的假反馈。
func (app *chat) copyText(s, okStatus string) {
	if s == "" {
		return
	}
	copyToClipboard(s)
	app.status = okStatus
}
