package agent

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// traceLogRecord 从捕获器中找出消息恰为 msg 的日志记录，要求恰好一条。
// 复用 turn_timing_wire_test.go 中的 turnTimingLogCapture：调用链摘要与逐阶段计时
// 日志走同一输出通道，这本身也验证了三者共用同一个 logger。
func traceLogRecord(t *testing.T, cap *turnTimingLogCapture, msg string) slog.Record {
	t.Helper()
	var found []slog.Record
	for _, r := range cap.snapshot() {
		if r.Message == msg {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		t.Fatalf("日志 %q 记录数 = %d, want 1: %+v", msg, len(found), found)
	}
	return found[0]
}

// traceSummary 返回唯一一条调用链日志里的 summary 字段值。
func traceSummary(t *testing.T, cap *turnTimingLogCapture) string {
	t.Helper()
	attrs := attrsOf(traceLogRecord(t, cap, "agent_tool_call_trace"))
	v, ok := attrs["summary"]
	if !ok {
		t.Fatal("调用链日志缺少 summary 字段")
	}
	return v.String()
}

// TestToolCallTraceWireEnvFlagValues 验证 env 解析：仅去空白后的 "1" 开启。
func TestToolCallTraceWireEnvFlagValues(t *testing.T) {
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
			t.Setenv("ARGENT_TOOL_CALL_TRACE", tc.value)
			if got := toolCallTraceEnabled(); got != tc.want {
				t.Fatalf("toolCallTraceEnabled(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// TestToolCallTraceWireBeginEndPaired 直接驱动 dispatch 接线点：每个 emitToolStart
// 都必须与 finishCall 配对，哪怕调用从未真正运行（ran=false，审批/防护/取消路径）。
// 断言没有悬空跨度、ran=false 的失败也被记入链。
func TestToolCallTraceWireBeginEndPaired(t *testing.T) {
	s := &runState{toolCallTrace: &ToolCallTrace{}}

	// 正常调用（ran=true，成功）。
	s.emitToolStart(0, toolCall("c1", "read", `{}`))
	s.finishCall(0, toolCall("c1", "read", `{}`), callResult{output: "ok", ran: true})

	// ran=false 的调用：审批拒绝/重复防护阻断/批次取消都走这条路径，
	// Begin 在 emitToolStart 发生，End 在 finishCall 结算，绝不留悬空跨度。
	s.emitToolStart(0, toolCall("c2", "denied", `{}`))
	s.finishCall(0, toolCall("c2", "denied", `{}`), callResult{output: "error: denied", isError: true})
	s.emitToolStart(0, toolCall("c3", "cancelled", `{}`))
	s.finishCall(0, toolCall("c3", "cancelled", `{}`), callResult{output: "tool did not run", isError: true})

	spans := s.toolCallTrace.Snapshot()
	if len(spans) != 3 {
		t.Fatalf("快照长度 = %d, want 3（Begin/End 必须成对）: %+v", len(spans), spans)
	}
	for i, want := range []struct {
		tool string
		ok   bool
	}{
		{"read", true},
		{"denied", false},
		{"cancelled", false},
	} {
		if spans[i].Tool != want.tool || spans[i].Ok != want.ok {
			t.Errorf("span[%d] = %+v, want tool=%q ok=%v", i, spans[i], want.tool, want.ok)
		}
	}
	// 所有 spanID 已被结算删除，没有悬挂在 pending 中。
	if len(s.toolCallSpans) != 0 {
		t.Fatalf("未配对 spanID 残留: %+v", s.toolCallSpans)
	}
	// 失败文本来自 isError 的 output。
	if !strings.Contains(spans[1].Error, "denied") {
		t.Fatalf("失败跨度错误文本 = %q, want 含 denied", spans[1].Error)
	}
}

// TestToolCallTraceWireLogFormat 用注入的确定时钟锁定摘要日志格式：
// 消息名、summary 键与中文 N次调用/M次失败/总耗时Xms 表达式。
func TestToolCallTraceWireLogFormat(t *testing.T) {
	cap := captureTurnTimingLogs(t)
	base := time.Unix(1_700_000_700, 0)
	var tr ToolCallTrace
	tr.now = clockFrom(
		base, base.Add(10*time.Millisecond),
		base, base.Add(5*time.Millisecond),
	)
	id1 := tr.Begin("a")
	tr.End(id1, true, nil)
	id2 := tr.Begin("b")
	tr.End(id2, true, nil)

	emitToolCallTraceLog(&tr)

	recs := cap.snapshot()
	if len(recs) != 1 {
		t.Fatalf("日志数 = %d, want 1", len(recs))
	}
	if recs[0].Message != "agent_tool_call_trace" {
		t.Fatalf("日志消息 = %q, want %q", recs[0].Message, "agent_tool_call_trace")
	}
	want := "2次调用/0次失败/总耗时15ms"
	if got := attrsOf(recs[0])["summary"].String(); got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

// TestToolCallTraceWireFailureStats 经 dispatch 混派成功/失败调用后断言：
// 失败数按 Ok=false 统计、错误文本落进跨度，且轮末日志 summary 与之一致。
func TestToolCallTraceWireFailureStats(t *testing.T) {
	cap := captureTurnTimingLogs(t)
	s := &runState{toolCallTrace: &ToolCallTrace{}}

	s.emitToolStart(0, toolCall("ok", "read", `{}`))
	s.finishCall(0, toolCall("ok", "read", `{}`), callResult{output: "ok", ran: true})
	s.emitToolStart(0, toolCall("fail1", "write", `{}`))
	s.finishCall(0, toolCall("fail1", "write", `{}`), callResult{output: "error: boom", isError: true, ran: true})
	s.emitToolStart(0, toolCall("fail2", "write", `{}`))
	s.finishCall(0, toolCall("fail2", "write", `{}`), callResult{output: "error: bang", isError: true, ran: true})

	fails := 0
	for _, span := range s.toolCallTrace.Snapshot() {
		if !span.Ok {
			fails++
			if span.Error == "" {
				t.Fatalf("失败跨度缺少错误文本: %+v", span)
			}
		}
	}
	if fails != 2 {
		t.Fatalf("失败数 = %d, want 2", fails)
	}

	s.finishTrace()
	got := traceSummary(t, cap)
	if !strings.HasPrefix(got, "3次调用/2次失败/总耗时") || !strings.HasSuffix(got, "ms") {
		t.Fatalf("summary = %q, want 形如 \"3次调用/2次失败/总耗时Xms\"", got)
	}
}

// TestToolCallTraceWireOffByDefault 验证默认关闭基线：不建实例、Begin/End/finish 全空操作、
// 整轮 Run 不产生任何调用链日志。
func TestToolCallTraceWireOffByDefault(t *testing.T) {
	t.Setenv("ARGENT_TOOL_CALL_TRACE", "")
	cap := captureTurnTimingLogs(t)

	s := newRunState(Options{}, "x", nil)
	if s.toolCallTrace != nil {
		t.Fatal("默认关闭时不应创建调用链追踪器")
	}
	s.traceToolBegin("id", "read")
	s.traceToolEnd("id", false, errors.New("boom"))
	s.finishTrace()
	emitToolCallTraceLog(nil)
	if got := len(cap.snapshot()); got != 0 {
		t.Fatalf("关闭时不应输出日志，实际 %d 条", got)
	}

	res, err := twoToolRunner(nil).Run(context.Background(), "inspect")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stopped != StopComplete {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopComplete)
	}
	for _, r := range cap.snapshot() {
		if r.Message == "agent_tool_call_trace" {
			t.Fatal("默认关闭却输出了调用链日志")
		}
	}
}

// TestToolCallTraceWireEnabledThroughRun 走完整 Run：开启后由 dispatch 的
// emitToolStart/finishCall 实际配对，轮末输出摘要；未开启 turnTiming/histogram 时
// 不应出现它们的日志，且事件流照常工作。
func TestToolCallTraceWireEnabledThroughRun(t *testing.T) {
	t.Setenv("ARGENT_TOOL_CALL_TRACE", "1")
	t.Setenv("ARGENT_TURN_TIMING", "")
	t.Setenv("ARGENT_TOOL_TIMING_HISTOGRAM", "")
	cap := captureTurnTimingLogs(t)

	sink := &eventSink{}
	res, err := twoToolRunner(sink.emit).Run(context.Background(), "inspect")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stopped != StopComplete {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopComplete)
	}

	// twoToolRunner 发起两个工具，摘要应恰好计两次调用。
	if got := traceSummary(t, cap); !strings.HasPrefix(got, "2次调用/") {
		t.Fatalf("summary = %q, want 前缀 \"2次调用/\"", got)
	}
	// 另外两个 opt-in 未开：整轮只应有一条调用链日志。
	if got := len(cap.snapshot()); got != 1 {
		t.Fatalf("日志数 = %d, want 1: %+v", got, cap.snapshot())
	}
	if !sink.has(EventToolEnd) {
		t.Fatal("开启调用链后事件流不应被破坏：缺少 tool_end")
	}
}

// TestToolCallTraceWireCoexistWithTurnTimingAndHistogram 验证三者共存：
// 三个 opt-in 同时开启时，同一 logger 恰好输出三条日志，互不覆盖、互不干扰。
func TestToolCallTraceWireCoexistWithTurnTimingAndHistogram(t *testing.T) {
	t.Setenv("ARGENT_TOOL_CALL_TRACE", "1")
	t.Setenv("ARGENT_TURN_TIMING", "1")
	t.Setenv("ARGENT_TOOL_TIMING_HISTOGRAM", "1")
	cap := captureTurnTimingLogs(t)

	if _, err := twoToolRunner(nil).Run(context.Background(), "inspect"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	recs := cap.snapshot()
	if len(recs) != 3 {
		t.Fatalf("日志数 = %d, want 3: %+v", len(recs), recs)
	}
	msgs := map[string]bool{}
	for _, r := range recs {
		msgs[r.Message] = true
	}
	for _, want := range []string{"agent_turn_timing", "agent_tool_timing_histogram", "agent_tool_call_trace"} {
		if !msgs[want] {
			t.Fatalf("缺少日志 %q, got %v", want, msgs)
		}
	}
	// 既有日志字段仍完整（共存不破坏它们）。
	if _, ok := attrsOf(traceLogRecord(t, cap, "agent_turn_timing"))["total_us"]; !ok {
		t.Fatal("共存时计时日志缺少 total_us 字段")
	}
	if top := histTopSlow(t, cap); top == "" {
		t.Fatal("共存时直方图摘要为空")
	}
	if got := traceSummary(t, cap); !strings.HasPrefix(got, "2次调用/") {
		t.Fatalf("共存时调用链 summary 异常: %q", got)
	}
}

// TestToolCallTraceWireCJKToolName 单独锁定 CJK：dispatch 接线后中文工具名与
// 中文错误文本被原样保留进跨度，摘要统计仍正确。
func TestToolCallTraceWireCJKToolName(t *testing.T) {
	s := &runState{toolCallTrace: &ToolCallTrace{}}
	s.emitToolStart(0, toolCall("c1", "读取文件", `{}`))
	s.finishCall(0, toolCall("c1", "读取文件", `{}`), callResult{output: "error: 磁盘已满", isError: true})

	spans := s.toolCallTrace.Snapshot()
	if len(spans) != 1 {
		t.Fatalf("快照长度 = %d, want 1", len(spans))
	}
	if spans[0].Tool != "读取文件" || spans[0].Error != "error: 磁盘已满" {
		t.Fatalf("CJK 工具名/错误文本未原样保留: %+v", spans[0])
	}
	if got := s.toolCallTrace.Summary(); !strings.HasPrefix(got, "1次调用/1次失败/总耗时") {
		t.Fatalf("Summary = %q, want 前缀 \"1次调用/1次失败/总耗时\"", got)
	}
}
