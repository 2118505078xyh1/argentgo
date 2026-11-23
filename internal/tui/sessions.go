package tui

import (
	"strings"
	"unicode/utf8"
)

// sessionsLines 在可选侧边栏中渲染生成的会话标题。
// 该栏复用与任务/工作区/计划面板相同的几何，因此
// 渲染器中无需额外的布局分支。活动过滤器在标题下方添加一条
// 栏，然后只按相关性顺序列出匹配的会话。
func (m *Model) sessionsLines(width, height int) []Line {
	matches := m.sessionMatches()

	title := NewLine()
	title.Push(text(m.locale, "sessions_title"), m.palette.accent())
	title.Push(" "+itoa(len(m.sessions)), m.palette.muted())
	out := []Line{title}

	// 让过滤栏紧贴在标题下方，使其即使在
	// 侧边栏只有几行高。焦点用强调样式和
	// 插入符表示；已确认但未聚焦的过滤栏保持暗色。
	if m.sessionFiltering || strings.TrimSpace(m.sessionFilter) != "" {
		bar := NewLine()
		bar.Push("> ", m.palette.muted())
		style := m.palette.muted()
		if m.sessionFiltering {
			style = m.palette.accent()
		}
		bar.Push(Clip(m.sessionFilter, maxInt(width-4, 1)), style)
		if m.sessionFiltering {
			bar.Push("▌", m.palette.accent())
		}
		out = append(out, bar)
	}
	out = append(out, plainPanelRule(width))

	start := clampScroll(m.sessionsScroll, maxInt(len(matches)-1, 0))
	rendered := 0
	for rank := start; rank < len(matches) && len(out) < height; rank++ {
		match := matches[rank]
		selected := match.index == m.sessionsCursor
		mark := "  "
		if selected {
			mark = "› "
		}
		line := NewLine()
		line.Push(mark, m.palette.accent())
		m.pushFilterLabel(&line, sessionLabel(m.sessions[match.index]), match.positions, selected, maxInt(width-4, 1))
		out = append(out, line)
		rendered++
	}
	// "no sessions yet" 仅在侧边栏真正为空时才真实。
	// 一个排除所有行的过滤是不同状态 —— 上方的标题
	// 已显示真实会话数，过滤栏显示查询
	// —— 因此在这里宣称没有会话会产生误导。当过滤活动时，
	// 分隔线下方的空列表是不言自明的。
	if rendered == 0 && len(out) < height && len(m.sessions) == 0 {
		hint := NewLine()
		hint.Push(Clip(text(m.locale, "no_sessions"), width), m.palette.dim())
		out = append(out, hint)
	}
	return out
}

// sessionLabel 是过滤和高亮操作的文本：生成的
// 标题，未命名的会话回退到路径。
func sessionLabel(row SessionInfo) string {
	if row.Title != "" {
		return row.Title
	}
	return row.Path
}

// sessionFuzzy 把 FuzzyMatch 适配为 MatchPattern 期望的回调形状。
func sessionFuzzy(pattern, text string) (int, []int, bool) {
	return FuzzyMatch(pattern, text, false)
}

// sessionMatch 是过滤后侧边栏的一行。index 保持原始的
// m.sessions 下标，使现有的光标/滚动状态保持意义；
// score 和 positions 驱动相关性顺序和字符高亮。
type sessionMatch struct {
	index     int
	score     int
	positions []int
}

// firstFilterPattern 提取第一个正项正文用于评分和
// 高亮。操作符和取反项仍影响 MatchPattern 的
// 是/否判定；只有第一个正项正文驱动视觉强调。
func firstFilterPattern(terms []SearchTerm) string {
	for _, term := range terms {
		if term.Kind == TermOr || term.Kind == TermInverse || term.Text == "" {
			continue
		}
		return term.Text
	}
	return ""
}

// sessionMatches 返回满足当前过滤的会话，按相关性
// 排序。空（或仅空白）的查询按
// 原始顺序返回所有会话：即过滤前的渲染基线。
func (m *Model) sessionMatches() []sessionMatch {
	terms := ParsePattern(m.sessionFilter)
	highlight := firstFilterPattern(terms)
	matches := make([]sessionMatch, 0, len(m.sessions))
	for i, row := range m.sessions {
		if m.sessionNamedOnly && strings.TrimSpace(row.Title) == "" {
			continue
		}
		label := sessionLabel(row)
		if !MatchPattern(terms, label, sessionFuzzy) {
			continue
		}
		score, positions := 0, []int(nil)
		if highlight != "" {
			score, positions, _ = FuzzyMatch(highlight, label, false)
		}
		matches = append(matches, sessionMatch{index: i, score: score, positions: positions})
	}
	// 没有正项时就没有相关性信号（例如 "!draft"），因此
	// 保持原始顺序，而不是仅按长度排序。
	if len(terms) == 0 || highlight == "" {
		return matches
	}

	ranked := make([]FuzzyResult, len(matches))
	for i, match := range matches {
		ranked[i] = FuzzyResult{
			Score:     match.score,
			Positions: match.positions,
			TextLen:   utf8.RuneCountInString(sessionLabel(m.sessions[match.index])),
			Index:     i,
		}
	}
	SortFuzzyResults(ranked)
	ordered := make([]sessionMatch, len(matches))
	for i, result := range ranked {
		ordered[i] = matches[result.Index]
	}
	return ordered
}

// pushFilterLabel 渲染 label，把匹配的 rune 用强调样式显示。
// 位置来自 FuzzyMatch 的 rune 索引；越界的条目被忽略，
// 使过期的位置绝不会切过字符串。maxCells 按显示单元格
// 测量，与旧的 Clip(label, width-4) 对 CJK 的行为一致。
func (m *Model) pushFilterLabel(line *Line, label string, positions []int, selected bool, maxCells int) {
	runes := []rune(label)
	marked := make([]bool, len(runes))
	for _, p := range positions {
		if p >= 0 && p < len(marked) {
			marked[p] = true
		}
	}

	styled := NewLine()
	base := Style{Bold: selected}
	accent := m.palette.accent()
	var run []rune
	runHighlighted := false
	flush := func() {
		if len(run) == 0 {
			return
		}
		style := base
		if runHighlighted {
			style = accent
		}
		styled.Push(string(run), style)
		run = nil
	}
	for i, r := range runes {
		if i == 0 {
			runHighlighted = marked[i]
		} else if marked[i] != runHighlighted {
			flush()
			runHighlighted = marked[i]
		}
		run = append(run, r)
	}
	flush()
	for _, span := range clipLine(styled, maxCells).Spans {
		line.PushSpan(span)
	}
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
