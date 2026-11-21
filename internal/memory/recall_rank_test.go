package memory

import (
	"math"
	"reflect"
	"testing"
)

func TestRankRecall(t *testing.T) {
	idsOf := func(cs []RecallCandidate) []string {
		out := make([]string, len(cs))
		for i := range cs {
			out[i] = cs[i].ID
		}
		return out
	}

	tests := []struct {
		name    string
		in      []RecallCandidate
		opts    RankOptions
		wantIDs []string
	}{
		{
			name:    "score descending",
			in:      []RecallCandidate{{ID: "A", Score: 1}, {ID: "B", Score: 3}, {ID: "C", Score: 2}},
			wantIDs: []string{"B", "C", "A"},
		},
		{
			name:    "recency descending breaks score tie",
			in:      []RecallCandidate{{ID: "A", Score: 5, Recency: 10}, {ID: "B", Score: 5, Recency: 30}, {ID: "C", Score: 5, Recency: 20}},
			wantIDs: []string{"B", "C", "A"},
		},
		{
			name:    "id ascending breaks full tie",
			in:      []RecallCandidate{{ID: "b", Score: 5, Recency: 10}, {ID: "a", Score: 5, Recency: 10}, {ID: "c", Score: 5, Recency: 10}},
			wantIDs: []string{"a", "b", "c"},
		},
		{
			name: "all keys combined",
			in: []RecallCandidate{
				{ID: "mid", Score: 2, Recency: 100},
				{ID: "z", Score: 2, Recency: 200},
				{ID: "a", Score: 3, Recency: 1},
				{ID: "low", Score: 1, Recency: 999},
			},
			wantIDs: []string{"a", "z", "mid", "low"},
		},
		{
			name: "tag filter keeps any matching tag",
			in: []RecallCandidate{
				{ID: "A", Tags: []string{"go", "api"}},
				{ID: "B", Tags: []string{"rust"}},
				{ID: "C", Tags: []string{"db", "go"}},
				{ID: "D"},
			},
			opts:    RankOptions{TagFilter: []string{"go"}},
			wantIDs: []string{"A", "C"},
		},
		{
			name:    "tag filter is case insensitive",
			in:      []RecallCandidate{{ID: "A", Tags: []string{"go"}}, {ID: "B", Tags: []string{"Go"}}, {ID: "C", Tags: []string{"rust"}}},
			opts:    RankOptions{TagFilter: []string{"GO"}},
			wantIDs: []string{"A", "B"},
		},
		{
			name:    "empty tag filter keeps all",
			in:      []RecallCandidate{{ID: "A"}, {ID: "B"}},
			wantIDs: []string{"A", "B"},
		},
		{
			name:    "tag filter with no match returns nil",
			in:      []RecallCandidate{{ID: "A", Tags: []string{"go"}}},
			opts:    RankOptions{TagFilter: []string{"zzz"}},
			wantIDs: nil,
		},
		{
			name:    "NaN score is lowest",
			in:      []RecallCandidate{{ID: "A", Score: 1}, {ID: "B", Score: math.NaN()}, {ID: "C", Score: 0}},
			wantIDs: []string{"A", "C", "B"},
		},
		{
			name: "NaN scores still ordered by recency",
			in: []RecallCandidate{
				{ID: "A", Score: math.NaN(), Recency: 100},
				{ID: "B", Score: math.NaN(), Recency: 300},
				{ID: "C", Score: math.NaN(), Recency: 200},
			},
			wantIDs: []string{"B", "C", "A"},
		},
		{
			name: "filter then rank",
			in: []RecallCandidate{
				{ID: "A", Score: 1, Tags: []string{"go"}},
				{ID: "B", Score: 9, Tags: []string{"rust"}},
				{ID: "C", Score: 5, Tags: []string{"go"}},
			},
			opts:    RankOptions{TagFilter: []string{"go"}},
			wantIDs: []string{"C", "A"},
		},
		{
			name:    "nil input",
			in:      nil,
			wantIDs: nil,
		},
		{
			name:    "empty input",
			in:      []RecallCandidate{},
			wantIDs: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := idsOf(RankRecall(tc.in, tc.opts))
			if len(got) != len(tc.wantIDs) {
				t.Fatalf("RankRecall = %v, want %v", got, tc.wantIDs)
			}
			for i := range tc.wantIDs {
				if got[i] != tc.wantIDs[i] {
					t.Fatalf("RankRecall = %v, want %v", got, tc.wantIDs)
				}
			}
		})
	}
}

// TestRankRecallStableTies 守护"完全相同（含两个 NaN）保持输入顺序"的
// 稳定性契约；用 Tags 区分彼此，以便观察相对顺序。
func TestRankRecallStableTies(t *testing.T) {
	in := []RecallCandidate{
		{ID: "X", Score: math.NaN(), Recency: 1, Tags: []string{"first"}},
		{ID: "X", Score: math.NaN(), Recency: 1, Tags: []string{"second"}},
	}
	got := RankRecall(in, RankOptions{})
	if len(got) != 2 || got[0].Tags[0] != "first" || got[1].Tags[0] != "second" {
		t.Fatalf("stable ties = %#v, want first before second", got)
	}
}

func TestRankRecallIsPure(t *testing.T) {
	in := []RecallCandidate{
		{ID: "b", Score: 2, Tags: []string{"x"}},
		{ID: "a", Score: 1, Tags: []string{"y"}},
	}
	snapshot := append([]RecallCandidate(nil), in...)

	out := RankRecall(in, RankOptions{})
	if !reflect.DeepEqual(in, snapshot) {
		t.Fatalf("input mutated: got %#v, want %#v", in, snapshot)
	}
	// 结果必须是新切片（不与入参共享外层底层数组）。
	out[0].ID = "CHANGED"
	if in[0].ID != "b" || in[1].ID != "a" {
		t.Fatalf("output aliases input: %#v", in)
	}
}

func TestDeduplicateRecall(t *testing.T) {
	tests := []struct {
		name string
		in   []RecallCandidate
		want []RecallCandidate
	}{
		{
			name: "dedupe keeps highest score at first-occurrence position",
			in: []RecallCandidate{
				{ID: "A", Score: 1, Tags: []string{"go"}},
				{ID: "B", Score: 3},
				{ID: "A", Score: 2, Tags: []string{"db"}},
			},
			want: []RecallCandidate{
				{ID: "A", Score: 2, Tags: []string{"db"}},
				{ID: "B", Score: 3},
			},
		},
		{
			name: "equal scores keep first occurrence",
			in: []RecallCandidate{
				{ID: "A", Score: 1, Recency: 5, Tags: []string{"first"}},
				{ID: "A", Score: 1, Recency: 99, Tags: []string{"second"}},
			},
			want: []RecallCandidate{
				{ID: "A", Score: 1, Recency: 5, Tags: []string{"first"}},
			},
		},
		{
			name: "lower later score does not replace",
			in: []RecallCandidate{
				{ID: "A", Score: 5},
				{ID: "B", Score: 1},
				{ID: "A", Score: 3},
			},
			want: []RecallCandidate{
				{ID: "A", Score: 5},
				{ID: "B", Score: 1},
			},
		},
		{
			name: "NaN loses to any real score",
			in: []RecallCandidate{
				{ID: "A", Score: math.NaN()},
				{ID: "A", Score: 1},
				{ID: "B", Score: math.NaN()},
				{ID: "B", Score: 2},
			},
			want: []RecallCandidate{
				{ID: "A", Score: 1},
				{ID: "B", Score: 2},
			},
		},
		{
			name: "two NaNs keep first occurrence",
			in: []RecallCandidate{
				{ID: "A", Score: math.NaN(), Tags: []string{"first"}},
				{ID: "A", Score: math.NaN(), Tags: []string{"second"}},
			},
			want: []RecallCandidate{
				{ID: "A", Score: math.NaN(), Tags: []string{"first"}},
			},
		},
		{
			name: "distinct IDs preserved in input order",
			in: []RecallCandidate{
				{ID: "b", Score: 1},
				{ID: "a", Score: 2},
				{ID: "c", Score: 3},
			},
			want: []RecallCandidate{
				{ID: "b", Score: 1},
				{ID: "a", Score: 2},
				{ID: "c", Score: 3},
			},
		},
		{
			name: "nil input",
			in:   nil,
			want: nil,
		},
		{
			name: "empty input",
			in:   []RecallCandidate{},
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DeduplicateRecall(tc.in)
			if !equalCandidateSlices(got, tc.want) {
				t.Fatalf("DeduplicateRecall = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestDeduplicateRecallIsPure(t *testing.T) {
	in := []RecallCandidate{
		{ID: "A", Score: 1},
		{ID: "A", Score: 2},
	}
	snapshot := append([]RecallCandidate(nil), in...)

	out := DeduplicateRecall(in)
	if !reflect.DeepEqual(in, snapshot) {
		t.Fatalf("input mutated: got %#v, want %#v", in, snapshot)
	}
	out[0].ID = "CHANGED"
	if in[0].ID != "A" || in[1].ID != "A" {
		t.Fatalf("output aliases input: %#v", in)
	}
}

// TestRecallCJKID 守护中文 ID 在排序（按 UTF-8 字节升序）与去重中的行为。
func TestRecallCJKID(t *testing.T) {
	t.Run("rank by score then CJK id", func(t *testing.T) {
		in := []RecallCandidate{
			{ID: "项目", Score: 1},
			{ID: "葡萄", Score: 3},
			{ID: "苹果", Score: 2},
		}
		got := RankRecall(in, RankOptions{})
		want := []string{"葡萄", "苹果", "项目"}
		if len(got) != len(want) {
			t.Fatalf("RankRecall = %#v, want %v", got, want)
		}
		for i := range want {
			if got[i].ID != want[i] {
				t.Fatalf("RankRecall = %#v, want %v", got, want)
			}
		}
	})

	t.Run("CJK id ascending tie", func(t *testing.T) {
		in := []RecallCandidate{
			{ID: "项目", Score: 1, Recency: 1},
			{ID: "苹果", Score: 1, Recency: 1},
		}
		got := RankRecall(in, RankOptions{})
		// "苹" (U+82F9) 的 UTF-8 字节小于 "项" (U+9879)。
		if len(got) != 2 || got[0].ID != "苹果" || got[1].ID != "项目" {
			t.Fatalf("RankRecall CJK tie = %#v, want [苹果 项目]", got)
		}
	})

	t.Run("dedupe CJK id keeps highest score", func(t *testing.T) {
		in := []RecallCandidate{
			{ID: "项目", Score: 1},
			{ID: "项目", Score: 5, Tags: []string{"高"}},
		}
		got := DeduplicateRecall(in)
		want := []RecallCandidate{{ID: "项目", Score: 5, Tags: []string{"高"}}}
		if !equalCandidateSlices(got, want) {
			t.Fatalf("DeduplicateRecall = %#v, want %#v", got, want)
		}
	})
}

// equalFloat 把 NaN 视为相等，便于在断言中比较含 NaN 的候选。
func equalFloat(a, b float64) bool {
	return a == b || (math.IsNaN(a) && math.IsNaN(b))
}

func equalCandidates(a, b RecallCandidate) bool {
	return a.ID == b.ID &&
		equalFloat(a.Score, b.Score) &&
		a.Recency == b.Recency &&
		reflect.DeepEqual(a.Tags, b.Tags)
}

func equalCandidateSlices(a, b []RecallCandidate) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !equalCandidates(a[i], b[i]) {
			return false
		}
	}
	return true
}
