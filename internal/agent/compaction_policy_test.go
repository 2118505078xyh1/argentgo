package agent

import (
	"math"
	"testing"
)

func TestCompactionActionValues(t *testing.T) {
	// 动作取值固定，便于快照、日志与跨语言协议比对。
	cases := []struct {
		got  CompactionAction
		want string
	}{
		{CompactionNone, "none"},
		{CompactionTrimLocal, "trim_local"},
		{CompactionSummarizeModel, "summarize_model"},
		{CompactionCompactFull, "compact_full"},
	}
	for _, tc := range cases {
		if string(tc.got) != tc.want {
			t.Fatalf("动作取值 = %q，期望 %q", tc.got, tc.want)
		}
	}
}

func TestSelectCompactionActionRules(t *testing.T) {
	cases := []struct {
		name string
		in   CompactionInput
		want CompactionAction
	}{
		{
			name: "预算突破优先于一切",
			in:   CompactionInput{UsageRatio: 0.1, ToolResults: 0, ErrorCount: 0, BudgetExceeded: true},
			want: CompactionCompactFull,
		},
		{
			name: "预算突破即使高用量错误多也仍为整体压缩",
			in:   CompactionInput{UsageRatio: 0.99, ToolResults: 100, ErrorCount: 10, BudgetExceeded: true},
			want: CompactionCompactFull,
		},
		{
			name: "用量超0.9走模型摘要",
			in:   CompactionInput{UsageRatio: 0.95},
			want: CompactionSummarizeModel,
		},
		{
			name: "用量高且工具结果大量走本地裁剪",
			in:   CompactionInput{UsageRatio: 0.8, ToolResults: compactionManyToolResults, ErrorCount: 0},
			want: CompactionTrimLocal,
		},
		{
			name: "用量高但工具结果不足则不动",
			in:   CompactionInput{UsageRatio: 0.8, ToolResults: compactionManyToolResults - 1, ErrorCount: 0},
			want: CompactionNone,
		},
		{
			name: "用量低即使工具结果海量也不动",
			in:   CompactionInput{UsageRatio: 0.5, ToolResults: 1000, ErrorCount: 0},
			want: CompactionNone,
		},
		{
			name: "全部为零则不动",
			in:   CompactionInput{},
			want: CompactionNone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SelectCompactionAction(tc.in); got != tc.want {
				t.Fatalf("SelectCompactionAction(%+v) = %s，期望 %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestSelectCompactionActionThresholds(t *testing.T) {
	// 阈值是严格大于（>），等于阈值不算越过；这里用大量工具结果隔离出 trim 分支。
	cases := []struct {
		name string
		in   CompactionInput
		want CompactionAction
	}{
		{
			name: "恰好0.9不算摘要但算高用量",
			in:   CompactionInput{UsageRatio: compactionSummarizeRatio, ToolResults: compactionManyToolResults},
			want: CompactionTrimLocal,
		},
		{
			name: "略高于0.9算摘要",
			in:   CompactionInput{UsageRatio: compactionSummarizeRatio + 1e-9, ToolResults: compactionManyToolResults},
			want: CompactionSummarizeModel,
		},
		{
			name: "恰好0.7不算高用量",
			in:   CompactionInput{UsageRatio: compactionTrimRatio, ToolResults: compactionManyToolResults},
			want: CompactionNone,
		},
		{
			name: "略高于0.7算高用量",
			in:   CompactionInput{UsageRatio: compactionTrimRatio + 1e-9, ToolResults: compactionManyToolResults},
			want: CompactionTrimLocal,
		},
		{
			name: "工具结果恰好达门槛",
			in:   CompactionInput{UsageRatio: 0.8, ToolResults: compactionManyToolResults},
			want: CompactionTrimLocal,
		},
		{
			name: "工具结果差一个不达门槛",
			in:   CompactionInput{UsageRatio: 0.8, ToolResults: compactionManyToolResults - 1},
			want: CompactionNone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SelectCompactionAction(tc.in); got != tc.want {
				t.Fatalf("SelectCompactionAction(%+v) = %s，期望 %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestSelectCompactionActionPreservesEvidenceOnErrors(t *testing.T) {
	// 错误多时不应激进裁剪：具备 trim 条件的输入应升级为模型摘要以保留证据。
	cases := []struct {
		name string
		errs int
		want CompactionAction
	}{
		{"无错误", 0, CompactionTrimLocal},
		{"1个错误", 1, CompactionTrimLocal},
		{"差一个达门槛", compactionManyErrors - 1, CompactionTrimLocal},
		{"恰好达门槛", compactionManyErrors, CompactionSummarizeModel},
		{"远超门槛", compactionManyErrors + 50, CompactionSummarizeModel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := CompactionInput{UsageRatio: 0.8, ToolResults: compactionManyToolResults, ErrorCount: tc.errs}
			if got := SelectCompactionAction(in); got != tc.want {
				t.Fatalf("错误数 %d: 得到 %s，期望 %s", tc.errs, got, tc.want)
			}
		})
	}
}

func selectCompactionActionClampCases() []struct {
	name  string
	ratio float64
	want  CompactionAction
} {
	// 低用量侧用 none 验证，高用量侧用 summarize_model 验证，以看清钳制效果。
	return []struct {
		name  string
		ratio float64
		want  CompactionAction
	}{
		{"负比例钳到0", -1.0, CompactionNone},
		{"零", 0.0, CompactionNone},
		{"超1钳到1", 1.5, CompactionSummarizeModel},
		{"远超1钳到1", 1e9, CompactionSummarizeModel},
		{"正无穷钳到1", math.Inf(1), CompactionSummarizeModel},
		{"负无穷钳到0", math.Inf(-1), CompactionNone},
		{"NaN视作0", math.NaN(), CompactionNone},
	}
}

func TestSelectCompactionActionClampsRatio(t *testing.T) {
	for _, tc := range selectCompactionActionClampCases() {
		t.Run(tc.name, func(t *testing.T) {
			in := CompactionInput{UsageRatio: tc.ratio}
			if got := SelectCompactionAction(in); got != tc.want {
				t.Fatalf("UsageRatio=%v: 得到 %s，期望 %s", tc.ratio, got, tc.want)
			}
		})
	}
}

func TestClampUsageRatio(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{-1.0, 0},
		{-0.0001, 0},
		{0.0, 0},
		{0.5, 0.5},
		{1.0, 1},
		{1.0001, 1},
		{math.Inf(1), 1},
		{math.Inf(-1), 0},
		{math.NaN(), 0},
	}
	for _, tc := range cases {
		got := clampUsageRatio(tc.in)
		// NaN 不应残留（否则与自身不相等，会破坏确定性）。
		if got != tc.want {
			t.Fatalf("clampUsageRatio(%v) = %v，期望 %v", tc.in, got, tc.want)
		}
	}
}

func TestSelectCompactionActionDeterministic(t *testing.T) {
	cases := []CompactionInput{
		{},
		{UsageRatio: 0.95},
		{UsageRatio: 0.8, ToolResults: compactionManyToolResults},
		{UsageRatio: 0.8, ToolResults: compactionManyToolResults, ErrorCount: compactionManyErrors},
		{UsageRatio: 0.8, ToolResults: 2, BudgetExceeded: true},
		{UsageRatio: math.NaN()},
	}
	for _, in := range cases {
		first := SelectCompactionAction(in)
		for i := 0; i < 5; i++ {
			if got := SelectCompactionAction(in); got != first {
				t.Fatalf("SelectCompactionAction(%+v) 不稳定：%s vs %s", in, got, first)
			}
		}
	}
}

func TestSelectCompactionActionDoesNotMutateInput(t *testing.T) {
	in := CompactionInput{
		UsageRatio:     0.8,
		ErrorCount:     compactionManyErrors,
		ToolResults:    compactionManyToolResults,
		BudgetExceeded: false,
	}
	want := in
	_ = SelectCompactionAction(in)
	if in != want {
		t.Fatalf("入参被修改：%+v，期望 %+v", in, want)
	}
}
