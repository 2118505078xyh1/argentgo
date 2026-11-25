package agent

import (
	"context"
	"strings"
	"testing"

	"example.com/argentgo/internal/llm"
)

// 本文件守护 R43 callTool 的 panic 围栏：注入的非 Registry ToolRunner 发生 panic 时，
// 异常被 safeToolPanic 规范成普通工具错误，循环继续、事件与会话记录照常产生；
// 正常路径的事件、消息与记录保持逐字节不变。

// runWithToolPanicHarness 驱动一次两轮运行：第一轮请求名为 name 的工具，第二轮以文本收尾。
// 同时收集事件与 tool 记录，供三个测试共用。
func runWithToolPanicHarness(t *testing.T, tools ToolRunner, name string) (Result, []Event, map[string]string) {
	t.Helper()
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", name, `{}`)), nil
		}
		return textReply("done"), nil
	}}
	sink := &eventSink{}
	var toolRecord map[string]string
	runner := &Runner{Opts: Options{
		Model: "m", Chatter: chat, Tools: tools, MaxTurns: 3, Emit: sink.emit,
		Record: RecorderFunc(func(kind string, payload any) error {
			if kind == "tool" {
				toolRecord, _ = payload.(map[string]string)
			}
			return nil
		}),
	}}
	res, err := runner.Run(context.Background(), "prompt")
	if err != nil {
		t.Fatalf("Run 不应被工具 panic 击穿: %v", err)
	}
	sink.mu.Lock()
	events := append([]Event(nil), sink.events...)
	sink.mu.Unlock()
	return res, events, toolRecord
}

// disableToolResultToggles 关闭所有会改写工具结果的 opt-in，
// 使断言针对的是未经装饰的原始字节；返回的闭包在测试结束时恢复默认（关闭）状态。
func disableToolResultToggles(t *testing.T) func() {
	t.Helper()
	SetToolErrorReflection(false)
	SetToolResultWrapping(false)
	SetToolOutputConfirmation(false)
	return func() {
		SetToolErrorReflection(false)
		SetToolResultWrapping(false)
		SetToolOutputConfirmation(false)
	}
}

// TestCallToolPanicBecomesToolError 检查注入的 panic 工具被转成可读的工具错误，
// Run 不击穿，事件与会话记录照常产生。
func TestCallToolPanicBecomesToolError(t *testing.T) {
	defer disableToolResultToggles(t)()

	const panicValue = "boom: registry 之外的工具崩溃"
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}, run: func(context.Context, string, string) (string, error) {
		panic(panicValue)
	}}
	res, events, record := runWithToolPanicHarness(t, tools, "read")

	// Run 完整走完两轮并给出最终文本，未被 panic 击穿。
	if res.Text != "done" {
		t.Fatalf("最终文本 = %q, want done", res.Text)
	}
	got := lastToolContent(res.Messages)
	if !strings.HasPrefix(got, "error: tool panic (string): ") {
		t.Fatalf("工具结果 = %q, want 可读的 panic 错误", got)
	}
	if !strings.Contains(got, panicValue) {
		t.Fatalf("工具结果 = %q, want 保留 panic 值", got)
	}

	// tool_end 事件以错误结果正常结算，而非终止运行。
	found := false
	for _, e := range events {
		if e.Kind == EventToolEnd && e.IsError && strings.Contains(e.Output, "tool panic (string): ") {
			found = true
		}
	}
	if !found {
		t.Fatalf("没有错误的 tool_end 事件: %+v", events)
	}

	// 会话记录照常写入，is_error 为真。
	if record == nil || record["is_error"] != "true" || !strings.Contains(record["output"], "tool panic") {
		t.Fatalf("tool 记录 = %+v, want is_error=true 且含 panic 文本", record)
	}
	if tools.count() != 1 {
		t.Fatalf("工具调用次数 = %d, want 1", tools.count())
	}
}

// TestCallToolNormalPathByteIdentical 检查正常工具路径的模型消息、事件与会话记录
// 均为逐字节基线，panic 围栏不改动任何正常字节。
func TestCallToolNormalPathByteIdentical(t *testing.T) {
	defer disableToolResultToggles(t)()

	const want = "read ok: 逐字节基线"
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}, run: func(context.Context, string, string) (string, error) {
		return want, nil
	}}
	res, events, record := runWithToolPanicHarness(t, tools, "read")

	if got := lastToolContent(res.Messages); got != want {
		t.Fatalf("工具结果 = %q, want 逐字节基线 %q", got, want)
	}
	var end *Event
	for i := range events {
		if events[i].Kind == EventToolEnd {
			end = &events[i]
		}
	}
	if end == nil {
		t.Fatal("没有 tool_end 事件")
	}
	if end.Output != want || end.IsError {
		t.Fatalf("tool_end = %+v, want output=%q isError=false", end, want)
	}
	if record == nil || record["output"] != want || record["is_error"] != "false" {
		t.Fatalf("tool 记录 = %+v, want 逐字节基线", record)
	}
}

// TestCallToolNestedPanicRecover 检查工具内部的 panic/recover 不与围栏互相干扰：
// 内部自行 recover 的 panic 不影响正常结果；内部重新抛出的 panic 仍被围栏捕获。
func TestCallToolNestedPanicRecover(t *testing.T) {
	defer disableToolResultToggles(t)()

	t.Run("内部recover吞掉panic", func(t *testing.T) {
		const want = "inner recovered"
		tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}, run: func(_ context.Context, _ string, _ string) (out string, err error) {
			defer func() {
				if p := recover(); p != nil {
					out, err = want, nil
				}
			}()
			panic("inner boom")
		}}
		res, _, _ := runWithToolPanicHarness(t, tools, "read")
		if got := lastToolContent(res.Messages); got != want {
			t.Fatalf("工具结果 = %q, want %q（内部 recover 的结果不应被围栏改写）", got, want)
		}
	})

	t.Run("内部repanic被围栏捕获", func(t *testing.T) {
		tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}, run: func(context.Context, string, string) (string, error) {
			defer func() {
				if p := recover(); p != nil {
					panic("outer: " + p.(string))
				}
			}()
			panic("inner")
		}}
		res, _, record := runWithToolPanicHarness(t, tools, "read")
		got := lastToolContent(res.Messages)
		if !strings.Contains(got, "tool panic (string): outer: inner") {
			t.Fatalf("工具结果 = %q, want 含重新抛出的 panic 值", got)
		}
		if record == nil || record["is_error"] != "true" {
			t.Fatalf("tool 记录 = %+v, want is_error=true", record)
		}
	})
}
