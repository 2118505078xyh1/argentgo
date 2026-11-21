package agent

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// turnTimingLogCapture 是一个进程内的 slog.Handler：把收到的记录克隆后暂存在内存，
// 供测试断言结构化日志的字段。slog.Handler 契约要求 Handle 可并发调用，故加锁。
type turnTimingLogCapture struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (c *turnTimingLogCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *turnTimingLogCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	c.recs = append(c.recs, r.Clone())
	c.mu.Unlock()
	return nil
}

// 本测试只调用 logger.Info，不会经过 With/WithGroup；返回自身即可。
func (c *turnTimingLogCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *turnTimingLogCapture) WithGroup(string) slog.Handler      { return c }

func (c *turnTimingLogCapture) snapshot() []slog.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]slog.Record(nil), c.recs...)
}

// captureTurnTimingLogs 把计时日志输出端替换为内存捕获器，并在测试结束时恢复。
func captureTurnTimingLogs(t *testing.T) *turnTimingLogCapture {
	t.Helper()
	cap := &turnTimingLogCapture{}
	prev := turnTimingLogger
	turnTimingLogger = func() *slog.Logger { return slog.New(cap) }
	t.Cleanup(func() { turnTimingLogger = prev })
	return cap
}

// attrsOf 把一条日志记录的属性摊平成键到 Value 的映射，便于逐字段断言。
func attrsOf(r slog.Record) map[string]slog.Value {
	m := make(map[string]slog.Value)
	r.Attrs(func(a slog.Attr) bool {
		m[a.Key] = a.Value
		return true
	})
	return m
}

// timingRecord 从捕获的记录中取出唯一的 agent_turn_timing 记录。
func timingRecord(t *testing.T, cap *turnTimingLogCapture) slog.Record {
	t.Helper()
	recs := cap.snapshot()
	if len(recs) != 1 {
		t.Fatalf("计时日志记录数 = %d, want 1: %+v", len(recs), recs)
	}
	if recs[0].Message != "agent_turn_timing" {
		t.Fatalf("日志消息 = %q, want %q", recs[0].Message, "agent_turn_timing")
	}
	return recs[0]
}

// toolThenTextRunner 构造一次“先工具、后作答”的运行，覆盖推理/工具/压缩/用户等待四个阶段。
func toolThenTextRunner(emit func(Event)) (*Runner, *fakeCompactor) {
	compactor := &fakeCompactor{}
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(n int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", "read", `{}`)), nil
		}
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{
		Model:            "m",
		Chatter:          chat,
		Tools:            tools,
		MaxTurns:         4,
		CompactThreshold: 1, // 极小阈值：工具批次后必然触发压缩
		Compactor:        compactor,
		Emit:             emit,
	}}
	return runner, compactor
}

// TestTurnTimingWireCompleteRun 验证 opt-in 开启且运行干净结束时：
// 四个阶段都被成对 Begin/End 结算进快照，日志字段齐全且 complete=true。
func TestTurnTimingWireCompleteRun(t *testing.T) {
	t.Setenv("ARGENT_TURN_TIMING", "1")
	cap := captureTurnTimingLogs(t)

	runner, compactor := toolThenTextRunner(nil)
	res, err := runner.Run(context.Background(), "inspect")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Stopped != StopComplete {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopComplete)
	}
	if compactor.calls == 0 {
		t.Fatal("compaction 阶段未发生：compactor 从未被调用")
	}

	attrs := attrsOf(timingRecord(t, cap))
	if got := attrs["stopped"].String(); got != StopComplete {
		t.Fatalf("stopped = %q, want %q", got, StopComplete)
	}
	if !attrs["complete"].Bool() {
		t.Fatal("complete = false, want true")
	}
	if _, ok := attrs["total_us"]; !ok {
		t.Fatal("日志缺少 total_us 字段")
	}
	// 五个阶段键都应当出现（overhead 本轮未使用，值为 0，但键仍稳定输出）。
	for _, p := range turnTimingPhases {
		key := string(p) + "_us"
		if _, ok := attrs[key]; !ok {
			t.Fatalf("日志缺少阶段字段 %q", key)
		}
	}
	// 实际发生的阶段应记到正的耗时（给足时钟分辨率余量；Windows 粗时钟下也可能为 0，
	// 故只要求键存在且非负，具体累加语义由 turn_timing_test.go 用注入时长守护）。
	for _, key := range []string{"inference_us", "tools_us", "compaction_us", "user_wait_us"} {
		if attrs[key].Int64() < 0 {
			t.Fatalf("%s = %d 为负", key, attrs[key].Int64())
		}
	}
}

// TestTurnTimingWireIncompleteRun 验证非正常停止时：complete=false，
// 且 incomplete_reasons 记录停止原因，与 Result.Stopped 一致。
func TestTurnTimingWireIncompleteRun(t *testing.T) {
	t.Setenv("ARGENT_TURN_TIMING", "1")
	cap := captureTurnTimingLogs(t)

	// 脚本化的 chatter 永远返回工具调用；MaxTurns=1 且 NoFinalAnswer 时，
	// 循环在工具批次后耗尽轮次预算，以 StopMaxTurns 结束。
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		return toolReply(toolCall("c1", "read", `{}`)), nil
	}}
	runner := &Runner{Opts: Options{
		Model:         "m",
		Chatter:       chat,
		Tools:         &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}},
		MaxTurns:      1,
		NoFinalAnswer: true,
	}}
	res, err := runner.Run(context.Background(), "inspect")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Stopped != StopMaxTurns {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopMaxTurns)
	}

	attrs := attrsOf(timingRecord(t, cap))
	if got := attrs["stopped"].String(); got != StopMaxTurns {
		t.Fatalf("stopped = %q, want %q", got, StopMaxTurns)
	}
	if attrs["complete"].Bool() {
		t.Fatal("complete = true, want false")
	}
	reasons := attrs["incomplete_reasons"].String()
	if !strings.Contains(reasons, StopMaxTurns) {
		t.Fatalf("incomplete_reasons = %q, want 含 %q", reasons, StopMaxTurns)
	}
}

// TestTurnTimingWireOffByDefault 验证默认关闭时：不建实例、不输出任何日志，
// 事件序列照常产生（与既有行为逐字节一致）。
func TestTurnTimingWireOffByDefault(t *testing.T) {
	// 显式清空，防止开发机外部环境变量污染默认断言。
	t.Setenv("ARGENT_TURN_TIMING", "")
	cap := captureTurnTimingLogs(t)

	if turnTimingEnabled() {
		t.Fatal("空值不应开启逐阶段计时")
	}

	sink := &eventSink{}
	runner, _ := toolThenTextRunner(sink.emit)
	res, err := runner.Run(context.Background(), "inspect")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Stopped != StopComplete {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopComplete)
	}
	if got := len(cap.snapshot()); got != 0 {
		t.Fatalf("默认关闭却记录了 %d 条计时日志", got)
	}
	if len(sink.kinds()) == 0 {
		t.Fatal("默认关闭时事件流不应为空")
	}
}

// TestTurnTimingWireEnabledFlagValues 验证 env 解析：仅去空白后的 "1" 开启。
func TestTurnTimingWireEnabledFlagValues(t *testing.T) {
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
			t.Setenv("ARGENT_TURN_TIMING", tc.value)
			if got := turnTimingEnabled(); got != tc.want {
				t.Fatalf("turnTimingEnabled(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// TestTurnTimingWireHelpersNoopWhenOff 验证 opt-in 关闭时所有计时薄包装都是空操作：
// 不建实例、孤立的 End 不污染统计、轮末 finish 不触发日志。
func TestTurnTimingWireHelpersNoopWhenOff(t *testing.T) {
	cap := captureTurnTimingLogs(t)
	var s runState
	s.turnTimingBegin(phaseInference)
	s.turnTimingEnd(phaseInference)
	s.turnTimingEnd(phaseTools) // 孤立 End：没有实例时更应彻底无副作用
	s.finishTurnTiming(StopComplete)
	if s.turnTiming != nil {
		t.Fatal("关闭时不应创建 TurnTiming 实例")
	}
	if got := len(cap.snapshot()); got != 0 {
		t.Fatalf("关闭时不应输出日志，实际 %d 条", got)
	}
}

// TestTurnTimingWireNoConflictWithEventTiming 验证新旧计时互不干扰：
// 同一轮里既产生新日志，又照常填充 reasoning/tool 事件的 StartedAtMS/FinishedAtMS。
func TestTurnTimingWireNoConflictWithEventTiming(t *testing.T) {
	t.Setenv("ARGENT_TURN_TIMING", "1")
	cap := captureTurnTimingLogs(t)

	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			reply := toolReply(toolCall("c1", "read", `{}`))
			reply.Reasoning = "思考一下"
			return reply, nil
		}
		return textReply("done"), nil
	}}
	sink := &eventSink{}
	runner := &Runner{Opts: Options{
		Model:    "m",
		Chatter:  chat,
		Tools:    &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}},
		MaxTurns: 3,
		Emit:     sink.emit,
	}}
	if _, err := runner.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 新计时：日志仍恰好一条。
	if got := len(cap.snapshot()); got != 1 {
		t.Fatalf("计时日志数 = %d, want 1", got)
	}

	// 既有 eventTiming：事件上的墙上时钟字段照常填充。
	sink.mu.Lock()
	events := append([]Event(nil), sink.events...)
	sink.mu.Unlock()
	var reasoning, start, end *Event
	for i := range events {
		switch events[i].Kind {
		case EventReasoningDelta:
			if reasoning == nil {
				reasoning = &events[i]
			}
		case EventToolStart:
			if start == nil {
				start = &events[i]
			}
		case EventToolEnd:
			if end == nil {
				end = &events[i]
			}
		}
	}
	if reasoning == nil || reasoning.StartedAtMS <= 0 || reasoning.FinishedAtMS < reasoning.StartedAtMS {
		t.Fatalf("reasoning_delta 计时被破坏: %+v", reasoning)
	}
	if start == nil || start.StartedAtMS <= 0 {
		t.Fatalf("tool_start 计时被破坏: %+v", start)
	}
	if end == nil || end.StartedAtMS <= 0 || end.FinishedAtMS < end.StartedAtMS {
		t.Fatalf("tool_end 计时被破坏: %+v", end)
	}
}
