package i18n

import (
	"fmt"
	"testing"
)

func TestPluralSelectsFormByLocaleAndCount(t *testing.T) {
	const singular, plural = "%d command", "%d commands"
	cases := []struct {
		name   string
		locale string
		n      int
		want   string
	}{
		{"en one is singular", EnUS, 1, singular},
		{"en zero is plural", EnUS, 0, plural},
		{"en two is plural", EnUS, 2, plural},
		{"en many is plural", EnUS, 100, plural},
		{"en negative is plural", EnUS, -1, plural},
		{"en short alias", "en", 1, singular},
		{"en region alias falls back to English", "en-GB", 2, plural},
		{"zh one uses neutral form", ZhCN, 1, singular},
		{"zh many uses neutral form", ZhCN, 3, singular},
		{"zh zero uses neutral form", ZhCN, 0, singular},
		{"zh short alias", "zh", 5, singular},
		{"zh Chinese name alias", "中文", 2, singular},
		{"unknown locale one falls back to English", "fr-FR", 1, singular},
		{"unknown locale many falls back to English", "fr-FR", 4, plural},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Plural(tc.locale, tc.n, singular, plural); got != tc.want {
				t.Errorf("Plural(%q, %d, ...) = %q, want %q", tc.locale, tc.n, got, tc.want)
			}
		})
	}
}

func TestPluralWithSingleFormAlwaysReturnsIt(t *testing.T) {
	cases := []struct {
		locale string
		n      int
	}{
		{EnUS, 1}, {EnUS, 3}, {ZhCN, 1}, {ZhCN, 3}, {"fr-FR", 2},
	}
	for _, tc := range cases {
		if got := Plural(tc.locale, tc.n, "only form"); got != "only form" {
			t.Errorf("Plural(%q, %d, one form) = %q", tc.locale, tc.n, got)
		}
	}
}

func TestPluralWithNoFormsReturnsEmpty(t *testing.T) {
	if got := Plural(EnUS, 1); got != "" {
		t.Errorf("Plural(EnUS, 1) with no forms = %q, want empty", got)
	}
	if got := Plural(ZhCN, 3); got != "" {
		t.Errorf("Plural(ZhCN, 3) with no forms = %q, want empty", got)
	}
}

func TestPluralIgnoresFormsBeyondTwo(t *testing.T) {
	if got := Plural(EnUS, 2, "one", "many", "extra"); got != "many" {
		t.Errorf("plural with extra form = %q, want many", got)
	}
	if got := Plural(EnUS, 1, "one", "many", "extra"); got != "one" {
		t.Errorf("singular with extra form = %q, want one", got)
	}
	if got := Plural(ZhCN, 9, "one", "many", "extra"); got != "one" {
		t.Errorf("zh with extra form = %q, want one", got)
	}
}

func TestPluralIntegratesWithCatalog(t *testing.T) {
	// 该能力必须用于真实嵌入的 activity.*_one/_many 键：
	// 选择形态，然后让 T/fmt.Sprintf 嵌入计数。
	cases := []struct {
		locale string
		n      int
		base   string
		want   string
	}{
		{EnUS, 1, "activity.command", "1 command"},
		{EnUS, 3, "activity.command", "3 commands"},
		{ZhCN, 1, "activity.command", "1 条命令"},
		{ZhCN, 3, "activity.command", "3 条命令"},
		{EnUS, 1, "activity.edit", "1 edit"},
		{EnUS, 2, "activity.edit", "2 edits"},
		{ZhCN, 2, "activity.edit", "2 次编辑"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s/%d", tc.locale, tc.n), func(t *testing.T) {
			form := Plural(tc.locale, tc.n,
				T(tc.locale, tc.base+"_one"),
				T(tc.locale, tc.base+"_many"),
			)
			if got := fmt.Sprintf(form, tc.n); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPluralIsPureAndDoesNotRecordMissing(t *testing.T) {
	ResetMissing()
	first := Plural(ZhCN, 3, "x", "y")
	second := Plural(ZhCN, 3, "x", "y")
	if first != second || first != "x" {
		t.Errorf("not deterministic: %q then %q", first, second)
	}
	if got := Missing(); len(got) != 0 {
		t.Errorf("Plural recorded missing keys: %#v", got)
	}
}
