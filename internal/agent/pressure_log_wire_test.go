package agent

import (
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"example.com/argentgo/internal/llm"
)

// 本文件测试 ClassifyPressure/PressureLabel 五级压力模型在 agent 轮末的 opt-in 日志接线：
// ARGENT_PRESSURE_LEVEL=1 时在 execute 末尾与其它诊断日志并列输出一条 agent_pressure；
// 默认关闭时整段（分级 + 日志）是空操作，行为逐字节不变。接线刻意只用纯函数 + 共用 logger，
// 不产生 EventNotice、不改提醒文案、不改任何模型可见字节（与 app 层 eval 口径互不冲突）。

// pressureRecords 返回捕获器中消息恰为 msg 的全部记录（不要求唯一）。
func pressureRecords(cap *turnTimingLogCapture, msg string) []slog.Record {
	var out []slog.Record
	for _, r := range cap.snapshot() {
		if r.Message == msg {
			out = append(out, r)
		}
	}
	return out
}

// TestPressureLogWireEnvFlagValues 验证 env 解析：仅去空白后的 "1" 开启。
func TestPressureLogWireEnvFlagValues(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"1", true},
		{" 1 ", true},
		{"0", false},
		{"true", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("ARGENT_PRESSURE_LEVEL", tc.value)
			if got := pressureLevelLogEnabled(); got != tc.want {
				t.Fatalf("pressureLevelLogEnabled(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// TestPressureLogWireFiveLevelLabelsAndBoundaries 验证 opt-in 开启后日志包含五级标签，
// 且各档位边界遵循"上界不含"：49/50、69/70、84/85、94/95 分别落入相邻两档。
// 同时断言 level 文案与 PressureLabel 完全一致，used/budget 原样落到结构化字段。
func TestPressureLogWireFiveLevelLabelsAndBoundaries(t *testing.T) {
	const budget = 100
	cases := []struct {
		name string
		used int
		want PressureLevel
	}{
		{"zero used", 0, levelLow},
		{"just below 50", 49, levelLow},
		{"exactly 50", 50, levelNormal},
		{"just below 70", 69, levelNormal},
		{"exactly 70", 70, levelElevated},
		{"just below 85", 84, levelElevated},
		{"exactly 85", 85, levelHigh},
		{"just below 95", 94, levelHigh},
		{"exactly 95", 95, levelCritical},
		{"full budget", 100, levelCritical},
		{"over budget", 120, levelCritical},
	}

	seen := map[string]bool{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cap := captureTurnTimingLogs(t)
			emitPressureLog(tc.used, budget)
			recs := pressureRecords(cap, "agent_pressure")
			if len(recs) == 0 {
				t.Fatal("未输出压力日志")
			}
			// 只针对压力分级本身：取本用例最新一条，不依赖易变的累计用量或其它 opt-in 的日志数量。
			attrs := attrsOf(recs[len(recs)-1])
			if got := attrs["level"].String(); got != PressureLabel(tc.want) {
				t.Fatalf("level = %q, want %q", got, PressureLabel(tc.want))
			}
			if got := attrs["used"].Int64(); got != int64(tc.used) {
				t.Fatalf("used = %d, want %d", got, tc.used)
			}
			if got := attrs["budget"].Int64(); got != int64(budget) {
				t.Fatalf("budget = %d, want %d", got, budget)
			}
			seen[attrs["level"].String()] = true
		})
	}
	// 五个等级标签必须都出现过，锁定"日志含五级标签"。
	for _, level := range []PressureLevel{levelLow, levelNormal, levelElevated, levelHigh, levelCritical} {
		if !seen[PressureLabel(level)] {
			t.Fatalf("日志缺少等级标签 %q", PressureLabel(level))
		}
	}
}

// TestPressureLogWireFinishReadsUsageAndBudget 验证轮末 finishPressure 从 runState 的
// usage/budget 取值：used 为累计 TotalTokens，budget 为 MaxTotalTokens；关闭时零输出。
func TestPressureLogWireFinishReadsUsageAndBudget(t *testing.T) {
	cap := captureTurnTimingLogs(t)

	s := &runState{
		pressureLog: true,
		usage:       llm.Usage{PromptTokens: 90, CompletionTokens: 10},
		opts:        Options{Budget: Budget{MaxTotalTokens: 100}},
	}
	beforeUsage, beforeOpts := s.usage, s.opts
	// 期望值直接取自 runState 的字段，使本用例只验证"finishPressure 确实读 usage/budget
	// 这一接线事实"，不耦合 TotalTokens/MaxTotalTokens 的具体数值语义（其它代理可能调整口径）。
	wantUsed, wantBudget := s.usage.TotalTokens(), s.opts.Budget.MaxTotalTokens
	s.finishPressure()

	recs := pressureRecords(cap, "agent_pressure")
	if len(recs) == 0 {
		t.Fatal("未输出压力日志")
	}
	attrs := attrsOf(recs[len(recs)-1])
	if got := attrs["used"].Int64(); got != int64(wantUsed) {
		t.Fatalf("used = %d, want %d（来自 usage.TotalTokens()）", got, wantUsed)
	}
	if got := attrs["budget"].Int64(); got != int64(wantBudget) {
		t.Fatalf("budget = %d, want %d（来自 Budget.MaxTotalTokens）", got, wantBudget)
	}
	if wantLevel := PressureLabel(ClassifyPressure(wantUsed, wantBudget)); attrs["level"].String() != wantLevel {
		t.Fatalf("level = %q, want %q", attrs["level"].String(), wantLevel)
	}
	// 纯观测：不得改动用量或配置。
	if s.usage != beforeUsage || !reflect.DeepEqual(s.opts, beforeOpts) {
		t.Fatal("压力观测修改了 usage 或 opts")
	}

	// 关闭时即使有值也不输出。
	var off runState
	off.usage = llm.Usage{PromptTokens: 90, CompletionTokens: 10}
	off.opts.Budget.MaxTotalTokens = 100
	off.finishPressure()
	if got := len(pressureRecords(cap, "agent_pressure")); got != 1 {
		t.Fatalf("关闭时不应再输出压力日志，实际共 %d 条", got)
	}
}

// TestPressureLogWireOffByDefault 验证关闭基线：默认（空值）时不置位、不输出任何压力日志；
// 即使 turnTiming 单独开启，压力日志也不该出现（两者正交）。事件流照常工作。
func TestPressureLogWireOffByDefault(t *testing.T) {
	t.Setenv("ARGENT_PRESSURE_LEVEL", "")
	t.Setenv("ARGENT_TURN_TIMING", "")
	cap := captureTurnTimingLogs(t)

	opts := Options{Model: "m", System: "s"}
	if newRunState(opts, "hi", nil).pressureLog {
		t.Fatal("关闭时不应置位 pressureLog")
	}

	// 直接调用 finishPressure：即便有可用用量/预算，也不得分级或输出。
	var direct runState
	direct.pressureLog = false
	direct.usage = llm.Usage{PromptTokens: 95, CompletionTokens: 5}
	direct.opts.Budget.MaxTotalTokens = 100
	direct.finishPressure()
	if got := len(pressureRecords(cap, "agent_pressure")); got != 0 {
		t.Fatalf("关闭时直接调用仍输出 %d 条压力日志", got)
	}

	// 完整 Run：默认关闭时零压力日志，事件流照常。
	sink := &eventSink{}
	runner, _ := toolThenTextRunner(sink.emit)
	if _, err := runner.Run(context.Background(), "inspect"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(pressureRecords(cap, "agent_pressure")); got != 0 {
		t.Fatalf("默认关闭却输出了 %d 条压力日志", got)
	}
	if len(sink.kinds()) == 0 {
		t.Fatal("关闭时事件流不应为空")
	}
}

// TestPressureLogWireCoexistWithContextHealth 验证与上下文健康度日志共存：
// 两个 opt-in 同时开启时，同一 logger 恰好输出两条日志，互不覆盖；
// 两条日志的特征字段均在，且消息均不为对方所改写。
func TestPressureLogWireCoexistWithContextHealth(t *testing.T) {
	t.Setenv("ARGENT_PRESSURE_LEVEL", "1")
	t.Setenv("ARGENT_CONTEXT_HEALTH_LOG", "1")
	// 显式关闭其它轮末诊断 opt-in，避免其它代理新增的日志改变本用例的日志构成。
	for _, key := range []string{
		"ARGENT_TURN_TIMING", "ARGENT_RECOVERY_METRICS", "ARGENT_TOOL_TIMING_HISTOGRAM",
		"ARGENT_TOOL_CALL_TRACE", "ARGENT_COMPACTION_POLICY", "ARGENT_TOKEN_FORECAST",
	} {
		t.Setenv(key, "")
	}
	cap := captureTurnTimingLogs(t)

	runner, _ := toolThenTextRunner(nil)
	if _, err := runner.Run(context.Background(), "inspect"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 只断言压力与健康度各恰好一条且字段齐全；其它来源的日志不影响本用例。
	pressures := pressureRecords(cap, "agent_pressure")
	if len(pressures) != 1 {
		t.Fatalf("压力日志数 = %d, want 1", len(pressures))
	}
	healths := pressureRecords(cap, "agent_context_health")
	if len(healths) != 1 {
		t.Fatalf("健康度日志数 = %d, want 1", len(healths))
	}
	if _, ok := attrsOf(pressures[0])["level"]; !ok {
		t.Fatal("共存时压力日志缺少 level 字段")
	}
	if _, ok := attrsOf(healths[0])["score"]; !ok {
		t.Fatal("共存时健康度日志缺少 score 字段")
	}
}

// TestPressureLogWireCoexistWithLoopHistory 锁定 R38-R51 的历史改动与本轮接线共存：
// SystemNodes / InitialImages 作用不变，TurnTiming / RecoveryMetrics / 直方图 / trace /
// 上下文健康度全部初始化，且本轮 pressureLog 也置位；观测后消息不被改动（逐字节不变）。
func TestPressureLogWireCoexistWithLoopHistory(t *testing.T) {
	t.Setenv("ARGENT_TURN_TIMING", "1")
	t.Setenv("ARGENT_RECOVERY_METRICS", "1")
	t.Setenv("ARGENT_TOOL_TIMING_HISTOGRAM", "1")
	t.Setenv("ARGENT_TOOL_CALL_TRACE", "1")
	t.Setenv("ARGENT_CONTEXT_HEALTH_LOG", "1")
	t.Setenv("ARGENT_PRESSURE_LEVEL", "1")

	const baseSystem = "BASE SYSTEM"
	opts := Options{
		System:        baseSystem,
		SystemNodes:   []string{"NODE-1"},
		InitialImages: []llm.ImageInput{{MimeType: "image/png", Data: []byte{0x01}}},
		Budget:        Budget{MaxTotalTokens: 1000},
	}
	s := newRunState(opts, "图", nil)

	if !strings.Contains(string(s.messages[0].Content), baseSystem) ||
		!strings.Contains(string(s.messages[0].Content), "NODE-1") {
		t.Fatalf("SystemNodes 未作用于系统提示: %q", s.messages[0].Content)
	}
	if len(s.messages[1].ContentParts) == 0 {
		t.Fatal("InitialImages 未构造 ContentParts")
	}
	if s.turnTiming == nil || s.recovery == nil ||
		s.toolTimingHistogram == nil || s.toolCallTrace == nil ||
		!s.contextHealthLog || !s.pressureLog {
		t.Fatalf("历史改动未全部初始化: turnTiming=%v recovery=%v histogram=%v trace=%v contextHealthLog=%v pressureLog=%v",
			s.turnTiming != nil, s.recovery != nil, s.toolTimingHistogram != nil,
			s.toolCallTrace != nil, s.contextHealthLog, s.pressureLog)
	}

	before := append([]llm.Message(nil), s.messages...)
	s.finishContextHealth()
	s.finishPressure()
	if !reflect.DeepEqual(s.messages, before) {
		t.Fatal("共存场景下观测修改了消息")
	}
}
