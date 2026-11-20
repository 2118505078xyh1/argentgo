package tui

import (
	"reflect"
	"strings"
	"testing"
)

// sessionFilterModel builds a model with the session rail visible and a set of
// titled sessions, which is the state the filter bar operates in.
func sessionFilterModel(titles ...string) *Model {
	m := New(Config{})
	m.showSessions = true
	rows := make([]SessionInfo, 0, len(titles))
	for _, title := range titles {
		rows = append(rows, SessionInfo{Title: title, Path: "sessions/" + title + ".jsonl"})
	}
	m.SetSessions(rows)
	return m
}

func matchTitles(m *Model) []string {
	matches := m.sessionMatches()
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		out = append(out, sessionLabel(m.sessions[match.index]))
	}
	return out
}

func TestSessionsFilterHitAndMiss(t *testing.T) {
	m := sessionFilterModel("alpha", "beta", "gamma")

	m.sessionFilter = "alp"
	if got, want := matchTitles(m), []string{"alpha"}; !reflect.DeepEqual(got, want) {
		t.Errorf("filter %q = %v, want %v", m.sessionFilter, got, want)
	}

	m.sessionFilter = "zzz"
	if got := matchTitles(m); len(got) != 0 {
		t.Errorf("missing query matched %v, want none", got)
	}

	// OR groups and inverse terms are parsed by ParsePattern / MatchPattern.
	m.sessionFilter = "al | ga"
	if got, want := matchTitles(m), []string{"alpha", "gamma"}; !reflect.DeepEqual(got, want) {
		t.Errorf("OR filter = %v, want %v", got, want)
	}
	m.sessionFilter = "!alpha"
	if got, want := matchTitles(m), []string{"beta", "gamma"}; !reflect.DeepEqual(got, want) {
		t.Errorf("inverse filter = %v, want %v", got, want)
	}
}

func TestSessionsFilterSortsByRelevance(t *testing.T) {
	m := sessionFilterModel("zzab", "aXbX", "abzz")
	m.sessionFilter = "ab"
	got := matchTitles(m)
	if len(got) != 3 {
		t.Fatalf("filter returned %v, want all three sessions", got)
	}
	if got[0] != "abzz" {
		t.Errorf("best match = %q, want \"abzz\" (start-of-word consecutive run)", got[0])
	}
	if got[len(got)-1] != "zzab" {
		t.Errorf("worst match = %q, want \"zzab\" (late run with a gap)", got[len(got)-1])
	}
}

func TestSessionsFilterHighlightPositionsInBounds(t *testing.T) {
	m := sessionFilterModel("alpha", "beta")
	m.sessionsCursor = 1 // leave "alpha" unselected so its row text is predictable
	m.sessionFilter = "alp"

	var found bool
	for _, line := range m.sessionsLines(40, 12) {
		if !strings.HasSuffix(line.Text(), "alpha") {
			continue
		}
		found = true
		// The mark plus the highlighted label must reconstruct exactly; a
		// rune-index position must never slice a CJK/ASCII string by bytes.
		if line.Text() != "  alpha" {
			t.Errorf("row text = %q, want %q", line.Text(), "  alpha")
		}
		// mark + highlighted "alp" + plain "ha" => at least three spans.
		if len(line.Spans) < 3 {
			t.Errorf("highlighted row has %d spans, want at least 3: %+v", len(line.Spans), line.Spans)
		}
	}
	if !found {
		t.Error("the filtered session row was not rendered")
	}

	// Explicitly out-of-range positions are ignored rather than sliced.
	line := NewLine()
	m.pushFilterLabel(&line, "ab", []int{-5, 0, 1, 100}, false, 10)
	if line.Text() != "ab" {
		t.Errorf("out-of-range positions rendered %q, want %q", line.Text(), "ab")
	}
}

func TestSessionsFilterEmptyQueryShowsAllInOrder(t *testing.T) {
	titles := []string{"gamma", "alpha", "beta"}
	m := sessionFilterModel(titles...)
	for _, query := range []string{"", "   ", "\t"} {
		m.sessionFilter = query
		if got := matchTitles(m); !reflect.DeepEqual(got, titles) {
			t.Errorf("empty filter %q = %v, want original order %v", query, got, titles)
		}
		for _, line := range m.sessionsLines(40, 12) {
			if strings.HasPrefix(line.Text(), "> ") {
				t.Errorf("empty filter %q rendered a filter bar", query)
			}
		}
	}
}

func TestSessionsFilterEscEnterState(t *testing.T) {
	m := sessionFilterModel("alpha", "beta")

	if action := m.HandleKey(Key{Kind: KindCtrlL}); action != ActionNone {
		t.Errorf("ctrl+l returned %v, want ActionNone", action)
	}
	if !m.sessionFiltering {
		t.Fatal("ctrl+l did not focus the filter bar")
	}

	m.HandleKey(Key{Kind: KindRune, Rune: 'a'})
	m.HandleKey(Key{Kind: KindRune, Rune: 'l'})
	if m.sessionFilter != "al" {
		t.Errorf("filter = %q, want %q", m.sessionFilter, "al")
	}
	if m.Input() != "" {
		t.Errorf("filter typing leaked into the prompt: %q", m.Input())
	}
	m.HandleKey(Key{Kind: KindBackspace})
	if m.sessionFilter != "a" {
		t.Errorf("filter after backspace = %q, want %q", m.sessionFilter, "a")
	}

	// Enter confirms the live filter and returns the keyboard to the prompt.
	if action := m.HandleKey(Key{Kind: KindEnter}); action != ActionNone {
		t.Errorf("filter Enter returned %v, want ActionNone", action)
	}
	if submitted := m.TakeSubmitted(); submitted != "" {
		t.Errorf("filter Enter submitted the prompt: %q", submitted)
	}
	if m.sessionFiltering {
		t.Error("Enter did not leave the filter bar")
	}
	if m.sessionFilter != "a" {
		t.Errorf("Enter cleared the confirmed filter: %q", m.sessionFilter)
	}

	// Esc cancels: leave the bar and reset the query.
	m.HandleKey(Key{Kind: KindCtrlL})
	if !m.sessionFiltering {
		t.Fatal("ctrl+l did not re-focus the filter bar")
	}
	m.HandleKey(Key{Kind: KindEsc})
	if m.sessionFiltering || m.sessionFilter != "" {
		t.Errorf("after Esc: filtering=%v filter=%q, want false and empty", m.sessionFiltering, m.sessionFilter)
	}

	// Ctrl+L is inert when the session rail is hidden.
	m.showSessions = false
	m.HandleKey(Key{Kind: KindCtrlL})
	if m.sessionFiltering {
		t.Error("ctrl+l focused the filter while the session rail was hidden")
	}
}

func TestSessionsFilterBarVisibleAndCtrlLToggles(t *testing.T) {
	m := sessionFilterModel("alpha", "beta")
	m.HandleKey(Key{Kind: KindCtrlL})
	m.HandleKey(Key{Kind: KindRune, Rune: 'a'})

	barText := func() string {
		for _, line := range m.sessionsLines(40, 12) {
			if strings.HasPrefix(line.Text(), "> ") {
				return line.Text()
			}
		}
		return ""
	}
	if bar := barText(); !strings.Contains(bar, "a") || !strings.Contains(bar, "▌") {
		t.Errorf("focused filter bar = %q, want query with caret", bar)
	}

	m.HandleKey(Key{Kind: KindCtrlL}) // toggles focus off, keeps query
	if m.sessionFiltering || m.sessionFilter != "a" {
		t.Errorf("ctrl+l toggle: filtering=%v filter=%q", m.sessionFiltering, m.sessionFilter)
	}
	if bar := barText(); !strings.Contains(bar, "a") || strings.Contains(bar, "▌") {
		t.Errorf("confirmed filter bar = %q, want query without caret", bar)
	}
}

func TestSessionsFilterCJK(t *testing.T) {
	m := sessionFilterModel("打开配置文件", "关闭窗口", "新建会话")

	m.sessionFilter = "配置"
	if got, want := matchTitles(m), []string{"打开配置文件"}; !reflect.DeepEqual(got, want) {
		t.Errorf("CJK substring filter = %v, want %v", got, want)
	}

	// A fuzzy subsequence must use rune (not byte) indices into the label.
	m.sessionFilter = "开配"
	scores, positions, ok := FuzzyMatch(m.sessionFilter, "打开配置文件", false)
	if !ok || len(positions) != 2 {
		t.Fatalf("CJK fuzzy match = %v %v %v", scores, positions, ok)
	}
	line := NewLine()
	m.pushFilterLabel(&line, "打开配置文件", positions, false, 40)
	if line.Text() != "打开配置文件" {
		t.Errorf("CJK highlight text = %q, want the full label", line.Text())
	}
	if len(line.Spans) < 3 {
		t.Errorf("CJK highlight produced %d spans, want at least 3: %+v", len(line.Spans), line.Spans)
	}
}
