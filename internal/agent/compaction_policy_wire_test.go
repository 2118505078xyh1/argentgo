package agent

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// 本文件测试压缩策略（SelectCompactionAction）在轮次中压缩触发点的 opt-in 观测接线：
// ARGENT_COMPACTION_POLICY=1 时在 maybeCompact 附近落一条决策日志；默认关闭时整段
// （构造输入 + 纯函数计算 + 日志）是空操作，既有阈值路径与事件流逐字节不变。
// 接线刻意纯观测：不改压缩阈值、裁剪、压缩器调用或任何模型输入。

// compactionPolicyLogCapture 与 turnTimingLogCapture 同构，捕获压缩策略日志。
type compactionPolicyLogCapture struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (c *compactionPolicyLogCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *compactionPolicyLogCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	c.recs = append(c.recs, r.Clone())
	c.mu.Unlock()
	return nil
}

func (c *compactionPolicyLogCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *compactionPolicyLogCapture) WithGroup(string) slog.Handler      { return c }

func (c *compactionPolicyLogCapture) snapshot() []slog.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]slog.Record(nil), c.recs...)
}

// captureCompactionPolicyLogs 替换日志输出端并在测试结束时恢复。
func captureCompactionPolicyLogs(t *testing.T) *compactionPolicyLogCapture {
	t.Helper()
	cap := &compactionPolicyLogCapture{}
	prev := compactionPolicyLogger
	compactionPolicyLogger = func() *slog.Logger { return slog.New(cap) }
	t.Cleanup(func() { compactionPolicyLogger = prev })
	return cap
}

func policyRecords(cap *compactionPolicyLogCapture) []slog.Record {
	var out []slog.Record
	for _, r := range cap.snapshot() {
		if r.Message == "compaction policy decision" {
			out = append(out, r)
		}
	}
	return out
}

// TestCompactionPolicyEnvFlagValues 验证 env 解析：仅去空白后的 "1" 开启。
func TestCompactionPolicyEnvFlagValues(t *testing.T) {
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
			t.Setenv("ARGENT_COMPACTION_POLICY", tc.value)
			if got := compactionPolicyEnabled(); got != tc.want {
				t.Fatalf("compactionPolicyEnabled(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// TestCompactionPolicyInputTiers 验证四档决策都能由现有信号喂出，并核对错误多→保据升级规则。
// ToolResults 混入 CJK 角色与多字节正文，确保计数对非 ASCII 安全。
func TestCompactionPolicyInputTiers(t *testing.T) {
	toolMsgs := []llm.Message{
		{Role: "tool", Content: "结果一"},
		{Role: " TOOL_RESULT ", Content: "中文输出"},
		{Role: "function", Content: "🔥 emoji"},
		{Role: "user", Content: "继续"},
		{Role: "assistant", Content: "好的"},
	}
	if got := countToolResults(toolMsgs); got != 3 {
		t.Fatalf("countToolResults = %d, want 3", got)
	}

	cases := []struct {
		name           string
		before         int
		threshold      int
		errors         int
		toolResults    int
		budgetExceeded bool
		want           CompactionAction
	}{
		{"low usage none", 10, 100, 0, 0, false, CompactionNone},
		{"budget exceeded full", 10, 100, 0, 0, true, CompactionCompactFull},
		{"very high summarize", 95, 100, 0, 0, false, CompactionSummarizeModel},
		{"many tool results trim", 80, 100, 0, 12, false, CompactionTrimLocal},
		{"errors preserve evidence", 80, 100, 5, 12, false, CompactionSummarizeModel},
		{"no budget signal", 50, 0, 5, 12, false, CompactionNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := compactionPolicyInput(tc.before, tc.threshold, tc.errors, tc.toolResults, tc.budgetExceeded)
			if got := SelectCompactionAction(in); got != tc.want {
				t.Fatalf("SelectCompactionAction(%+v) = %s, want %s", in, got, tc.want)
			}
		})
	}
}

// TestCompactionPolicyOffByDefault 验证默认关闭时零日志、逐字节基线不变：
// 不置位、observe 空操作、既有压缩事件与压缩器调用照常。
func TestCompactionPolicyOffByDefault(t *testing.T) {
	t.Setenv("ARGENT_COMPACTION_POLICY", "")
	cap := captureCompactionPolicyLogs(t)

	if newRunState(Options{Model: "m", System: "s"}, "hi", nil).compactionPolicy {
		t.Fatal("关闭时不应置位 compactionPolicy")
	}

	// 即使消息与错误数足以触发任意档位，直接调用也必须空操作。
	var direct runState
	direct.compactionPolicy = false
	direct.compactThreshold = 100
	direct.messages = []llm.Message{
		{Role: "system", Content: "规则"},
		{Role: "user", Content: "问题"},
		{Role: "tool", Content: strings.Repeat("中文结果", 40)},
	}
	direct.mistakes = NewMistakeTracker(0, nil)
	direct.mistakes.Record(MistakeKindToolExecutionFailed)
	direct.observeCompactionPolicy()
	if got := len(policyRecords(cap)); got != 0 {
		t.Fatalf("关闭时直接调用仍输出 %d 条策略日志", got)
	}

	// 完整 Run：关闭时零策略日志，压缩器照常被调用（既有阈值路径未被替换）。
	sink := &eventSink{}
	runner, compactor := toolThenTextRunner(sink.emit)
	if _, err := runner.Run(context.Background(), "inspect"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(policyRecords(cap)); got != 0 {
		t.Fatalf("默认关闭却输出了 %d 条策略日志", got)
	}
	if compactor.calls == 0 {
		t.Fatal("关闭时既有压缩器仍应被调用")
	}
	if len(sink.kinds()) == 0 {
		t.Fatal("关闭时事件流不应为空")
	}
}

// TestCompactionPolicyOnEmitsDecision 验证 opt-in 开启时日志字段齐全且动作正确。
func TestCompactionPolicyOnEmitsDecision(t *testing.T) {
	t.Setenv("ARGENT_COMPACTION_POLICY", "1")
	cap := captureCompactionPolicyLogs(t)

	s := newRunState(Options{Model: "m", System: "s"}, "hi", nil)
	if !s.compactionPolicy {
		t.Fatal("开启时应置位 compactionPolicy")
	}
	s.compactThreshold = 100
	s.messages = []llm.Message{
		{Role: "system", Content: "规则"},
		{Role: "user", Content: "继续"},
		{Role: "tool", Content: strings.Repeat("中文结果", 20)},
	}
	s.mistakes = NewMistakeTracker(0, nil)
	for i := 0; i < 5; i++ {
		s.mistakes.Record(MistakeKindToolExecutionFailed)
	}
	s.observeCompactionPolicy()

	recs := policyRecords(cap)
	if len(recs) != 1 {
		t.Fatalf("策略日志数 = %d, want 1", len(recs))
	}
	attrs := attrsOf(recs[0])
	for _, key := range []string{"action", "usage_ratio", "error_count", "tool_results", "budget_exceeded", "turn"} {
		if _, ok := attrs[key]; !ok {
			t.Fatalf("策略日志缺少字段 %q", key)
		}
	}
	if got := attrs["action"].String(); got != string(CompactionSummarizeModel) {
		t.Fatalf("action = %q, want %q", got, CompactionSummarizeModel)
	}
	if got := attrs["error_count"].Int64(); got != 5 {
		t.Fatalf("error_count = %d, want 5", got)
	}
	if got := attrs["tool_results"].Int64(); got != 1 {
		t.Fatalf("tool_results = %d, want 1", got)
	}
}

// TestCompactionPolicyCoexistsWithExistingCompaction 验证开启后既有压缩照常发生，
// 日志与压缩器调用共存，且每条日志的 action 都是稳定的四档之一。
func TestCompactionPolicyCoexistsWithExistingCompaction(t *testing.T) {
	t.Setenv("ARGENT_COMPACTION_POLICY", "1")
	cap := captureCompactionPolicyLogs(t)

	runner, compactor := toolThenTextRunner(nil)
	if _, err := runner.Run(context.Background(), "inspect"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if compactor.calls == 0 {
		t.Fatal("开启后既有压缩器仍应被调用")
	}
	recs := policyRecords(cap)
	if len(recs) == 0 {
		t.Fatal("开启后应至少有一条策略日志")
	}
	valid := map[string]bool{
		string(CompactionNone): true, string(CompactionTrimLocal): true,
		string(CompactionSummarizeModel): true, string(CompactionCompactFull): true,
	}
	for _, r := range recs {
		action := attrsOf(r)["action"].String()
		if !valid[action] {
			t.Fatalf("非法 action = %q", action)
		}
	}
}

// TestCompactionPolicyCoexistsWithPriorOptIns 验证 R38-R47 的历史 opt-in 仍全部共存，
// 新开关与它们正交：逐个开启不会破坏彼此的置位。
func TestCompactionPolicyCoexistsWithPriorOptIns(t *testing.T) {
	t.Setenv("ARGENT_TURN_TIMING", "1")
	t.Setenv("ARGENT_RECOVERY_METRICS", "1")
	t.Setenv("ARGENT_TOOL_TIMING_HISTOGRAM", "1")
	t.Setenv("ARGENT_TOOL_CALL_TRACE", "1")
	t.Setenv("ARGENT_CONTEXT_HEALTH_LOG", "1")
	t.Setenv("ARGENT_COMPACTION_POLICY", "1")

	s := newRunState(Options{Model: "m", System: "s"}, "hi", nil)
	if s.turnTiming == nil || s.recovery == nil || s.toolTimingHistogram == nil ||
		s.toolCallTrace == nil || !s.contextHealthLog || !s.compactionPolicy {
		t.Fatalf("历史 opt-in 未全部共存: turnTiming=%v recovery=%v histogram=%v trace=%v contextHealthLog=%v compactionPolicy=%v",
			s.turnTiming != nil, s.recovery != nil, s.toolTimingHistogram != nil,
			s.toolCallTrace != nil, s.contextHealthLog, s.compactionPolicy)
	}
}
