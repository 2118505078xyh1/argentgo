package i18n

import (
	"reflect"
	"testing"
)

func setOf(keys ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		set[key] = struct{}{}
	}
	return set
}

func TestRealTablesHaveAlignedKeys(t *testing.T) {
	cases := []struct {
		name  string
		check func() KeyAlignment
	}{
		{"catalog", AuditCatalogKeyAlignment},
		{"error messages", AuditErrorMessagesKeyAlignment},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ResetMissing()
			got := tc.check()
			if !got.Aligned() {
				t.Errorf("key sets are not aligned: %#v", got)
			}
			if problems := got.Problems(); len(problems) != 0 {
				t.Errorf("unexpected problems: %#v", problems)
			}
			// 一致性检查必须是纯的：不向 Missing 记录任何内容。
			if missing := Missing(); len(missing) != 0 {
				t.Errorf("check recorded missing keys: %#v", missing)
			}
			// 重复调用结果必须确定。
			if again := tc.check(); !reflect.DeepEqual(got, again) {
				t.Errorf("non-deterministic result: %#v then %#v", got, again)
			}
		})
	}
}

func TestCompareKeySetsDetectsMissingAndExtra(t *testing.T) {
	cases := []struct {
		name          string
		en            map[string]struct{}
		zh            map[string]struct{}
		wantMissingEn []string
		wantMissingZh []string
	}{
		{
			name: "identical sets are aligned",
			en:   setOf("a", "b"),
			zh:   setOf("a", "b"),
		},
		{
			name:          "key only in en is missing in zh",
			en:            setOf("a", "b"),
			zh:            setOf("a"),
			wantMissingZh: []string{"b"},
		},
		{
			name:          "key only in zh is missing in en",
			en:            setOf("a"),
			zh:            setOf("a", "b"),
			wantMissingEn: []string{"b"},
		},
		{
			name:          "both directions are reported",
			en:            setOf("a", "b"),
			zh:            setOf("b", "c"),
			wantMissingEn: []string{"c"},
			wantMissingZh: []string{"a"},
		},
		{
			name: "empty sets are aligned",
			en:   setOf(),
			zh:   setOf(),
		},
		{
			name:          "zh missing every key",
			en:            setOf("a", "b"),
			zh:            setOf(),
			wantMissingZh: []string{"a", "b"},
		},
		{
			name:          "en missing every key",
			en:            setOf(),
			zh:            setOf("a", "b"),
			wantMissingEn: []string{"a", "b"},
		},
		{
			name:          "disjoint sets are sorted in both directions",
			en:            setOf("z", "a"),
			zh:            setOf("m", "b"),
			wantMissingEn: []string{"b", "m"},
			wantMissingZh: []string{"a", "z"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := compareKeySets(tc.en, tc.zh)
			want := KeyAlignment{MissingInEn: tc.wantMissingEn, MissingInZh: tc.wantMissingZh}
			if want.MissingInEn == nil {
				want.MissingInEn = []string{}
			}
			if want.MissingInZh == nil {
				want.MissingInZh = []string{}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("compareKeySets = %#v, want %#v", got, want)
			}
		})
	}
}

func TestCheckKeyAlignmentFromTable(t *testing.T) {
	cases := []struct {
		name          string
		table         map[string]localeTexts
		wantMissingEn []string
		wantMissingZh []string
	}{
		{
			name:  "both texts present",
			table: map[string]localeTexts{"k": {"E", "Z"}},
		},
		{
			name:          "empty en text is a missing en key",
			table:         map[string]localeTexts{"k": {"", "Z"}},
			wantMissingEn: []string{"k"},
		},
		{
			name:          "empty zh text is a missing zh key",
			table:         map[string]localeTexts{"k": {"E", ""}},
			wantMissingZh: []string{"k"},
		},
		{
			name:  "both texts empty is not a key-set mismatch",
			table: map[string]localeTexts{"k": {"", ""}},
		},
		{
			name: "mixed table reports both directions",
			table: map[string]localeTexts{
				"both":   {"E", "Z"},
				"enOnly": {"E", ""},
				"zhOnly": {"", "Z"},
			},
			wantMissingEn: []string{"zhOnly"},
			wantMissingZh: []string{"enOnly"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checkKeyAlignment(tc.table)
			want := KeyAlignment{MissingInEn: tc.wantMissingEn, MissingInZh: tc.wantMissingZh}
			if want.MissingInEn == nil {
				want.MissingInEn = []string{}
			}
			if want.MissingInZh == nil {
				want.MissingInZh = []string{}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("checkKeyAlignment = %#v, want %#v", got, want)
			}
		})
	}
}

func TestKeyAlignmentProblemsDescribeBothSides(t *testing.T) {
	got := KeyAlignment{MissingInEn: []string{"c"}, MissingInZh: []string{"a", "b"}}.Problems()
	want := []string{
		"en-US missing key (extra in zh-CN): c",
		"zh-CN missing key (extra in en-US): a",
		"zh-CN missing key (extra in en-US): b",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Problems() = %#v, want %#v", got, want)
	}
	if problems := (KeyAlignment{}).Problems(); len(problems) != 0 {
		t.Errorf("aligned alignment produced problems: %#v", problems)
	}
}
