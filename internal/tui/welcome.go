package tui

import "strings"

// welcomeMark 是首屏欢迎卡的固定前缀，与通知徽章 "! " 区分，避免让用户误以为
// 欢迎卡是一条警告。它不进入任何事件或会话记录，只参与首帧绘制。
const welcomeMark = "✦ "

// welcomeVersion 与权威的 cli.Version 保持一致。TUI 不能反向 import cli（会成环），
// 改版本号时由 internal/cli/version_consistency_test.go 同时守护这两处字面量。
const welcomeVersion = "0.8.2"

// welcomeText 返回按 locale 选择的单行欢迎文案：先介绍 argentgo 的版本，
// 再用一句话说明它是什么、入口在哪。刻意不再堆示例 prompt、快捷键和撤销提示，
// 把首屏空间留给对话本身。
func welcomeText(locale string) string {
	if strings.HasPrefix(strings.ToLower(locale), "zh") {
		return "argentgo " + welcomeVersion + "：通用 AI 助手，输入 /help 查看全部命令"
	}
	return "argentgo " + welcomeVersion + ": a general-purpose AI assistant. Type /help for all commands"
}

// welcomeLinesLocked 把欢迎卡渲染成恰好一行。它只在记录为空时被调用，
// 因此不会进入 entryCache，也不影响思考块的行范围映射。
func (m *Model) welcomeLinesLocked(width int) []Line {
	inner := maxInt(width-TextWidth(welcomeMark), 1)
	line := NewLine()
	line.Push(welcomeMark, m.palette.accent())
	// 窄终端下截断而不是折行，保证欢迎信息永远只占一行。
	line.Push(Truncate(welcomeText(m.locale), inner), m.palette.muted())
	return []Line{line}
}
