package config

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestBuildIntegrityReportCounts 覆盖各类统计：nil/空、全致命、全警告、混合。
// 这里只关心计数，Entries 的内容不影响结果。
func TestBuildIntegrityReportCounts(t *testing.T) {
	tests := []struct {
		name         string
		entries      []MissingEntry
		wantTotal    int
		wantFatal    int
		wantWarnings int
	}{
		{
			name:    "nil entries",
			entries: nil,
		},
		{
			name:    "empty entries",
			entries: []MissingEntry{},
		},
		{
			name: "all fatal",
			entries: []MissingEntry{
				{Entry: "$.model", Fatal: true},
				{Entry: "$.baseURL", Fatal: true},
				{Entry: "$.cwd", Fatal: true},
			},
			wantTotal: 3, wantFatal: 3,
		},
		{
			name: "all warnings",
			entries: []MissingEntry{
				{Entry: "$.provider", Fatal: false},
				{Entry: "$.apiKey", Fatal: false},
			},
			wantTotal: 2, wantWarnings: 2,
		},
		{
			name: "mixed fatal and warnings",
			entries: []MissingEntry{
				{Entry: "$.model", Fatal: true},
				{Entry: "$.provider", Fatal: false},
				{Entry: "$.cwd", Fatal: true},
				{Entry: "$.apiKey", Fatal: false},
			},
			wantTotal: 4, wantFatal: 2, wantWarnings: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildIntegrityReport(tc.entries)
			if got.Total != tc.wantTotal {
				t.Errorf("Total = %d, want %d", got.Total, tc.wantTotal)
			}
			if got.Fatal != tc.wantFatal {
				t.Errorf("Fatal = %d, want %d", got.Fatal, tc.wantFatal)
			}
			if got.Warnings != tc.wantWarnings {
				t.Errorf("Warnings = %d, want %d", got.Warnings, tc.wantWarnings)
			}
			// 恒等式：总数必须等于致命项与警告项之和。
			if got.Total != got.Fatal+got.Warnings {
				t.Errorf("invariant broken: Total=%d, Fatal+Warnings=%d", got.Total, got.Fatal+got.Warnings)
			}
		})
	}
}

// TestBuildIntegrityReportSorts 覆盖排序契约：按 Entry 字典序，且 Entry 相等时
// 保留原始相对顺序（稳定排序）。
func TestBuildIntegrityReportSorts(t *testing.T) {
	tests := []struct {
		name    string
		entries []MissingEntry
		want    []MissingEntry
	}{
		{
			name: "sorted by Entry regardless of input order",
			entries: []MissingEntry{
				{Entry: "$.storageDir", Reason: "storage", Fatal: true},
				{Entry: "$.model", Reason: "model", Fatal: true},
				{Entry: "$.baseURL", Reason: "baseURL", Fatal: true},
			},
			want: []MissingEntry{
				{Entry: "$.baseURL", Reason: "baseURL", Fatal: true},
				{Entry: "$.model", Reason: "model", Fatal: true},
				{Entry: "$.storageDir", Reason: "storage", Fatal: true},
			},
		},
		{
			name: "stable order for equal Entry",
			entries: []MissingEntry{
				{Entry: "$.same", Reason: "first", Fatal: true},
				{Entry: "$.aaa", Reason: "zzz", Fatal: false},
				{Entry: "$.same", Reason: "second", Fatal: false},
			},
			want: []MissingEntry{
				{Entry: "$.aaa", Reason: "zzz", Fatal: false},
				{Entry: "$.same", Reason: "first", Fatal: true},
				{Entry: "$.same", Reason: "second", Fatal: false},
			},
		},
		{
			name:    "nil stays empty",
			entries: nil,
			want:    nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildIntegrityReport(tc.entries).Entries
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Entries = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestBuildIntegrityReportDoesNotMutateInput 验证"副本独立"：构建过程既不改变
// 入参顺序，返回的 Entries 与入参也不共享底层数组，双向修改互不影响。
func TestBuildIntegrityReportDoesNotMutateInput(t *testing.T) {
	in := []MissingEntry{
		{Entry: "$.b", Reason: "bee", Fatal: true},
		{Entry: "$.a", Reason: "ay", Fatal: false},
	}
	before := append([]MissingEntry(nil), in...)

	r := BuildIntegrityReport(in)

	// 构建本身不得改动入参顺序或内容。
	if !reflect.DeepEqual(in, before) {
		t.Fatalf("BuildIntegrityReport mutated input: got %+v, want %+v", in, before)
	}
	// 报告中的顺序应已排序：a 在前，b 在后。
	if r.Entries[0].Entry != "$.a" || r.Entries[1].Entry != "$.b" {
		t.Fatalf("Entries not sorted: %+v", r.Entries)
	}

	// 改入参不得影响报告。
	in[0].Reason = "MUTATED"
	if got := r.Entries[1].Reason; got != "bee" {
		t.Errorf("mutating input changed report: got %q, want %q", got, "bee")
	}

	// 改报告不得影响入参。
	r.Entries[0].Reason = "REPORT-MUTATED"
	if got := in[1].Reason; got != "ay" {
		t.Errorf("mutating report changed input: got %q, want %q", got, "ay")
	}
}

// TestIntegrityReportJSON 覆盖 JSON 的字节契约：字段固定、空切片归一化为 []、
// 稳定排序生效以及 CJK/HTML 字符不转义。期望字符串逐字节比对，天然保证无尾随换行。
func TestIntegrityReportJSON(t *testing.T) {
	tests := []struct {
		name   string
		report IntegrityReport
		want   string
	}{
		{
			name:   "zero value report",
			report: IntegrityReport{},
			want:   `{"total":0,"fatal":0,"warnings":0,"entries":[]}`,
		},
		{
			name:   "nil entries normalized to empty array",
			report: IntegrityReport{Total: 2, Fatal: 1, Warnings: 1},
			want:   `{"total":2,"fatal":1,"warnings":1,"entries":[]}`,
		},
		{
			name: "single fatal entry",
			report: BuildIntegrityReport([]MissingEntry{
				{Entry: "$.model", Reason: "model is required", Fatal: true},
			}),
			want: `{"total":1,"fatal":1,"warnings":0,"entries":[{"entry":"$.model","reason":"model is required","fatal":true}]}`,
		},
		{
			name: "mixed entries serialized in sorted order",
			report: BuildIntegrityReport([]MissingEntry{
				{Entry: "$.storageDir", Reason: "storage", Fatal: true},
				{Entry: "$.model", Reason: "model", Fatal: true},
				{Entry: "$.providers.openai.apiKey", Reason: "cred", Fatal: false},
			}),
			want: `{"total":3,"fatal":2,"warnings":1,"entries":[{"entry":"$.model","reason":"model","fatal":true},{"entry":"$.providers.openai.apiKey","reason":"cred","fatal":false},{"entry":"$.storageDir","reason":"storage","fatal":true}]}`,
		},
		{
			name: "equal Entry keeps original relative order",
			report: BuildIntegrityReport([]MissingEntry{
				{Entry: "$.same", Reason: "first", Fatal: true},
				{Entry: "$.aaa", Reason: "zzz", Fatal: false},
				{Entry: "$.same", Reason: "second", Fatal: false},
			}),
			want: `{"total":3,"fatal":1,"warnings":2,"entries":[{"entry":"$.aaa","reason":"zzz","fatal":false},{"entry":"$.same","reason":"first","fatal":true},{"entry":"$.same","reason":"second","fatal":false}]}`,
		},
		{
			name: "CJK and HTML characters are not escaped",
			report: BuildIntegrityReport([]MissingEntry{
				{Entry: "$.providers.中文.apiKey", Reason: "缺少凭据 <a> & b", Fatal: false},
			}),
			want: `{"total":1,"fatal":0,"warnings":1,"entries":[{"entry":"$.providers.中文.apiKey","reason":"缺少凭据 <a> & b","fatal":false}]}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.report.JSON()
			if string(got) != tc.want {
				t.Errorf("JSON() = %s, want %s", got, tc.want)
			}
			if bytes.HasSuffix(got, []byte("\n")) {
				t.Errorf("JSON() has trailing newline: %q", got)
			}
			if !json.Valid(got) {
				t.Errorf("JSON() produced invalid JSON: %q", got)
			}
			if strings.Contains(string(got), `\u`) {
				t.Errorf("JSON() contains \\u escape: %q", got)
			}
		})
	}
}

// TestIntegrityReportJSONStability 验证从不同初始顺序的相同集合构造出的报告
// 逐字节一致，且对同一报告重复调用 JSON() 结果不变，并显式确认无尾随换行。
func TestIntegrityReportJSONStability(t *testing.T) {
	entriesA := []MissingEntry{
		{Entry: "$.storageDir", Reason: "storage", Fatal: true},
		{Entry: "$.model", Reason: "model", Fatal: true},
		{Entry: "$.providers.openai.apiKey", Reason: "cred", Fatal: false},
	}
	entriesB := []MissingEntry{
		{Entry: "$.model", Reason: "model", Fatal: true},
		{Entry: "$.providers.openai.apiKey", Reason: "cred", Fatal: false},
		{Entry: "$.storageDir", Reason: "storage", Fatal: true},
	}

	a := BuildIntegrityReport(entriesA).JSON()
	b := BuildIntegrityReport(entriesB).JSON()
	if !bytes.Equal(a, b) {
		t.Fatalf("JSON depends on input order:\nA = %s\nB = %s", a, b)
	}
	if again := BuildIntegrityReport(entriesA).JSON(); !bytes.Equal(a, again) {
		t.Fatalf("repeated build produced different bytes:\nfirst  = %s\nsecond = %s", a, again)
	}
	// 同一报告值重复调用也必须稳定。
	report := BuildIntegrityReport(entriesA)
	if again := report.JSON(); !bytes.Equal(a, again) {
		t.Fatalf("repeated JSON() produced different bytes:\nfirst  = %s\nsecond = %s", a, again)
	}
	if bytes.HasSuffix(a, []byte("\n")) {
		t.Errorf("JSON() has trailing newline: %q", a)
	}
}
