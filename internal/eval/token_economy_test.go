package eval

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"example.com/argentgo/internal/llm"
)

// tokenResult 构造带指定 token 用量的 Result（全部计入 PromptTokens，TotalTokens 即 tokens）。
func tokenResult(tokens int) Result {
	return Result{Usage: llm.Usage{PromptTokens: tokens}}
}

// tokenResults 把 token 序列构造为 Result 向量，便于逐场景配对。
func tokenResults(tokens []int) []Result {
	out := make([]Result, len(tokens))
	for i, t := range tokens {
		out[i] = tokenResult(t)
	}
	return out
}

// TestTokenEconomyDiagnosisDirections 锁定 token 增/减/持平三种方向的配对诊断数值与格式。
// 差值与 95%CI 由 PairedDelta 给出；样本内 token 恒定时标准差为零，CI 收敛到均值差。
func TestTokenEconomyDiagnosisDirections(t *testing.T) {
	cases := []struct {
		name       string
		baseline   []int
		candidate  []int
		wantSubstr []string
	}{
		{
			name:      "token 增加",
			baseline:  []int{100, 100},
			candidate: []int{200, 200},
			wantSubstr: []string{
				"基线 100.0 → 候选 200.0",
				"Δ+100.0",
				"95%CI=[+100.0, +100.0]",
				"n=2",
			},
		},
		{
			name:      "token 减少",
			baseline:  []int{200, 200},
			candidate: []int{100, 100},
			wantSubstr: []string{
				"基线 200.0 → 候选 100.0",
				"Δ-100.0",
				"95%CI=[-100.0, -100.0]",
				"n=2",
			},
		},
		{
			name:      "token 持平",
			baseline:  []int{100, 100},
			candidate: []int{100, 100},
			wantSubstr: []string{
				"基线 100.0 → 候选 100.0",
				"Δ+0.0",
				"95%CI=[+0.0, +0.0]",
				"n=2",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TokenEconomyDiagnosis(tokenResults(tc.baseline), tokenResults(tc.candidate))
			if !strings.HasPrefix(got, tokenEconomyPrefix) {
				t.Errorf("missing stable prefix: %q", got)
			}
			for _, want := range tc.wantSubstr {
				if !strings.Contains(got, want) {
					t.Errorf("diagnosis %q missing %q", got, want)
				}
			}
			// 钳制保证不出现 NaN / ±Inf，且输出是单行。
			if strings.Contains(got, "NaN") || strings.Contains(got, "Inf") {
				t.Errorf("diagnosis must clamp non-finite values: %q", got)
			}
			if strings.ContainsAny(got, "\r\n") {
				t.Errorf("diagnosis must be a single line: %q", got)
			}
		})
	}
}

// TestTokenEconomyDiagnosisConservative 覆盖样本不足、长度不等与两臂全零三类保守分支。
func TestTokenEconomyDiagnosisConservative(t *testing.T) {
	cases := []struct {
		name      string
		baseline  []Result
		candidate []Result
		want      string
	}{
		{
			name:      "n=0",
			baseline:  nil,
			candidate: nil,
			want:      "token: 样本不足，跳过 token 经济学诊断（配对数 n=0，需至少 2 对）",
		},
		{
			name:      "n=1",
			baseline:  tokenResults([]int{100}),
			candidate: tokenResults([]int{200}),
			want:      "token: 样本不足，跳过 token 经济学诊断（配对数 n=1，需至少 2 对）",
		},
		{
			name:      "长度不等",
			baseline:  tokenResults([]int{100, 200}),
			candidate: tokenResults([]int{100}),
			want:      "token: 两臂向量长度不等，无可比数据，跳过 token 经济学诊断（基线 n=2，候选 n=1）",
		},
		{
			name:      "两臂全零",
			baseline:  tokenResults([]int{0, 0}),
			candidate: tokenResults([]int{0, 0}),
			want:      tokenEconomyNoData,
		},
		{
			name:      "一臂全零另一臂有值仍参与配对",
			baseline:  tokenResults([]int{0, 0}),
			candidate: tokenResults([]int{100, 200}),
			want:      "token: 基线 0.0 → 候选 150.0（Δ+150.0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TokenEconomyDiagnosis(tc.baseline, tc.candidate)
			if strings.HasSuffix(tc.want, "（Δ+150.0") {
				// 最后一例只锁定关键数值前缀，CI 随 t 临界值变化不写死。
				if !strings.HasPrefix(got, tc.want) {
					t.Errorf("diagnosis = %q, want prefix %q", got, tc.want)
				}
				return
			}
			if got != tc.want {
				t.Errorf("diagnosis = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTokenEconomyDiagnosisCJK 覆盖中文场景名与 prompt+completion 求和：
// TotalTokens = PromptTokens + CompletionTokens，不能只取单一字段。
func TestTokenEconomyDiagnosisCJK(t *testing.T) {
	baseline := []Result{
		{Name: "场景一", Usage: llm.Usage{PromptTokens: 10, CompletionTokens: 5}},
		{Name: "场景二", Usage: llm.Usage{PromptTokens: 20, CompletionTokens: 7}},
	}
	candidate := []Result{
		{Name: "场景一", Usage: llm.Usage{PromptTokens: 30, CompletionTokens: 5}},
		{Name: "场景二", Usage: llm.Usage{PromptTokens: 40, CompletionTokens: 7}},
	}
	got := TokenEconomyDiagnosis(baseline, candidate)
	if !utf8.ValidString(got) {
		t.Errorf("diagnosis is not valid UTF-8: %q", got)
	}
	for _, want := range []string{"基线", "候选", "95%CI", "n=2"} {
		if !strings.Contains(got, want) {
			t.Errorf("diagnosis %q missing %q", got, want)
		}
	}
	// 基线均值 (15+27)/2=21，候选均值 (35+47)/2=41。
	if !strings.Contains(got, "基线 21.0 → 候选 41.0") {
		t.Errorf("diagnosis must sum prompt+completion tokens: %q", got)
	}
}

// TestTokenEconomyDiagnosisDoesNotChangeVerdict 证明 token 用量不参与通过门与 verdict：
// 同样的通过序列，token 翻 100 倍也不改任何 verdict 文本与样本不足守卫。
func TestTokenEconomyDiagnosisDoesNotChangeVerdict(t *testing.T) {
	mkResults := func(passed []bool, tokens int) []Result {
		out := make([]Result, len(passed))
		for i, p := range passed {
			out[i] = Result{Name: "s", Passed: p, Usage: llm.Usage{PromptTokens: tokens}}
		}
		return out
	}
	passedBase := []bool{false, false, false, false, false, false}
	passedCand := []bool{true, true, true, true, true, true}

	low, err := CompareResults(mkResults(passedBase, 10), mkResults(passedCand, 10))
	if err != nil {
		t.Fatalf("CompareResults(low): %v", err)
	}
	high, err := CompareResults(mkResults(passedBase, 1000), mkResults(passedCand, 1000))
	if err != nil {
		t.Fatalf("CompareResults(high): %v", err)
	}
	if firstVerdictLine(low) != firstVerdictLine(high) {
		t.Errorf("verdict changed by token usage: %q vs %q", firstVerdictLine(low), firstVerdictLine(high))
	}
	if !strings.Contains(firstVerdictLine(high), "样本不足") {
		t.Errorf("token usage must not override insufficient verdict: %q", firstVerdictLine(high))
	}
	if !strings.Contains(high, "基线 1000.0 → 候选 1000.0") {
		t.Errorf("token doubling must still appear as an aside line:\n%s", high)
	}
	// 诊断行必须位于 R38 诊断段之后，且不早于 verdict。
	verdictIdx := strings.Index(high, "verdict:")
	diagIdx := strings.Index(high, "诊断段:")
	tokIdx := strings.Index(high, "token:")
	if verdictIdx < 0 || diagIdx < verdictIdx || tokIdx < diagIdx {
		t.Errorf("unexpected section order: verdict=%d diag=%d token=%d\n%s",
			verdictIdx, diagIdx, tokIdx, high)
	}
}

// TestTokenEconomyDiagnosisDeterminism 锁定纯函数性：重复调用逐字节一致，且不修改入参。
func TestTokenEconomyDiagnosisDeterminism(t *testing.T) {
	baseline := []Result{
		{Usage: llm.Usage{PromptTokens: 100, CompletionTokens: 20}},
		{Usage: llm.Usage{PromptTokens: 250, CompletionTokens: 30}},
		{Usage: llm.Usage{PromptTokens: 90, CompletionTokens: 10}},
	}
	candidate := []Result{
		{Usage: llm.Usage{PromptTokens: 300, CompletionTokens: 20}},
		{Usage: llm.Usage{PromptTokens: 200, CompletionTokens: 30}},
		{Usage: llm.Usage{PromptTokens: 180, CompletionTokens: 10}},
	}
	first := TokenEconomyDiagnosis(baseline, candidate)
	for i := 0; i < 5; i++ {
		if got := TokenEconomyDiagnosis(baseline, candidate); got != first {
			t.Fatalf("call %d differs: %q vs %q", i, got, first)
		}
	}
	baseCopy := append([]Result(nil), baseline...)
	candCopy := append([]Result(nil), candidate...)
	_ = TokenEconomyDiagnosis(baseline, candidate)
	if !reflect.DeepEqual(baseline, baseCopy) || !reflect.DeepEqual(candidate, candCopy) {
		t.Fatal("TokenEconomyDiagnosis mutated its inputs")
	}

	// CompareResults 整体输出同样确定性。
	a, err := CompareResults(baseline, candidate)
	if err != nil {
		t.Fatalf("CompareResults: %v", err)
	}
	b, err := CompareResults(baseline, candidate)
	if err != nil {
		t.Fatalf("CompareResults: %v", err)
	}
	if a != b {
		t.Fatalf("CompareResults not deterministic:\n%s\n---\n%s", a, b)
	}
}
