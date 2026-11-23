package tui

import "strings"

// welcomeMark 是首屏欢迎卡的固定前缀，与通知徽章 "! " 区分，避免让用户误以为
// 欢迎卡是一条警告。它不进入任何事件或会话记录，只参与首帧绘制。
const welcomeMark = "✦ "

// welcomeStrings 返回按 locale 选择的欢迎卡四行文案：一句话定位、示例 prompt、
// 快捷键入口与撤销提示。示例 prompt 刻意选只读+测试的组合，新用户直接回车也安全。
func welcomeStrings(locale string) []string {
	if strings.HasPrefix(strings.ToLower(locale), "zh") {
		return []string{
			"argentgo：会读代码、改代码、跑测试的终端编码助手",
			"试试：读一下 main.go，给 parseArgs 补一个单元测试",
			"输入 /help 查看全部快捷键",
			"Alt+U 撤销上一轮",
		}
	}
	return []string{
		"argentgo: a coding agent that reads, edits and tests your code",
		"Try: read main.go and add a unit test for parseArgs",
		"Type /help to see all shortcuts",
		"Alt+U to undo the last turn",
	}
}

// welcomeLinesLocked 把欢迎卡渲染成若干行。它只在记录为空时被调用，
// 因此不会进入 entryCache，也不影响思考块的行范围映射。
func (m *Model) welcomeLinesLocked(width int) []Line {
	inner := maxInt(width-TextWidth(welcomeMark), 1)
	var out []Line
	for _, s := range welcomeStrings(m.locale) {
		for _, seg := range Wrap(s, inner) {
			line := NewLine()
			line.Push(welcomeMark, m.palette.accent())
			line.Push(seg, m.palette.muted())
			out = append(out, line)
		}
	}
	// 末尾留一空行，与随后到来的第一条用户消息分开。
	out = append(out, NewLine())
	return out
}
