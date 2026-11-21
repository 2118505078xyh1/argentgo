package i18n

import (
	"reflect"
	"testing"
	"unicode"
)

// diffDetail 构造一条差异明细，减少表驱动用例的样板代码。
func diffDetail(table, key, issue string) AuditDetail {
	return AuditDetail{Table: table, Key: key, Issue: issue}
}

// diffDetailsAreSorted 报告明细是否按 (Table, Key, Issue) 字典序非降排列。
func diffDetailsAreSorted(details []AuditDetail) bool {
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

func TestAuditDiff(t *testing.T) {
	a := diffDetail("catalog", "a", "i1")
	b := diffDetail("catalog", "b", "i2")
	c := diffDetail("catalog", "c", "i3")

	cases := []struct {
		name        string
		before      []AuditDetail
		after       []AuditDetail
		wantAdded   []AuditDetail
		wantRemoved []AuditDetail
	}{
		{
			name: "nil inputs yield no diffs",
		},
		{
			name:   "empty slices yield no diffs",
			before: []AuditDetail{},
			after:  []AuditDetail{},
		},
		{
			name:   "identical batches yield no diffs",
			before: []AuditDetail{a, b},
			after:  []AuditDetail{a, b},
		},
		{
			name:   "order-only difference yields no diffs",
			before: []AuditDetail{a, b},
			after:  []AuditDetail{b, a},
		},
		{
			name:      "added only",
			before:    []AuditDetail{a},
			after:     []AuditDetail{a, b},
			wantAdded: []AuditDetail{b},
		},
		{
			name:        "removed only",
			before:      []AuditDetail{a, b},
			after:       []AuditDetail{a},
			wantRemoved: []AuditDetail{b},
		},
		{
			name:        "both added and removed",
			before:      []AuditDetail{a, b},
			after:       []AuditDetail{a, c},
			wantAdded:   []AuditDetail{c},
			wantRemoved: []AuditDetail{b},
		},
		{
			name:        "issue change counts as one added and one removed",
			before:      []AuditDetail{diffDetail("catalog", "k", "i1")},
			after:       []AuditDetail{diffDetail("catalog", "k", "i2")},
			wantAdded:   []AuditDetail{diffDetail("catalog", "k", "i2")},
			wantRemoved: []AuditDetail{diffDetail("catalog", "k", "i1")},
		},
		{
			name:      "duplicates collapse via set semantics",
			before:    []AuditDetail{a, a},
			after:     []AuditDetail{a, b, b},
			wantAdded: []AuditDetail{b},
		},
		{
			name:   "stable sorting by table then key then issue",
			before: nil,
			after: []AuditDetail{
				diffDetail("error messages", "b", "i2"),
				diffDetail("catalog", "z", "i3"),
				diffDetail("catalog", "a", "i5"),
				diffDetail("catalog", "a", "i4"),
			},
			wantAdded: []AuditDetail{
				diffDetail("catalog", "a", "i4"),
				diffDetail("catalog", "a", "i5"),
				diffDetail("catalog", "z", "i3"),
				diffDetail("error messages", "b", "i2"),
			},
		},
		{
			name:   "CJK keys and tables sort by UTF-8 byte order",
			before: []AuditDetail{diffDetail("中文表", "键一", "问题一")},
			after: []AuditDetail{
				diffDetail("中文表", "键二", "问题二"),
				diffDetail("catalog", "中文键", "提示"),
			},
			wantAdded: []AuditDetail{
				diffDetail("catalog", "中文键", "提示"),
				diffDetail("中文表", "键二", "问题二"),
			},
			wantRemoved: []AuditDetail{diffDetail("中文表", "键一", "问题一")},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			added, removed := AuditDiff(tc.before, tc.after)
			if !reflect.DeepEqual(added, tc.wantAdded) {
				t.Errorf("AuditDiff added = %#v, want %#v", added, tc.wantAdded)
			}
			if !reflect.DeepEqual(removed, tc.wantRemoved) {
				t.Errorf("AuditDiff removed = %#v, want %#v", removed, tc.wantRemoved)
			}
			if !diffDetailsAreSorted(added) {
				t.Errorf("added not sorted by (Table, Key, Issue): %#v", added)
			}
			if !diffDetailsAreSorted(removed) {
				t.Errorf("removed not sorted by (Table, Key, Issue): %#v", removed)
			}

			// 重复调用必须确定。
			addedAgain, removedAgain := AuditDiff(tc.before, tc.after)
			if !reflect.DeepEqual(added, addedAgain) || !reflect.DeepEqual(removed, removedAgain) {
				t.Errorf("non-deterministic result: (%#v, %#v) then (%#v, %#v)",
					added, removed, addedAgain, removedAgain)
			}
		})
	}
}

func TestAuditDiffDoesNotMutateInputs(t *testing.T) {
	before := []AuditDetail{
		diffDetail("catalog", "a", "i1"),
		diffDetail("catalog", "b", "i2"),
	}
	after := []AuditDetail{
		diffDetail("catalog", "a", "i1"),
		diffDetail("catalog", "c", "i3"),
	}
	beforeCopy := append([]AuditDetail(nil), before...)
	afterCopy := append([]AuditDetail(nil), after...)

	AuditDiff(before, after)

	if !reflect.DeepEqual(before, beforeCopy) {
		t.Errorf("AuditDiff mutated before: %#v, want %#v", before, beforeCopy)
	}
	if !reflect.DeepEqual(after, afterCopy) {
		t.Errorf("AuditDiff mutated after: %#v, want %#v", after, afterCopy)
	}
}

func TestAuditDiffReturnsIndependentCopies(t *testing.T) {
	before := []AuditDetail{diffDetail("catalog", "a", "i1"), diffDetail("catalog", "b", "i2")}
	after := []AuditDetail{diffDetail("catalog", "a", "i1"), diffDetail("catalog", "c", "i3")}
	added, removed := AuditDiff(before, after)

	// 修改结果不影响入参。
	added[0].Issue = "changed-added"
	removed[0].Issue = "changed-removed"
	if after[1].Issue != "i3" {
		t.Errorf("mutating added changed input after: %#v", after)
	}
	if before[1].Issue != "i2" {
		t.Errorf("mutating removed changed input before: %#v", before)
	}

	// 修改入参也不影响已返回的结果（值副本）。
	after[1].Issue = "input-changed"
	before[1].Issue = "input-changed"
	if added[0].Issue != "changed-added" || removed[0].Issue != "changed-removed" {
		t.Errorf("mutating inputs changed results: added=%#v removed=%#v", added, removed)
	}
}

func TestAuditDiffSummary(t *testing.T) {
	one := []AuditDetail{diffDetail("catalog", "a", "i1")}
	two := []AuditDetail{diffDetail("catalog", "a", "i1"), diffDetail("catalog", "b", "i2")}
	three := []AuditDetail{
		diffDetail("catalog", "a", "i1"),
		diffDetail("catalog", "b", "i2"),
		diffDetail("catalog", "c", "i3"),
	}

	cases := []struct {
		name    string
		added   []AuditDetail
		removed []AuditDetail
		want    string
	}{
		{
			name: "no change with nil inputs",
			want: auditDiffSummaryNoChange,
		},
		{
			name:    "no change with empty slices",
			added:   []AuditDetail{},
			removed: []AuditDetail{},
			want:    auditDiffSummaryNoChange,
		},
		{
			name:  "added only",
			added: two,
			want:  "i18n 文案审计差异：新增 2 项，移除 0 项",
		},
		{
			name:    "removed only",
			removed: three,
			want:    "i18n 文案审计差异：新增 0 项，移除 3 项",
		},
		{
			name:    "both added and removed",
			added:   two,
			removed: three,
			want:    "i18n 文案审计差异：新增 2 项，移除 3 项",
		},
		{
			name:    "single added and removed",
			added:   one,
			removed: one,
			want:    "i18n 文案审计差异：新增 1 项，移除 1 项",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addedBefore := cloneAuditDetails(tc.added)
			removedBefore := cloneAuditDetails(tc.removed)

			got := AuditDiffSummary(tc.added, tc.removed)
			if got != tc.want {
				t.Errorf("AuditDiffSummary = %q, want %q", got, tc.want)
			}
			// 同一批差异重复调用必须确定。
			if again := AuditDiffSummary(tc.added, tc.removed); again != tc.want {
				t.Errorf("non-deterministic summary: %q then %q", tc.want, again)
			}
			// 摘要不得改动入参。
			if !reflect.DeepEqual(tc.added, addedBefore) ||
				!reflect.DeepEqual(tc.removed, removedBefore) {
				t.Errorf("AuditDiffSummary mutated inputs")
			}
		})
	}
}

// cloneAuditDetails 返回明细切片的值副本（保留 nil 与非 nil 空切片的区别），
// 用作调用前快照。
func cloneAuditDetails(in []AuditDetail) []AuditDetail {
	if in == nil {
		return nil
	}
	out := make([]AuditDetail, len(in))
	copy(out, in)
	return out
}

// diffHasHan 报告 s 是否含至少一个汉字，用来验证摘要是中文。
func diffHasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func TestAuditDiffSummaryIsChinese(t *testing.T) {
	cases := []struct {
		name    string
		added   []AuditDetail
		removed []AuditDetail
	}{
		{"no change summary", nil, nil},
		{"diff summary", []AuditDetail{diffDetail("catalog", "a", "i1")}, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			summary := AuditDiffSummary(tc.added, tc.removed)
			if !diffHasHan(summary) {
				t.Errorf("summary has no Chinese characters: %q", summary)
			}
		})
	}
}
