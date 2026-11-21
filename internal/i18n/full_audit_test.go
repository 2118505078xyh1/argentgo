package i18n

import (
	"reflect"
	"testing"
	"unicode"
)

// placeholderIssue 构造一份带指定缺失方向的占位符问题，便于表驱动断言。
func placeholderIssue(missingInEn, missingInZh []string) PlaceholderIssue {
	return PlaceholderIssue{
		Key:         "k",
		Table:       placeholderTableCatalog,
		MissingInEn: missingInEn,
		MissingInZh: missingInZh,
	}
}

func TestNewAuditReportCountsIssues(t *testing.T) {
	cases := []struct {
		name                  string
		catalog               KeyAlignment
		errorMessages         KeyAlignment
		placeholders          []PlaceholderIssue
		wantKeyIssues         int
		wantPlaceholderIssues int
		wantOK                bool
	}{
		{
			name:   "all clean reports OK",
			wantOK: true,
		},
		{
			name:          "one catalog missing direction counts one key issue",
			catalog:       KeyAlignment{MissingInEn: []string{"a"}},
			wantKeyIssues: 1,
		},
		{
			name:          "catalog both directions count two key issues",
			catalog:       KeyAlignment{MissingInEn: []string{"a"}, MissingInZh: []string{"b", "c"}},
			wantKeyIssues: 3,
		},
		{
			name:          "error message misalignment counts toward key issues",
			errorMessages: KeyAlignment{MissingInZh: []string{"ecode"}},
			wantKeyIssues: 1,
		},
		{
			name:                  "one-direction placeholder mismatch counts one issue",
			placeholders:          []PlaceholderIssue{placeholderIssue(nil, []string{"%s"})},
			wantPlaceholderIssues: 1,
		},
		{
			name:                  "two-direction placeholder mismatch counts two issues",
			placeholders:          []PlaceholderIssue{placeholderIssue([]string{"{{name}}"}, []string{"%d"})},
			wantPlaceholderIssues: 2,
		},
		{
			name:                  "combined issues from every source",
			catalog:               KeyAlignment{MissingInEn: []string{"a"}},
			errorMessages:         KeyAlignment{MissingInZh: []string{"ecode"}},
			placeholders:          []PlaceholderIssue{placeholderIssue(nil, []string{"%s"})},
			wantKeyIssues:         2,
			wantPlaceholderIssues: 1,
		},
		{
			name:         "placeholder issue with no directions contributes nothing",
			placeholders: []PlaceholderIssue{placeholderIssue(nil, nil)},
			wantOK:       true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := newAuditReport(tc.catalog, tc.errorMessages, tc.placeholders)
			want := AuditReport{
				KeyIssues:         tc.wantKeyIssues,
				PlaceholderIssues: tc.wantPlaceholderIssues,
				OK:                tc.wantOK,
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("newAuditReport = %#v, want %#v", got, want)
			}
		})
	}
}

func TestFullAuditMatchesRealSubAuditsAndIsPure(t *testing.T) {
	ResetMissing()

	// 真实文案表上的聚合结果必须与逐项审计的口径一致。
	want := newAuditReport(
		AuditCatalogKeyAlignment(),
		AuditErrorMessagesKeyAlignment(),
		AuditPlaceholders(),
	)
	got := FullAudit()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FullAudit = %#v, want %#v", got, want)
	}

	// 真实文案表当前是干净的；若将来出现真实问题，上面的逐项对比仍成立。
	if got.OK != (got.KeyIssues == 0 && got.PlaceholderIssues == 0) {
		t.Errorf("OK inconsistent with counts: %#v", got)
	}

	// 聚合审计必须纯：不向 Missing 记录任何内容。
	if missing := Missing(); len(missing) != 0 {
		t.Errorf("FullAudit recorded missing keys: %#v", missing)
	}

	// 重复调用必须确定。
	if again := FullAudit(); !reflect.DeepEqual(got, again) {
		t.Errorf("non-deterministic result: %#v then %#v", got, again)
	}
}

func TestAuditReportSummary(t *testing.T) {
	cases := []struct {
		name   string
		report AuditReport
		want   string
	}{
		{
			name:   "OK uses the fixed literal",
			report: AuditReport{OK: true},
			want:   auditSummaryOK,
		},
		{
			name:   "OK literal ignores stale counts",
			report: AuditReport{KeyIssues: 5, PlaceholderIssues: 7, OK: true},
			want:   auditSummaryOK,
		},
		{
			name:   "key issues only",
			report: AuditReport{KeyIssues: 2},
			want:   "i18n 审计未通过：键问题 2 项，占位符问题 0 项",
		},
		{
			name:   "placeholder issues only",
			report: AuditReport{PlaceholderIssues: 3},
			want:   "i18n 审计未通过：键问题 0 项，占位符问题 3 项",
		},
		{
			name:   "both issue kinds",
			report: AuditReport{KeyIssues: 1, PlaceholderIssues: 4},
			want:   "i18n 审计未通过：键问题 1 项，占位符问题 4 项",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.report.Summary(); got != tc.want {
				t.Errorf("Summary() = %q, want %q", got, tc.want)
			}
			// 同一份报告重复调用必须确定。
			if again := tc.report.Summary(); again != tc.want {
				t.Errorf("non-deterministic summary: %q then %q", tc.want, again)
			}
		})
	}
}

// containsHan 报告 s 是否含至少一个汉字，用来验证摘要是中文。
func containsHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func TestAuditReportSummaryIsChinese(t *testing.T) {
	cases := []struct {
		name   string
		report AuditReport
	}{
		{"ok summary", AuditReport{OK: true}},
		{"failed summary", AuditReport{KeyIssues: 1, PlaceholderIssues: 1}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			summary := tc.report.Summary()
			if !containsHan(summary) {
				t.Errorf("summary has no Chinese characters: %q", summary)
			}
		})
	}
}
