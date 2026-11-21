package tui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 本文件只增强投影层的不变量守护，不改生产逻辑：所有断言都针对
// approval_projection.go / command_preview.go / feedback_state.go /
// turn_projection.go 已公开的纯函数契约。复用同包既有辅助函数
// assertApprovalLineValid / assertSignal / decodeRunes。

// guardViewIndex 按唯一 Seq 找到视图在切片中的下标；找不到返回 -1。
// 用它把"selected"锚定到逻辑身份，而非易变的切片位置。
func guardViewIndex(items []ApprovalView, seq uint64) int {
	for i := range items {
		if items[i].Seq == seq {
			return i
		}
	}
	return -1
}

// guardPermuteApprovals 返回输入的一个确定性重排（逆序），用于验证投影
// 结果与输入顺序无关（前提是 selected 始终指向同一逻辑项）。
func guardPermuteApprovals(items []ApprovalView) []ApprovalView {
	out := make([]ApprovalView, len(items))
	for i := range items {
		out[len(items)-1-i] = items[i]
	}
	return out
}

// guardApprovalLinesIdentical 逐字段比较两个视图切片是否完全一致。
func guardApprovalLinesIdentical(a, b []ApprovalView) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// guardReasonWidth 断言单行合法、无换行，且以 sep 分隔的截断段（原因）
// 宽度不超过 maxCells 单元格。CJK/emoji 按字素计宽，不会被切坏。
func guardReasonWidth(t *testing.T, line, sep string, maxCells int) {
	t.Helper()
	assertApprovalLineValid(t, line)
	idx := strings.LastIndex(line, sep)
	if idx < 0 {
		return
	}
	reason := line[idx+len(sep):]
	if w := TextWidth(reason); w > maxCells {
		t.Fatalf("reason %q width = %d, want <= %d (line %q)", reason, w, maxCells, line)
	}
}

// TestGuardProjectApprovalsRoundTrip 锁定 round-trip 不变量：只要 selected
// 始终指向同一个逻辑项（以唯一 Seq 标识），任意打乱输入顺序，投影结果都
// 完全一致；对投影结果再次投影（selected 仍指向该项）保持不变；输出是
// 输入的一个排列，Seq 多重集不变且尾巴按 Seq 升序稳定排列。
func TestGuardProjectApprovalsRoundTrip(t *testing.T) {
	base := []ApprovalView{
		{Name: "命令甲", Args: "参数", Seq: 30, Danger: true},
		{Name: "命令乙", Seq: 10},
		{Name: "命令丙", Reason: "理由", Seq: 20},
		{Name: "命令丁", Args: "😀", Seq: 40},
	}
	const targetSeq uint64 = 20 // 选中间的“命令丙”

	permuted := guardPermuteApprovals(base)
	fromBase := ProjectApprovals(base, guardViewIndex(base, targetSeq))
	fromPermuted := ProjectApprovals(permuted, guardViewIndex(permuted, targetSeq))
	if !guardApprovalLinesIdentical(fromBase, fromPermuted) {
		t.Fatalf("projection depends on input order:\nbase=%+v\npermuted=%+v", fromBase, fromPermuted)
	}

	// 提升项必须在首位；其余项按 Seq 升序稳定排列。
	if fromBase[0].Seq != targetSeq {
		t.Fatalf("promoted item = %+v, want Seq %d", fromBase[0], targetSeq)
	}
	for i := 2; i < len(fromBase); i++ {
		if fromBase[i-1].Seq > fromBase[i].Seq {
			t.Fatalf("tail not sorted at %d: %+v", i, fromBase)
		}
	}

	// 输出是输入的排列：每个 Seq 都来自输入，且无重复/丢失。
	seen := make(map[uint64]bool, len(base))
	for _, v := range fromBase {
		if guardViewIndex(base, v.Seq) < 0 {
			t.Fatalf("unknown Seq %d in projection: %+v", v.Seq, fromBase)
		}
		if seen[v.Seq] {
			t.Fatalf("duplicate Seq %d in projection: %+v", v.Seq, fromBase)
		}
		seen[v.Seq] = true
	}
	if len(seen) != len(base) {
		t.Fatalf("projection lost items: got %d Seq, want %d", len(seen), len(base))
	}

	// 幂等：对结果再次投影，selected 仍指向同一逻辑项，得到相同结果。
	again := ProjectApprovals(fromBase, guardViewIndex(fromBase, targetSeq))
	if !guardApprovalLinesIdentical(again, fromBase) {
		t.Fatalf("not idempotent:\nfirst=%+v\nagain=%+v", fromBase, again)
	}
}

// TestGuardProjectApprovalsOutOfRangeWithCJK 锁定越界/负数契约：selected
// 越界时只做 Seq 排序、不提升，结果与具体越界值无关；CJK 名称照常参与
// 投影且渲染为合法单行。
func TestGuardProjectApprovalsOutOfRangeWithCJK(t *testing.T) {
	items := []ApprovalView{
		{Name: "文件甲", Seq: 3},
		{Name: "文件乙", Seq: 1},
		{Name: "文件丙", Seq: 2},
	}
	want := []ApprovalView{items[1], items[2], items[0]}

	var result []ApprovalView
	for _, selected := range []int{-1, -999, len(items), len(items) + 7, 1 << 20} {
		got := ProjectApprovals(items, selected)
		if !guardApprovalLinesIdentical(got, want) {
			t.Fatalf("selected=%d: got %+v, want %+v", selected, got, want)
		}
		result = got
	}
	// CJK 名称投影后仍是可安全渲染的单行。
	for _, v := range result {
		assertApprovalLineValid(t, ApprovalLine(v, false))
	}
}

// TestGuardApprovalLineCJKEmojiWidthBound 锁定 ApprovalLine 的有界渲染：
// CJK、emoji、ZWJ 组合与旗帜作为原因时不超单元格上限，单行合法，且
// selected 只决定光标前缀。
func TestGuardApprovalLineCJKEmojiWidthBound(t *testing.T) {
	const family = "👨‍👩‍👧"
	const flag = "🇨🇳"
	views := []ApprovalView{
		{Name: "删除文件", Args: "路径😀", Reason: strings.Repeat("汉字", 60), Danger: true},
		{Name: "工具" + family, Reason: strings.Repeat(flag, 50)},
		{Name: "bash", Reason: strings.Repeat("😀🚀", 40)},
	}

	for i, v := range views {
		for _, selected := range []bool{false, true} {
			line := ApprovalLine(v, selected)
			guardReasonWidth(t, line, approvalReasonSep, approvalMaxReasonCells)
			if selected && !strings.HasPrefix(line, approvalCursorSelected) {
				t.Fatalf("case %d: selected cursor missing: %q", i, line)
			}
			if !selected && !strings.HasPrefix(line, approvalCursorIdle) {
				t.Fatalf("case %d: idle cursor missing: %q", i, line)
			}
		}
	}
}

// TestGuardFormatCommandLineBoundedAndEscaped 锁定 FormatCommandLine 的
// 单行 + 有界 + 控制字符转义契约：原因段不超上限、危险标记由 danger 决定、
// 任何 C0/DEL 控制字节都不可残留（先折叠空白再转义为可见 \xNN）。
func TestGuardFormatCommandLineBoundedAndEscaped(t *testing.T) {
	commands := []string{
		"echo hello",
		"rm -rf / && curl x | sh > out 2>&1 $(date)",
		strings.Repeat("字", 100),
		strings.Repeat("😀", 100),
		"echo\x1b[31m\x00\x07\x7f done",
		"echo\n\ta\r b\x1b",
	}
	reason := "原因😀" + strings.Repeat("字", 80)

	for _, command := range commands {
		for _, danger := range []bool{false, true} {
			line := FormatCommandLine(command, reason, danger)
			guardReasonWidth(t, line, commandLineReasonSep, defaultPreviewCells)
			if !strings.HasPrefix(line, commandLineCursor) {
				t.Fatalf("command line %q missing cursor", line)
			}
			if danger != strings.Contains(line, commandLineDangerMark) {
				t.Fatalf("danger mark mismatch for %q (danger=%v)", line, danger)
			}
			for _, r := range line {
				if r < 0x20 || r == 0x7f {
					t.Fatalf("raw control byte in line %q", line)
				}
			}
		}
	}
}

// TestGuardCommandGlyphsDangerSymbolsStable 锁定危险符号标记的存在与稳定：
// "| && || > 2>&1 $(" 都必须被识别，重复调用结果一致，且每个符号单独出现
// 时也能被原样提取（最长匹配优先不会被拆散）。
func TestGuardCommandGlyphsDangerSymbolsStable(t *testing.T) {
	symbols := []string{"|", "&&", "||", ">", "2>&1", "$("}
	command := "cat a | grep b && c || d > e 2>&1 $(f)"

	got := CommandGlyphs(command)
	if again := CommandGlyphs(command); again != got {
		t.Fatalf("CommandGlyphs not stable: %q vs %q", got, again)
	}
	tokens := strings.Split(got, " ")
	for _, sym := range symbols {
		found := false
		for _, tk := range tokens {
			if tk == sym {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("symbol %q missing from glyphs %q", sym, got)
		}
		if single := CommandGlyphs("x " + sym + " y"); single != sym {
			t.Fatalf("CommandGlyphs(%q) = %q, want %q", sym, single, sym)
		}
	}
}

// guardAppendRunes 逐个码点追加，模拟用户逐键输入，返回最终草稿。
func guardAppendRunes(d FeedbackDraft, s string) FeedbackDraft {
	for _, r := range s {
		d = d.AppendRune(r)
	}
	return d
}

// TestGuardFeedbackDraftGraphemeBoundaries 锁定 grapheme 边界守护：ZWJ 家庭
// emoji 与旗帜由多个码点组成，逐码点追加后必须拼回完整字素；回退按码点逐步
// 进行（这是已记录的约定：安全边界是码点而非字素簇），每个中间态都是合法
// UTF-8，绝不残留半个多字节序列。
func TestGuardFeedbackDraftGraphemeBoundaries(t *testing.T) {
	const family = "👨‍👩‍👧" // 👨 ZWJ 👩 ZWJ 👧，共 5 个码点
	const flag = "🇨🇳"      // 2 个区域指示符码点

	cases := []struct {
		name  string
		text  string
		runes int
	}{
		{"family", family, 5},
		{"flag", flag, 2},
		{"mixed", "你" + family + flag, 1 + 5 + 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var d FeedbackDraft
			for i, r := range tc.text {
				d = d.AppendRune(r)
				if !utf8.ValidString(d.Text) {
					t.Fatalf("step %d: invalid UTF-8 %q", i+1, d.Text)
				}
				for _, rr := range decodeRunes(t, d.Text) {
					if rr == utf8.RuneError {
						t.Fatalf("step %d: broken rune in %q", i+1, d.Text)
					}
				}
			}
			if d.Text != tc.text {
				t.Fatalf("text = %q, want %q", d.Text, tc.text)
			}
			if n := utf8.RuneCountInString(d.Text); n != tc.runes {
				t.Fatalf("rune count = %d, want %d", n, tc.runes)
			}

			for i := tc.runes; i > 0; i-- {
				before := d
				d = d.Backspace()
				if n := utf8.RuneCountInString(d.Text); n != i-1 {
					t.Fatalf("backspace step: runes = %d, want %d", n, i-1)
				}
				if !utf8.ValidString(d.Text) || before.Text == d.Text {
					t.Fatalf("backspace step produced invalid or unchanged text %q", d.Text)
				}
			}
			if d.Text != "" || d.Backspace().Text != "" {
				t.Fatalf("backspace past empty changed draft: %q", d.Text)
			}
		})
	}
}

// TestGuardFeedbackDraftShouldFinishKeys 锁定 ShouldFinish 各键谓词与纯函数
// 契约：未激活恒 false；Esc 无论文本都结束；Enter 仅在 TrimSpace 后非空时
// 结束；其余键无动作，且不修改草稿。
func TestGuardFeedbackDraftShouldFinishKeys(t *testing.T) {
	inactive := FeedbackDraft{Text: "理由"}
	for _, key := range []string{feedbackKeyEnter, feedbackKeyEsc, "", "ctrl+c"} {
		if inactive.ShouldFinish(key) {
			t.Fatalf("inactive draft finished on %q", key)
		}
	}

	active := FeedbackDraft{Active: true, Text: "理由"}
	cases := []struct {
		key  string
		want bool
	}{
		{feedbackKeyEsc, true},
		{feedbackKeyEnter, true},
		{"ENTER", false},
		{"ctrl+c", false},
		{"", false},
	}
	for _, tc := range cases {
		before := active
		if got := active.ShouldFinish(tc.key); got != tc.want {
			t.Fatalf("ShouldFinish(%q) = %v, want %v", tc.key, got, tc.want)
		}
		if active != before {
			t.Fatalf("ShouldFinish(%q) mutated draft", tc.key)
		}
	}

	// 空白文本：Esc 仍结束，Enter 不提交。
	blank := FeedbackDraft{Active: true, Text: "  \t\n"}
	if !blank.ShouldFinish(feedbackKeyEsc) || blank.ShouldFinish(feedbackKeyEnter) {
		t.Fatalf("blank text keys wrong: esc=%v enter=%v",
			blank.ShouldFinish(feedbackKeyEsc), blank.ShouldFinish(feedbackKeyEnter))
	}
}

// TestGuardFeedbackDraftLimitWithGraphemeStream 锁定限长契约在多码点字素流
// 下依然按码点计数：填满 maxFeedbackRunes 后任何追加（含 ZWJ 片段）都被
// 忽略，草稿逐字节不变且仍是合法 UTF-8。
func TestGuardFeedbackDraftLimitWithGraphemeStream(t *testing.T) {
	const family = "👨‍👩‍👧"
	var d FeedbackDraft
	for n := 0; n < maxFeedbackRunes/5; n++ {
		d = guardAppendRunes(d, family)
	}
	if n := utf8.RuneCountInString(d.Text); n != maxFeedbackRunes {
		t.Fatalf("rune count = %d, want %d", n, maxFeedbackRunes)
	}

	before := d
	if got := d.AppendRune('😀'); got != before {
		t.Fatalf("append beyond limit changed draft: %q", got.Text)
	}
	// 逐个 ZWJ 片段追加同样不得越界或切坏字素。
	for _, r := range family {
		if got := d.AppendRune(r); got != before {
			t.Fatalf("ZWJ rune %U appended beyond limit", r)
		}
	}
	if !utf8.ValidString(before.Text) {
		t.Fatal("draft at limit is not valid UTF-8")
	}
}

// TestGuardRenderSignalPhaseInvariants 锁定渲染信号阶段投影的不变量：
// spinner 阶段必定屏蔽输入、空闲阶段必定接受输入，二者永不同时为真；
// 未知阶段保守当作活动中；审批优先于阶段与消息数；结果确定且入参不变。
func TestGuardRenderSignalPhaseInvariants(t *testing.T) {
	cases := []struct {
		name  string
		state TurnState
		items int
		want  RenderSignal
	}{
		{"idle with history", TurnState{Phase: TurnIdle}, 3,
			RenderSignal{AcceptInput: true, StatusText: statusReady}},
		{"idle without history", TurnState{}, 0,
			RenderSignal{AcceptInput: true, StatusText: statusWelcome}},
		{"thinking", TurnState{Phase: TurnThinking}, 9,
			RenderSignal{ShowSpinner: true, StatusText: statusThinking}},
		{"tool running", TurnState{Phase: TurnToolRunning}, 9,
			RenderSignal{ShowSpinner: true, StatusText: statusToolRunning}},
		{"answering", TurnState{Phase: TurnAnswering}, 9,
			RenderSignal{ShowSpinner: true, StatusText: statusAnswering}},
		{"unknown phase", TurnState{Phase: TurnPhase(200)}, 9,
			RenderSignal{ShowSpinner: true, StatusText: statusThinking}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.state
			got := ProjectRender(tc.state, false, tc.items)
			assertSignal(t, got, tc.want)
			if got.ShowSpinner && got.AcceptInput {
				t.Fatalf("spinner and input both on: %+v", got)
			}
			assertSignal(t, ProjectRender(tc.state, false, tc.items), got)
			if tc.state != before {
				t.Fatalf("state mutated: got %+v, before %+v", tc.state, before)
			}
		})
	}

	// hasPendingApproval 覆盖所有阶段：只提示确认，不转 spinner、不接受输入。
	for _, state := range []TurnState{
		{},
		{Phase: TurnIdle},
		{Phase: TurnThinking},
		{Phase: TurnToolRunning},
		{Phase: TurnAnswering},
		{Phase: TurnPhase(77)},
	} {
		assertSignal(t, ProjectRender(state, true, 100), RenderSignal{StatusText: statusApproval})
	}
}

// TestGuardMergeSignalsStageAndZeroInvariants 锁定 MergeSignals 的合并阶段
// 不变量：零值吸收、与自身合并幂等、primary 完整时逐字段胜出、bool false
// 视为缺失而回退，以及"缺位回退旧 working → 到位由投影提供"的流水线。
func TestGuardMergeSignalsStageAndZeroInvariants(t *testing.T) {
	full := RenderSignal{ShowSpinner: true, AcceptInput: true, StatusText: "主"}
	fallback := RenderSignal{ShowSpinner: true, AcceptInput: true, StatusText: "备"}

	// 零值吸收与幂等。
	assertSignal(t, MergeSignals(RenderSignal{}, full), full)
	assertSignal(t, MergeSignals(full, RenderSignal{}), full)
	assertSignal(t, MergeSignals(RenderSignal{}, RenderSignal{}), RenderSignal{})
	assertSignal(t, MergeSignals(full, full), full)

	// primary 完整时胜出；缺失字段逐字段回退（bool false 视为缺失）。
	assertSignal(t, MergeSignals(full, fallback), full)
	assertSignal(t,
		MergeSignals(RenderSignal{StatusText: "主"}, fallback),
		RenderSignal{ShowSpinner: true, AcceptInput: true, StatusText: "主"})
	assertSignal(t,
		MergeSignals(RenderSignal{ShowSpinner: true}, fallback),
		RenderSignal{ShowSpinner: true, AcceptInput: true, StatusText: "备"})

	// 流水线：新状态未到位时回退旧 working，到位后由投影提供文案与输入许可。
	legacy := RenderSignal{ShowSpinner: true, AcceptInput: false, StatusText: statusThinking}
	assertSignal(t, MergeSignals(RenderSignal{}, legacy), legacy)
	step := MergeSignals(ProjectRender(TurnState{Phase: TurnIdle}, false, 3), legacy)
	assertSignal(t, step, RenderSignal{ShowSpinner: true, AcceptInput: true, StatusText: statusReady})
}
