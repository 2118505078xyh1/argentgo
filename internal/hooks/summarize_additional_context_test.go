package hooks

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// TestSummarizeAggregatesAdditionalContexts 确认 Summarize 按 outcome 顺序聚合
// Outcome.AdditionalContext 的非空值，去重且原样保留 CJK 文本；原有决策字段不受影响。
func TestSummarizeAggregatesAdditionalContexts(t *testing.T) {
	result := Summarize([]Outcome{
		{Event: PostToolUse, Command: "a", AdditionalContext: "注意：只改 loop.go"},
		{Event: PostToolUse, Command: "b", AdditionalContext: ""},
		{Event: PreToolUse, Command: "c", ExitCode: 1, Stderr: "hook exploded", AdditionalContext: "记得跑 go vet"},
		{Event: PostToolUse, Command: "d", AdditionalContext: "注意：只改 loop.go"}, // 重复，应被丢弃
		{Event: PostToolUse, Command: "e", AdditionalContext: " 上下文带空白 "},
	})
	want := []string{"注意：只改 loop.go", "记得跑 go vet", " 上下文带空白 "}
	if !reflect.DeepEqual(result.AdditionalContexts, want) {
		t.Fatalf("AdditionalContexts = %q, want %q", result.AdditionalContexts, want)
	}
	if result.Blocked {
		t.Fatal("a non-blocking exit-1 outcome must not set Blocked")
	}
	if !strings.Contains(result.Message, "exit 1") || !strings.Contains(result.Message, "hook exploded") {
		t.Fatalf("summary lost the failure warning: %q", result.Message)
	}
}

// TestSummarizeNoAdditionalContextByteIdentical 确认没有附加上下文时新增字段保持 nil，
// 且拦截/续跑/警告等既有聚合结果逐字节不变。
func TestSummarizeNoAdditionalContextByteIdentical(t *testing.T) {
	result := Summarize([]Outcome{
		{Event: Stop, Command: "a", ExitCode: 2, Blocked: true, Continuation: "请继续", Stdout: "请继续"},
		{Event: PostToolUse, Command: "b", ExitCode: 1, Stderr: "失败"},
	})
	if result.AdditionalContexts != nil {
		t.Fatalf("AdditionalContexts = %q, want nil", result.AdditionalContexts)
	}
	if !result.Blocked || result.Continuation != "请继续" {
		t.Fatalf("existing decision fields changed: %+v", result)
	}
	if !strings.Contains(result.Message, "blocked") || !strings.Contains(result.Message, "exit 1") {
		t.Fatalf("summary lost existing warnings: %q", result.Message)
	}
}

// TestDispatchAggregatesAdditionalContexts 覆盖完整 hooks 侧路径：注入式 runner 的 stdout
// 声明 additionalContext，经 Outcome 产出收口提取后，由 Dispatch 聚合进 Result。
func TestDispatchAggregatesAdditionalContexts(t *testing.T) {
	config := &Config{Commands: []Command{
		{Event: PostToolUse, Command: "ctx-a"},
		{Event: PostToolUse, Command: "ctx-b"},
	}}
	dispatcher := &Dispatcher{Runner: func(_ context.Context, command Command, _ Payload) Outcome {
		stdout := `{"additionalContext":"上下文A"}`
		if command.Command == "ctx-b" {
			stdout = `{"additionalContext":"上下文B"}`
		}
		return Outcome{Event: command.Event, Command: command.Command, ExitCode: 0, Stdout: stdout}
	}}
	result, err := dispatcher.Dispatch(context.Background(), config, Payload{Event: string(PostToolUse)})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	want := []string{"上下文A", "上下文B"}
	if !reflect.DeepEqual(result.AdditionalContexts, want) {
		t.Fatalf("AdditionalContexts = %q, want %q", result.AdditionalContexts, want)
	}
}
