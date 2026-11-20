package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestToolTimingBracket 检查核心计算：工具的墙上时钟起点和终点成为事件时间戳，且经过时间完全匹配。记录的起点只被消费一次，
// 因此回放不会重复该区间。
// （接上行）
func TestToolTimingBracket(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	finish := start.Add(1500 * time.Millisecond)
	s := &runState{}

	s.startTool("c1", start)
	got := s.finishTool("c1", finish)
	if got.StartedAtMS != start.UnixMilli() {
		t.Fatalf("StartedAtMS = %d, want %d", got.StartedAtMS, start.UnixMilli())
	}
	if got.FinishedAtMS != finish.UnixMilli() {
		t.Fatalf("FinishedAtMS = %d, want %d", got.FinishedAtMS, finish.UnixMilli())
	}
	if got.Elapsed != 1500*time.Millisecond {
		t.Fatalf("Elapsed = %v, want 1.5s", got.Elapsed)
	}
	if again := s.finishTool("c1", finish); again != (eventTiming{}) {
		t.Fatalf("second finishTool = %+v, want zero (start must be consumed)", again)
	}
}

// TestReasoningTimingBracket 检查推理窗口：它返回真实的请求到回复区间并重置，之后的调用若没有起点则是安全的空操作，
// 而非编造的时长。
// （接上行）
func TestReasoningTimingBracket(t *testing.T) {
	start := time.Unix(1_700_000_100, 0)
	finish := start.Add(275 * time.Millisecond)
	s := &runState{}

	s.startReasoning(start)
	got := s.finishReasoning(finish)
	if got.StartedAtMS != start.UnixMilli() || got.FinishedAtMS != finish.UnixMilli() {
		t.Fatalf("reasoning bracket = %+v", got)
	}
	if got.Elapsed != 275*time.Millisecond {
		t.Fatalf("reasoning Elapsed = %v, want 275ms", got.Elapsed)
	}
	if again := s.finishReasoning(finish); again != (eventTiming{}) {
		t.Fatalf("finishReasoning without start = %+v, want zero", again)
	}
	// 倒置的时间对绝不会产生正的时长。
	s.startReasoning(finish)
	if inverted := s.finishReasoning(start); inverted.Elapsed != 0 {
		t.Fatalf("inverted reasoning Elapsed = %v, want 0", inverted.Elapsed)
	}
}

// TestTimingNilRunState 证明计时在零值 runState（许多测试手工构造的形态）上是空操作：映射被延迟创建，未知 id 或缺失的推理起点
// 都不会编造时间戳。
// （接上行）
func TestTimingNilRunState(t *testing.T) {
	var s runState
	now := time.Unix(1_700_000_200, 0)
	if got := s.finishTool("missing", now); got != (eventTiming{}) {
		t.Fatalf("finishTool on nil state = %+v, want zero", got)
	}
	if got := s.finishReasoning(now); got != (eventTiming{}) {
		t.Fatalf("finishReasoning on nil state = %+v, want zero", got)
	}
	s.startTool("c1", now)
	if got := s.finishTool("c1", now.Add(time.Second)); got.Elapsed != time.Second {
		t.Fatalf("lazy map Elapsed = %v, want 1s", got.Elapsed)
	}
}

// TestToolTimingConcurrentIndependent 让许多调用同时经过同一个状态。每个 id 必须保留自己的起点并产生自己的区间；之后映射必须为空。这是 timingMu 的竞态检测目标。
// （接上行）
// （接上行）
func TestToolTimingConcurrentIndependent(t *testing.T) {
	const n = 32
	s := &runState{}
	base := time.Unix(1_700_000_300, 0)
	errs := make(chan string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		i := i
		id := fmt.Sprintf("c%d", i)
		start := base.Add(time.Duration(i) * 10 * time.Millisecond)
		wantElapsed := time.Duration(i+1) * 7 * time.Millisecond
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.startTool(id, start)
			got := s.finishTool(id, start.Add(wantElapsed))
			if got.Elapsed != wantElapsed ||
				got.StartedAtMS != start.UnixMilli() ||
				got.FinishedAtMS != start.Add(wantElapsed).UnixMilli() {
				errs <- fmt.Sprintf("call %s bracket = %+v, want start=%d elapsed=%v", id, got, start.UnixMilli(), wantElapsed)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
	s.timingMu.Lock()
	left := len(s.toolTimes)
	s.timingMu.Unlock()
	if left != 0 {
		t.Fatalf("toolTimes has %d leftover entries, want 0", left)
	}
}

// TestTimingZeroValueKeepsEventJSON 守护附加字段的承诺：不带计时的事件序列化时不得出现任何新键，使现有 session/RPC JSON 逐字节不变。
// （接上行）
// （接上行）
func TestTimingZeroValueKeepsEventJSON(t *testing.T) {
	data, err := json.Marshal(Event{Kind: EventToolEnd, Turn: 3, ToolCallID: "c", DurationMS: 42})
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, key := range []string{"started_at_ms", "finished_at_ms", "StartedAtMS", "FinishedAtMS"} {
		if strings.Contains(text, key) {
			t.Fatalf("zero timing leaked into event JSON: %s", text)
		}
	}
}

// TestTimingFieldsPopulatedOnEmit 端到端验证发出接线：推理块和工具调用都在前端实际消费的事件上获得绝对的、有序的时间戳。
// （接上行）
// （接上行）
func TestTimingFieldsPopulatedOnEmit(t *testing.T) {
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
		t.Fatalf("reasoning_delta timing = %+v", reasoning)
	}
	if start == nil || start.StartedAtMS <= 0 {
		t.Fatalf("tool_start timing = %+v", start)
	}
	if end == nil || end.StartedAtMS <= 0 || end.FinishedAtMS < end.StartedAtMS {
		t.Fatalf("tool_end timing = %+v", end)
	}
}
