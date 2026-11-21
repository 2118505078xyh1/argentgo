package memory

import (
	"math"
	"reflect"
	"testing"
)

func TestExplainScore(t *testing.T) {
	tests := []struct {
		name string
		cand RecallCandidate
		opts RankOptions
		want []ScoreFactor
	}{
		{
			name: "zero tag filter: all score is base",
			cand: RecallCandidate{ID: "a", Score: 4, Recency: 7, Tags: []string{"go"}},
			want: []ScoreFactor{
				{Name: factorBase, Contribution: 4},
				{Name: factorTagMatch, Contribution: 0},
				{Name: factorRecency, Contribution: 7},
			},
		},
		{
			name: "nil tags and no filter",
			cand: RecallCandidate{ID: "b", Score: 2, Recency: 0},
			want: []ScoreFactor{
				{Name: factorBase, Contribution: 2},
				{Name: factorTagMatch, Contribution: 0},
				{Name: factorRecency, Contribution: 0},
			},
		},
		{
			name: "single tag match splits base and tag bonus",
			cand: RecallCandidate{ID: "c", Score: 5, Recency: 1, Tags: []string{"work", "go"}},
			opts: RankOptions{TagFilter: []string{"work"}},
			want: []ScoreFactor{
				{Name: factorBase, Contribution: 2},
				{Name: factorTagMatch, Contribution: 3},
				{Name: factorRecency, Contribution: 1},
			},
		},
		{
			name: "tag mismatch: no tag bonus",
			cand: RecallCandidate{ID: "d", Score: 5, Recency: 1, Tags: []string{"work", "go"}},
			opts: RankOptions{TagFilter: []string{"zzz"}},
			want: []ScoreFactor{
				{Name: factorBase, Contribution: 5},
				{Name: factorTagMatch, Contribution: 0},
				{Name: factorRecency, Contribution: 1},
			},
		},
		{
			name: "multiple tag matches accumulate bonus",
			cand: RecallCandidate{ID: "e", Score: 8, Recency: 2, Tags: []string{"work", "go", "db"}},
			opts: RankOptions{TagFilter: []string{"work", "go"}},
			want: []ScoreFactor{
				{Name: factorBase, Contribution: 2},
				{Name: factorTagMatch, Contribution: 6},
				{Name: factorRecency, Contribution: 2},
			},
		},
		{
			name: "tag match is case insensitive",
			cand: RecallCandidate{ID: "f", Score: 3, Tags: []string{"Work"}},
			opts: RankOptions{TagFilter: []string{"work"}},
			want: []ScoreFactor{
				{Name: factorBase, Contribution: 0},
				{Name: factorTagMatch, Contribution: 3},
				{Name: factorRecency, Contribution: 0},
			},
		},
		{
			name: "CJK tag matches",
			cand: RecallCandidate{ID: "g", Score: 3, Recency: 9, Tags: []string{"工作", "记忆"}},
			opts: RankOptions{TagFilter: []string{"工作"}},
			want: []ScoreFactor{
				{Name: factorBase, Contribution: 0},
				{Name: factorTagMatch, Contribution: 3},
				{Name: factorRecency, Contribution: 9},
			},
		},
		{
			name: "NaN score: base treated as zero",
			cand: RecallCandidate{ID: "h", Score: math.NaN(), Recency: 5, Tags: []string{"work"}},
			opts: RankOptions{TagFilter: []string{"work"}},
			want: []ScoreFactor{
				{Name: factorBase, Contribution: 0},
				{Name: factorTagMatch, Contribution: 3},
				{Name: factorRecency, Contribution: 5},
			},
		},
		{
			name: "negative score keeps literal remainder",
			cand: RecallCandidate{ID: "i", Score: -2, Recency: 3, Tags: []string{"work"}},
			opts: RankOptions{TagFilter: []string{"work"}},
			want: []ScoreFactor{
				{Name: factorBase, Contribution: -5},
				{Name: factorTagMatch, Contribution: 3},
				{Name: factorRecency, Contribution: 3},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExplainScore(tt.cand, tt.opts)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ExplainScore() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestExplainScoreStableOrder(t *testing.T) {
	// 无论 tag 顺序与数量如何，要素顺序恒为 base、tag_match、recency。
	cands := []RecallCandidate{
		{ID: "a", Score: 1},
		{ID: "b", Score: 5, Tags: []string{"x", "y", "z"}},
		{ID: "c", Score: 9, Recency: 42, Tags: []string{"go"}},
	}
	wantNames := []string{factorBase, factorTagMatch, factorRecency}
	for _, c := range cands {
		got := ExplainScore(c, RankOptions{TagFilter: []string{"go", "x"}})
		if len(got) != len(wantNames) {
			t.Fatalf("candidate %q: got %d factors, want %d", c.ID, len(got), len(wantNames))
		}
		for i, name := range wantNames {
			if got[i].Name != name {
				t.Errorf("candidate %q: factor[%d].Name = %q, want %q", c.ID, i, got[i].Name, name)
			}
		}
	}
}

func TestExplainScoreDoesNotMutateInput(t *testing.T) {
	cand := RecallCandidate{ID: "a", Score: 5, Tags: []string{"Work", "go"}}
	opts := RankOptions{TagFilter: []string{"work"}}
	tagsBefore := append([]string(nil), cand.Tags...)
	filterBefore := append([]string(nil), opts.TagFilter...)

	got := ExplainScore(cand, opts)

	if !reflect.DeepEqual(cand.Tags, tagsBefore) {
		t.Errorf("candidate.Tags mutated: got %v, want %v", cand.Tags, tagsBefore)
	}
	if !reflect.DeepEqual(opts.TagFilter, filterBefore) {
		t.Errorf("opts.TagFilter mutated: got %v, want %v", opts.TagFilter, filterBefore)
	}
	// 修改返回切片不得影响入参或下次调用。
	got[0].Contribution = -999
	again := ExplainScore(cand, opts)
	if again[0].Contribution != 2 {
		t.Errorf("returned slice aliased internal state: got %v", again[0].Contribution)
	}
}

func TestFormatExplanation(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		factors []ScoreFactor
		want    string
	}{
		{
			name: "typical factors",
			id:   "abc",
			factors: []ScoreFactor{
				{Name: factorBase, Contribution: 2},
				{Name: factorTagMatch, Contribution: 3},
				{Name: factorRecency, Contribution: 42},
			},
			want: "召回[id=abc] 基础分=2；tag匹配加分=3；新近度=42",
		},
		{
			name:    "no factors",
			id:      "x",
			factors: nil,
			want:    "召回[id=x]",
		},
		{
			name: "CJK id preserved",
			id:   "记忆-测试-001",
			factors: []ScoreFactor{
				{Name: factorBase, Contribution: 0},
				{Name: factorTagMatch, Contribution: 3},
				{Name: factorRecency, Contribution: 9},
			},
			want: "召回[id=记忆-测试-001] 基础分=0；tag匹配加分=3；新近度=9",
		},
		{
			name: "fractional and negative contributions",
			id:   "n",
			factors: []ScoreFactor{
				{Name: factorBase, Contribution: -5},
				{Name: factorTagMatch, Contribution: 0},
				{Name: factorRecency, Contribution: 3},
			},
			want: "召回[id=n] 基础分=-5；tag匹配加分=0；新近度=3",
		},
		{
			name: "unknown factor label falls back to name",
			id:   "u",
			factors: []ScoreFactor{
				{Name: "custom", Contribution: 1.5},
			},
			want: "召回[id=u] custom=1.5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatExplanation(tt.id, tt.factors); got != tt.want {
				t.Fatalf("FormatExplanation() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatExplanationStable(t *testing.T) {
	factors := []ScoreFactor{
		{Name: factorBase, Contribution: 2},
		{Name: factorTagMatch, Contribution: 3},
		{Name: factorRecency, Contribution: 42},
	}
	before := append([]ScoreFactor(nil), factors...)

	first := FormatExplanation("稳定", factors)
	second := FormatExplanation("稳定", factors)
	if first != second {
		t.Errorf("FormatExplanation not stable: %q != %q", first, second)
	}
	if !reflect.DeepEqual(factors, before) {
		t.Errorf("FormatExplanation mutated factors: got %v, want %v", factors, before)
	}
	// 端到端：解释文本包含 ExplainScore 的全部要素数值。
	cand := RecallCandidate{ID: "记忆", Score: 5, Recency: 42, Tags: []string{"工作"}}
	text := FormatExplanation(cand.ID, ExplainScore(cand, RankOptions{TagFilter: []string{"工作"}}))
	want := "召回[id=记忆] 基础分=2；tag匹配加分=3；新近度=42"
	if text != want {
		t.Errorf("end-to-end explanation = %q, want %q", text, want)
	}
}
