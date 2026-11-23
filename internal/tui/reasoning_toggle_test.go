package tui

import (
	"reflect"
	"strings"
	"testing"
)

// TestDecoderReadsCtrlTForThinkingToggle 论证 0x14 是一个此前空闲的按键：
// 现在解码为 KindCtrlT，且不侵占相邻的 ctrl+o（0x0f）。
func TestDecoderReadsCtrlTForThinkingToggle(t *testing.T) {
	cases := map[byte]Kind{
		0x14: KindCtrlT,
		0x0f: KindCtrlO,
	}
	for input, want := range cases {
		keys := (&Decoder{}).Feed([]byte{input})
		if len(keys) != 1 || keys[0].Kind != want {
			t.Errorf("byte %#x decoded to %v, want %v", input, keys, want)
		}
	}
}

// toggleModel 构造含两块多行思考的模型，第二行带各自可检索的标记。
// AppendReasoning 会合并连续的思考块，因此中间插入一条用户消息分隔；
// 返回两块各自的 entries 下标。
func toggleModel(locale string) (*Model, int, int) {
	m := New(Config{Locale: locale})
	m.AppendReasoning("AAA-FIRST parser state.\nAAA-SECOND-MARKER inspect the migration.")
	idxA := len(m.entries) - 1
	m.AppendUser("between turns")
	m.AppendReasoning("BBB-FIRST parser state.\nBBB-SECOND-MARKER run the tests.")
	idxB := len(m.entries) - 1
	return m, idxA, idxB
}

func assertReasoningMarkers(t *testing.T, frame string, wantAAA, wantBBB bool) {
	t.Helper()
	for _, tc := range []struct {
		marker string
		want   bool
	}{
		{"AAA-SECOND-MARKER", wantAAA},
		{"BBB-SECOND-MARKER", wantBBB},
	} {
		got := strings.Contains(frame, tc.marker)
		if got != tc.want {
			t.Fatalf("marker %q present = %v, want %v\nframe:\n%s", tc.marker, got, tc.want, frame)
		}
	}
}

// TestReasoningGlobalToggleExpandsAndCollapsesAll 验证全局开关翻转所有
// 思考块的展开态：默认全折叠，ctrl+t 全部展开，再按一次全部折叠。
func TestReasoningGlobalToggleExpandsAndCollapsesAll(t *testing.T) {
	m, _, _ := toggleModel("en")
	assertReasoningMarkers(t, frameText(m, 100, 24), false, false)

	m.HandleKey(Key{Kind: KindCtrlT})
	if !m.reasoningAllExpanded {
		t.Fatal("ctrl+t did not set the global expansion flag")
	}
	assertReasoningMarkers(t, frameText(m, 100, 24), true, true)

	m.HandleKey(Key{Kind: KindCtrlT})
	if m.reasoningAllExpanded {
		t.Fatal("second ctrl+t did not clear the global expansion flag")
	}
	assertReasoningMarkers(t, frameText(m, 100, 24), false, false)
}

// TestReasoningToggleStatusNoticeEnZh 验证中英文状态栏都提示当前状态，
// 且连续切换时显示最新的一条通知（NoticeStack.Peek 以最新序号破平局）。
func TestReasoningToggleStatusNoticeEnZh(t *testing.T) {
	for _, locale := range []string{"en", "zh"} {
		t.Run(locale, func(t *testing.T) {
			m := New(Config{Locale: locale})
			m.HandleKey(Key{Kind: KindCtrlT})
			if got := frameText(m, 100, 24); !strings.Contains(got, text(locale, "thinking_all_on")) {
				t.Fatalf("expanded status missing %q:\n%s", text(locale, "thinking_all_on"), got)
			}
			m.HandleKey(Key{Kind: KindCtrlT})
			if got := frameText(m, 100, 24); !strings.Contains(got, text(locale, "thinking_all_off")) {
				t.Fatalf("folded status missing %q:\n%s", text(locale, "thinking_all_off"), got)
			}
		})
	}
}

// clickReasoningBlock 依据上一次 Paint 的命中区点击第 idx 块思考。
func clickReasoningBlock(t *testing.T, m *Model, idx int) {
	t.Helper()
	m.Frame(100, 24)
	for _, hit := range m.reasoningHits {
		if hit.index == idx {
			m.MouseDown(3, hit.top)
			return
		}
	}
	t.Fatalf("no reasoning hit for block %d: %+v", idx, m.reasoningHits)
}

// TestReasoningPerBlockClickOverridesGlobal 验证逐块点击覆盖全局状态：
// 全局展开后点击单块只折叠该块；全局折叠后点击单块只展开该块。
func TestReasoningPerBlockClickOverridesGlobal(t *testing.T) {
	m, idxA, idxB := toggleModel("en")

	// 全局展开 → 两块都展开。
	m.HandleKey(Key{Kind: KindCtrlT})
	assertReasoningMarkers(t, frameText(m, 100, 24), true, true)

	// 点击 A 块覆盖：只有它折叠，B 块仍展开。
	clickReasoningBlock(t, m, idxA)
	if m.reasoningExpandedAt(idxA) || !m.reasoningExpandedAt(idxB) {
		t.Fatalf("per-block override wrong: A=%v B=%v", m.reasoningExpandedAt(idxA), m.reasoningExpandedAt(idxB))
	}
	assertReasoningMarkers(t, frameText(m, 100, 24), false, true)

	// 全局折叠清空覆盖 → 两块都折叠。
	m.HandleKey(Key{Kind: KindCtrlT})
	if len(m.reasoningExpanded) != 0 {
		t.Fatalf("global toggle should clear per-block overrides: %+v", m.reasoningExpanded)
	}
	assertReasoningMarkers(t, frameText(m, 100, 24), false, false)

	// 折叠态下点击 A 块覆盖：只有它展开。
	clickReasoningBlock(t, m, idxA)
	if !m.reasoningExpandedAt(idxA) || m.reasoningExpandedAt(idxB) {
		t.Fatalf("per-block override wrong: A=%v B=%v", m.reasoningExpandedAt(idxA), m.reasoningExpandedAt(idxB))
	}
	assertReasoningMarkers(t, frameText(m, 100, 24), true, false)
}

// TestReasoningPlaceholderOptInDefaultBytes 逐字节论证占位标签：默认关闭
// 时折叠块仍显示 R33 摘要（含短语），不出现裸占位；opt-in 打开后折叠块
// 显示暗色 "◇ 思考…" 占位且不再泄漏摘要短语。展开态始终不走占位。
func TestReasoningPlaceholderOptInDefaultBytes(t *testing.T) {
	prev := ReasoningPlaceholder
	defer func() { ReasoningPlaceholder = prev }()

	ReasoningPlaceholder = false
	m := New(Config{Locale: "zh"})
	m.AppendReasoning("SECRET-PHRASE inspect the parser.")
	folded := frameText(m, 100, 24)
	if !strings.Contains(folded, "SECRET-PHRASE") {
		t.Fatalf("default folded frame lost the R33 summary phrase:\n%s", folded)
	}
	if strings.Contains(folded, "◇ 思考…") {
		t.Fatalf("default frame must not show the opt-in placeholder:\n%s", folded)
	}

	ReasoningPlaceholder = true
	placeholder := frameText(m, 100, 24)
	if !strings.Contains(placeholder, "◇ 思考…") {
		t.Fatalf("opt-in frame is missing the dim placeholder:\n%s", placeholder)
	}
	if strings.Contains(placeholder, "SECRET-PHRASE") {
		t.Fatalf("opt-in placeholder leaked the summary phrase:\n%s", placeholder)
	}

	// 整行只有一个 dim span，样式等于 p.dim()。Style 无 italic 位，
	// 以 dim 作为"斜体暗色"在当前样式模型下的视觉近似。
	p := summaryTestPalette(t)
	line := reasoningPlaceholderLine("zh", 80, p)
	if line.Text() != "◇ 思考…" {
		t.Fatalf("placeholder text = %q, want %q", line.Text(), "◇ 思考…")
	}
	if len(line.Spans) != 1 || line.Spans[0].Style != p.dim() {
		t.Fatalf("placeholder spans = %+v, want one dim span", line.Spans)
	}

	// 展开态回到逐行渲染，不受占位开关影响。
	m.HandleKey(Key{Kind: KindCtrlT})
	expanded := frameText(m, 100, 24)
	if !strings.Contains(expanded, "SECRET-PHRASE") {
		t.Fatalf("expanded frame should show full reasoning:\n%s", expanded)
	}
}

// TestReasoningToggleDefaultFrameByteIdentical 论证无操作时默认帧逐字节
// 不变：来回切换两次全局开关（并清理由此产生的状态提示）后，行文本
// 与初始帧深度相等，思考块也没有残留任何逐块覆盖。
func TestReasoningToggleDefaultFrameByteIdentical(t *testing.T) {
	m, _, _ := toggleModel("en")
	before := rowTexts(m.Frame(100, 24))

	m.HandleKey(Key{Kind: KindCtrlT})
	m.HandleKey(Key{Kind: KindCtrlT})
	m.SetStatus("") // 切换的合法状态提示不应计入默认帧
	after := rowTexts(m.Frame(100, 24))

	if !reflect.DeepEqual(before, after) {
		t.Fatalf("default frame changed after toggling twice:\nbefore:\n%s\nafter:\n%s",
			strings.Join(before, "\n"), strings.Join(after, "\n"))
	}
	if m.reasoningAllExpanded || len(m.reasoningExpanded) != 0 {
		t.Fatalf("toggle left residual state: all=%v overrides=%+v",
			m.reasoningAllExpanded, m.reasoningExpanded)
	}
}
