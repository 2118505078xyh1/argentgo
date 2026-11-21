package agent

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// histLogRecord 从捕获器中找出消息恰为 msg 的日志记录，要求恰好一条。
// 复用 turn_timing_wire_test.go 中的 turnTimingLogCapture：两者共用同一个 logger，
// 这本身也验证了直方图摘要与逐阶段计时日志走同一输出通道。
func histLogRecord(t *testing.T, cap *turnTimingLogCapture, msg string) slog.Record {
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

// histTopSlow 返回唯一一条直方图日志里的 tool_top_slow 字段值。
func histTopSlow(t *testing.T, cap *turnTimingLogCapture) string {
	t.Helper()
	attrs := attrsOf(histLogRecord(t, cap, "agent_tool_timing_histogram"))
	v, ok := attrs["tool_top_slow"]
	if !ok {
		t.Fatal("直方图日志缺少 tool_top_slow 字段")
	}
	return v.String()
}

// twoToolRunner 构造一次请求两个工具（含一个中文名）后作答的运行。
func twoToolRunner(emit func(Event)) *Runner {
	return &Runner{Opts: Options{
		Model: "m",
		Chatter: &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
			if n == 1 {
				return toolReply(
					toolCall("c1", "read", `{}`),
					toolCall("c2", "读取文件", `{}`),
				), nil
			}
			return textReply("done"), nil
		}},
		Tools:    &fakeTools{specs: []llm.ToolSpec{toolSpec("read"), toolSpec("读取文件")}},
		MaxTurns: 3,
		Emit:     emit,
	}}
}

// TestToolTimingHistogramWireEnvFlagValues 验证 env 解析：仅去空白后的 "1" 开启。
func TestToolTimingHistogramWireEnvFlagValues(t *testing.T) {
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
			t.Setenv("ARGENT_TOOL_TIMING_HISTOGRAM", tc.value)
			if got := toolTimingHistogramEnabled(); got != tc.want {
				t.Fatalf("toolTimingHistogramEnabled(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// TestToolTimingHistogramWireFinishCallRecords 直接驱动 finishCall，锁定
// dispatch 接线点：工具名作为键、res.duration 作为样本被精确 Record；
// 未开启（直方图为 nil）时同一调用完全无副作用且不 panic。
func TestToolTimingHistogramWireFinishCallRecords(t *testing.T) {
	s := &runState{toolTimingHistogram: &ToolTimingHistogram{}}
	// 手工构造状态上没有 startTool 的起点；finishTool 对未知 id 是安全的空操作。
	s.finishCall(0, toolCall("c1", "读取文件", `{}`), callResult{output: "ok", duration: 12 * time.Millisecond})
	s.finishCall(0, toolCall("c2", "read", `{}`), callResult{output: "ok", duration: 8 * time.Millisecond})
	s.finishCall(0, toolCall("c3", "读取文件", `{}`), callResult{output: "ok", duration: 20 * time.Millisecond})

	cjk := s.toolTimingHistogram.Stats("读取文件")
	if cjk.Count != 2 || cjk.Total != 32*time.Millisecond {
		t.Fatalf("读取文件 统计 = %+v, want Count=2 Total=32ms", cjk)
	}
	read := s.toolTimingHistogram.Stats("read")
	if read.Count != 1 || read.Total != 8*time.Millisecond {
		t.Fatalf("read 统计 = %+v, want Count=1 Total=8ms", read)
	}
	// TopSlow 按 P95 降序：读取文件(20ms) 在 read(8ms) 之前。
	top := s.toolTimingHistogram.TopSlow(5)
	if len(top) != 2 || top[0].Tool != "读取文件" || top[1].Tool != "read" {
		t.Fatalf("TopSlow = %+v, want [读取文件 read]", top)
	}

	// 关闭基线：nil 直方图时 finishCall 照常发出事件，但不会 panic、不记录耗时。
	var off runState
	off.finishCall(0, toolCall("c4", "read", `{}`), callResult{output: "ok", duration: time.Second})
	if off.toolTimingHistogram != nil {
		t.Fatal("关闭时不应创建直方图")
	}
}

// TestToolTimingHistogramWireLogFormat 用注入的确定样本锁定摘要日志格式：
// P95 表达、按 P95 降序、逗号连接、CJK 名称原样保留。
func TestToolTimingHistogramWireLogFormat(t *testing.T) {
	cap := captureTurnTimingLogs(t)
	var h ToolTimingHistogram
	h.Record("read", 12*time.Millisecond)
	h.Record("bash", 8*time.Millisecond)
	h.Record("读取文件", 30*time.Millisecond)

	emitToolTimingHistogramLog(&h)

	recs := cap.snapshot()
	if len(recs) != 1 {
		t.Fatalf("日志数 = %d, want 1", len(recs))
	}
	if recs[0].Message != "agent_tool_timing_histogram" {
		t.Fatalf("日志消息 = %q, want %q", recs[0].Message, "agent_tool_timing_histogram")
	}
	want := "读取文件:30ms,read:12ms,bash:8ms"
	if got := attrsOf(recs[0])["tool_top_slow"].String(); got != want {
		t.Fatalf("tool_top_slow = %q, want %q", got, want)
	}
}

// TestToolTimingHistogramWireHelpersNoopWhenOff 验证 opt-in 关闭时的空操作：
// nil 直方图既不输出日志，也不产生任何分配副作用。
func TestToolTimingHistogramWireHelpersNoopWhenOff(t *testing.T) {
	cap := captureTurnTimingLogs(t)
	var s runState
	emitToolTimingHistogramLog(nil)
	s.finishToolTimingHistogram()
	if got := len(cap.snapshot()); got != 0 {
		t.Fatalf("关闭时不应输出日志，实际 %d 条", got)
	}
}

// TestToolTimingHistogramWireEnabledThroughRun 走完整 Run：开启后由 dispatch
// 的 finishCall 实际 Record，轮末输出 TopSlow 摘要；未开启 turnTiming 时不应有
// 逐阶段计时日志，且事件与工具仍照常工作。
func TestToolTimingHistogramWireEnabledThroughRun(t *testing.T) {
	t.Setenv("ARGENT_TOOL_TIMING_HISTOGRAM", "1")
	t.Setenv("ARGENT_TURN_TIMING", "")
	cap := captureTurnTimingLogs(t)

	sink := &eventSink{}
	res, err := twoToolRunner(sink.emit).Run(context.Background(), "inspect")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stopped != StopComplete {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopComplete)
	}

	top := histTopSlow(t, cap)
	if !strings.Contains(top, "read:") || !strings.Contains(top, "读取文件:") {
		t.Fatalf("摘要日志缺少工具名: %q", top)
	}
	// turnTiming 未开启时不应出现逐阶段计时日志（两者正交）。
	for _, r := range cap.snapshot() {
		if r.Message == "agent_turn_timing" {
			t.Fatal("turnTiming 未开启却输出了逐阶段计时日志")
		}
	}
	if !sink.has(EventToolEnd) {
		t.Fatal("开启直方图后事件流不应被破坏：缺少 tool_end")
	}
}

// TestToolTimingHistogramWireOffByDefault 验证默认关闭基线：不建直方图、
// 不 Record、不输出；newRunState 与整轮 Run 都不产生任何直方图日志。
func TestToolTimingHistogramWireOffByDefault(t *testing.T) {
	t.Setenv("ARGENT_TOOL_TIMING_HISTOGRAM", "")
	cap := captureTurnTimingLogs(t)

	s := newRunState(Options{}, "x", nil)
	if s.toolTimingHistogram != nil {
		t.Fatal("默认关闭时不应创建直方图")
	}

	res, err := twoToolRunner(nil).Run(context.Background(), "inspect")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stopped != StopComplete {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopComplete)
	}
	if got := len(cap.snapshot()); got != 0 {
		t.Fatalf("默认关闭却输出了 %d 条日志", got)
	}
}

// TestToolTimingHistogramWireCoexistWithTurnTiming 验证与 TurnTiming 共存：
// 两个 opt-in 同时开启时，同一 logger 恰好输出两条日志（计时 + 直方图摘要），
// 互不覆盖、互不干扰。
func TestToolTimingHistogramWireCoexistWithTurnTiming(t *testing.T) {
	t.Setenv("ARGENT_TOOL_TIMING_HISTOGRAM", "1")
	t.Setenv("ARGENT_TURN_TIMING", "1")
	cap := captureTurnTimingLogs(t)

	if _, err := twoToolRunner(nil).Run(context.Background(), "inspect"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	recs := cap.snapshot()
	if len(recs) != 2 {
		t.Fatalf("日志数 = %d, want 2: %+v", len(recs), recs)
	}
	msgs := map[string]bool{}
	for _, r := range recs {
		msgs[r.Message] = true
	}
	if !msgs["agent_turn_timing"] || !msgs["agent_tool_timing_histogram"] {
		t.Fatalf("两条日志应分别为计时与直方图摘要, got %v", msgs)
	}
	// 计时日志字段仍完整（coexist 不破坏既有日志）。
	attrs := attrsOf(histLogRecord(t, cap, "agent_turn_timing"))
	if _, ok := attrs["total_us"]; !ok {
		t.Fatal("共存时计时日志缺少 total_us 字段")
	}
	if top := histTopSlow(t, cap); top == "" {
		t.Fatal("共存时直方图摘要为空")
	}
}

// TestToolTimingHistogramWireCJKToolName 单独锁定 CJK 工具名：整轮 Run 后
// 摘要日志原样保留中文工具名，且 TopSlow 排序可区分不同工具。
func TestToolTimingHistogramWireCJKToolName(t *testing.T) {
	t.Setenv("ARGENT_TOOL_TIMING_HISTOGRAM", "1")
	cap := captureTurnTimingLogs(t)

	if _, err := twoToolRunner(nil).Run(context.Background(), "inspect"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	top := histTopSlow(t, cap)
	if !strings.Contains(top, "读取文件") {
		t.Fatalf("摘要日志未保留 CJK 工具名: %q", top)
	}
	// 每个工具名后都应紧跟 ':' 加一个时长（形如 名称:12ms）。
	for _, part := range strings.Split(top, ",") {
		i := strings.IndexByte(part, ':')
		if i <= 0 || i == len(part)-1 {
			t.Fatalf("摘要项格式异常: %q", part)
		}
	}
}
