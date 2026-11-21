package i18n

import (
	"reflect"
	"testing"
)

func markerSet(markers ...string) map[string]int {
	out := make(map[string]int, len(markers))
	for _, marker := range markers {
		out[marker]++
	}
	return out
}

func TestPlaceholderMarkersRecognizesAllSyntaxes(t *testing.T) {
	cases := []struct {
		name string
		text string
		want map[string]int
	}{
		{name: "plain text has no markers", text: "ready 就绪", want: map[string]int{}},
		{name: "fmt string verb", text: "model error: %s", want: markerSet("%s")},
		{name: "fmt int verb in CJK text", text: "可用技能 %d 个", want: markerSet("%d")},
		{name: "positional fmt verb normalizes to letter", text: "arg %[2]s", want: markerSet("%s")},
		{name: "escaped percent is not a verb", text: "100%%s done", want: map[string]int{}},
		{name: "stray percent is ignored", text: "50% 完成", want: map[string]int{}},
		{name: "other fmt verbs", text: "%v %q %x", want: markerSet("%v", "%q", "%x")},
		{name: "double brace named placeholder", text: "Hello {{name}}", want: markerSet("{{name}}")},
		{name: "double brace not double counted as single", text: "{{name}}", want: markerSet("{{name}}")},
		{name: "single brace named placeholder", text: "Hello {name}", want: markerSet("{name}")},
		{name: "single brace index placeholder", text: "arg {0}", want: markerSet("{0}")},
		{name: "digit and underscore name", text: "{a_1} and {2}", want: markerSet("{a_1}", "{2}")},
		{
			name: "mixed syntaxes with CJK",
			text: "中文 %s，等待 {name} 与 {{token}}，共 %d 项",
			want: markerSet("%s", "{name}", "{{token}}", "%d"),
		},
		{name: "repeated markers keep counts", text: "%s and %s plus %d", want: map[string]int{"%s": 2, "%d": 1}},
		{name: "invalid braces are not markers", text: "{a-b} {  {{", want: map[string]int{}},
		{name: "single brace next to CJK", text: "请输入{n}后继续", want: markerSet("{n}")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := placeholderMarkers(tc.text)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("placeholderMarkers(%q) = %#v, want %#v", tc.text, got, tc.want)
			}
		})
	}
}

func TestDiffMarkersReportsBothDirectionsAndCounts(t *testing.T) {
	cases := []struct {
		name          string
		en            map[string]int
		zh            map[string]int
		wantMissingEn []string
		wantMissingZh []string
	}{
		{name: "identical sets", en: markerSet("%s", "%d"), zh: markerSet("%s", "%d")},
		{
			name:          "marker only in en is missing in zh",
			en:            markerSet("%s", "%d"),
			zh:            markerSet("%s"),
			wantMissingZh: []string{"%d"},
		},
		{
			name:          "marker only in zh is missing in en",
			en:            markerSet("%s"),
			zh:            markerSet("%s", "{{name}}"),
			wantMissingEn: []string{"{{name}}"},
		},
		{
			name:          "syntax mismatch is reported both ways",
			en:            markerSet("{name}"),
			zh:            markerSet("{{name}}"),
			wantMissingEn: []string{"{{name}}"},
			wantMissingZh: []string{"{name}"},
		},
		{
			name:          "count shortage reports expected count",
			en:            map[string]int{"%s": 2},
			zh:            markerSet("%s"),
			wantMissingZh: []string{"%s*2"},
		},
		{
			name:          "multiple shortages are sorted",
			en:            markerSet("%d", "%s"),
			zh:            markerSet("{0}"),
			wantMissingEn: []string{"{0}"},
			wantMissingZh: []string{"%d", "%s"},
		},
		{
			name:          "both sides deficient in different markers",
			en:            markerSet("%s", "%d"),
			zh:            markerSet("%s", "{{token}}"),
			wantMissingEn: []string{"{{token}}"},
			wantMissingZh: []string{"%d"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			missingEn, missingZh := diffMarkers(tc.en, tc.zh)
			if tc.wantMissingEn == nil {
				tc.wantMissingEn = []string{}
			}
			if tc.wantMissingZh == nil {
				tc.wantMissingZh = []string{}
			}
			if !reflect.DeepEqual(missingEn, tc.wantMissingEn) {
				t.Errorf("diffMarkers missingInEn = %#v, want %#v", missingEn, tc.wantMissingEn)
			}
			if !reflect.DeepEqual(missingZh, tc.wantMissingZh) {
				t.Errorf("diffMarkers missingInZh = %#v, want %#v", missingZh, tc.wantMissingZh)
			}
		})
	}
}

func TestCheckPlaceholderTableFromSimulatedEntries(t *testing.T) {
	cases := []struct {
		name  string
		table map[string]localeTexts
		want  []PlaceholderIssue
	}{
		{
			name:  "consistent entries produce no issues",
			table: map[string]localeTexts{"a": {"hello %s", "你好 %s"}},
		},
		{
			name:  "single mismatch reports missing direction",
			table: map[string]localeTexts{"a": {"hello %s", "你好"}},
			want: []PlaceholderIssue{
				{Key: "a", Table: "sim", MissingInEn: []string{}, MissingInZh: []string{"%s"}},
			},
		},
		{
			name: "both directions across entries",
			table: map[string]localeTexts{
				"a":   {"hello %s", "你好 %s"},
				"bad": {"hello {{name}}", "你好 {name}"},
			},
			want: []PlaceholderIssue{
				{Key: "bad", Table: "sim", MissingInEn: []string{"{name}"}, MissingInZh: []string{"{{name}}"}},
			},
		},
		{
			name: "empty translation is skipped and not a placeholder issue",
			table: map[string]localeTexts{
				"emptyZh": {"hello %s", ""},
				"emptyEn": {"", "你好 %s"},
			},
		},
		{
			name:  "CJK text with matching marker is consistent",
			table: map[string]localeTexts{"cjk": {"%d items", "共 %d 项"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checkPlaceholderTable(tc.table, "sim")
			if tc.want == nil {
				tc.want = []PlaceholderIssue{}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("checkPlaceholderTable = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestAuditPlaceholderTablesSortsStably(t *testing.T) {
	// 模拟两张表，键以乱序写入；结果必须按 Table、Key 稳定排序。
	catalogTable := placeholderTable{name: placeholderTableCatalog, entries: map[string]localeTexts{
		"zeta":  {"hello %s", "你好"},
		"alpha": {"count %d", "数量 %s"},
	}}
	errorTable := placeholderTable{name: placeholderTableErrorMessages, entries: map[string]localeTexts{
		"beta": {"failed %s", "失败 {{why}}"},
	}}

	got := auditPlaceholderTables(errorTable, catalogTable)
	want := []PlaceholderIssue{
		{
			Key: "alpha", Table: placeholderTableCatalog,
			MissingInEn: []string{"%s"}, MissingInZh: []string{"%d"},
		},
		{
			Key: "zeta", Table: placeholderTableCatalog,
			MissingInEn: []string{}, MissingInZh: []string{"%s"},
		},
		{
			Key: "beta", Table: placeholderTableErrorMessages,
			MissingInEn: []string{"{{why}}"}, MissingInZh: []string{"%s"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("auditPlaceholderTables = %#v, want %#v", got, want)
	}

	// 重复调用结果必须一致（纯函数 + 稳定排序）。
	if again := auditPlaceholderTables(errorTable, catalogTable); !reflect.DeepEqual(got, again) {
		t.Errorf("non-deterministic result: %#v then %#v", got, again)
	}
}

func TestPlaceholderIssueProblemsDescribeBothSides(t *testing.T) {
	issue := PlaceholderIssue{
		Key:         "k",
		Table:       placeholderTableCatalog,
		MissingInEn: []string{"{{name}}"},
		MissingInZh: []string{"%d", "%s"},
	}
	want := []string{
		"catalog key=k en-US missing placeholder: {{name}}",
		"catalog key=k zh-CN missing placeholder: %d",
		"catalog key=k zh-CN missing placeholder: %s",
	}
	if got := issue.Problems(); !reflect.DeepEqual(got, want) {
		t.Errorf("Problems() = %#v, want %#v", got, want)
	}
	if problems := (PlaceholderIssue{}).Problems(); len(problems) != 0 {
		t.Errorf("empty issue produced problems: %#v", problems)
	}
}

func TestAuditPlaceholdersOnRealTablesIsCleanAndPure(t *testing.T) {
	ResetMissing()
	got := AuditPlaceholders()
	if len(got) != 0 {
		t.Errorf("real tables have placeholder mismatches: %#v", got)
	}
	// 审计必须纯：不向 Missing 记录任何内容。
	if missing := Missing(); len(missing) != 0 {
		t.Errorf("audit recorded missing keys: %#v", missing)
	}
	// 重复调用必须确定。
	if again := AuditPlaceholders(); !reflect.DeepEqual(got, again) {
		t.Errorf("non-deterministic result: %#v then %#v", got, again)
	}
}
