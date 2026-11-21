package eval

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// cloneRatchet 复制状态。RatchetEntry 全是值类型，因此复制切片即可视为深拷贝。
func cloneRatchet(s RatchetState) RatchetState {
	c := RatchetState{BestScore: s.BestScore}
	if s.History != nil {
		c.History = make([]RatchetEntry, len(s.History))
		copy(c.History, s.History)
	}
	return c
}

// sampleRatchetEntries 提供带中文 reason 的两条历史，覆盖 round-trip 与 CJK 用例。
func sampleRatchetEntries() []RatchetEntry {
	return []RatchetEntry{
		{
			TrainMeanDiff: 0.05, HoldoutCILow: 0.02, Epsilon: 0.01,
			PrevBestScore: 1, BestScore: 1.05, Accept: true,
			Reason: "接受：训练集提升且保留集未显著退化",
		},
		{
			TrainMeanDiff: 0.1, HoldoutCILow: -0.005, Epsilon: 0.01,
			PrevBestScore: 1.05, BestScore: 1.15, Accept: true,
			Reason: "接受：噪声在容忍度内",
		},
	}
}

// TestRatchetJSONRoundTrip 验证 ToJSON/FromJSON 互逆：nil 与空 History
// 得到区分保留，带中文 reason 的多条目历史也能原样还原，且不产生尾随换行。
func TestRatchetJSONRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		state RatchetState
	}{
		{
			name:  "nil history",
			state: RatchetState{BestScore: 0},
		},
		{
			name:  "empty non-nil history",
			state: RatchetState{BestScore: 1, History: []RatchetEntry{}},
		},
		{
			name:  "single entry",
			state: RatchetState{BestScore: 1.05, History: sampleRatchetEntries()[:1]},
		},
		{
			name:  "multiple entries with CJK reason",
			state: RatchetState{BestScore: 1.15, History: sampleRatchetEntries()},
		},
		{
			name: "negative score",
			state: RatchetState{BestScore: -3.25, History: []RatchetEntry{
				{
					TrainMeanDiff: -0.5, HoldoutCILow: -0.02, Epsilon: 0.01,
					PrevBestScore: -2.75, BestScore: -3.25, Accept: false,
					Reason: "拒绝：训练集未提升",
				},
			}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := cloneRatchet(tc.state)

			data, err := RatchetToJSON(tc.state)
			if err != nil {
				t.Fatalf("RatchetToJSON() error = %v", err)
			}
			if bytes.HasSuffix(data, []byte("\n")) {
				t.Fatalf("RatchetToJSON() output has trailing newline: %q", data)
			}
			if !reflect.DeepEqual(tc.state, before) {
				t.Fatalf("RatchetToJSON() mutated input: got %+v, want %+v", tc.state, before)
			}

			got, err := RatchetFromJSON(data)
			if err != nil {
				t.Fatalf("RatchetFromJSON() error = %v (data=%q)", err, data)
			}
			if !reflect.DeepEqual(got, tc.state) {
				t.Fatalf("round-trip mismatch: got %+v, want %+v (data=%q)", got, tc.state, data)
			}
		})
	}
}

// TestRatchetToJSONStableBytes 锁定字段顺序与格式：对同一状态重复编码字节一致，
// 且与写死的期望字节完全相同，任何字段重排都会让此测试失败。
func TestRatchetToJSONStableBytes(t *testing.T) {
	tests := []struct {
		name string
		in   RatchetState
		want string
	}{
		{
			name: "nil history",
			in:   RatchetState{BestScore: 0},
			want: `{"best_score":0,"history":null}`,
		},
		{
			name: "one entry field order",
			in: RatchetState{BestScore: 1.5, History: []RatchetEntry{{
				TrainMeanDiff: 0.05, HoldoutCILow: 0.02, Epsilon: 0.01,
				PrevBestScore: 1, BestScore: 1.05, Accept: true, Reason: "接受",
			}}},
			want: `{"best_score":1.5,"history":[{"train_mean_diff":0.05,"holdout_ci_low":0.02,"epsilon":0.01,"prev_best_score":1,"best_score":1.05,"accept":true,"reason":"接受"}]}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			first, err := RatchetToJSON(tc.in)
			if err != nil {
				t.Fatalf("RatchetToJSON() error = %v", err)
			}
			second, err := RatchetToJSON(tc.in)
			if err != nil {
				t.Fatalf("RatchetToJSON() second call error = %v", err)
			}
			if !bytes.Equal(first, second) {
				t.Fatalf("unstable bytes: first=%q second=%q", first, second)
			}
			if string(first) != tc.want {
				t.Fatalf("RatchetToJSON() = %q, want %q", first, tc.want)
			}
		})
	}
}

// TestRatchetToJSONNoHTMLEscape 验证 SetEscapeHTML(false) 生效：中文与
// <>& 原样输出，不出现 \uXXXX 转义序列。
func TestRatchetToJSONNoHTMLEscape(t *testing.T) {
	s := RatchetState{BestScore: 2, History: []RatchetEntry{{
		Reason: "接受：<候选> & 基线对比，保留集稳定",
	}}}
	out, err := RatchetToJSON(s)
	if err != nil {
		t.Fatalf("RatchetToJSON() error = %v", err)
	}
	text := string(out)

	if !strings.Contains(text, "接受：<候选> & 基线对比") {
		t.Fatalf("CJK / HTML chars were escaped: %q", text)
	}
	if strings.Contains(text, `\u`) {
		t.Fatalf("output contains \\uXXXX escape: %q", text)
	}
}

// TestRatchetFromJSONInvalid 验证空/非对象/畸形数据都返回包裹
// ErrInvalidRatchetJSON 的错误，errors.Is 可匹配，且结果状态为零值。
func TestRatchetFromJSONInvalid(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{name: "nil input", data: nil},
		{name: "empty input", data: []byte{}},
		{name: "whitespace only", data: []byte(" \t\n ")},
		{name: "null", data: []byte("null")},
		{name: "array", data: []byte("[]")},
		{name: "number", data: []byte("123")},
		{name: "string", data: []byte(`"text"`)},
		{name: "truncated object", data: []byte("{")},
		{name: "wrong field type", data: []byte(`{"best_score":"x","history":null}`)},
		{name: "bad history element", data: []byte(`{"best_score":1,"history":[{"train_mean_diff":"bad"}]}`)},
		{name: "trailing garbage", data: []byte(`{"best_score":1,"history":null} trailing`)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RatchetFromJSON(tc.data)
			if !errors.Is(err, ErrInvalidRatchetJSON) {
				t.Fatalf("RatchetFromJSON(%q) error = %v, want ErrInvalidRatchetJSON", tc.data, err)
			}
			if !reflect.DeepEqual(got, RatchetState{}) {
				t.Fatalf("RatchetFromJSON(%q) state = %+v, want zero value", tc.data, got)
			}
		})
	}
}

// TestMergeRatchet 表驱动覆盖 BestScore 取值、History 去重、顺序与各边界组合。
func TestMergeRatchet(t *testing.T) {
	e1 := RatchetEntry{TrainMeanDiff: 0.05, PrevBestScore: 1, BestScore: 1.05, Accept: true, Reason: "接受一"}
	e2 := RatchetEntry{TrainMeanDiff: 0.1, PrevBestScore: 1.05, BestScore: 1.15, Accept: true, Reason: "接受二"}
	e3 := RatchetEntry{TrainMeanDiff: 0.5, PrevBestScore: 2, BestScore: 2.5, Accept: true, Reason: "接受三"}

	tests := []struct {
		name     string
		a        RatchetState
		b        RatchetState
		wantBest float64
		wantHist []RatchetEntry
	}{
		{
			name:     "a has higher best",
			a:        RatchetState{BestScore: 5, History: []RatchetEntry{e1}},
			b:        RatchetState{BestScore: 3, History: []RatchetEntry{e2}},
			wantBest: 5,
			wantHist: []RatchetEntry{e1, e2},
		},
		{
			name:     "b has higher best",
			a:        RatchetState{BestScore: 3, History: []RatchetEntry{e1}},
			b:        RatchetState{BestScore: 5, History: []RatchetEntry{e2}},
			wantBest: 5,
			wantHist: []RatchetEntry{e1, e2},
		},
		{
			name:     "equal best",
			a:        RatchetState{BestScore: 4, History: []RatchetEntry{e1}},
			b:        RatchetState{BestScore: 4, History: []RatchetEntry{e2}},
			wantBest: 4,
			wantHist: []RatchetEntry{e1, e2},
		},
		{
			name:     "dedupe shared entries keeping first position",
			a:        RatchetState{BestScore: 1, History: []RatchetEntry{e1, e2}},
			b:        RatchetState{BestScore: 2, History: []RatchetEntry{e2, e3}},
			wantBest: 2,
			wantHist: []RatchetEntry{e1, e2, e3},
		},
		{
			name:     "dedupe duplicate inside one history",
			a:        RatchetState{BestScore: 1, History: []RatchetEntry{e1, e1, e2}},
			b:        RatchetState{BestScore: 0},
			wantBest: 1,
			wantHist: []RatchetEntry{e1, e2},
		},
		{
			name:     "both histories nil",
			a:        RatchetState{BestScore: 1},
			b:        RatchetState{BestScore: 2},
			wantBest: 2,
			wantHist: nil,
		},
		{
			name:     "empty history stays non-nil",
			a:        RatchetState{BestScore: 3, History: []RatchetEntry{}},
			b:        RatchetState{BestScore: 1},
			wantBest: 3,
			wantHist: []RatchetEntry{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MergeRatchet(tc.a, tc.b)
			want := RatchetState{BestScore: tc.wantBest, History: tc.wantHist}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("MergeRatchet() = %+v, want %+v", got, want)
			}
		})
	}
}

// TestMergeRatchetDoesNotMutateInputs 验证 Merge 是纯函数：不修改入参，
// 且返回的历史切片与入参不共享底层数组。
func TestMergeRatchetDoesNotMutateInputs(t *testing.T) {
	e1 := RatchetEntry{TrainMeanDiff: 0.05, PrevBestScore: 1, BestScore: 1.05, Reason: "接受一"}
	e2 := RatchetEntry{TrainMeanDiff: 0.1, PrevBestScore: 1.05, BestScore: 1.15, Reason: "接受二"}
	a := RatchetState{BestScore: 1, History: []RatchetEntry{e1}}
	b := RatchetState{BestScore: 2, History: []RatchetEntry{e2}}
	aBefore, bBefore := cloneRatchet(a), cloneRatchet(b)

	merged := MergeRatchet(a, b)
	if !reflect.DeepEqual(a, aBefore) {
		t.Fatalf("MergeRatchet() mutated a: got %+v, want %+v", a, aBefore)
	}
	if !reflect.DeepEqual(b, bBefore) {
		t.Fatalf("MergeRatchet() mutated b: got %+v, want %+v", b, bBefore)
	}

	// 改动返回值不应影响任一入参，确认 History 是副本。
	merged.History[0].Reason = "被改动"
	merged.History[1].Reason = "被改动"
	if a.History[0].Reason != e1.Reason {
		t.Fatalf("MergeRatchet() result aliases a.History: %q", a.History[0].Reason)
	}
	if b.History[0].Reason != e2.Reason {
		t.Fatalf("MergeRatchet() result aliases b.History: %q", b.History[0].Reason)
	}
}
