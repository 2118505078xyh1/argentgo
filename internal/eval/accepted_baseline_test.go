package eval

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

// cloneResults 深拷贝一份结果向量，用于验证纯函数不修改入参。
func cloneResults(results []Result) []Result {
	out := make([]Result, len(results))
	copy(out, results)
	for i := range out {
		if results[i].Tools != nil {
			out[i].Tools = append([]string(nil), results[i].Tools...)
		}
	}
	return out
}

func sampleBaselineResults() []Result {
	return []Result{
		{Index: 1, Name: "读文件", Passed: true, Reason: "ok", Tools: []string{"read"}, Turns: 2},
		{Index: 2, Name: "写文件", Passed: false, Reason: "path escape", Tools: []string{"write"}, Turns: 3, Stopped: true},
		{Index: 3, Name: "查帮助", Passed: true, Reason: "ok", Turns: 1},
		{Index: 4, Name: "列目录", Passed: true, Reason: "ok", Turns: 1},
	}
}

// TestBaselineFromResultsScore 验证完成率计算正确：3 passed / 4 总数 = 0.75。
func TestBaselineFromResultsScore(t *testing.T) {
	got := BaselineFromResults(sampleBaselineResults(), "v1 已接受")
	if math.Abs(got.Score-0.75) > 1e-12 {
		t.Fatalf("Score = %v, want 0.75", got.Score)
	}
	if got.Note != "v1 已接受" {
		t.Fatalf("Note = %q, want %q", got.Note, "v1 已接受")
	}
	if got.Fingerprint == "" || got.ResultsHash == "" {
		t.Fatalf("Fingerprint / ResultsHash 不应为空: %+v", got)
	}
	if len(got.Fingerprint) != 64 || len(got.ResultsHash) != 64 {
		t.Fatalf("指纹应为 64 位小写十六进制: %+v", got)
	}
}

// TestBaselineFromResultsEmpty 验证空向量得到完成率 0 且指纹稳定非空。
func TestBaselineFromResultsEmpty(t *testing.T) {
	a := BaselineFromResults(nil, "空")
	b := BaselineFromResults([]Result{}, "空")
	if a.Score != 0 || b.Score != 0 {
		t.Fatalf("空向量 Score 应为 0, got %v / %v", a.Score, b.Score)
	}
	if a.Fingerprint != b.Fingerprint || a.ResultsHash != b.ResultsHash {
		t.Fatal("nil 与空切片的指纹应一致")
	}
}

// TestBaselineFromResultsDeterministic 验证相同输入得到相同记录，且两次调用结果一致。
func TestBaselineFromResultsDeterministic(t *testing.T) {
	results := sampleBaselineResults()
	a := BaselineFromResults(results, "note")
	b := BaselineFromResults(results, "note")
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("相同输入应得到相同基线:\n%#v\n%#v", a, b)
	}
}

// TestBaselineHashDistinguishesOutcomes 验证结果向量哈希能区分通过模式与场景名的变化。
func TestBaselineHashDistinguishesOutcomes(t *testing.T) {
	base := sampleBaselineResults()
	changedPass := cloneResults(base)
	changedPass[1].Passed = true
	changedName := cloneResults(base)
	changedName[0].Name = "读文件2"
	changedReason := cloneResults(base)
	changedReason[0].Reason = "changed"

	h0 := BaselineFromResults(base, "").ResultsHash
	cases := []struct {
		name    string
		results []Result
	}{
		{"通过模式变化", changedPass},
		{"场景名变化", changedName},
		{"理由变化", changedReason},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := BaselineFromResults(tc.results, "").ResultsHash; got == h0 {
				t.Fatalf("结果变化应改变 ResultsHash: %q", got)
			}
		})
	}
}

// TestBaselineDiagnosticFieldsExcludedFromHash 验证 token/耗时等诊断字段不进入结果哈希。
func TestBaselineDiagnosticFieldsExcludedFromHash(t *testing.T) {
	a := sampleBaselineResults()
	b := cloneResults(a)
	b[0].Duration = 1 << 40
	b[0].Usage.PromptTokens = 99999
	b[0].Text = "不同文本"
	b[0].Tools = []string{"shell"}
	if BaselineFromResults(a, "").ResultsHash != BaselineFromResults(b, "").ResultsHash {
		t.Fatal("诊断字段不应改变 ResultsHash")
	}
}

// TestBaselineFromResultsDoesNotMutate 验证基线建立不修改入参。
func TestBaselineFromResultsDoesNotMutate(t *testing.T) {
	results := sampleBaselineResults()
	before := cloneResults(results)
	BaselineFromResults(results, "note")
	if !reflect.DeepEqual(results, before) {
		t.Fatalf("入参被修改:\nbefore=%#v\nafter=%#v", before, results)
	}
}

// TestCheckRegression 验证三态判定：接受、退化、样本不足。
func TestCheckRegression(t *testing.T) {
	baseline := BaselineFromResults(sampleBaselineResults(), "v1") // Score = 0.75

	allPass := []Result{
		{Passed: true}, {Passed: true}, {Passed: true}, {Passed: true},
	}
	same := []Result{
		{Passed: true}, {Passed: false}, {Passed: true}, {Passed: true},
	}
	oneFail := []Result{
		{Passed: true}, {Passed: false}, {Passed: false}, {Passed: true},
	}
	halfFail := []Result{
		{Passed: true}, {Passed: false}, {Passed: false}, {Passed: false},
	}

	tests := []struct {
		name      string
		baseline  AcceptedBaseline
		candidate []Result
		want      RegressionVerdict
	}{
		{"完成率提升", baseline, allPass, RegressionAccepted},
		{"完成率持平", baseline, same, RegressionAccepted},
		{"完成率下降", baseline, oneFail, RegressionRegressed},
		{"大幅下降", baseline, halfFail, RegressionRegressed},
		{"单个样本", baseline, []Result{{Passed: false}}, RegressionInsufficient},
		{"空候选", baseline, nil, RegressionInsufficient},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CheckRegression(tc.baseline, tc.candidate); got != tc.want {
				t.Fatalf("CheckRegression = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCheckRegressionEpsilon 验证浮点容差：用 2/3 级别的分数与近似值比较时不误判退化。
func TestCheckRegressionEpsilon(t *testing.T) {
	baseline := AcceptedBaseline{Score: 2.0 / 3.0}
	candidate := []Result{{Passed: true}, {Passed: true}, {Passed: false}}
	if got := CheckRegression(baseline, candidate); got != RegressionAccepted {
		t.Fatalf("近似相等应判为 accepted, got %q", got)
	}
}

// TestCheckRegressionDoesNotMutate 验证判定不修改候选与基线。
func TestCheckRegressionDoesNotMutate(t *testing.T) {
	baseline := BaselineFromResults(sampleBaselineResults(), "v1")
	baselineBefore := baseline
	candidate := sampleBaselineResults()
	candidateBefore := cloneResults(candidate)
	CheckRegression(baseline, candidate)
	if !reflect.DeepEqual(baseline, baselineBefore) {
		t.Fatalf("基线被修改: %#v -> %#v", baselineBefore, baseline)
	}
	if !reflect.DeepEqual(candidate, candidateBefore) {
		t.Fatalf("候选被修改: %#v -> %#v", candidateBefore, candidate)
	}
}

// TestBaselineCJKNote 验证中文 note 在 JSON 中原样保留，不转义为 \uXXXX。
func TestBaselineCJKNote(t *testing.T) {
	b := BaselineFromResults(sampleBaselineResults(), "中文备注：已接受基线 <>&")
	data, err := BaselineToJSON(b)
	if err != nil {
		t.Fatalf("BaselineToJSON: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "中文备注：已接受基线 <>&") {
		t.Fatalf("中文 note 未原样输出: %s", text)
	}
	if strings.Contains(text, `\u`) {
		t.Fatalf("不应出现 \\uXXXX 转义: %s", text)
	}
	if strings.HasSuffix(text, "\n") {
		t.Fatalf("输出不应带尾随换行: %q", text)
	}
}

// TestBaselineJSONRoundTrip 验证序列化/反序列化 round-trip 等价。
func TestBaselineJSONRoundTrip(t *testing.T) {
	original := BaselineFromResults(sampleBaselineResults(), "中文备注 v1")
	data, err := BaselineToJSON(original)
	if err != nil {
		t.Fatalf("BaselineToJSON: %v", err)
	}
	got, err := BaselineFromJSON(data)
	if err != nil {
		t.Fatalf("BaselineFromJSON: %v", err)
	}
	if !reflect.DeepEqual(got, original) {
		t.Fatalf("round-trip 不等价:\noriginal=%#v\ngot=%#v", original, got)
	}
}

// TestBaselineToJSONInvalidFloat 验证无法编码的浮点分数返回错误。
func TestBaselineToJSONInvalidFloat(t *testing.T) {
	if _, err := BaselineToJSON(AcceptedBaseline{Score: math.NaN()}); err == nil {
		t.Fatal("NaN 分数应编码失败")
	}
}

// TestBaselineFromJSONInvalid 验证空输入、非对象与畸形 JSON 都返回可匹配的哨兵错误。
func TestBaselineFromJSONInvalid(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{"空输入", ""},
		{"空白输入", "   \n\t"},
		{"数组", "[]"},
		{"null", "null"},
		{"标量", `"hello"`},
		{"畸形", `{"fingerprint":`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BaselineFromJSON([]byte(tc.data))
			if err == nil {
				t.Fatalf("期望错误, got nil")
			}
			if !errors.Is(err, ErrInvalidBaselineJSON) {
				t.Fatalf("错误应包裹 ErrInvalidBaselineJSON, got %v", err)
			}
		})
	}
}
