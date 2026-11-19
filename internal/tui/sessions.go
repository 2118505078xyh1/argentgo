package tui

// sessionsLines renders generated session titles in the optional side rail.
// The rail reuses the same geometry as the tasks / workspace / plan panels, so
// no extra layout branch is needed in the renderer.
func (m *Model) sessionsLines(width, height int) []Line {
	title := NewLine()
	title.Push(text(m.locale, "sessions_title"), m.palette.accent())
	title.Push(" "+itoa(len(m.sessions)), m.palette.muted())
	out := []Line{title, plainPanelRule(width)}

	start := clampScroll(m.sessionsScroll, maxInt(len(m.sessions)-1, 0))
	for i := start; i < len(m.sessions) && len(out) < height; i++ {
		row := m.sessions[i]
		mark := "  "
		if i == m.sessionsCursor {
			mark = "› "
		}
		line := NewLine()
		line.Push(mark, m.palette.accent())
		label := row.Title
		if label == "" {
			label = row.Path
		}
		line.Push(Clip(label, maxInt(width-4, 1)), Style{Bold: i == m.sessionsCursor})
		out = append(out, line)
	}
	if len(out) == 2 && height > 2 {
		hint := NewLine()
		hint.Push(Clip(text(m.locale, "no_sessions"), width), m.palette.dim())
		out = append(out, hint)
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
