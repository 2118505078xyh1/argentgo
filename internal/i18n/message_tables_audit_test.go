package i18n

import (
	"reflect"
	"testing"
)

// TestMessageTablesKeyAlignmentAndPlaceholdersAreClean 把四张独立消息表纳入
// 与 catalog 相同的门禁：键对齐与占位符（含 fmt 动词）当前都必须一致，
// 且审计必须纯、确定。
func TestMessageTablesKeyAlignmentAndPlaceholdersAreClean(t *testing.T) {
	ResetMissing()

	views := messageTableViews()
	if len(views) != 4 {
		t.Fatalf("messageTableViews count = %d, want 4", len(views))
	}

	alignments := AuditMessageTablesKeyAlignment()
	if len(alignments) != len(views) {
		t.Fatalf("AuditMessageTablesKeyAlignment count = %d, want %d", len(alignments), len(views))
	}
	for i, alignment := range alignments {
		if alignment.name != views[i].name {
			t.Errorf("alignment[%d].name = %q, want %q", i, alignment.name, views[i].name)
		}
		if !alignment.align.Aligned() {
			t.Errorf("table %q key sets not aligned: %#v", alignment.name, alignment.align)
		}
	}

	// AuditPlaceholders 现已覆盖四张表：这些表当前一致，故不产生任何问题。
	if issues := AuditPlaceholders(); len(issues) != 0 {
		t.Errorf("message tables have placeholder mismatches: %#v", issues)
	}
	if missing := Missing(); len(missing) != 0 {
		t.Errorf("audit recorded missing keys: %#v", missing)
	}
	if again := AuditMessageTablesKeyAlignment(); !reflect.DeepEqual(alignments, again) {
		t.Errorf("non-deterministic key alignment: %#v then %#v", alignments, again)
	}
	if again := AuditPlaceholders(); len(again) != 0 {
		t.Errorf("non-deterministic placeholders: %#v", again)
	}
}

// TestNewAuditReportWithTablesCountsExtraAlignments 验证新增的 extra 参数
// 被计入键问题总数；nil 时与旧口径完全一致。
func TestNewAuditReportWithTablesCountsExtraAlignments(t *testing.T) {
	cases := []struct {
		name            string
		extra           []namedKeyAlignment
		wantKeys        int
		wantPlaceholder int
		wantOK          bool
	}{
		{
			name:   "nil extra matches old signature",
			wantOK: true,
		},
		{
			name: "aligned extra contributes nothing",
			extra: []namedKeyAlignment{
				{name: placeholderTableCommandMessages, align: KeyAlignment{}},
			},
			wantOK: true,
		},
		{
			name: "extra missing keys are counted",
			extra: []namedKeyAlignment{
				{name: placeholderTableReliabilityMessages, align: KeyAlignment{MissingInEn: []string{"a"}}},
				{name: placeholderTableOptInMessages, align: KeyAlignment{MissingInZh: []string{"b", "c"}}},
			},
			wantKeys: 3,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := newAuditReportWithTables(KeyAlignment{}, KeyAlignment{}, tc.extra, nil)
			want := AuditReport{
				KeyIssues:         tc.wantKeys,
				PlaceholderIssues: tc.wantPlaceholder,
				OK:                tc.wantOK,
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("newAuditReportWithTables = %#v, want %#v", got, want)
			}
		})
	}
}

// TestNewAuditDetailsWithTablesExpandsExtraAlignments 验证 extra 中的每条
// 缺失键都按来源表名展开为一条明细，并参与稳定排序。
func TestNewAuditDetailsWithTablesExpandsExtraAlignments(t *testing.T) {
	extra := []namedKeyAlignment{
		{name: placeholderTableOptInMessages, align: KeyAlignment{MissingInZh: []string{"b"}}},
		{name: placeholderTableCommandMessages, align: KeyAlignment{MissingInEn: []string{"a"}}},
	}
	want := []AuditDetail{
		{Table: placeholderTableCommandMessages, Key: "a", Issue: "en-US missing key (extra in zh-CN): a"},
		{Table: placeholderTableOptInMessages, Key: "b", Issue: "zh-CN missing key (extra in en-US): b"},
	}

	got := newAuditDetailsWithTables(KeyAlignment{}, KeyAlignment{}, extra, nil)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("newAuditDetailsWithTables = %#v, want %#v", got, want)
	}
	if !detailsAreSorted(got) {
		t.Errorf("details not sorted by (Table, Key, Issue): %#v", got)
	}
	if nilDetails := newAuditDetailsWithTables(KeyAlignment{}, KeyAlignment{}, nil, nil); nilDetails != nil {
		t.Errorf("nil extra should keep nil result, got %#v", nilDetails)
	}
}

// TestAuditLocaleTextsDetectsAllIssueKinds 验证四张表复用的通用检查器确实
// 能发现缺失文本、fmt 动词不一致与命名占位符不一致三类问题。
func TestAuditLocaleTextsDetectsAllIssueKinds(t *testing.T) {
	table := map[string]localeTexts{
		"missing": {"hello", ""},
		"verb":    {"count %d", "数量 %s"},
		"named":   {"hi {name}", "你好 {other}"},
		"healthy": {"value %s", "值 %s"},
	}

	issues := auditLocaleTexts(table, nil)
	got := make(map[string][]IssueKind)
	for _, issue := range issues {
		got[issue.Key] = append(got[issue.Key], issue.Kind)
	}
	want := map[string][]IssueKind{
		"missing": {IssueMissingText},
		"verb":    {IssueFormatVerbMismatch},
		"named":   {IssuePlaceholderMismatch},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("auditLocaleTexts kinds = %#v, want %#v", got, want)
	}
	if len(issues) == 0 {
		t.Fatal("auditLocaleTexts returned no issues")
	}
	// 结果必须按 (Key, Locale, Kind, Detail) 稳定排序。
	for i := 1; i < len(issues); i++ {
		a, b := issues[i-1], issues[i]
		if a.Key > b.Key || (a.Key == b.Key && a.Locale > b.Locale) {
			t.Fatalf("issues not sorted: %#v then %#v", a, b)
		}
	}
}

// TestAuditTranslationsCoversMessageTables 验证 AuditTranslations 已把四张表
// 纳入审计：真实表当前干净，且各表视图确实存在。
func TestAuditTranslationsCoversMessageTables(t *testing.T) {
	ResetMissing()
	issues := AuditTranslations()
	if len(issues) != 0 {
		t.Errorf("healthy message tables produced issues: %#v", issues)
	}
	for _, table := range messageTableViews() {
		if table.name == "" || len(table.entries) == 0 {
			t.Errorf("message table %q has no entries", table.name)
		}
	}
	if again := AuditTranslations(); !reflect.DeepEqual(issues, again) {
		t.Errorf("non-deterministic AuditTranslations: %#v then %#v", issues, again)
	}
}
