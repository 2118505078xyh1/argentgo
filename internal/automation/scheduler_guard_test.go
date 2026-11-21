package automation

import (
	"bytes"
	"math/big"
	"reflect"
	"strings"
	"testing"
)

// 本文件对 AssessSchedule / Summary 做属性与差分守护。边界常量用显式位运算
// 推导，避免依赖具体 Go 版本对 math.MaxInt64 的导出位置，便于锁定 int64 极值。
const (
	guardMaxI = int64(^uint64(0) >> 1)
	guardMinI = -guardMaxI - 1
)

// guardReferenceStale 用 math/big 精确计算 nowMS-lastRunMS 并与阈值比较，
// 作为独立于生产实现“相减溢出即过期”技巧的差分预言机。staleAfterMS<=0 时
// 与生产一致地回退到默认阈值。
func guardReferenceStale(nowMS, lastRunMS, staleAfterMS int64) bool {
	if staleAfterMS <= 0 {
		staleAfterMS = defaultStaleAfterMS
	}
	var n, l, s, delta big.Int
	n.SetInt64(nowMS)
	l.SetInt64(lastRunMS)
	s.SetInt64(staleAfterMS)
	delta.Sub(&n, &l)
	return delta.Cmp(&s) > 0
}

// guardReferenceVerdict 独立推导期望 Verdict，用于与 AssessSchedule 差分比对。
func guardReferenceVerdict(h ScheduleHealth, nowMS, staleAfterMS int64) string {
	stale := guardReferenceStale(nowMS, h.LastRunMS, staleAfterMS)
	switch {
	case h.ConsecutiveFail >= stuckFailThreshold || (stale && h.Missed > 0):
		return VerdictStuck
	case stale || h.Missed > 0 || h.ConsecutiveFail > 0:
		return VerdictLate
	default:
		return VerdictHealthy
	}
}

// guardAllowedVerdicts 是锁定的稳定判定集合；AssessSchedule 只能返回其中之一。
var guardAllowedVerdicts = map[string]bool{
	VerdictHealthy: true,
	VerdictLate:    true,
	VerdictStuck:   true,
}

// guardAssertOnlyVerdictChanged 校验纯函数契约：入参未被修改，且返回副本中
// 除 Verdict 外所有字段（含 CJK Expression）按值原样保留。
func guardAssertOnlyVerdictChanged(t *testing.T, original, input, got ScheduleHealth) {
	t.Helper()
	if !reflect.DeepEqual(input, original) {
		t.Fatalf("入参被修改: got %+v, want %+v", input, original)
	}
	reset := got
	reset.Verdict = original.Verdict
	if !reflect.DeepEqual(reset, original) {
		t.Fatalf("除 Verdict 外字段被改写: got %+v, want %+v", reset, original)
	}
	if !bytes.Equal([]byte(got.Expression), []byte(original.Expression)) {
		t.Fatalf("Expression 未按字节保留: got %q, want %q", got.Expression, original.Expression)
	}
}

// TestGuardAssessScheduleInt64OverflowBoundaries 锁定 MaxInt64/MinInt64 附近的
// 溢出与临界行为：真实超大间隔必须判过期，恰在阈值上不过期，回退到默认阈值
// 的极值边界同样稳定。
func TestGuardAssessScheduleInt64OverflowBoundaries(t *testing.T) {
	cases := []struct {
		name       string
		h          ScheduleHealth
		nowMS      int64
		staleAfter int64
		want       string
	}{
		{"min to max overflow is stale", ScheduleHealth{LastRunMS: guardMinI}, guardMaxI, 1000, VerdictLate},
		{"minus one to max overflow is stale", ScheduleHealth{LastRunMS: -1}, guardMaxI, 1000, VerdictLate},
		{"zero to max exact delta is stale", ScheduleHealth{LastRunMS: 0}, guardMaxI, 1000, VerdictLate},
		{"minus one to max minus one exact delta is stale", ScheduleHealth{LastRunMS: -1}, guardMaxI - 1, 1000, VerdictLate},
		{"delta at max threshold is not stale", ScheduleHealth{LastRunMS: 0}, guardMaxI, guardMaxI, VerdictHealthy},
		{"min plus default threshold is not stale", ScheduleHealth{LastRunMS: guardMinI}, guardMinI + defaultStaleAfterMS, 0, VerdictHealthy},
		{"min plus default threshold plus one is stale", ScheduleHealth{LastRunMS: guardMinI}, guardMinI + defaultStaleAfterMS + 1, 0, VerdictLate},
		{"near max one past threshold is stale", ScheduleHealth{LastRunMS: guardMaxI - 1001}, guardMaxI, 1000, VerdictLate},
		{"near max exactly at threshold is not stale", ScheduleHealth{LastRunMS: guardMaxI - 1000}, guardMaxI, 1000, VerdictHealthy},
		{"one millisecond past min is fresh", ScheduleHealth{LastRunMS: guardMinI}, guardMinI + 1, 1000, VerdictHealthy},
		{"overflow with missed is stuck", ScheduleHealth{LastRunMS: guardMinI, Missed: 1}, guardMaxI, 1000, VerdictStuck},
		{"overflow with consecutive failure is stuck", ScheduleHealth{LastRunMS: guardMinI, ConsecutiveFail: stuckFailThreshold}, guardMaxI, 1000, VerdictStuck},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := tc.h
			got := AssessSchedule(tc.h, tc.nowMS, tc.staleAfter)
			if got.Verdict != tc.want {
				t.Fatalf("Verdict = %q, want %q", got.Verdict, tc.want)
			}
			guardAssertOnlyVerdictChanged(t, tc.h, input, got)
		})
	}
}

// TestGuardAssessScheduleClockRollback 验证时钟回拨（含极端回拨）永不触发过期，
// 但已存在的错过与连续失败仍按优先级给出正确判定。
func TestGuardAssessScheduleClockRollback(t *testing.T) {
	cases := []struct {
		name       string
		h          ScheduleHealth
		nowMS      int64
		staleAfter int64
		want       string
	}{
		{"moderate rollback", ScheduleHealth{LastRunMS: 1000}, 500, 1000, VerdictHealthy},
		{"equal timestamps", ScheduleHealth{LastRunMS: 1000}, 1000, 1000, VerdictHealthy},
		{"one millisecond rollback", ScheduleHealth{LastRunMS: 1000}, 999, 1000, VerdictHealthy},
		{"extreme rollback min now max last", ScheduleHealth{LastRunMS: guardMaxI}, guardMinI, 1000, VerdictHealthy},
		{"rollback near min", ScheduleHealth{LastRunMS: guardMinI + 1}, guardMinI, 1000, VerdictHealthy},
		{"rollback with missed is late", ScheduleHealth{LastRunMS: 1000, Missed: 1}, 500, 1000, VerdictLate},
		{"rollback with single failure is late", ScheduleHealth{LastRunMS: 1000, ConsecutiveFail: 1}, 500, 1000, VerdictLate},
		{"rollback with stuck failure is stuck", ScheduleHealth{LastRunMS: 1000, ConsecutiveFail: stuckFailThreshold}, 500, 1000, VerdictStuck},
		{"rollback with stuck failure and missed is stuck", ScheduleHealth{LastRunMS: 1000, Missed: 2, ConsecutiveFail: stuckFailThreshold}, 500, 1000, VerdictStuck},
		{"rollback with missed and sub-threshold failure is late", ScheduleHealth{LastRunMS: 1000, Missed: 1, ConsecutiveFail: stuckFailThreshold - 1}, 500, 1000, VerdictLate},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := tc.h
			got := AssessSchedule(tc.h, tc.nowMS, tc.staleAfter)
			if got.Verdict != tc.want {
				t.Fatalf("Verdict = %q, want %q", got.Verdict, tc.want)
			}
			guardAssertOnlyVerdictChanged(t, tc.h, input, got)
		})
	}
}

// TestGuardAssessScheduleVerdictPriority 用覆盖 (是否过期, 是否有错过,
// 连续失败档位) 组合的表锁定判定优先级：stuck > late > healthy，
// 且“过期+错过”与“连续失败达阈值”是仅有的两条 stuck 路径。
func TestGuardAssessScheduleVerdictPriority(t *testing.T) {
	const (
		fresh = int64(1500)
		stale = int64(2001)
	)
	cases := []struct {
		name       string
		h          ScheduleHealth
		nowMS      int64
		staleAfter int64
		want       string
	}{
		{"fresh clean", ScheduleHealth{LastRunMS: 1000}, fresh, 1000, VerdictHealthy},
		{"fresh missed", ScheduleHealth{LastRunMS: 1000, Missed: 1}, fresh, 1000, VerdictLate},
		{"fresh failed", ScheduleHealth{LastRunMS: 1000, ConsecutiveFail: 1}, fresh, 1000, VerdictLate},
		{"fresh missed and failed", ScheduleHealth{LastRunMS: 1000, Missed: 1, ConsecutiveFail: 1}, fresh, 1000, VerdictLate},
		{"stale clean", ScheduleHealth{LastRunMS: 1000}, stale, 1000, VerdictLate},
		{"stale missed is stuck", ScheduleHealth{LastRunMS: 1000, Missed: 1}, stale, 1000, VerdictStuck},
		{"stale failed is late", ScheduleHealth{LastRunMS: 1000, ConsecutiveFail: 1}, stale, 1000, VerdictLate},
		{"stale missed and failed is stuck", ScheduleHealth{LastRunMS: 1000, Missed: 1, ConsecutiveFail: 1}, stale, 1000, VerdictStuck},
		{"fresh at stuck threshold is stuck", ScheduleHealth{LastRunMS: 1000, ConsecutiveFail: stuckFailThreshold}, fresh, 1000, VerdictStuck},
		{"fresh many missed sub-threshold fail stays late", ScheduleHealth{LastRunMS: 1000, Missed: 99, ConsecutiveFail: stuckFailThreshold - 1}, fresh, 1000, VerdictLate},
		{"stale at stuck threshold is stuck", ScheduleHealth{LastRunMS: 1000, ConsecutiveFail: stuckFailThreshold}, stale, 1000, VerdictStuck},
		{"stale missed and stuck failure is stuck", ScheduleHealth{LastRunMS: 1000, Missed: 5, ConsecutiveFail: stuckFailThreshold + 5}, stale, 1000, VerdictStuck},
		{"fresh above stuck threshold is stuck", ScheduleHealth{LastRunMS: 1000, ConsecutiveFail: stuckFailThreshold + 5}, fresh, 1000, VerdictStuck},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := tc.h
			got := AssessSchedule(tc.h, tc.nowMS, tc.staleAfter)
			if got.Verdict != tc.want {
				t.Fatalf("Verdict = %q, want %q", got.Verdict, tc.want)
			}
			guardAssertOnlyVerdictChanged(t, tc.h, input, got)
		})
	}
}

// TestGuardAssessScheduleCJKExpressionBytePreserved 验证多语种 CJK、
// 全角标点、emoji 与 4 字节字符表达式在返回副本中逐字节原样保留，
// 入参同样不被改写。
func TestGuardAssessScheduleCJKExpressionBytePreserved(t *testing.T) {
	exprs := []string{
		"*/5 * * * * 中文表达式",
		"0 3 * * * 每日巡检",
		"0 0 * * 一，二，三，四，五",
		"0 0 * * * 日本語のスケジュール",
		"0 0 * * * 한국어 표현식",
		"0 0 * * * 任务-甲（测试）",
		"0 0 * * * 早晨☀️",
		"0 0 * * * 换行\t制表",
		"0 0 * * * 𝄞 高音谱号",
	}
	for i, expr := range exprs {
		h := ScheduleHealth{
			ID: "任务-42", Expression: expr,
			LastRunMS: 1000, Missed: 2, ConsecutiveFail: 1, Verdict: "OLD",
		}
		input := h
		got := AssessSchedule(h, 9999, 1000)
		if !bytes.Equal([]byte(expr), []byte(got.Expression)) {
			t.Fatalf("用例 %d: Expression 字节被改写: got %q (% x), want %q (% x)",
				i, got.Expression, []byte(got.Expression), expr, []byte(expr))
		}
		if containsCJK(expr) && !containsCJK(got.Expression) {
			t.Fatalf("用例 %d: CJK 汉字丢失: got %q", i, got.Expression)
		}
		guardAssertOnlyVerdictChanged(t, h, input, got)
	}
}

// TestGuardVerdictLabelsLocked 锁定 Verdict 常量的字面值、三个中文标签及其
// 互异性、未知取值原样保留，以及 AssessSchedule 输出只能落在锁定集合内。
func TestGuardVerdictLabelsLocked(t *testing.T) {
	constPairs := []struct{ verdict, want string }{
		{VerdictHealthy, "healthy"},
		{VerdictLate, "late"},
		{VerdictStuck, "stuck"},
	}
	for _, p := range constPairs {
		if p.verdict != p.want {
			t.Fatalf("Verdict 常量被改写: got %q, want %q", p.verdict, p.want)
		}
	}

	labels := map[string]string{
		VerdictHealthy: "健康",
		VerdictLate:    "滞后",
		VerdictStuck:   "卡死",
	}
	seen := map[string]bool{}
	for verdict, want := range labels {
		got := verdictLabel(verdict)
		if got != want {
			t.Fatalf("verdictLabel(%q) = %q, want %q", verdict, got, want)
		}
		if again := verdictLabel(verdict); again != got {
			t.Fatalf("verdictLabel(%q) 不稳定: %q vs %q", verdict, got, again)
		}
		seen[got] = true
	}
	if len(seen) != 3 {
		t.Fatalf("标签集合应恰有 3 个互异取值, got %d: %v", len(seen), seen)
	}

	for _, unknown := range []string{"CUSTOM", "", "healthy "} {
		if got := verdictLabel(unknown); got != unknown {
			t.Fatalf("未知 Verdict %q 应原样保留, got %q", unknown, got)
		}
	}

	for _, tc := range []struct {
		h          ScheduleHealth
		now, stale int64
	}{
		{ScheduleHealth{LastRunMS: 1000}, 1500, 1000},
		{ScheduleHealth{LastRunMS: 1000, Missed: 1}, 2001, 1000},
		{ScheduleHealth{LastRunMS: 1000, ConsecutiveFail: stuckFailThreshold}, 500, 1000},
	} {
		v := AssessSchedule(tc.h, tc.now, tc.stale).Verdict
		if !guardAllowedVerdicts[v] {
			t.Fatalf("AssessSchedule 返回未锁定 Verdict %q", v)
		}
	}
}

// TestGuardSummaryExcludesRawVerdictToken 锁定 Summary 只暴露稳定中文标签，
// 不泄漏内部英文 Verdict 枚举 token 或 Go 结构体字段名；需要机器判定的
// 调用方应读取 Verdict 字段，而非解析摘要文本。
func TestGuardSummaryExcludesRawVerdictToken(t *testing.T) {
	labelByVerdict := map[string]string{
		VerdictHealthy: "健康",
		VerdictLate:    "滞后",
		VerdictStuck:   "卡死",
	}
	rawTokens := []string{VerdictHealthy, VerdictLate, VerdictStuck}
	fieldNames := []string{"ID", "Expression", "LastRunMS", "Missed", "ConsecutiveFail", "Verdict"}

	for verdict, label := range labelByVerdict {
		h := ScheduleHealth{
			ID: "job-1", Expression: "*/5 * * * *",
			LastRunMS: 123, Missed: 2, ConsecutiveFail: 1, Verdict: verdict,
		}
		summary := h.Summary()
		if !strings.Contains(summary, label) {
			t.Fatalf("Verdict %q: 摘要 %q 不含中文标签 %q", verdict, summary, label)
		}
		for _, raw := range rawTokens {
			if strings.Contains(summary, raw) {
				t.Fatalf("Verdict %q: 摘要 %q 泄漏原始判定 token %q", verdict, summary, raw)
			}
		}
		for _, name := range fieldNames {
			if strings.Contains(summary, name) {
				t.Fatalf("Verdict %q: 摘要 %q 泄漏原始字段名 %q", verdict, summary, name)
			}
		}
		for _, want := range []string{"123 毫秒", "错过 2 次", "连续失败 1 次"} {
			if !strings.Contains(summary, want) {
				t.Fatalf("Verdict %q: 摘要 %q 不含字段 %q", verdict, summary, want)
			}
		}
		if strings.Contains(summary, "%!") {
			t.Fatalf("摘要含格式化残留: %q", summary)
		}
		if !containsCJK(summary) {
			t.Fatalf("摘要 %q 不是中文", summary)
		}
		if again := h.Summary(); again != summary {
			t.Fatalf("摘要不稳定: %q vs %q", summary, again)
		}
	}

	// 未知 Verdict 沿用既有行为原样保留，不影响上述锁定。
	unknown := ScheduleHealth{ID: "job-1", Expression: "*/5 * * * *", Verdict: "CUSTOM_ENUM"}
	if got := unknown.Summary(); !strings.Contains(got, "CUSTOM_ENUM") {
		t.Fatalf("未知 Verdict 未原样保留: %q", got)
	}
}

// TestGuardAssessScheduleDoesNotMutateInput 以含 CJK 的样本验证纯函数契约：
// 入参不被修改，重复调用一致，且修改返回副本不会污染入参或后续调用。
func TestGuardAssessScheduleDoesNotMutateInput(t *testing.T) {
	original := ScheduleHealth{
		ID: "任务-42", Expression: "*/5 * * * * 中文",
		LastRunMS: 1000, Missed: 2, ConsecutiveFail: 1, Verdict: "OLD",
	}
	input := original
	first := AssessSchedule(input, 9999, 1000)
	guardAssertOnlyVerdictChanged(t, original, input, first)
	second := AssessSchedule(input, 9999, 1000)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("重复调用不一致: %+v vs %+v", first, second)
	}

	first.ID = "篡改"
	first.Expression = "篡改"
	first.LastRunMS = -1
	first.Missed = 99
	first.ConsecutiveFail = 99
	first.Verdict = "篡改"
	third := AssessSchedule(input, 9999, 1000)
	if !reflect.DeepEqual(third, second) {
		t.Fatalf("修改返回值影响后续调用: got %+v, want %+v", third, second)
	}
	guardAssertOnlyVerdictChanged(t, original, input, third)
}

// guardCase 是一条确定性的守护样本。
type guardCase struct {
	h          ScheduleHealth
	nowMS      int64
	staleAfter int64
}

// guardDeterministicCases 生成恰好 n 条可复现样本：前若干条为手工挑选的极值，
// 其余由固定种子的 LCG 派生，覆盖极端 int64、阈值边界与 CJK 表达式。
func guardDeterministicCases(n int) []guardCase {
	edges := []guardCase{
		{ScheduleHealth{LastRunMS: guardMinI}, guardMaxI, 1000},
		{ScheduleHealth{LastRunMS: -1}, guardMaxI, 1000},
		{ScheduleHealth{LastRunMS: guardMaxI}, guardMinI, 1000},
		{ScheduleHealth{LastRunMS: guardMinI, Missed: 1}, guardMaxI, 0},
		{ScheduleHealth{LastRunMS: guardMaxI, ConsecutiveFail: stuckFailThreshold}, guardMinI, -1},
		{ScheduleHealth{LastRunMS: 0, Expression: "中文表达式"}, guardMaxI, guardMaxI},
		{ScheduleHealth{LastRunMS: 1000, Missed: 1}, 2001, 1000},
		{ScheduleHealth{LastRunMS: 1000, ConsecutiveFail: 1}, 999, 1000},
	}
	cases := make([]guardCase, 0, n)
	cases = append(cases, edges...)
	seed := uint64(0x9E3779B97F4A7C15)
	next := func() uint64 {
		seed = seed*6364136223846793005 + 1442695040888963407
		return seed
	}
	staleChoices := []int64{0, -1, 1, 1000, defaultStaleAfterMS, guardMaxI}
	exprChoices := []string{"*/5 * * * *", "0 3 * * * 中文", "0 0 * * 一，二，三", ""}
	for len(cases) < n {
		cases = append(cases, guardCase{
			h: ScheduleHealth{
				ID:              "任务",
				Expression:      exprChoices[next()%uint64(len(exprChoices))],
				LastRunMS:       int64(next()),
				Missed:          int(next() % 5),
				ConsecutiveFail: int(next() % (stuckFailThreshold + 3)),
				Verdict:         "OLD",
			},
			nowMS:      int64(next()),
			staleAfter: staleChoices[next()%uint64(len(staleChoices))],
		})
	}
	return cases[:n]
}

// TestGuardAssessScheduleDeterministicDifferential100 以 100 条确定性样本做
// 属性与差分守护：每条样本重复调用结果一致，且与基于 math/big 的独立预言机
// 判定相同；同时校验输出落在锁定集合、入参不被修改、非 Verdict 字段原样保留。
func TestGuardAssessScheduleDeterministicDifferential100(t *testing.T) {
	cases := guardDeterministicCases(100)
	if len(cases) != 100 {
		t.Fatalf("样本数 = %d, want 100", len(cases))
	}
	for i, tc := range cases {
		input := tc.h
		got := AssessSchedule(tc.h, tc.nowMS, tc.staleAfter)
		for rep := 0; rep < 2; rep++ {
			again := AssessSchedule(tc.h, tc.nowMS, tc.staleAfter)
			if !reflect.DeepEqual(again, got) {
				t.Fatalf("样本 %d 第 %d 次调用不一致: %+v vs %+v", i, rep, again, got)
			}
		}
		want := guardReferenceVerdict(tc.h, tc.nowMS, tc.staleAfter)
		if got.Verdict != want {
			t.Fatalf("样本 %d: Verdict = %q, want (差分) %q; h=%+v now=%d stale=%d",
				i, got.Verdict, want, tc.h, tc.nowMS, tc.staleAfter)
		}
		if !guardAllowedVerdicts[got.Verdict] {
			t.Fatalf("样本 %d: 未锁定 Verdict %q", i, got.Verdict)
		}
		guardAssertOnlyVerdictChanged(t, tc.h, input, got)
	}
}
