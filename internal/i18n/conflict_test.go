package i18n

import "testing"

func TestHasPlaceholderConflictDetectsMixedStyles(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"empty string", "", false},
		{"plain ASCII text", "plain text", false},
		{"plain CJK text", "已加载技能", false},
		{"only positional %s", "model error: %s", false},
		{"only positional %d", "%d commands", false},
		{"only named placeholder", "Hello, {name}!", false},
		{"only named placeholder in CJK", "你好，{name}！", false},
		{"both styles", "error %s for {name}", true},
		{"both styles in CJK", "错误 %s，用户 {name}", true},
		{"positional after named", "{name} took %d ms", true},
		{"repeated placeholders of both styles", "%s %s {a} {b}", true},
		{"percent next to CJK is not a verb", "完成度 100% {name}", false},
		{"invalid brace shape is not a placeholder", "%s and {a-b}", false},
		{"escaped percent then a real verb still conflicts", "%% %s {name}", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasPlaceholderConflict(tc.text); got != tc.want {
				t.Errorf("HasPlaceholderConflict(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestHasPlaceholderConflictRespectsEscapes(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"escaped percent is not a verb", "100%% done {name}", false},
		{"escaped braces are not placeholders", "literal {{name}} with %s", false},
		{"both escaped", "100%% and {{name}}", false},
		{"escaped braces around a live placeholder", "{{{name}}} with %s", true},
		{"escaped percent beside a live verb", "%%%s with {name}", true},
		{"CJK with escaped braces only", "主题 {{name}}：%d", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasPlaceholderConflict(tc.text); got != tc.want {
				t.Errorf("HasPlaceholderConflict(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestHasPlaceholderConflictIsPureAndDoesNotRecordMissing(t *testing.T) {
	ResetMissing()
	text := "错误 %s，用户 {name}"
	first := HasPlaceholderConflict(text)
	second := HasPlaceholderConflict(text)
	if !first || !second {
		t.Fatalf("not deterministic: %v then %v", first, second)
	}
	if got := Missing(); len(got) != 0 {
		t.Errorf("HasPlaceholderConflict recorded missing keys: %#v", got)
	}
}
