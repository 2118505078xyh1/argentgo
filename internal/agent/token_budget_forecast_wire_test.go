package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// 本文件测试 token_budget_forecast 原语在循环中的 opt-in 接线：
// ARGENT_TOKEN_FORECAST=1 时 account() 记录有界用量历史，请求前若预测会触及总预算，
// 提前发一条 EventNotice 并复用阈值压缩一次；默认关闭时整段是零分配的空操作。

const forecastNoticePrefix = "token budget forecast:"

func forecastNotices(events []Event) []Event {
	var out []Event
	for _, e := range events {
		if e.Kind == EventNotice && strings.Contains(e.Text, forecastNoticePrefix) {
			out = append(out, e)
		}
	}
	return out
}

func compactNotices(events []Event) []Event {
	var out []Event
	for _, e := range events {
		if e.Kind == EventNotice && strings.Contains(e.Text, "compacted context") {
			out = append(out, e)
		}
	}
	return out
}

// TestTokenForecastEnvFlagValues 验证 env 解析：仅去空白后的 "1" 开启。
func TestTokenForecastEnvFlagValues(t *testing.T) {
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
			t.Setenv("ARGENT_TOKEN_FORECAST", tc.value)
			if got := tokenForecastEnabled(); got != tc.want {
				t.Fatalf("tokenForecastEnabled(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// TestTokenForecastAccountHistory 验证用量历史只在开启时记录、且有界不重复分配。
func TestTokenForecastAccountHistory(t *testing.T) {
	// 关闭：account 不追加，usageHistory 保持 nil。
	t.Setenv("ARGENT_TOKEN_FORECAST", "")
	off := newRunState(Options{Model: "m"}, "q", nil)
	reply := textReply("x")
	reply.Usage = &llm.Usage{PromptTokens: 60, CompletionTokens: 40}
	off.account(reply, 100)
	if off.tokenForecast || off.usageHistory != nil {
		t.Fatalf("关闭时不应记录用量历史: flag=%v history=%v", off.tokenForecast, off.usageHistory)
	}

	// 开启：每轮记录 prompt+completion，超过上限后丢弃最旧样本且容量不变。
	t.Setenv("ARGENT_TOKEN_FORECAST", "1")
	on := newRunState(Options{Model: "m"}, "q", nil)
	if !on.tokenForecast || cap(on.usageHistory) != usageHistoryLimit {
		t.Fatalf("开启时应预分配容量 %d: flag=%v cap=%d", usageHistoryLimit, on.tokenForecast, cap(on.usageHistory))
	}
	for i := 1; i <= usageHistoryLimit+5; i++ {
		r := textReply("x")
		r.Usage = &llm.Usage{PromptTokens: i, CompletionTokens: i * 2}
		on.account(r, 3*i)
	}
	want := make([]int, usageHistoryLimit)
	for i := range want {
		round := i + 6 // 保留第 6..25 轮，每轮样本为 3*round（最新一轮 3*25=75）
		want[i] = 3 * round
	}
	if !reflect.DeepEqual(on.usageHistory, want) {
		t.Fatalf("usageHistory = %v, want %v", on.usageHistory, want)
	}
	if cap(on.usageHistory) != usageHistoryLimit {
		t.Fatalf("有界历史发生了重新分配: cap = %d, want %d", cap(on.usageHistory), usageHistoryLimit)
	}
}

// TestTokenForecastWire 喂 [100,200,300,400] 的逐轮用量：累计 1000，下一轮预测 350，
// 触及总预算 1200。断言在 Budget.Breach 之前恰好提前压缩一次并发出一条 EventNotice。
func TestTokenForecastWire(t *testing.T) {
	t.Setenv("ARGENT_TOKEN_FORECAST", "1")

	usages := []int{100, 200, 300, 400}
	compactor := &fakeCompactor{}
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n <= len(usages) {
			reply := toolReply(toolCall(fmt.Sprintf("c%d", n), "read", `{}`))
			reply.Usage = &llm.Usage{PromptTokens: usages[n-1] * 6 / 10, CompletionTokens: usages[n-1] * 4 / 10}
			return reply, nil
		}
		reply := textReply("done")
		reply.Usage = &llm.Usage{PromptTokens: 30, CompletionTokens: 20}
		return reply, nil
	}}

	var events []Event
	runner := &Runner{Opts: Options{
		Model:            "m",
		Chatter:          chat,
		Tools:            tools,
		MaxTurns:         8,
		CompactThreshold: 1, // 极小阈值：提前压缩分支必然真正调用压缩器
		Compactor:        compactor,
		Budget:           Budget{MaxTotalTokens: 1200},
		Emit:             func(e Event) { events = append(events, e) },
	}}

	res, err := runner.Run(context.Background(), "inspect")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stopped != StopComplete {
		t.Fatalf("stopped = %q, want %q（提前压缩应在 breach 之前发生）", res.Stopped, StopComplete)
	}

	notices := forecastNotices(events)
	if len(notices) != 1 {
		t.Fatalf("预测提示数 = %d, want 1: %+v", len(notices), notices)
	}
	// 预算停止提示不得出现在预测提示之前。
	for _, e := range events {
		if e.Text == StopMaxTotalTokens || strings.Contains(e.Text, StopMaxTotalTokens) {
			t.Fatalf("预测提示之前出现了预算停止提示: %+v", e)
		}
	}

	// 预测提示后紧跟的必须是它触发的那一次压缩，证明"提前压缩一次"。
	idx := -1
	for i, e := range events {
		if e == notices[0] {
			idx = i
			break
		}
	}
	if idx < 0 || idx+1 >= len(events) {
		t.Fatalf("无法定位预测提示及其后续压缩事件: events=%+v", events)
	}
	next := events[idx+1]
	if next.Kind != EventNotice || !strings.Contains(next.Text, "compacted context") {
		t.Fatalf("预测提示后未紧跟压缩事件: got %+v", next)
	}
	if got := len(compactNotices(events[idx:])); got != 1 {
		t.Fatalf("预测触发的压缩次数 = %d, want 1", got)
	}
	if compactor.calls == 0 {
		t.Fatal("预测触发时压缩器未被调用")
	}
}

// TestTokenForecastOffIsZeroAlloc 验证关闭时两个新增入口都是零分配的空操作。
func TestTokenForecastOffIsZeroAlloc(t *testing.T) {
	t.Setenv("ARGENT_TOKEN_FORECAST", "")
	s := newRunState(Options{Model: "m"}, "q", nil)
	allocs := testing.AllocsPerRun(100, func() {
		s.appendUsageHistory(100)
		s.maybeForecastCompact()
	})
	if allocs != 0 {
		t.Fatalf("关闭时新增入口分配了 %v 次对象，want 0", allocs)
	}
}

// TestTokenForecastOffIsByteIdentical 验证关闭时事件流与无预算的开启运行逐字节相同：
// 关闭不追加用量、不预测、不发事件；预算未设限时 WillExceedBudget 恒假，同样不改变行为。
func TestTokenForecastOffIsByteIdentical(t *testing.T) {
	drive := func() (Result, []Event) {
		chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
			return textReply("done"), nil
		}}
		var events []Event
		runner := &Runner{Opts: Options{
			Model: "m", Chatter: chat, Tools: &fakeTools{},
			Emit: func(e Event) { events = append(events, e) },
		}}
		res, err := runner.Run(context.Background(), "hi")
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		return res, events
	}

	t.Setenv("ARGENT_TOKEN_FORECAST", "")
	offRes, offEvents := drive()
	t.Setenv("ARGENT_TOKEN_FORECAST", "1")
	onRes, onEvents := drive()

	if !reflect.DeepEqual(offRes, onRes) {
		t.Fatalf("关闭与无预算开启的 Result 不同:\n got=%+v\nwant=%+v", onRes, offRes)
	}
	offJSON, err := json.Marshal(offEvents)
	if err != nil {
		t.Fatalf("marshal off events: %v", err)
	}
	onJSON, err := json.Marshal(onEvents)
	if err != nil {
		t.Fatalf("marshal on events: %v", err)
	}
	if string(offJSON) != string(onJSON) {
		t.Fatalf("关闭与无预算开启的事件流不是逐字节相同:\n got=%s\nwant=%s", onJSON, offJSON)
	}
	if got := len(forecastNotices(offEvents)); got != 0 {
		t.Fatalf("关闭时发出了 %d 条预测提示", got)
	}
	// 事件流基线：一次无工具作答恰好是 turn_start / turn_end / usage / stop。
	wantKinds := []EventKind{EventTurnStart, EventTurnEnd, EventUsage, EventStop}
	if len(offEvents) != len(wantKinds) {
		t.Fatalf("事件数 = %d, want %d: %+v", len(offEvents), len(wantKinds), offEvents)
	}
	for i, want := range wantKinds {
		if offEvents[i].Kind != want {
			t.Fatalf("事件 %d = %q, want %q", i, offEvents[i].Kind, want)
		}
	}
}

// toolPairMessages 检查每条助手工具调用都有同 id 的工具结果，且结果不会触发补全。
func toolPairMessages(messages []llm.Message) bool {
	if len(NormalizeToolPairing(messages)) != len(messages) {
		return false
	}
	results := make(map[string]bool)
	for _, m := range messages {
		if m.Role == "tool" {
			results[m.ToolCallID] = true
		}
	}
	for _, m := range messages {
		if m.Role != "assistant" {
			continue
		}
		for _, call := range m.ToolCalls {
			if !results[call.ID] {
				return false
			}
		}
	}
	return true
}

// TestTokenForecastEarlyCompactionPreservesToolPair 验证提前压缩发生在配对的轮末视图上，
// 不会拆散 tool_use/tool_result，且每次运行只触发一次。
func TestTokenForecastEarlyCompactionPreservesToolPair(t *testing.T) {
	t.Setenv("ARGENT_TOKEN_FORECAST", "1")

	compactor := &fakeCompactor{}
	s := newRunState(Options{
		Model:            "m",
		Budget:           Budget{MaxTotalTokens: 1200},
		CompactThreshold: 1,
		Compactor:        compactor,
	}, "q", nil)
	s.usage = llm.Usage{PromptTokens: 1000}
	s.usageHistory = []int{100, 200, 300, 400}
	s.messages = []llm.Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "q"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("c1", "read", `{}`)}},
		{Role: "tool", ToolCallID: "c1", Content: "ok"},
	}
	if !toolPairMessages(s.messages) {
		t.Fatal("测试前置的 tool_use/tool_result 对不配对")
	}

	s.maybeForecastCompact()
	if compactor.calls != 1 {
		t.Fatalf("提前压缩次数 = %d, want 1", compactor.calls)
	}
	if !s.forecastCompacted {
		t.Fatal("提前压缩后应置位 forecastCompacted")
	}
	if got := len(forecastNotices(s.eventsSnapshot())); got != 1 {
		t.Fatalf("预测提示数 = %d, want 1", got)
	}
	if !toolPairMessages(s.messages) {
		t.Fatalf("提前压缩破坏了 tool_use/tool_result 配对: %+v", s.messages)
	}

	// 同一趋势再次检查不得重复告警或压缩。
	s.maybeForecastCompact()
	if compactor.calls != 1 {
		t.Fatalf("重复检查后压缩次数 = %d, want 1", compactor.calls)
	}
	if got := len(forecastNotices(s.eventsSnapshot())); got != 1 {
		t.Fatalf("重复检查后预测提示数 = %d, want 1", got)
	}
}

// TestTokenForecastCoexistsWithPriorOptIns 验证 R38-R50 的历史 opt-in 与本开关正交共存。
func TestTokenForecastCoexistsWithPriorOptIns(t *testing.T) {
	t.Setenv("ARGENT_TURN_TIMING", "1")
	t.Setenv("ARGENT_RECOVERY_METRICS", "1")
	t.Setenv("ARGENT_TOOL_TIMING_HISTOGRAM", "1")
	t.Setenv("ARGENT_TOOL_CALL_TRACE", "1")
	t.Setenv("ARGENT_CONTEXT_HEALTH_LOG", "1")
	t.Setenv("ARGENT_COMPACTION_POLICY", "1")
	t.Setenv("ARGENT_TOKEN_FORECAST", "1")

	s := newRunState(Options{Model: "m", System: "s"}, "hi", nil)
	if s.turnTiming == nil || s.recovery == nil || s.toolTimingHistogram == nil ||
		s.toolCallTrace == nil || !s.contextHealthLog || !s.compactionPolicy || !s.tokenForecast {
		t.Fatalf("历史 opt-in 未全部共存: turnTiming=%v recovery=%v histogram=%v trace=%v contextHealthLog=%v compactionPolicy=%v tokenForecast=%v",
			s.turnTiming != nil, s.recovery != nil, s.toolTimingHistogram != nil,
			s.toolCallTrace != nil, s.contextHealthLog, s.compactionPolicy, s.tokenForecast)
	}
	if cap(s.usageHistory) != usageHistoryLimit {
		t.Fatalf("用量历史容量 = %d, want %d", cap(s.usageHistory), usageHistoryLimit)
	}
}
