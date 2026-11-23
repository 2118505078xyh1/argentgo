package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// loopWireResult 是一次循环检测接线场景的可观测结果。
type loopWireResult struct {
	messages  []llm.Message
	events    []Event
	toolRuns  int
	stopped   string
	loopHints int
	recovery  int
}

// runLoopWire 驱动一段固定脚本：前 toolRounds 个请求各发出一次相同工具的调用，
// 之后返回一个纯文本答案让循环干净结束。
//   - detectOn 控制 SetLoopDetection；
//   - outputMode 为 "const" 时工具每次返回相同输出，"vary" 时每次返回不同输出；
//   - maxRepeats 透传给选项，用于验证与重复防护共存；
//   - failAll 为 true 时工具每次都失败（推动 mistake 指引）。
//
// 本助手刻意不调用 t.Parallel：它切换进程级 opt-in 全局。
func runLoopWire(t *testing.T, detectOn bool, toolRounds int, outputMode string, maxRepeats int, failAll bool) loopWireResult {
	t.Helper()
	SetLoopDetection(detectOn)
	t.Cleanup(func() { SetLoopDetection(true) })
	SetGuidanceDedupe(true)
	t.Cleanup(func() { SetGuidanceDedupe(false) })

	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n <= toolRounds {
			return toolReply(toolCall(fmt.Sprintf("c%d", n), "probe", `{"k":"v"}`)), nil
		}
		return textReply("done"), nil
	}}

	var mu sync.Mutex
	callNo := 0
	ft := &fakeTools{
		specs: []llm.ToolSpec{toolSpec("probe")},
		run: func(context.Context, string, string) (string, error) {
			mu.Lock()
			callNo++
			n := callNo
			mu.Unlock()
			if failAll {
				return "", errors.New("boom")
			}
			if outputMode == "vary" {
				return fmt.Sprintf("result-%d", n), nil
			}
			return "result", nil
		},
	}

	s := newRunState(Options{
		Model:      "test",
		Chatter:    chat,
		Tools:      ft,
		MaxTurns:   24,
		MaxRepeats: maxRepeats,
	}, "go", nil)
	s.mistakes = NewMistakeTracker(2, func(MistakeKind, int) string { return "RECOVERY" })

	res, err := s.loop(context.Background())
	if err != nil {
		t.Fatalf("loop() error: %v", err)
	}
	out := loopWireResult{
		messages: res.Messages,
		events:   s.eventsSnapshot(),
		toolRuns: ft.count(),
		stopped:  res.Stopped,
	}
	for _, m := range out.messages {
		if m.Role != "user" {
			continue
		}
		switch m.Content {
		case loopDetectionGuidance:
			out.loopHints++
		case "RECOVERY":
			out.recovery++
		}
	}
	return out
}

// jsonBytes 把值序列化为稳定字节，供逐字节比较使用。
func jsonBytes(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// eventKinds 抽取事件种类序列；种类不受墙上时钟影响，可做确定性比较。
func eventKinds(events []Event) []EventKind {
	out := make([]EventKind, len(events))
	for i, e := range events {
		out[i] = e.Kind
	}
	return out
}

// TestToolStepsFromEvents 验证收集器只取 tool_end 事件，并把参数规范化后投影为 ToolStep。
func TestToolStepsFromEvents(t *testing.T) {
	events := []Event{
		{Kind: EventToolStart, ToolCallID: "a", ToolName: "read", Args: `{"b":2,"a":1}`},
		{Kind: EventNotice, Text: "steering: 提示"},
		{Kind: EventToolEnd, ToolCallID: "a", ToolName: "read", Args: `{"a":1,"b":2}`, Output: "内容"},
		{Kind: EventToolEnd, ToolCallID: "b", ToolName: "shell", Args: `{"x":1}`, Output: "ok"},
	}
	got := toolStepsFromEvents(events)
	want := []ToolStep{
		{Name: "read", Input: NormalizeArgs(`{"a":1,"b":2}`), Output: "内容"},
		{Name: "shell", Input: NormalizeArgs(`{"x":1}`), Output: "ok"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("toolStepsFromEvents() = %#v, want %#v", got, want)
	}
}

// TestLoopDetectionWireInjectsGuidanceOnRepeatedCalls 验证 opt-in 开启时，
// 同一工具以相同参数反复返回相同结果会注入一条模型可读提示，且不硬停。
func TestLoopDetectionWireInjectsGuidanceOnRepeatedCalls(t *testing.T) {
	out := runLoopWire(t, true, 6, "const", 100, false)
	if out.stopped != StopComplete {
		t.Fatalf("Stopped = %q, want %q（软提示不应硬停）", out.stopped, StopComplete)
	}
	if out.loopHints != 1 {
		t.Fatalf("循环提示条数 = %d, want 1（按签名去重，每条循环只提示一次）", out.loopHints)
	}
	// 提示必须作为用户轮次进入模型可见的记录。
	if !containsUser(out.messages, loopDetectionGuidance) {
		t.Fatal("模型可见记录中缺少循环检测提示")
	}
}

// TestLoopDetectionWireNoFalsePositiveOnVaryingOutput 验证输出随调用变化时不误报：
// 同名同参但结果不同的调用属于有进展，不应注入循环提示。
func TestLoopDetectionWireNoFalsePositiveOnVaryingOutput(t *testing.T) {
	out := runLoopWire(t, true, 6, "vary", 100, false)
	if out.loopHints != 0 {
		t.Fatalf("输出变化时仍注入了 %d 条循环提示，want 0", out.loopHints)
	}
}

// TestLoopDetectionWireOffByteForByteBaseline 验证 opt-in 关闭（默认）时逐字节不变：
// 同一场景两次运行的消息与事件序列完全一致，且不含任何循环检测产物。
func TestLoopDetectionWireOffByteForByteBaseline(t *testing.T) {
	first := runLoopWire(t, false, 6, "const", 100, false)
	second := runLoopWire(t, false, 6, "const", 100, false)

	if !bytes.Equal(jsonBytes(t, first.messages), jsonBytes(t, second.messages)) {
		t.Fatal("关闭时消息记录两次运行不一致，出现非确定性改动")
	}
	if !reflect.DeepEqual(eventKinds(first.events), eventKinds(second.events)) {
		t.Fatal("关闭时事件序列两次运行不一致")
	}
	if first.loopHints != 0 || second.loopHints != 0 {
		t.Fatal("关闭时不应注入循环提示")
	}
	if bytes.Contains(jsonBytes(t, first.messages), []byte(loopDetectionGuidance)) {
		t.Fatal("关闭时的消息记录中混入了循环检测提示")
	}
}

// TestLoopDetectionWireCoexistsWithDedupeAndGuard 验证循环检测与既有的失败指引去重、
// 重复防护共存：开启循环检测不改变它们的既有行为，只额外贡献自己的软提示。
func TestLoopDetectionWireCoexistsWithDedupeAndGuard(t *testing.T) {
	// （一）与失败指引去重共存：同样全部失败的场景，开/关循环检测时 RECOVERY 条数与
	// 实际工具执行次数完全一致；只有开启侧额外出现循环提示。
	off := runLoopWire(t, false, 7, "const", 100, true)
	on := runLoopWire(t, true, 7, "const", 100, true)
	if off.recovery != 1 || on.recovery != 1 {
		t.Fatalf("失败指引去重被破坏：off=%d on=%d, want 1/1", off.recovery, on.recovery)
	}
	if off.toolRuns != on.toolRuns {
		t.Fatalf("工具执行次数被改变：off=%d on=%d", off.toolRuns, on.toolRuns)
	}
	if on.loopHints == 0 {
		t.Fatal("开启侧未注入循环提示")
	}
	if off.loopHints != 0 {
		t.Fatal("关闭侧不应注入循环提示")
	}

	// （二）与重复防护共存：MaxRepeats=2 时防护仍把实际执行限制在 2 次，
	// 循环检测在这批被阻断的相同错误结果上触发软提示，但不会硬停运行。
	guard := runLoopWire(t, true, 10, "const", 2, false)
	if guard.toolRuns != 2 {
		t.Fatalf("重复防护失效：实际执行 %d 次, want 2", guard.toolRuns)
	}
	if guard.loopHints == 0 {
		t.Fatal("被防护阻断的重复调用同样应触发循环提示")
	}
	if guard.stopped != StopComplete {
		t.Fatalf("Stopped = %q, want %q", guard.stopped, StopComplete)
	}
}
