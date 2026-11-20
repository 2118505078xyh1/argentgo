package tui

import "strings"

// panelSection 是右侧栏的一个垂直切片。
type panelSection struct {
	top, height int
	lines       []Line
}

func clampScroll(value, max int) int {
	if value < 0 {
		return 0
	}
	if max < 0 {
		return 0
	}
	if value > max {
		return max
	}
	return value
}

func plainPanelRule(width int) Line {
	line := NewLine()
	line.Push(strings.Repeat("─", maxInt(width, 1)), Style{})
	return line
}

func (m *Model) panelsVisible() bool {
	return m.showTasks || m.showWorkspace || m.showPlan || m.showSessions
}

// panelSections 在当前开启的面板之间划分侧边栏。
// 记录、提示和审批弹窗保持其现有几何。
func (m *Model) panelSections(l layout) []panelSection {
	var kinds []int
	if m.showTasks {
		kinds = append(kinds, 0)
	}
	if m.showWorkspace {
		kinds = append(kinds, 1)
	}
	if m.showPlan {
		kinds = append(kinds, 2)
	}
	if m.showSessions {
		kinds = append(kinds, 3)
	}
	if len(kinds) == 0 {
		return nil
	}
	base := l.bodyH / len(kinds)
	extra := l.bodyH % len(kinds)
	var out []panelSection
	top := l.bodyTop
	for i, kind := range kinds {
		remaining := l.bodyTop + l.bodyH - top
		if remaining <= 0 {
			break
		}
		height := base
		if i < extra {
			height++
		}
		if height > remaining {
			height = remaining
		}
		var lines []Line
		switch kind {
		case 0:
			lines = m.tasksLines(l.panelW, height)
		case 1:
			lines = m.workspaceLines(l.panelW, height)
		case 2:
			lines = m.planLines(l.panelW, height)
		case 3:
			lines = m.sessionsLines(l.panelW, height)
		}
		out = append(out, panelSection{top: top, height: height, lines: lines})
		top += height
	}
	return out
}

// paintPanels 绘制分隔符和每个可见面板。Buffer.Flush 按
// 整行做差异比较，因此更新的任务或预览只重绘受影响的行。
func (m *Model) paintPanels(buf *Buffer, l layout) {
	if l.panelW <= 0 || l.panelX <= 0 {
		return
	}
	border := m.palette.border()
	for y := l.bodyTop; y < l.bodyTop+l.bodyH && y < buf.Height(); y++ {
		buf.Set(l.panelX-1, y, "│", border)
	}
	for _, section := range m.panelSections(l) {
		for i, line := range section.lines {
			if i >= section.height {
				break
			}
			m.paintLineAt(buf, section.top+i, l.panelX, line)
		}
	}
}
