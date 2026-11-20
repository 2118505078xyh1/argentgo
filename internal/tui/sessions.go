package tui

import (
	"strings"
	"unicode/utf8"
)

// sessionsLines renders generated session titles in the optional side rail.
// The rail reuses the same geometry as the tasks / workspace / plan panels, so
// no extra layout branch is needed in the renderer. An active filter adds one
// bar under the title, then lists only the matching sessions in relevance order.
func (m *Model) sessionsLines(width, height int) []Line {
	matches := m.sessionMatches()

	title := NewLine()
	title.Push(text(m.locale, "sessions_title"), m.palette.accent())
	title.Push(" "+itoa(len(m.sessions)), m.palette.muted())
	out := []Line{title}

	// Keep the bar directly under the title so it stays visible even when the
	// rail is only a few rows tall. Focus is shown with the accent style and a
	// caret; a confirmed-but-unfocused filter stays muted.
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
	if rendered == 0 && len(out) < height {
		hint := NewLine()
		hint.Push(Clip(text(m.locale, "no_sessions"), width), m.palette.dim())
		out = append(out, hint)
	}
	return out
}

// sessionLabel is the text the filter and highlight operate on: the generated
// title, falling back to the path for untitled sessions.
func sessionLabel(row SessionInfo) string {
	if row.Title != "" {
		return row.Title
	}
	return row.Path
}

// sessionFuzzy adapts FuzzyMatch to the callback shape MatchPattern expects.
func sessionFuzzy(pattern, text string) (int, []int, bool) {
	return FuzzyMatch(pattern, text, false)
}

// sessionMatch is one row of the filtered rail. index stays the original
// m.sessions index so the existing cursor/scroll state keeps its meaning;
// score and positions drive relevance order and character highlighting.
type sessionMatch struct {
	index     int
	score     int
	positions []int
}

// firstFilterPattern extracts the first positive term body for scoring and
// highlighting. Operators and inverse terms still affect MatchPattern's
// yes/no decision; only the first positive body drives visual emphasis.
func firstFilterPattern(terms []SearchTerm) string {
	for _, term := range terms {
		if term.Kind == TermOr || term.Kind == TermInverse || term.Text == "" {
			continue
		}
		return term.Text
	}
	return ""
}

// sessionMatches returns the sessions that satisfy the current filter, ordered
// by relevance. An empty (or whitespace-only) query returns every session in
// its original order: the pre-filter rendering baseline.
func (m *Model) sessionMatches() []sessionMatch {
	terms := ParsePattern(m.sessionFilter)
	highlight := firstFilterPattern(terms)
	matches := make([]sessionMatch, 0, len(m.sessions))
	for i, row := range m.sessions {
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
	// With no positive term there is no relevance signal (e.g. "!draft"), so
	// keep the original order instead of sorting on length alone.
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

// pushFilterLabel renders label with matched runes in the accent style. The
// positions are rune indices from FuzzyMatch; out-of-range entries are ignored
// so a stale position can never slice past the string. maxCells is measured in
// display cells, matching the old Clip(label, width-4) behaviour for CJK.
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
