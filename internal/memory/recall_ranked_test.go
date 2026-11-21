package memory

import (
	"reflect"
	"testing"
)

// rankedEntry 是表驱动测试中的一条待保留记忆。
type rankedEntry struct {
	text string
	tags []string
}

// memoryTexts 提取内存文本序列，便于表驱动断言顺序。
func memoryTexts(ms []Memory) []string {
	out := make([]string, len(ms))
	for i := range ms {
		out[i] = ms[i].Text
	}
	return out
}

// newRankedBank 用给定条目构建 bank，返回保留顺序对应的 Memory。
func newRankedBank(t *testing.T, entries []rankedEntry) (*Bank, []Memory) {
	t.Helper()
	bank := NewBank(t.TempDir())
	memories := make([]Memory, 0, len(entries))
	for _, e := range entries {
		id, err := bank.Retain(e.text, e.tags, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range bank.All() {
			if m.ID == id {
				memories = append(memories, m)
				break
			}
		}
	}
	return bank, memories
}

// TestRecallRankedOrdering 表驱动验证 RecallRanked 的排序：相关性高者靠前，
// 平局时新近插入者靠前，标签通过加分参与排序。
func TestRecallRankedOrdering(t *testing.T) {
	tests := []struct {
		name    string
		entries []rankedEntry
		query   string
		tags    []string
		limit   int
		want    []string
	}{
		{
			name: "relevance descending",
			entries: []rankedEntry{
				{"alpha beta gamma", nil},
				{"alpha alpha alpha", nil},
				{"beta gamma", nil},
			},
			query: "alpha",
			want:  []string{"alpha alpha alpha", "alpha beta gamma"},
		},
		{
			name: "empty query returns newest first",
			entries: []rankedEntry{
				{"first", nil},
				{"second", nil},
				{"third", nil},
			},
			want: []string{"third", "second", "first"},
		},
		{
			name: "tag bonus ranks tagged memory ahead",
			entries: []rankedEntry{
				{"alpha beta", []string{"home"}},
				{"alpha", []string{"work"}},
			},
			query: "alpha",
			tags:  []string{"work"},
			want:  []string{"alpha", "alpha beta"},
		},
		{
			name: "tag bonus without query keeps only tagged newest first",
			entries: []rankedEntry{
				{"work one", []string{"work"}},
				{"home note", []string{"home"}},
				{"work two", []string{"WORK"}},
			},
			tags: []string{"work"},
			want: []string{"work two", "work one"},
		},
		{
			name:    "no match returns empty slice",
			entries: []rankedEntry{{"alpha beta", nil}},
			query:   "zzz",
			want:    []string{},
		},
		{
			name:    "empty bank returns nil",
			entries: nil,
			want:    nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bank, _ := newRankedBank(t, tc.entries)
			got := memoryTexts(bank.RecallRanked(tc.query, tc.tags, tc.limit))
			if len(got) != len(tc.want) {
				t.Fatalf("RecallRanked = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("RecallRanked = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestRecallRankedMatchesRecall 验证 RecallRanked 与 Recall 的关系：
// 相同参数下返回的内存逐字节一致（含顺序），包括 CJK 与 limit 场景。
func TestRecallRankedMatchesRecall(t *testing.T) {
	tests := []struct {
		name    string
		entries []rankedEntry
		query   string
		tags    []string
		limit   int
	}{
		{
			name:    "empty bank",
			entries: nil,
			query:   "alpha",
		},
		{
			name: "relevance",
			entries: []rankedEntry{
				{"alpha beta gamma", nil},
				{"alpha alpha alpha", nil},
				{"beta gamma", nil},
			},
			query: "alpha",
		},
		{
			name: "newest first",
			entries: []rankedEntry{
				{"first", nil},
				{"second", nil},
				{"third", nil},
			},
			limit: 10,
		},
		{
			name: "tag bonus does not refilter",
			entries: []rankedEntry{
				{"alpha beta", []string{"home"}},
				{"alpha alpha", []string{"work"}},
				{"gamma", []string{"work"}},
			},
			query: "alpha",
			tags:  []string{"work"},
		},
		{
			name: "tag only",
			entries: []rankedEntry{
				{"work one", []string{"work"}},
				{"home note", []string{"home"}},
				{"shared", []string{"work", "home"}},
			},
			tags: []string{"work"},
		},
		{
			name: "CJK query and tags",
			entries: []rankedEntry{
				{"我喜欢Go。Go很快。", []string{"语言"}},
				{"Python也不错。", []string{"语言"}},
				{"Go 与 Python 都好", []string{"编程"}},
			},
			query: "Go",
			tags:  []string{"语言"},
		},
		{
			name: "CJK empty query tag filter",
			entries: []rankedEntry{
				{"项目笔记", []string{"项目"}},
				{"日常记录", []string{"日常"}},
			},
			tags: []string{"项目"},
		},
		{
			name: "limit one",
			entries: []rankedEntry{
				{"alpha one", nil},
				{"alpha two", nil},
				{"alpha three", nil},
			},
			query: "alpha",
			limit: 1,
		},
		{
			name: "limit zero uses default",
			entries: []rankedEntry{
				{"alpha one", nil},
				{"alpha two", nil},
			},
			query: "alpha",
			limit: 0,
		},
		{
			name: "negative limit uses default",
			entries: []rankedEntry{
				{"alpha one", nil},
				{"alpha two", nil},
			},
			query: "alpha",
			limit: -5,
		},
		{
			name: "invalidated excluded",
			entries: []rankedEntry{
				{"alpha kept", nil},
				{"alpha gone", nil},
			},
			query: "alpha",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bank, mems := newRankedBank(t, tc.entries)
			if tc.name == "invalidated excluded" && len(mems) == 2 {
				if err := bank.Invalidate(mems[1].ID, "test"); err != nil {
					t.Fatal(err)
				}
			}
			got := bank.RecallRanked(tc.query, tc.tags, tc.limit)
			want := bank.Recall(tc.query, tc.tags, tc.limit)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("RecallRanked = %#v, Recall = %#v", got, want)
			}
		})
	}
}

// TestRecallRankedTagIsNotRefiltered 守护"不二次过滤"契约：非空 query 下，
// 仅靠查询命中（不带目标标签）的内存必须保留；若把标签传给 RankRecall 的
// TagFilter，这类内存会被错误剔除。
func TestRecallRankedTagIsNotRefiltered(t *testing.T) {
	bank := NewBank(t.TempDir())
	for _, item := range []struct {
		text string
		tags []string
	}{
		{"alpha beta", []string{"home"}},
		{"alpha alpha", []string{"work"}},
		{"gamma delta", nil},
	} {
		if _, err := bank.Retain(item.text, item.tags, ""); err != nil {
			t.Fatal(err)
		}
	}

	got := memoryTexts(bank.RecallRanked("alpha", []string{"work"}, 10))
	want := []string{"alpha alpha", "alpha beta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RecallRanked = %v, want %v (no second tag filter)", got, want)
	}
	// 与 Recall 逐字节一致，证明没有行为漂移。
	if ranked, original := bank.RecallRanked("alpha", []string{"work"}, 10), bank.Recall("alpha", []string{"work"}, 10); !reflect.DeepEqual(ranked, original) {
		t.Fatalf("RecallRanked = %#v, Recall = %#v", ranked, original)
	}
}

// TestRecallRankedCJK 表驱动验证 CJK 文本与标签下的排序正确性。
func TestRecallRankedCJK(t *testing.T) {
	tests := []struct {
		name    string
		entries []rankedEntry
		query   string
		tags    []string
		want    []string
	}{
		{
			name: "CJK query per-rune overlap",
			entries: []rankedEntry{
				{"我喜欢Go。Go很快。", nil},
				{"Python也不错。", nil},
			},
			query: "喜欢",
			want:  []string{"我喜欢Go。Go很快。"},
		},
		{
			name: "CJK tag filter case and newest first",
			entries: []rankedEntry{
				{"项目笔记", []string{"项目"}},
				{"日常记录", []string{"日常"}},
				{"项目复盘", []string{"项目"}},
			},
			tags: []string{"项目"},
			want: []string{"项目复盘", "项目笔记"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bank := NewBank(t.TempDir())
			for _, e := range tc.entries {
				if _, err := bank.Retain(e.text, e.tags, ""); err != nil {
					t.Fatal(err)
				}
			}
			got := memoryTexts(bank.RecallRanked(tc.query, tc.tags, 10))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("RecallRanked = %v, want %v", got, tc.want)
			}
			if !reflect.DeepEqual(bank.RecallRanked(tc.query, tc.tags, 10), bank.Recall(tc.query, tc.tags, 10)) {
				t.Fatal("RecallRanked diverged from Recall on CJK data")
			}
		})
	}
}

// TestRecallRankedLimit 表驱动验证 limit 截断：正数取前 N 条，
// 非正数回退到 defaultRecallLimit。
func TestRecallRankedLimit(t *testing.T) {
	bank := NewBank(t.TempDir())
	for i := 0; i < 12; i++ {
		if _, err := bank.Retain("alpha item", nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name  string
		limit int
		want  int
	}{
		{"limit one", 1, 1},
		{"limit five", 5, 5},
		{"limit above cap", 100, 12},
		{"limit zero", 0, defaultRecallLimit},
		{"limit negative", -3, defaultRecallLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := bank.RecallRanked("alpha", nil, tc.limit)
			if len(got) != tc.want {
				t.Fatalf("RecallRanked limit %d = %d hits, want %d", tc.limit, len(got), tc.want)
			}
			if !reflect.DeepEqual(got, bank.Recall("alpha", nil, tc.limit)) {
				t.Fatalf("RecallRanked diverged from Recall at limit %d", tc.limit)
			}
		})
	}
}
