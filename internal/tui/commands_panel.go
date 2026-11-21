package tui

// commandsLines 在可选侧边栏中渲染斜杠命令补全候选：
// 标题 + 计数 + 分隔线 + 逐行候选。与任务/会话栏共用几何，因此
// 渲染器无需额外的布局分支。所有文本经 Clip 按字素簇裁剪，
// CJK 与 emoji 绝不会被从中间切开。
func (m *Model) commandsLines(width, height int) []Line {
	visible := m.visibleCommands()

	// 补全候选只带 Command/Description；参数提示由 commandInfo 按命令名
	// 补齐。同名命令取第一条，保持渲染确定。
	hints := make(map[string]string, len(m.commandInfo))
	for _, info := range m.commandInfo {
		if _, ok := hints[info.Command]; !ok {
			hints[info.Command] = info.ArgumentHint
		}
	}

	title := NewLine()
	title.Push(text(m.locale, "commands_title"), m.palette.accent())
	title.Push(" "+itoa(len(visible)), m.palette.muted())
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
	for i := start; i < len(visible) && len(out) < height; i++ {
		c := visible[i]
		selected := i == cursor
		mark := "  "
		if selected {
			mark = "› "
		}
		line := NewLine()
		line.Push(mark, m.palette.accent())

		label := "/" + c.Command
		if hint := hints[c.Command]; hint != "" {
			label += " " + hint
		}
		if c.Description != "" {
			label += "  " + c.Description
		}
		style := m.palette.muted()
		if selected {
			style = m.palette.accent()
		}
		line.Push(Clip(label, maxInt(width-2, 1)), style)
		out = append(out, line)
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
