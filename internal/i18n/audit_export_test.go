package i18n

import (
	"reflect"
	"testing"
)

// detailsAreSorted 报告明细是否按 (Table, Key, Issue) 字典序非降排列。
func detailsAreSorted(details []AuditDetail) bool {
	for i := 1; i < len(details); i++ {
		a, b := details[i-1], details[i]
		switch {
		case a.Table > b.Table:
			return false
		case a.Table == b.Table && a.Key > b.Key:
			return false
		case a.Table == b.Table && a.Key == b.Key && a.Issue > b.Issue:
			return false
		}
	}
	return true
}

func cloneKeyAlignment(a KeyAlignment) KeyAlignment {
	return KeyAlignment{
		MissingInEn: append([]string(nil), a.MissingInEn...),
		MissingInZh: append([]string(nil), a.MissingInZh...),
	}
}

func clonePlaceholderIssues(issues []PlaceholderIssue) []PlaceholderIssue {
	out := make([]PlaceholderIssue, len(issues))
	for i, issue := range issues {
		out[i] = PlaceholderIssue{
			Key:         issue.Key,
			Table:       issue.Table,
			MissingInEn: append([]string(nil), issue.MissingInEn...),
			MissingInZh: append([]string(nil), issue.MissingInZh...),
		}
	}
	return out
}

func TestAuditDetailsOnRealTablesIsCleanAndPure(t *testing.T) {
	ResetMissing()

	got := AuditDetails()
	if got != nil {
		t.Errorf("real tables should be clean, got details: %#v", got)
	}
	if missing := Missing(); len(missing) != 0 {
		t.Errorf("AuditDetails recorded missing keys: %#v", missing)
	}
	if again := AuditDetails(); !reflect.DeepEqual(got, again) {
		t.Errorf("non-deterministic result: %#v then %#v", got, again)
	}
}

func TestNewAuditDetailsExpandsAndSorts(t *testing.T) {
	cases := []struct {
		name          string
		catalog       KeyAlignment
		errorMessages KeyAlignment
		placeholders  []PlaceholderIssue
		want          []AuditDetail
	}{
		{
			name: "no problems returns nil",
		},
		{
			name:    "catalog key issues expand both directions and sort by key",
			catalog: KeyAlignment{MissingInEn: []string{"z"}, MissingInZh: []string{"a", "b"}},
			want: []AuditDetail{
				{Table: placeholderTableCatalog, Key: "a", Issue: "zh-CN missing key (extra in en-US): a"},
				{Table: placeholderTableCatalog, Key: "b", Issue: "zh-CN missing key (extra in en-US): b"},
				{Table: placeholderTableCatalog, Key: "z", Issue: "en-US missing key (extra in zh-CN): z"},
			},
		},
		{
			name:          "error message key issues use error messages table",
			errorMessages: KeyAlignment{MissingInEn: []string{"ecode"}},
			want: []AuditDetail{
				{Table: placeholderTableErrorMessages, Key: "ecode", Issue: "en-US missing key (extra in zh-CN): ecode"},
			},
		},
		{
			name: "placeholder problems expand every line",
			placeholders: []PlaceholderIssue{
				{Key: "k", Table: placeholderTableCatalog, MissingInEn: []string{"{{name}}"}, MissingInZh: []string{"%d", "%s"}},
			},
			want: []AuditDetail{
				{Table: placeholderTableCatalog, Key: "k", Issue: "catalog key=k en-US missing placeholder: {{name}}"},
				{Table: placeholderTableCatalog, Key: "k", Issue: "catalog key=k zh-CN missing placeholder: %d"},
				{Table: placeholderTableCatalog, Key: "k", Issue: "catalog key=k zh-CN missing placeholder: %s"},
			},
		},
		{
			name:          "all sources combined sort by table then key then issue",
			catalog:       KeyAlignment{MissingInEn: []string{"a"}, MissingInZh: []string{"b"}},
			errorMessages: KeyAlignment{MissingInZh: []string{"ecode"}},
			placeholders: []PlaceholderIssue{
				{Key: "c", Table: placeholderTableCatalog, MissingInEn: []string{"%s"}, MissingInZh: []string{"%d"}},
			},
			want: []AuditDetail{
				{Table: placeholderTableCatalog, Key: "a", Issue: "en-US missing key (extra in zh-CN): a"},
				{Table: placeholderTableCatalog, Key: "b", Issue: "zh-CN missing key (extra in en-US): b"},
				{Table: placeholderTableCatalog, Key: "c", Issue: "catalog key=c en-US missing placeholder: %s"},
				{Table: placeholderTableCatalog, Key: "c", Issue: "catalog key=c zh-CN missing placeholder: %d"},
				{Table: placeholderTableErrorMessages, Key: "ecode", Issue: "zh-CN missing key (extra in en-US): ecode"},
			},
		},
		{
			name:    "issue tie-break sorts placeholder before key problems",
			catalog: KeyAlignment{MissingInEn: []string{"dup"}, MissingInZh: []string{"dup"}},
			placeholders: []PlaceholderIssue{
				{Key: "dup", Table: placeholderTableCatalog, MissingInEn: []string{"%s"}},
			},
			want: []AuditDetail{
				{Table: placeholderTableCatalog, Key: "dup", Issue: "catalog key=dup en-US missing placeholder: %s"},
				{Table: placeholderTableCatalog, Key: "dup", Issue: "en-US missing key (extra in zh-CN): dup"},
				{Table: placeholderTableCatalog, Key: "dup", Issue: "zh-CN missing key (extra in en-US): dup"},
			},
		},
		{
			name: "empty placeholder issue contributes nothing",
			placeholders: []PlaceholderIssue{
				{Key: "k", Table: placeholderTableCatalog},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := newAuditDetails(tc.catalog, tc.errorMessages, tc.placeholders)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("newAuditDetails = %#v, want %#v", got, tc.want)
			}
			if !detailsAreSorted(got) {
				t.Errorf("details not sorted by (Table, Key, Issue): %#v", got)
			}
			if again := newAuditDetails(tc.catalog, tc.errorMessages, tc.placeholders); !reflect.DeepEqual(got, again) {
				t.Errorf("non-deterministic result: %#v then %#v", got, again)
			}
		})
	}
}

func TestNewAuditDetailsExpansionIsComplete(t *testing.T) {
	// 明细行数必须恰好等于各来源问题行数之和，与 FullAudit 的统计口径一致。
	catalog := KeyAlignment{MissingInEn: []string{"a", "b"}, MissingInZh: []string{"c"}}
	errorMessages := KeyAlignment{MissingInZh: []string{"ecode"}}
	placeholders := []PlaceholderIssue{
		{Key: "k1", Table: placeholderTableCatalog, MissingInEn: []string{"{{name}}"}, MissingInZh: []string{"%d", "%s"}},
		{Key: "k2", Table: placeholderTableErrorMessages, MissingInZh: []string{"{0}"}},
	}

	wantCount := len(catalog.MissingInEn) + len(catalog.MissingInZh) +
		len(errorMessages.MissingInEn) + len(errorMessages.MissingInZh) +
		len(placeholders[0].Problems()) + len(placeholders[1].Problems())
	got := newAuditDetails(catalog, errorMessages, placeholders)
	if len(got) != wantCount {
		t.Errorf("detail count = %d, want %d: %#v", len(got), wantCount, got)
	}

	report := newAuditReport(catalog, errorMessages, placeholders)
	if len(got) != report.KeyIssues+report.PlaceholderIssues {
		t.Errorf("detail count %d disagrees with audit report %#v", len(got), report)
	}
}

func TestNewAuditDetailsHandlesCJK(t *testing.T) {
	// CJK 键与占位符标记必须原样保留，并按 UTF-8 字节序稳定排序。
	catalog := KeyAlignment{MissingInZh: []string{"中文键", "a"}}
	placeholders := []PlaceholderIssue{
		{Key: "提示", Table: placeholderTableCatalog, MissingInEn: []string{"%d"}, MissingInZh: []string{"{{名字}}"}},
	}

	want := []AuditDetail{
		{Table: placeholderTableCatalog, Key: "a", Issue: "zh-CN missing key (extra in en-US): a"},
		{Table: placeholderTableCatalog, Key: "中文键", Issue: "zh-CN missing key (extra in en-US): 中文键"},
		{Table: placeholderTableCatalog, Key: "提示", Issue: "catalog key=提示 en-US missing placeholder: %d"},
		{Table: placeholderTableCatalog, Key: "提示", Issue: "catalog key=提示 zh-CN missing placeholder: {{名字}}"},
	}
	got := newAuditDetails(catalog, KeyAlignment{}, placeholders)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CJK details = %#v, want %#v", got, want)
	}
	if !detailsAreSorted(got) {
		t.Errorf("CJK details not sorted: %#v", got)
	}
}

func TestNewAuditDetailsDoesNotMutateInputs(t *testing.T) {
	catalog := KeyAlignment{MissingInEn: []string{"b", "a"}, MissingInZh: []string{"z"}}
	errorMessages := KeyAlignment{MissingInZh: []string{"e"}}
	placeholders := []PlaceholderIssue{
		{Key: "k", Table: placeholderTableCatalog, MissingInEn: []string{"%s"}, MissingInZh: []string{"%d"}},
	}

	catalogBefore, errorBefore := cloneKeyAlignment(catalog), cloneKeyAlignment(errorMessages)
	placeholdersBefore := clonePlaceholderIssues(placeholders)

	newAuditDetails(catalog, errorMessages, placeholders)

	if !reflect.DeepEqual(catalog, catalogBefore) {
		t.Errorf("catalog mutated: %#v, want %#v", catalog, catalogBefore)
	}
	if !reflect.DeepEqual(errorMessages, errorBefore) {
		t.Errorf("errorMessages mutated: %#v, want %#v", errorMessages, errorBefore)
	}
	if !reflect.DeepEqual(placeholders, placeholdersBefore) {
		t.Errorf("placeholders mutated: %#v, want %#v", placeholders, placeholdersBefore)
	}
}

func TestGroupByTable(t *testing.T) {
	cases := []struct {
		name    string
		details []AuditDetail
		want    map[string][]AuditDetail
	}{
		{
			name: "nil input returns empty map",
			want: map[string][]AuditDetail{},
		},
		{
			name:    "empty input returns empty map",
			details: []AuditDetail{},
			want:    map[string][]AuditDetail{},
		},
		{
			name: "single detail",
			details: []AuditDetail{
				{Table: "catalog", Key: "a", Issue: "i1"},
			},
			want: map[string][]AuditDetail{
				"catalog": {{Table: "catalog", Key: "a", Issue: "i1"}},
			},
		},
		{
			name: "groups multiple tables preserving input order",
			details: []AuditDetail{
				{Table: "error messages", Key: "b", Issue: "i2"},
				{Table: "catalog", Key: "a", Issue: "i1"},
				{Table: "catalog", Key: "c", Issue: "i3"},
			},
			want: map[string][]AuditDetail{
				"catalog":        {{Table: "catalog", Key: "a", Issue: "i1"}, {Table: "catalog", Key: "c", Issue: "i3"}},
				"error messages": {{Table: "error messages", Key: "b", Issue: "i2"}},
			},
		},
		{
			name: "CJK table name is a valid group key",
			details: []AuditDetail{
				{Table: "中文表", Key: "键", Issue: "问题"},
			},
			want: map[string][]AuditDetail{
				"中文表": {{Table: "中文表", Key: "键", Issue: "问题"}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var before []AuditDetail
			if tc.details != nil {
				before = make([]AuditDetail, len(tc.details))
				copy(before, tc.details)
			}
			got := GroupByTable(tc.details)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("GroupByTable = %#v, want %#v", got, tc.want)
			}
			if !reflect.DeepEqual(tc.details, before) {
				t.Errorf("GroupByTable mutated input: %#v, want %#v", tc.details, before)
			}
		})
	}
}

func TestGroupByTableReturnsIndependentCopies(t *testing.T) {
	details := []AuditDetail{
		{Table: "t2", Key: "b", Issue: "i2"},
		{Table: "t1", Key: "a", Issue: "i1"},
		{Table: "t1", Key: "c", Issue: "i3"},
	}
	got := GroupByTable(details)

	// 修改结果不影响入参，也不影响其它分组。
	got["t1"][0].Issue = "changed"
	if details[1].Issue != "i1" {
		t.Errorf("mutating result changed input: %#v", details)
	}
	if got["t2"][0].Issue != "i2" {
		t.Errorf("groups share backing array: %#v", got)
	}

	// 向一个分组追加不影响其它分组或入参。
	got["t1"] = append(got["t1"], AuditDetail{Table: "t1", Key: "d", Issue: "i4"})
	if len(got["t2"]) != 1 {
		t.Errorf("append to one group affected another: %#v", got)
	}
	if len(details) != 3 {
		t.Errorf("append to result changed input length: %#v", details)
	}

	// 修改入参同样不影响已返回的结果。
	details[1].Issue = "input-changed"
	if got["t1"][0].Issue != "changed" {
		t.Errorf("mutating input changed result: %#v", got)
	}
}
