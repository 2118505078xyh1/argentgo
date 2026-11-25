package agent

import (
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"example.com/argentgo/internal/llm"
)

// 本文件测试 ScoreContextHealth 在 agent 轮末的 opt-in 观测接线：
// ARGENT_CONTEXT_HEALTH_LOG=1 时在 execute 末尾与其它诊断日志并列输出一条
// agent_context_health；默认关闭时整段（采样 + 日志）是空操作，行为逐字节不变。
// 接线刻意只用纯函数 ScoreContextHealth + 共用 logger，不碰全局 SetContextHealthLogger。

// contextHealthRecords 返回捕获器中消息恰为 msg 的全部记录（不要求唯一）。
func contextHealthRecords(cap *turnTimingLogCapture, msg string) []slog.Record {
	var out []slog.Record
	for _, r := range cap.snapshot() {
		if r.Message == msg {
			out = append(out, r)
		}
	}
	return out
}

// TestContextHealthLogWireEnvFlagValues 验证 env 解析：仅去空白后的 "1" 开启。
func TestContextHealthLogWireEnvFlagValues(t *testing.T) {
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
			t.Setenv("ARGENT_CONTEXT_HEALTH_LOG", tc.value)
			if got := contextHealthLogEnabled(); got != tc.want {
				t.Fatalf("contextHealthLogEnabled(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// TestContextHealthLogWireOffByDefault 验证关闭基线：
// 默认（空值）时不置位、不采样、不输出任何健康度日志；其它轮末日志关闭时也不受影响；
// 即使 turnTiming 单独开启，健康度日志也不该出现（两者正交）。事件流照常工作。
func TestContextHealthLogWireOffByDefault(t *testing.T) {
	t.Setenv("ARGENT_CONTEXT_HEALTH_LOG", "")
	t.Setenv("ARGENT_TURN_TIMING", "")
	cap := captureTurnTimingLogs(t)

	opts := Options{Model: "m", System: "s"}
	if newRunState(opts, "hi", nil).contextHealthLog {
		t.Fatal("关闭时不应置位 contextHealthLog")
	}

	// 直接调用 finishContextHealth：即便有污染消息，也不得采样或输出。
	var direct runState
	direct.messages = []llm.Message{{Role: "user", Content: "guidance: retry"}}
	direct.finishContextHealth()
	if got := len(contextHealthRecords(cap, "agent_context_health")); got != 0 {
		t.Fatalf("关闭时直接调用仍输出 %d 条健康度日志", got)
	}

	// 完整 Run：默认关闭时零健康度日志，事件流照常。
	sink := &eventSink{}
	runner, _ := toolThenTextRunner(sink.emit)
	if _, err := runner.Run(context.Background(), "inspect"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(contextHealthRecords(cap, "agent_context_health")); got != 0 {
		t.Fatalf("默认关闭却输出了 %d 条健康度日志", got)
	}
	if len(sink.kinds()) == 0 {
		t.Fatal("关闭时事件流不应为空")
	}
}

// TestContextHealthLogWireEmitsScoringFields 验证 opt-in 开启后由 execute 轮末输出日志：
// 消息名固定、score/guidance_repeat/error_stub 三个评分字段齐全、score 落在 [0,1]。
func TestContextHealthLogWireEmitsScoringFields(t *testing.T) {
	t.Setenv("ARGENT_CONTEXT_HEALTH_LOG", "1")
	cap := captureTurnTimingLogs(t)

	runner, _ := toolThenTextRunner(nil)
	if _, err := runner.Run(context.Background(), "inspect"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	recs := contextHealthRecords(cap, "agent_context_health")
	if len(recs) != 1 {
		t.Fatalf("健康度日志数 = %d, want 1: %+v", len(recs), recs)
	}
	attrs := attrsOf(recs[0])
	for _, key := range []string{"score", "guidance_repeat", "error_stub"} {
		if _, ok := attrs[key]; !ok {
			t.Fatalf("健康度日志缺少字段 %q", key)
		}
	}
	score := attrs["score"].Float64()
	if score < 0 || score > 1 {
		t.Fatalf("score = %v 越界 [0,1]", score)
	}
}

// TestContextHealthLogWireHealthyVsPolluted 验证健康对话与被污染对话的评分差异：
// 健康对话三项满分/零污染；污染对话重复指引与 stub 拉低评分，且健康分更高。
// 同时断言观测不改消息。
func TestContextHealthLogWireHealthyVsPolluted(t *testing.T) {
	t.Run("healthy", func(t *testing.T) {
		cap := captureTurnTimingLogs(t)
		s := &runState{
			contextHealthLog: true,
			messages: []llm.Message{
				{Role: "system", Content: "You are a helpful assistant."},
				{Role: "user", Content: "请读取配置文件"},
				{Role: "assistant", Content: "已经读取完成，内容正常。"},
			},
		}
		before := append([]llm.Message(nil), s.messages...)
		s.finishContextHealth()
		if !reflect.DeepEqual(s.messages, before) {
			t.Fatal("健康度观测修改了消息")
		}
		recs := contextHealthRecords(cap, "agent_context_health")
		if len(recs) != 1 {
			t.Fatalf("健康度日志数 = %d, want 1", len(recs))
		}
		attrs := attrsOf(recs[0])
		if w := 1.0; !nearHealth(attrs["score"].Float64(), w) {
			t.Fatalf("健康对话 score = %v, want %v", attrs["score"].Float64(), w)
		}
		if got := attrs["guidance_repeat"].Float64(); got != 0 {
			t.Fatalf("健康对话 guidance_repeat = %v, want 0", got)
		}
		if got := attrs["error_stub"].Float64(); got != 0 {
			t.Fatalf("健康对话 error_stub = %v, want 0", got)
		}
	})

	t.Run("polluted", func(t *testing.T) {
		cap := captureTurnTimingLogs(t)
		s := &runState{
			contextHealthLog: true,
			messages: []llm.Message{
				{Role: "user", Content: "guidance: retry"},
				{Role: "assistant", Content: "guidance: retry"},
				{Role: "tool", Content: "[tool output trimmed: 100 chars]"},
			},
		}
		before := append([]llm.Message(nil), s.messages...)
		s.finishContextHealth()
		if !reflect.DeepEqual(s.messages, before) {
			t.Fatal("健康度观测修改了消息")
		}
		recs := contextHealthRecords(cap, "agent_context_health")
		if len(recs) != 1 {
			t.Fatalf("健康度日志数 = %d, want 1", len(recs))
		}
		attrs := attrsOf(recs[0])
		// 两条相同指引 → 1 次重复 / 2 = 0.5；1 条 stub / 3 ≈ 0.333333；
		// Score = 1 - (0.6*0.5 + 0.5/3) ≈ 0.533333。以纯函数的 6 位舍入结果为准。
		want := ScoreContextHealth([]string{"guidance: retry", "guidance: retry", "[tool output trimmed: 100 chars]"})
		if w := want.GuidanceRepeatRatio; !nearHealth(attrs["guidance_repeat"].Float64(), w) {
			t.Fatalf("污染对话 guidance_repeat = %v, want %v", attrs["guidance_repeat"].Float64(), w)
		}
		if w := want.ErrorStubRatio; !nearHealth(attrs["error_stub"].Float64(), w) {
			t.Fatalf("污染对话 error_stub = %v, want %v", attrs["error_stub"].Float64(), w)
		}
		if w := want.Score; !nearHealth(attrs["score"].Float64(), w) {
			t.Fatalf("污染对话 score = %v, want %v", attrs["score"].Float64(), w)
		}
		if !(attrs["score"].Float64() < 1) {
			t.Fatal("污染对话 score 应小于健康对话的 1")
		}
	})
}

// TestContextHealthLogWireCJKSafe 验证中文 + emoji 消息经轮末投影后安全评分：
// 日志字段齐全且与直接对投影文本调用 ScoreContextHealth 的结果一致，无 panic、无乱码截断。
func TestContextHealthLogWireCJKSafe(t *testing.T) {
	cap := captureTurnTimingLogs(t)
	s := &runState{
		contextHealthLog: true,
		messages: []llm.Message{
			{Role: "assistant", Content: "指引：请先诊断原因，再改用其他工具 🛠️"},
			{Role: "assistant", Content: "指引：请先诊断原因，再改用其他工具 🛠️"},
			{Role: "tool", Content: "输出被裁剪 [truncated] 后面还有中文"},
			{Role: "user", Content: "中文内容完全不同，不含任何标记"},
		},
	}
	want := ScoreContextHealth([]string{
		"指引：请先诊断原因，再改用其他工具 🛠️",
		"指引：请先诊断原因，再改用其他工具 🛠️",
		"输出被裁剪 [truncated] 后面还有中文",
		"中文内容完全不同，不含任何标记",
	})
	s.finishContextHealth()

	recs := contextHealthRecords(cap, "agent_context_health")
	if len(recs) != 1 {
		t.Fatalf("健康度日志数 = %d, want 1", len(recs))
	}
	attrs := attrsOf(recs[0])
	if !nearHealth(attrs["score"].Float64(), want.Score) ||
		!nearHealth(attrs["guidance_repeat"].Float64(), want.GuidanceRepeatRatio) ||
		!nearHealth(attrs["error_stub"].Float64(), want.ErrorStubRatio) {
		t.Fatalf("CJK 日志字段 = (score=%v, guidance_repeat=%v, error_stub=%v), want %+v",
			attrs["score"].Float64(), attrs["guidance_repeat"].Float64(), attrs["error_stub"].Float64(), want)
	}
	// 中文重复指引 + stub 应确实拉低评分。
	if !(attrs["score"].Float64() < 1) {
		t.Fatal("CJK 污染对话 score 应小于 1")
	}
}

// TestContextHealthLogWireCoexistWithEndOfTurnLogs 验证与其它轮末日志共存：
// 四个 opt-in 同时开启时，同一 logger 恰好输出四条日志，互不覆盖；
// 健康度日志字段完整，既有计时/直方图/trace 的特征字段仍在。
func TestContextHealthLogWireCoexistWithEndOfTurnLogs(t *testing.T) {
	t.Setenv("ARGENT_CONTEXT_HEALTH_LOG", "1")
	t.Setenv("ARGENT_TURN_TIMING", "1")
	t.Setenv("ARGENT_TOOL_TIMING_HISTOGRAM", "1")
	t.Setenv("ARGENT_TOOL_CALL_TRACE", "1")
	cap := captureTurnTimingLogs(t)

	if _, err := twoToolRunner(nil).Run(context.Background(), "inspect"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	recs := cap.snapshot()
	if len(recs) != 4 {
		t.Fatalf("日志数 = %d, want 4: %+v", len(recs), recs)
	}
	msgs := map[string]bool{}
	for _, r := range recs {
		msgs[r.Message] = true
	}
	for _, want := range []string{
		"agent_turn_timing", "agent_tool_timing_histogram",
		"agent_tool_call_trace", "agent_context_health",
	} {
		if !msgs[want] {
			t.Fatalf("缺少日志 %q, got %v", want, msgs)
		}
	}
	if _, ok := attrsOf(histLogRecord(t, cap, "agent_turn_timing"))["total_us"]; !ok {
		t.Fatal("共存时计时日志缺少 total_us 字段")
	}
	if top := histTopSlow(t, cap); top == "" {
		t.Fatal("共存时直方图摘要为空")
	}
	if summary := traceSummary(t, cap); summary == "" {
		t.Fatal("共存时调用链 summary 为空")
	}
	if _, ok := attrsOf(histLogRecord(t, cap, "agent_context_health"))["score"]; !ok {
		t.Fatal("共存时健康度日志缺少 score 字段")
	}
}

// TestContextHealthLogWireCoexistWithLoopHistory 锁定 R38-R45 的历史改动与本轮接线共存：
// SystemNodes / InitialImages 作用不变，TurnTiming / RecoveryMetrics / 直方图 / trace 全部初始化，
// 且本轮 contextHealthLog 也置位；观测后消息不被改动（逐字节不变）。
func TestContextHealthLogWireCoexistWithLoopHistory(t *testing.T) {
	t.Setenv("ARGENT_TURN_TIMING", "1")
	t.Setenv("ARGENT_RECOVERY_METRICS", "1")
	t.Setenv("ARGENT_TOOL_TIMING_HISTOGRAM", "1")
	t.Setenv("ARGENT_TOOL_CALL_TRACE", "1")
	t.Setenv("ARGENT_CONTEXT_HEALTH_LOG", "1")

	const baseSystem = "BASE SYSTEM"
	opts := Options{
		System:        baseSystem,
		SystemNodes:   []string{"NODE-1"},
		InitialImages: []llm.ImageInput{{MimeType: "image/png", Data: []byte{0x01}}},
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
		s.toolTimingHistogram == nil || s.toolCallTrace == nil || !s.contextHealthLog {
		t.Fatalf("历史改动未全部初始化: turnTiming=%v recovery=%v histogram=%v trace=%v contextHealthLog=%v",
			s.turnTiming != nil, s.recovery != nil, s.toolTimingHistogram != nil,
			s.toolCallTrace != nil, s.contextHealthLog)
	}

	before := append([]llm.Message(nil), s.messages...)
	s.finishContextHealth()
	if !reflect.DeepEqual(s.messages, before) {
		t.Fatal("共存场景下健康度观测修改了消息")
	}
}
