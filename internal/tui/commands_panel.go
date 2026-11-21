package tui

import "strconv"

// commandHints 按命令名建立参数提示表。同名命令取第一条，保持
// 侧栏与浮层的渲染确定。
func commandHints(infos []CommandInfo) map[string]string {
	hints := make(map[string]string, len(infos))
	for _, info := range infos {
		if _, ok := hints[info.Command]; !ok {
			hints[info.Command] = info.ArgumentHint
		}
	}
	return hints
}

// commandCandidateLine 渲染一条候选。选中项用 "> " 标记并把整行反白；
// 未选中项保持历史的两空格缩进，因此关闭浮层时侧栏的旧行为不变。
// 所有文本经 Clip 按字素簇裁剪，CJK 与 emoji 不会被从中间切开。
func commandCandidateLine(c CompletionCandidate, hint string, selected bool, width int, p Palette) Line {
	mark, style := "  ", p.muted()
	if selected {
		mark = "> "
		style = p.accent()
		style.Reverse = true
	}
	line := NewLine()
	line.Push(mark, style)
	label := "/" + c.Command
	if hint != "" {
		label += " " + hint
	}
	if c.Description != "" {
		label += "  " + c.Description
	}
	line.Push(Clip(label, maxInt(width-2, 1)), style)
	return line
}

// commandsCandidateLines 渲染从 start 开始的候选行，cursor 是绝对选中
// 下标。侧栏与输入浮层共用它，避免两处的选中态发生漂移。
func (m *Model) commandsCandidateLines(visible []CompletionCandidate, hints map[string]string, width, height, cursor, start int) []Line {
	var out []Line
	for i := start; i < len(visible) && len(out) < height; i++ {
		c := visible[i]
		out = append(out, commandCandidateLine(c, hints[c.Command], i == cursor, width, m.palette))
	}
	return out
}

// commandsLines 在可选侧边栏中渲染斜杠命令补全候选：
// 标题 + 计数 + 分隔线 + 逐行候选 + "(选中/总数)"。与任务/会话栏共用
// 几何，因此渲染器无需额外的布局分支。
func (m *Model) commandsLines(width, height int) []Line {
	visible := m.visibleCommands()
	hints := commandHints(m.commandInfo)

	title := NewLine()
	title.Push(text(m.locale, "commands_title"), m.palette.accent())
	title.Push(" "+strconv.Itoa(len(visible)), m.palette.muted())
	out := []Line{clipLine(title, maxInt(width, 1)), plainPanelRule(width)}

	if len(visible) == 0 {
		if len(out) < height {
			hint := NewLine()
			hint.Push(Clip(text(m.locale, "no_commands"), width), m.palette.dim())
			out = append(out, hint)
		}
		return out
	}

	cursor := clampScroll(m.commandsCursor, maxInt(len(visible)-1, 0))
	start := clampScroll(m.commandsScroll, maxInt(len(visible)-1, 0))
	out = append(out, m.commandsCandidateLines(visible, hints, width, maxInt(height-len(out), 0), cursor, start)...)
	if len(out) < height {
		count := "(" + strconv.Itoa(cursor+1) + "/" + strconv.Itoa(len(visible)) + ")"
		out = append(out, clipLine(PlainLine(count), maxInt(width, 1)))
	}
	return out
}

// visibleCommands 返回当前查询下的候选。补全状态未注入时为空列表，
// 因此仅按下 Ctrl+O 而没有提供元数据时面板显示"暂无命令"。
func (m *Model) visibleCommands() []CompletionCandidate {
	if m.completion == nil {
		return nil
	}
	return m.completion.Visible()
}
