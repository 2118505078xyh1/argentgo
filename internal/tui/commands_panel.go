package tui

import (
	"strconv"
	"strings"
)

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
// commandPrimaryWidth 计算命令主列宽度：取所有候选的 "/name hint" 最大显示
// 宽度，钳制到 [12,32]，与 pi/omp 的两列布局一致。窄终端（主列+描述放不下）
// 由调用方退化为单列（descWidth=0）。
func commandPrimaryWidth(visible []CompletionCandidate, hints map[string]string) int {
	maxw := 12
	for _, c := range visible {
		primary := "/" + c.Command
		if h := strings.TrimSpace(hints[c.Command]); h != "" {
			primary += " " + h
		}
		if w := TextWidth(primary); w > maxw {
			maxw = w
		}
	}
	return minInt(maxw, 32)
}

// commandCandidateLine 渲染单个候选为"两列对齐"一行：
//
//	> /name <args>  description
//
// 主列（命令名+参数提示）与描述列分别在固定起始位置，描述不随选中反白，
// 因此长短命令名混排时也整齐。descWidth<=0 时退化为单列（只显示主列）。
func commandCandidateLine(c CompletionCandidate, hint string, selected bool, width, primaryWidth int, p Palette) Line {
	mark, style := "  ", p.muted()
	if selected {
		mark = "> "
		style = p.accent()
		style.Reverse = true
	}
	line := NewLine()
	line.Push(mark, style)
	primary := "/" + c.Command
	if h := strings.TrimSpace(hint); h != "" {
		primary += " " + h
	}
	// Pad 而非 Clip：不足主列宽时补空格，使描述列在所有行统一起始位置。
	primary = Pad(primary, maxInt(primaryWidth, 1))
	line.Push(primary, style)

	descStyle := p.muted() // 描述列独立着色，不随选中反白
	if descWidth := width - 2 - primaryWidth - 2; descWidth > 0 && c.Description != "" {
		line.Push("  ", style)
		line.Push(Clip(c.Description, descWidth), descStyle)
	}
	return line
}

// commandsCandidateLines 渲染从 start 开始的候选行，cursor 是绝对选中
// 下标。侧栏与输入浮层共用它，避免两处的选中态发生漂移。先算一次主列宽，
// 使所有候选的描述列对齐。
func (m *Model) commandsCandidateLines(visible []CompletionCandidate, hints map[string]string, width, height, cursor, start int) []Line {
	primary := commandPrimaryWidth(visible, hints)
	// 主列+两空格+至少一单元格描述放不下时退化为单列。
	if 2+primary+2+1 > width {
		primary = maxInt(width-4, 1)
	}
	var out []Line
	for i := start; i < len(visible) && len(out) < height; i++ {
		c := visible[i]
		out = append(out, commandCandidateLine(c, hints[c.Command], i == cursor, width, primary, m.palette))
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
