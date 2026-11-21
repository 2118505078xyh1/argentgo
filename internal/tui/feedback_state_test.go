package tui

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// decodeRunes 把字符串拆成码点切片，便于对 CJK / emoji 逐码点断言，
// 同时借由 utf8.RuneError 暴露任何被切坏的 UTF-8 序列。
func decodeRunes(t *testing.T, s string) []rune {
	t.Helper()
	if !utf8.ValidString(s) {
		t.Fatalf("text %q is not valid UTF-8", s)
	}
	return []rune(s)
}

// TestFeedbackDraftAppendRune 表驱动覆盖 ASCII、CJK、emoji 的追加，确认
// Active 标志不变、文本按码点累加，且结果始终是合法 UTF-8。
func TestFeedbackDraftAppendRune(t *testing.T) {
	tests := []struct {
		name    string
		draft   FeedbackDraft
		r       rune
		want    string
		wantLen int // 期望的码点数
	}{
		{
			name:    "ascii into empty draft",
			draft:   FeedbackDraft{},
			r:       'a',
			want:    "a",
			wantLen: 1,
		},
		{
			name:    "ascii appended to existing text",
			draft:   FeedbackDraft{Text: "ab"},
			r:       'c',
			want:    "abc",
			wantLen: 3,
		},
		{
			name:    "CJK rune preserves multibyte boundaries",
			draft:   FeedbackDraft{Text: "你"},
			r:       '好',
			want:    "你好",
			wantLen: 2,
		},
		{
			name:    "emoji rune appended intact",
			draft:   FeedbackDraft{Text: "ok"},
			r:       '😀',
			want:    "ok😀",
			wantLen: 3,
		},
		{
			name:    "active draft keeps active flag",
			draft:   FeedbackDraft{Active: true, Text: "反"},
			r:       '对',
			want:    "反对",
			wantLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := tt.draft
			got := tt.draft.AppendRune(tt.r)
			if got.Text != tt.want {
				t.Fatalf("Text = %q, want %q", got.Text, tt.want)
			}
			if got.Active != before.Active {
				t.Fatalf("Active = %v, want %v", got.Active, before.Active)
			}
			if n := utf8.RuneCountInString(got.Text); n != tt.wantLen {
				t.Fatalf("rune count = %d, want %d", n, tt.wantLen)
			}
			for _, r := range decodeRunes(t, got.Text) {
				if r == utf8.RuneError {
					t.Fatalf("broken UTF-8 rune in %q", got.Text)
				}
			}
			// 纯函数：入参草稿不得被修改。
			if tt.draft != before {
				t.Fatalf("input draft mutated: got %+v, before %+v", tt.draft, before)
			}
		})
	}
}

// TestFeedbackDraftAppendRuneLimit 锁定限长契约：按 rune 而非字节计数，
// 达到 2000 码点后追加被忽略；用 CJK / emoji 验证多字节字符也按码点计数。
func TestFeedbackDraftAppendRuneLimit(t *testing.T) {
	// 恰好填满上限。
	var full FeedbackDraft
	for i := 0; i < maxFeedbackRunes; i++ {
		full = full.AppendRune('字')
	}
	if n := utf8.RuneCountInString(full.Text); n != maxFeedbackRunes {
		t.Fatalf("filled rune count = %d, want %d", n, maxFeedbackRunes)
	}

	// 已满时再追加（含 emoji）应无变化，且返回值与入参逐字节一致。
	before := full
	got := full.AppendRune('😀')
	if got != before {
		t.Fatalf("append beyond limit changed draft: got %q, want %q", got.Text, before.Text)
	}
	if full != before {
		t.Fatalf("input draft mutated at limit")
	}

	// 用 emoji 填充时同样按码点数限长，不会被 4 字节宽度提前截断。
	var emoji FeedbackDraft
	for i := 0; i < maxFeedbackRunes+5; i++ {
		emoji = emoji.AppendRune('🚀')
	}
	if n := utf8.RuneCountInString(emoji.Text); n != maxFeedbackRunes {
		t.Fatalf("emoji rune count = %d, want %d", n, maxFeedbackRunes)
	}
	if !utf8.ValidString(emoji.Text) {
		t.Fatal("emoji-filled text is not valid UTF-8")
	}

	// 接近上限时再追加一个应成功。
	near := FeedbackDraft{}
	for i := 0; i < maxFeedbackRunes-1; i++ {
		near = near.AppendRune('a')
	}
	near = near.AppendRune('b')
	if n := utf8.RuneCountInString(near.Text); n != maxFeedbackRunes || !strings.HasSuffix(near.Text, "b") {
		t.Fatalf("append at last slot failed: runes=%d text=%q", n, near.Text)
	}
}

// TestFeedbackDraftBackspace 表驱动覆盖空文本、ASCII、CJK、emoji 的回退，
// 断言每次只删除一个码点且结果合法。
func TestFeedbackDraftBackspace(t *testing.T) {
	tests := []struct {
		name  string
		draft FeedbackDraft
		want  string
	}{
		{
			name:  "empty text unchanged",
			draft: FeedbackDraft{},
			want:  "",
		},
		{
			name:  "ascii removes one byte",
			draft: FeedbackDraft{Text: "abc"},
			want:  "ab",
		},
		{
			name:  "single ascii becomes empty",
			draft: FeedbackDraft{Text: "a"},
			want:  "",
		},
		{
			name:  "CJK removes one whole rune",
			draft: FeedbackDraft{Text: "你好"},
			want:  "你",
		},
		{
			name:  "single CJK becomes empty",
			draft: FeedbackDraft{Text: "好"},
			want:  "",
		},
		{
			name:  "emoji removes one whole rune",
			draft: FeedbackDraft{Text: "😀😀"},
			want:  "😀",
		},
		{
			name:  "mixed text removes trailing emoji",
			draft: FeedbackDraft{Active: true, Text: "拒绝😀"},
			want:  "拒绝",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := tt.draft
			got := tt.draft.Backspace()
			if got.Text != tt.want {
				t.Fatalf("Text = %q, want %q", got.Text, tt.want)
			}
			if got.Active != before.Active {
				t.Fatalf("Active = %v, want %v", got.Active, before.Active)
			}
			for _, r := range decodeRunes(t, got.Text) {
				if r == utf8.RuneError {
					t.Fatalf("broken UTF-8 rune in %q", got.Text)
				}
			}
			if tt.draft != before {
				t.Fatalf("input draft mutated: got %+v, before %+v", tt.draft, before)
			}
		})
	}
}

// TestFeedbackDraftBackspaceCJKEmojiBoundary 专门锁定多字节边界：逐码点
// 回退时每个中间态都必须与预期码点序列一致，绝不能出现半个 UTF-8 序列。
func TestFeedbackDraftBackspaceCJKEmojiBoundary(t *testing.T) {
	d := FeedbackDraft{Text: "你😀好🚀"}
	wantSeq := []string{"你😀好", "你😀", "你", ""}
	for i, want := range wantSeq {
		d = d.Backspace()
		if d.Text != want {
			t.Fatalf("step %d: Text = %q, want %q", i+1, d.Text, want)
		}
		rs := decodeRunes(t, d.Text)
		for _, r := range rs {
			if r == utf8.RuneError {
				t.Fatalf("step %d: broken UTF-8 rune in %q", i+1, d.Text)
			}
		}
	}
	// 已空时继续回退仍是空。
	if got := d.Backspace(); got.Text != "" {
		t.Fatalf("backspace on empty produced %q", got.Text)
	}
}

// TestFeedbackDraftShouldFinish 表驱动覆盖激活态、空/空白文本、各按键以及
// 大小写与未知键，确认 Esc 取消、Enter 仅在非空时提交、其余键无动作。
func TestFeedbackDraftShouldFinish(t *testing.T) {
	tests := []struct {
		name  string
		draft FeedbackDraft
		key   string
		want  bool
	}{
		{
			name:  "inactive enter with text does not finish",
			draft: FeedbackDraft{Text: "理由"},
			key:   feedbackKeyEnter,
			want:  false,
		},
		{
			name:  "inactive esc does not finish",
			draft: FeedbackDraft{},
			key:   feedbackKeyEsc,
			want:  false,
		},
		{
			name:  "active enter with text finishes",
			draft: FeedbackDraft{Active: true, Text: "不同意"},
			key:   feedbackKeyEnter,
			want:  true,
		},
		{
			name:  "active enter with CJK emoji text finishes",
			draft: FeedbackDraft{Active: true, Text: "危险😀"},
			key:   feedbackKeyEnter,
			want:  true,
		},
		{
			name:  "active enter with empty text does not finish",
			draft: FeedbackDraft{Active: true},
			key:   feedbackKeyEnter,
			want:  false,
		},
		{
			name:  "active enter with whitespace only does not finish",
			draft: FeedbackDraft{Active: true, Text: "   \t\n"},
			key:   feedbackKeyEnter,
			want:  false,
		},
		{
			name:  "active esc with text cancels",
			draft: FeedbackDraft{Active: true, Text: "理由"},
			key:   feedbackKeyEsc,
			want:  true,
		},
		{
			name:  "active esc with empty text cancels",
			draft: FeedbackDraft{Active: true},
			key:   feedbackKeyEsc,
			want:  true,
		},
		{
			name:  "active unknown key does not finish",
			draft: FeedbackDraft{Active: true, Text: "理由"},
			key:   "ctrl+c",
			want:  false,
		},
		{
			name:  "key matching is case sensitive",
			draft: FeedbackDraft{Active: true, Text: "理由"},
			key:   "ENTER",
			want:  false,
		},
		{
			name:  "empty key does not finish",
			draft: FeedbackDraft{Active: true, Text: "理由"},
			key:   "",
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := tt.draft
			got := tt.draft.ShouldFinish(tt.key)
			if got != tt.want {
				t.Fatalf("ShouldFinish(%q) = %v, want %v", tt.key, got, tt.want)
			}
			if tt.draft != before {
				t.Fatalf("input draft mutated: got %+v, before %+v", tt.draft, before)
			}
		})
	}
}

// TestApplyDraftToSignal 表驱动反馈草稿到渲染信号的投影：未激活时原样返回
// （默认关闭逐字节不变），激活时只改 StatusText、保留其它所有字段。
func TestApplyDraftToSignal(t *testing.T) {
	tests := []struct {
		name  string
		draft FeedbackDraft
		sig   RenderSignal
		want  RenderSignal
	}{
		{
			name:  "inactive zero draft leaves zero signal unchanged",
			draft: FeedbackDraft{},
			sig:   RenderSignal{},
			want:  RenderSignal{},
		},
		{
			name:  "inactive draft leaves full signal byte-for-byte",
			draft: FeedbackDraft{Text: "理由"},
			sig:   RenderSignal{ShowSpinner: true, AcceptInput: true, StatusText: statusReady},
			want:  RenderSignal{ShowSpinner: true, AcceptInput: true, StatusText: statusReady},
		},
		{
			name:  "active draft overrides only status text",
			draft: FeedbackDraft{Active: true, Text: "理由"},
			sig:   RenderSignal{ShowSpinner: true, AcceptInput: true, StatusText: statusReady},
			want:  RenderSignal{ShowSpinner: true, AcceptInput: true, StatusText: feedbackHint},
		},
		{
			name:  "active draft with empty text still overrides status",
			draft: FeedbackDraft{Active: true},
			sig:   RenderSignal{AcceptInput: true, StatusText: statusWelcome},
			want:  RenderSignal{AcceptInput: true, StatusText: feedbackHint},
		},
		{
			name:  "active draft preserves false bool fields",
			draft: FeedbackDraft{Active: true},
			sig:   RenderSignal{ShowSpinner: false, AcceptInput: false, StatusText: statusApproval},
			want:  RenderSignal{ShowSpinner: false, AcceptInput: false, StatusText: feedbackHint},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sigBefore := tt.sig
			draftBefore := tt.draft
			assertSignal(t, ApplyDraftToSignal(tt.draft, tt.sig), tt.want)
			// 纯函数：两个入参都不得被修改。
			if tt.sig != sigBefore || tt.draft != draftBefore {
				t.Fatalf("input mutated: sig=%+v draft=%+v", tt.sig, tt.draft)
			}
		})
	}
}

// TestFeedbackDraftZeroValue 锁定零值契约：零值草稿对所有原语都惰性——
// 不提交、不改信号、回退仍为空；仅追加会写入单个码点（纯文本变换）。
func TestFeedbackDraftZeroValue(t *testing.T) {
	var d FeedbackDraft
	if d.Active || d.Text != "" {
		t.Fatalf("zero draft is not zero: %+v", d)
	}
	if got := d.Backspace(); got != d {
		t.Fatalf("backspace changed zero draft: %+v", got)
	}
	for _, key := range []string{feedbackKeyEnter, feedbackKeyEsc, "", "x"} {
		if d.ShouldFinish(key) {
			t.Fatalf("zero draft finished on %q", key)
		}
	}
	sig := RenderSignal{ShowSpinner: true, AcceptInput: true, StatusText: statusReady}
	assertSignal(t, ApplyDraftToSignal(d, sig), sig)

	got := d.AppendRune('你')
	if got.Text != "你" || got.Active {
		t.Fatalf("append rune to zero draft = %+v", got)
	}
}

// TestFeedbackHintIsChinese 锁定提示文案契约（与 turn_projection.go 的阶段
// 文案一致）：非空、合法 UTF-8 且含汉字，日后迁入 locale 表时应保持中文。
func TestFeedbackHintIsChinese(t *testing.T) {
	if feedbackHint == "" {
		t.Fatal("feedbackHint must not be empty")
	}
	if !utf8.ValidString(feedbackHint) {
		t.Fatalf("feedbackHint %q is not valid UTF-8", feedbackHint)
	}
	hasHan := false
	for _, r := range feedbackHint {
		if unicode.Is(unicode.Han, r) {
			hasHan = true
			break
		}
	}
	if !hasHan {
		t.Fatalf("feedbackHint %q must contain a Han character", feedbackHint)
	}
}
