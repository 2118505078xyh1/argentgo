package tui

import (
	"reflect"
	"strings"
	"testing"
)

// commandInfos 故意给出 CJK 描述和参数提示，候选经补全状态排序后为
// commit / compile / config / 帮助。
func commandInfos() []CommandInfo {
	return []CommandInfo{
		{Command: "commit", Description: "提交", ArgumentHint: "<msg>"},
		{Command: "compile", Description: "编译", ArgumentHint: "<path>"},
		{Command: "config", Description: "配置", ArgumentHint: "<key>"},
		{Command: "帮助", Description: "显示帮助"},
	}
}

func typeCommandText(m *Model, s string) {
	for _, r := range s {
		m.HandleKey(Key{Kind: KindRune, Rune: r})
	}
}

// frameBytes 渲染一帧的完整字节（全新缓冲区会强制整屏刷新）。
func frameBytes(m *Model, width, height int) string {
	buf := NewBuffer(width, height)
	m.Paint(buf)
	var out strings.Builder
	_, _ = buf.Flush(&out)
	return out.String()
}

func TestCompletionAtAndVisibleDoNotMoveSelection(t *testing.T) {
	s := NewCompletionState(sampleCandidates())
	s.Query("co")
	s.Next()

	visible := s.Visible()
	if got := len(visible); got != 3 {
		t.Fatalf("Visible() len = %d, want 3", got)
	}
	if got := commandsOf(s); !reflect.DeepEqual(got, []string{"commit", "compile", "config"}) {
		t.Fatalf("Visible order = %v", got)
	}
	if c, ok := s.At(1); !ok || c.Command != "compile" {
		t.Fatalf("At(1) = (%+v, %v), want compile/true", c, ok)
	}
	if _, ok := s.At(-1); ok {
		t.Fatal("At(-1) should fail")
	}
	if _, ok := s.At(3); ok {
		t.Fatal("At(3) past the end should fail")
	}
	if s.index != 1 {
		t.Fatalf("At/Visible moved the selection: index = %d, want 1", s.index)
	}
}

func TestCommandTabOutsideSlashKeepsLegacyTwoSpacesByteExact(t *testing.T) {
	for _, withInfo := range []bool{false, true} {
		m1, m2 := New(Config{}), New(Config{})
		if withInfo {
			m1.SetCommandInfo(commandInfos())
			m2.SetCommandInfo(commandInfos())
		}
		typeCommandText(m1, "hi")
		typeCommandText(m2, "hi")

		m1.HandleKey(Key{Kind: KindTab})
		typeCommandText(m2, "  ")

		if m1.Input() != "hi  " || m2.Input() != "hi  " {
			t.Fatalf("withInfo=%v: inputs = (%q, %q), want (\"hi  \", \"hi  \")", withInfo, m1.Input(), m2.Input())
		}
		if got, want := frameBytes(m1, 100, 24), frameBytes(m2, 100, 24); got != want {
			t.Fatalf("withInfo=%v: Tab changed the frame bytes:\ngot:\n%q\nwant:\n%q", withInfo, got, want)
		}
	}
}

func TestCommandTabNoVisibleCandidateFallsBack(t *testing.T) {
	m1, m2 := New(Config{}), New(Config{})
	m1.SetCommandInfo(commandInfos())
	m2.SetCommandInfo(commandInfos())
	typeCommandText(m1, "/zzz")
	typeCommandText(m2, "/zzz")
	if m1.completion == nil || m1.completion.Count() != 0 {
		t.Fatalf("unmatched query Count = %d, want 0", m1.completion.Count())
	}

	m1.HandleKey(Key{Kind: KindTab})
	typeCommandText(m2, "  ")
	if m1.Input() != "/zzz  " {
		t.Fatalf("input = %q, want \"/zzz  \"", m1.Input())
	}
	if got, want := frameBytes(m1, 100, 24), frameBytes(m2, 100, 24); got != want {
		t.Fatalf("no-candidate Tab changed the frame bytes:\ngot:\n%q\nwant:\n%q", got, want)
	}
}

func TestCommandTabAcceptsSlashCandidate(t *testing.T) {
	m := New(Config{})
	m.SetCommandInfo(commandInfos())
	typeCommandText(m, "/co")
	if !m.completionActiveLocked() {
		t.Fatal("completion should be active for /co")
	}
	if action := m.HandleKey(Key{Kind: KindTab}); action != ActionNone {
		t.Fatalf("accepting a candidate returned %v, want ActionNone", action)
	}
	if got := m.Input(); got != "/commit " {
		t.Fatalf("input after Tab = %q, want \"/commit \"", got)
	}
	if m.completion.Count() != 0 {
		t.Fatalf("count after acceptance = %d, want 0 (trailing space ends the query)", m.completion.Count())
	}
	// 已经接受后 Tab 回归为插两空格。
	m.HandleKey(Key{Kind: KindTab})
	if got := m.Input(); got != "/commit   " {
		t.Fatalf("second Tab input = %q, want two spaces appended", got)
	}
}

func TestCommandNavigationEscAndEnter(t *testing.T) {
	m := New(Config{})
	m.SetCommandInfo(commandInfos())
	typeCommandText(m, "/co")

	current := func() string {
		c, ok := m.completion.Current()
		if !ok {
			return "<none>"
		}
		return c.Command
	}
	if got := current(); got != "commit" {
		t.Fatalf("initial candidate = %q, want commit", got)
	}
	m.HandleKey(Key{Kind: KindDown})
	if got := current(); got != "compile" {
		t.Fatalf("after Down = %q, want compile", got)
	}
	m.HandleKey(Key{Kind: KindDown})
	if got := current(); got != "config" {
		t.Fatalf("after second Down = %q, want config", got)
	}
	m.HandleKey(Key{Kind: KindUp})
	if got := current(); got != "compile" {
		t.Fatalf("after Up = %q, want compile", got)
	}

	// Enter 接受当前候选，不产生提交。
	if action := m.HandleKey(Key{Kind: KindEnter}); action != ActionNone {
		t.Fatalf("Enter accepted candidate returned %v, want ActionNone", action)
	}
	if got := m.Input(); got != "/compile " {
		t.Fatalf("input after Enter = %q, want \"/compile \"", got)
	}
	if got := m.TakeSubmitted(); got != "" {
		t.Fatalf("accepting a candidate submitted %q", got)
	}
	// 再次 Enter 才真正提交。
	if action := m.HandleKey(Key{Kind: KindEnter}); action != ActionSubmit {
		t.Fatalf("second Enter returned %v, want ActionSubmit", action)
	}
	if got := m.TakeSubmitted(); got != "/compile " {
		t.Fatalf("submitted = %q, want \"/compile \"", got)
	}

	// Esc 逐层关闭：浮层 → 面板 → 补全；下一次输入重开，关闭后 Tab 回退为两空格。
	esc := New(Config{})
	esc.SetCommandInfo(commandInfos())
	typeCommandText(esc, "/co")
	esc.HandleKey(Key{Kind: KindCtrlO})
	if !esc.showCommands {
		t.Fatal("Ctrl+O did not open the commands panel")
	}
	esc.HandleKey(Key{Kind: KindEsc}) // 关浮层
	if !esc.showCommands || esc.completion == nil {
		t.Fatal("first Esc should close only the overlay")
	}
	esc.HandleKey(Key{Kind: KindEsc}) // 关面板
	esc.HandleKey(Key{Kind: KindEsc}) // 关补全
	if esc.showCommands || esc.completion != nil {
		t.Fatal("Esc did not close the panel and the completion layer by layer")
	}
	typeCommandText(esc, "m") // "/com"
	if esc.completion == nil || esc.completion.Count() == 0 {
		t.Fatal("typing after Esc did not reopen the completion")
	}
	esc.HandleKey(Key{Kind: KindEsc}) // 浮层
	esc.HandleKey(Key{Kind: KindEsc}) // 补全
	esc.HandleKey(Key{Kind: KindTab})
	if got := esc.Input(); got != "/com  " {
		t.Fatalf("Tab after Esc = %q, want two spaces appended", got)
	}
}

func TestCommandsPanelCJKClipWidth(t *testing.T) {
	m := New(Config{})
	m.SetCommandInfo([]CommandInfo{{
		Command:      "帮助",
		Description:  strings.Repeat("显示帮助内容", 20),
		ArgumentHint: "<对象>",
	}})
	typeCommandText(m, "/帮")
	if m.completion.Count() != 1 {
		t.Fatalf("CJK query Count = %d, want 1", m.completion.Count())
	}

	const width = 24
	lines := m.commandsLines(width, 10)
	if len(lines) < 3 {
		t.Fatalf("commandsLines returned %d lines, want at least 3", len(lines))
	}
	for i, line := range lines {
		if w := TextWidth(line.Text()); w > width {
			t.Errorf("line %d width = %d, want <= %d: %q", i, w, width, line.Text())
		}
	}
	if !strings.Contains(lines[2].Text(), "/帮助") {
		t.Fatalf("the CJK candidate line is missing: %q", lines[2].Text())
	}
	if !strings.Contains(lines[2].Text(), "<对象>") {
		t.Fatalf("the argument hint is missing: %q", lines[2].Text())
	}
}

func TestSetCommandInfoKeepsDefaultFrameByteExact(t *testing.T) {
	base := New(Config{})
	want := frameBytes(base, 100, 24)

	with := New(Config{})
	with.SetCommandInfo(commandInfos())
	if got := frameBytes(with, 100, 24); got != want {
		t.Fatalf("SetCommandInfo changed the default frame:\ngot:\n%q\nwant:\n%q", got, want)
	}

	empty := New(Config{})
	empty.SetCommandInfo(nil)
	if got := frameBytes(empty, 100, 24); got != want {
		t.Fatalf("SetCommandInfo(nil) changed the default frame:\ngot:\n%q\nwant:\n%q", got, want)
	}
}

func TestFrameGateWithCommandInfoWritesNoBytesOnSecondFrame(t *testing.T) {
	m := New(Config{})
	m.SetCommandInfo(commandInfos())
	buffer := NewBuffer(80, 24)
	var out strings.Builder
	fw := &frameWriter{}

	if err := fw.draw(m, buffer, &out, true); err != nil {
		t.Fatalf("first draw: %v", err)
	}
	out.Reset()
	if err := fw.draw(m, buffer, &out, false); err != nil {
		t.Fatalf("second draw: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("an unchanged second frame wrote %d bytes: %q", out.Len(), out.String())
	}
}

func TestCommandsPanelHiddenByDefaultAndToggleRestoresFrame(t *testing.T) {
	m := New(Config{})
	m.SetCommandInfo(commandInfos())
	typeCommandText(m, "/co")
	if m.showCommands || m.panelsVisible() {
		t.Fatal("the commands panel must start hidden")
	}
	before := frameBytes(m, 100, 24)
	if strings.Contains(before, "COMMANDS") {
		t.Fatal("the hidden panel leaked into the default frame")
	}

	keys := (&Decoder{}).Feed([]byte{0x0f})
	if len(keys) != 1 || keys[0].Kind != KindCtrlO {
		t.Fatalf("0x0f decoded to %v, want KindCtrlO", keys)
	}
	m.HandleKey(Key{Kind: KindCtrlO})
	if !m.showCommands || !m.panelsVisible() {
		t.Fatal("Ctrl+O did not show the commands panel")
	}
	shown := panelText(m, 100, 24)
	for _, want := range []string{"COMMANDS", "/commit", "<msg>"} {
		if !strings.Contains(shown, want) {
			t.Fatalf("the commands panel is missing %q:\n%s", want, shown)
		}
	}

	m.HandleKey(Key{Kind: KindCtrlO})
	if m.showCommands || m.panelsVisible() {
		t.Fatal("Ctrl+O did not hide the commands panel")
	}
	m.SetStatus("")
	if got := frameBytes(m, 100, 24); got != before {
		t.Fatalf("hiding the panel did not restore the frame:\ngot:\n%q\nwant:\n%q", got, before)
	}
}

func TestCommandsPanelShowsEmptyHintWithoutInfo(t *testing.T) {
	m := New(Config{})
	m.HandleKey(Key{Kind: KindCtrlO})
	if !strings.Contains(panelText(m, 100, 24), "no commands") {
		t.Fatalf("the empty commands hint is missing:\n%s", panelText(m, 100, 24))
	}
}

// TestCommandFuzzyQueryShowsSubsequenceCandidates 从输入行这一层验证
// 非前缀缩写能激活补全并出现在命令面板中。查询由 refreshCompletionLocked
// 去掉前导 "/" 得到，因此 "/rv" 的查询词是 "rv"。
func TestCommandFuzzyQueryShowsSubsequenceCandidates(t *testing.T) {
	infos := []CommandInfo{
		{Command: "format", Description: "格式化"},
		{Command: "review", Description: "审查"},
	}
	for _, tc := range []struct {
		typed string
		want  string
	}{
		{"/rv", "review"},
		{"/fmt", "format"},
		{"/RV", "review"}, // 大小写不敏感
	} {
		m := New(Config{})
		m.SetCommandInfo(infos)
		typeCommandText(m, tc.typed)
		if !m.completionActiveLocked() {
			t.Fatalf("%s: completion should be active for a fuzzy query", tc.typed)
		}
		if c, ok := m.completion.Current(); !ok || c.Command != tc.want {
			t.Fatalf("%s: current = (%+v, %v), want %s", tc.typed, c, ok, tc.want)
		}
		m.HandleKey(Key{Kind: KindCtrlO})
		if shown := panelText(m, 100, 24); !strings.Contains(shown, "/"+tc.want) {
			t.Fatalf("%s: panel is missing /%s:\n%s", tc.typed, tc.want, shown)
		}
	}
}

// TestCommandFuzzyQueryTrailingSpaceEndsQuery 验证模糊匹配没有改变
// "尾随空格终止查询" 的既有交互：输入空格后候选数归零。
func TestCommandFuzzyQueryTrailingSpaceEndsQuery(t *testing.T) {
	m := New(Config{})
	m.SetCommandInfo([]CommandInfo{{Command: "review", Description: "审查"}})
	typeCommandText(m, "/review ")
	if m.completionActiveLocked() {
		t.Fatal("completion should be inactive after a trailing space")
	}
	if got := m.completion.Count(); got != 0 {
		t.Fatalf("Count after trailing space = %d, want 0", got)
	}
}

// TestCommandFuzzyScoreRanksCommitFirst 从输入行与面板两层验证分数排序：
// "cmt" 同时子序列命中 comment 与 commit，字典序中 comment 在前，但
// FuzzyMatch 分数 commit 更高，故面板与接受动作都指向 /commit。
func TestCommandFuzzyScoreRanksCommitFirst(t *testing.T) {
	m := New(Config{})
	m.SetCommandInfo([]CommandInfo{
		{Command: "comment", Description: "注释"},
		{Command: "commit", Description: "提交"},
	})
	typeCommandText(m, "/cmt")
	if c, ok := m.completion.Current(); !ok || c.Command != "commit" {
		t.Fatalf("current = (%+v, %v), want commit", c, ok)
	}

	m.HandleKey(Key{Kind: KindCtrlO})
	shown := panelText(m, 100, 24)
	iCommit, iComment := strings.Index(shown, "/commit"), strings.Index(shown, "/comment")
	if iCommit < 0 || iComment < 0 {
		t.Fatalf("panel is missing both candidates:\n%s", shown)
	}
	if iCommit > iComment {
		t.Fatalf("panel put /comment before /commit:\n%s", shown)
	}

	if action := m.HandleKey(Key{Kind: KindTab}); action != ActionNone {
		t.Fatalf("accepting a candidate returned %v, want ActionNone", action)
	}
	if got := m.Input(); got != "/commit " {
		t.Fatalf("input after Tab = %q, want \"/commit \"", got)
	}
}
