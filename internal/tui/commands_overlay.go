package tui

import (
	"strconv"
	"strings"
)

// completionOverlayMaxRows 限制浮层一次显示的候选数，避免吃掉整个记录区。
const completionOverlayMaxRows = 8

// paintCompletionOverlay 在输入框上方绘制 `/` 补全面板。它复用侧栏的
// 候选行渲染（含 "> " 标记与选中反白），并用 paintInput 同款 ╭╮╰╯ 边框
// 包住。没有候选时清空命中框并返回，因此默认帧与按 Ctrl+O 之前的
// 经典行为逐字节不变。
//
// 命中框只在绘制时更新，不触碰 dirty：它不影响输出字节，只服务于
// 下一次 MouseDown 的命中测试。
func (m *Model) paintCompletionOverlay(buf *Buffer, l layout) {
	m.completionHits = m.completionHits[:0]
	if !m.completionOverlay || m.pending != nil {
		return
	}
	visible := m.visibleCommands()
	if len(visible) == 0 {
		return
	}
	// 两行边框 + 一行计数；剩余空间才留给候选。
	avail := l.inputTop - l.bodyTop
	maxCand := minInt(avail-3, completionOverlayMaxRows)
	if maxCand < 1 {
		return
	}

	width := l.width
	cursor := clampScroll(m.commandsCursor, maxInt(len(visible)-1, 0))
	start := clampScroll(m.commandsScroll, maxInt(len(visible)-1, 0))
	if cursor < start {
		start = cursor
	}
	if cursor >= start+maxCand {
		start = cursor - maxCand + 1
	}
	cands := m.commandsCandidateLines(visible, commandHints(m.commandInfo), maxInt(width-4, 1), maxCand, cursor, start)
	total := len(cands) + 3
	top := l.inputTop - total
	if top < l.bodyTop {
		top = l.bodyTop
	}

	border := m.palette.border()
	edge := func(left, right string) Line {
		line := NewLine()
		line.Push(left+strings.Repeat("─", maxInt(width-2, 0))+right, border)
		return line
	}
	m.paintLine(buf, top, edge("╭", "╮"))
	for i, line := range cands {
		row := top + 1 + i
		boxed := NewLine()
		boxed.Push("│ ", border)
		for _, span := range line.Spans {
			boxed.PushSpan(span)
		}
		boxed.Fill(maxInt(width-2, 1), Style{})
		boxed.Push(" │", border)
		m.paintLine(buf, row, boxed)
		m.completionHits = append(m.completionHits, completionHit{
			index: start + i, row: row, colFrom: 1, colTo: maxInt(width-2, 1),
		})
	}

	countRow := top + 1 + len(cands)
	count := NewLine()
	count.Push("│ ", border)
	count.Push("("+strconv.Itoa(cursor+1)+"/"+strconv.Itoa(len(visible))+")", m.palette.dim())
	count.Fill(maxInt(width-2, 1), Style{})
	count.Push(" │", border)
	m.paintLine(buf, countRow, count)
	m.paintLine(buf, countRow+1, edge("╰", "╯"))
}
