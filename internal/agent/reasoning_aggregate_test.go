package agent

import "testing"

func TestMergeReasoningDelta(t *testing.T) {
	tests := []struct {
		name        string
		current     string
		delta       string
		wantNext    string
		wantReplace bool
	}{
		{name: "first delta from empty", current: "", delta: "让", wantNext: "让"},
		{name: "strict increments append", current: "让我", delta: "逐步思考", wantNext: "让我逐步思考"},
		{name: "cumulative snapshot replaces", current: "让", delta: "让我逐步", wantNext: "让我逐步", wantReplace: true},
		{name: "exact repeat is no-op", current: "让我思考", delta: "让我思考", wantNext: "让我思考"},
		{name: "empty delta is no-op", current: "已有", delta: "", wantNext: "已有"},
		{name: "empty current accepts delta", current: "", delta: "新内容", wantNext: "新内容"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			next, replace := MergeReasoningDelta(tc.current, tc.delta)
			if next != tc.wantNext {
				t.Fatalf("next = %q, want %q", next, tc.wantNext)
			}
			if replace != tc.wantReplace {
				t.Fatalf("replace = %v, want %v", replace, tc.wantReplace)
			}
		})
	}
}

func TestReasoningEndOverride(t *testing.T) {
	tests := []struct {
		name        string
		current     string
		final       string
		wantNext    string
		wantReplace bool
	}{
		{name: "end overrides accumulated", current: "部分思考", final: "完整思考内容", wantNext: "完整思考内容", wantReplace: true},
		{name: "end re-sends full block", current: "让我思考", final: "让我思考：结论", wantNext: "让我思考：结论", wantReplace: true},
		{name: "identical end is no-op", current: "完整", final: "完整", wantNext: "完整"},
		{name: "empty end keeps text", current: "完整", final: "", wantNext: "完整"},
		{name: "end from empty", current: "", final: "完整", wantNext: "完整", wantReplace: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			next, replace := ReasoningEndOverride(tc.current, tc.final)
			if next != tc.wantNext {
				t.Fatalf("next = %q, want %q", next, tc.wantNext)
			}
			if replace != tc.wantReplace {
				t.Fatalf("replace = %v, want %v", replace, tc.wantReplace)
			}
		})
	}
}

func TestReasoningAggregatorIncremental(t *testing.T) {
	agg := &ReasoningAggregator{}
	for _, d := range []string{"让", "我", "逐步", "思考"} {
		op := agg.Add(d)
		if op.Replace {
			t.Fatalf("unexpected replace for fragment %q", d)
		}
		if op.Text != d {
			t.Fatalf("append op text = %q, want fragment %q", op.Text, d)
		}
	}
	if got, want := agg.Text(), "让我逐步思考"; got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}

func TestReasoningAggregatorUTF8(t *testing.T) {
	agg := &ReasoningAggregator{}
	agg.Add("让我")
	agg.Add("🧠")
	agg.Add("思考🙂")
	if got, want := agg.Text(), "让我🧠思考🙂"; got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
	// 累积快照用更多 CJK/emoji 重新发送整个块。它必须替换而非重复，且绝不能切断多字节 rune。
	// （接上行）
	const full = "让我🧠思考🙂：看输入"
	op := agg.Add(full)
	if !op.Replace || op.Text != full {
		t.Fatalf("snapshot op = %+v, want replace with full text", op)
	}
	if got := agg.Text(); got != full {
		t.Fatalf("text = %q, want %q", got, full)
	}
}

func TestReasoningAggregatorNoDuplicateOnResend(t *testing.T) {
	agg := &ReasoningAggregator{}
	agg.Add("分析输入")
	agg.Add("分析输入，得到结论")    // 累积快照
	agg.Add("分析输入，得到结论")    // 完全重复
	agg.Finish("分析输入，得到结论") // 权威的结束帧重新发送整个块
	if got, want := agg.Text(), "分析输入，得到结论"; got != want {
		t.Fatalf("text = %q, want %q (no duplication)", got, want)
	}
}

func TestReasoningAggregatorFinish(t *testing.T) {
	agg := &ReasoningAggregator{}
	agg.Add("局部")
	const final = "局部 → 完整的最终思考"
	op := agg.Finish(final)
	if !op.Replace || op.Text != final {
		t.Fatalf("finish op = %+v, want replace with %q", op, final)
	}
	if got := agg.Text(); got != final {
		t.Fatalf("text = %q, want %q", got, final)
	}
	// 幂等的结束帧不得触发多余的重绘。
	if op := agg.Finish(final); op.Replace || op.Text != "" {
		t.Fatalf("idempotent finish op = %+v, want no-op", op)
	}
}

func TestReasoningAggregatorEmptyInput(t *testing.T) {
	agg := &ReasoningAggregator{}
	if op := agg.Add(""); op.Replace || op.Text != "" {
		t.Fatalf("empty add op = %+v, want no-op", op)
	}
	if op := agg.Finish(""); op.Replace || op.Text != "" {
		t.Fatalf("empty finish op = %+v, want no-op", op)
	}
	if got := agg.Text(); got != "" {
		t.Fatalf("text = %q, want empty", got)
	}
}
