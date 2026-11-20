package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestToolNamesAreNormalizedBeforeDispatch 覆盖现有验证只检查了
// 一半的边界：validateTurn 接受带首尾空白的工具名，
// TestToolNamesAreNormalizedBeforeDispatch 覆盖现有验证只检查了一半的边界：validateTurn 接受带首尾空白的工具名（它检查 strings.TrimSpace(call.Name)），但
// 脚本化 provider 却把原始名称转发给工具 runner。真实工具 registry 做精确
// 脚本化 provider 把原始名称转发给工具 runner。真实工具 registry 做精确 map 查找，因此 " read " 在运行时变成"未知工具"，即使文档被接受为有效。规范名称是
// 查找，因此 " read " 在运行时变成未知工具，即使文档被接受为有效。规范名称是
// 裁剪后的那个；两个 provider 都必须分发验证所使用的同一值。
// inspected.
func TestToolNamesAreNormalizedBeforeDispatch(t *testing.T) {
	t.Run("validation accepts padded names", func(t *testing.T) {
		turn := ScriptTurn{Respond: Respond{ToolCalls: []ToolCall{{
			Name: " ls ", Arguments: json.RawMessage(`{"path":"."}`),
		}}}}
		if err := (Scenario{Name: "case", Provider: []ScriptTurn{turn}}).Validate(); err != nil {
			t.Fatalf("Validate rejected a padded tool name: %v", err)
		}
	})

	t.Run("scripted provider dispatches the trimmed name", func(t *testing.T) {
		provider := NewScripted([]ScriptTurn{{
			Respond: Respond{ToolCalls: []ToolCall{{
				Name: "  read  ", Arguments: json.RawMessage(`{"path":"a.txt"}`),
			}}},
		}}, false)
		reply, err := provider.Chat(context.Background(), "m", nil, nil, nil)
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if len(reply.ToolCalls) != 1 {
			t.Fatalf("tool calls = %+v", reply.ToolCalls)
		}
		if got := reply.ToolCalls[0].Function.Name; got != "read" {
			t.Fatalf("tool name = %q, want %q", got, "read")
		}
	})

	t.Run("orchestration provider dispatches the trimmed name", func(t *testing.T) {
		chatter := newOrchChatter("root", []OrchTurn{{
			Target:  orchTargetParent,
			Respond: Respond{ToolCalls: []ToolCall{{Name: "  task  "}}},
		}})
		messages := []llm.Message{
			{Role: "system", Content: "root"},
			{Role: "user", Content: "delegate"},
		}
		reply, err := chatter.Chat(context.Background(), "m", messages, nil, nil)
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if len(reply.ToolCalls) != 1 {
			t.Fatalf("tool calls = %+v", reply.ToolCalls)
		}
		if got := reply.ToolCalls[0].Function.Name; got != "task" {
			t.Fatalf("tool name = %q, want %q", got, "task")
		}
	})

	// 端到端：fakeTools 只认识 "ls"，因此未裁剪的名称会作为未知工具返回，期望的工具序列不匹配。
	// 作为未知工具返回，期望的工具序列不匹配。
	t.Run("a padded name still reaches the real tool through the runner", func(t *testing.T) {
		scenario := Scenario{
			Name:   "padded name",
			Prompt: "list",
			Provider: []ScriptTurn{
				{Respond: Respond{ToolCalls: []ToolCall{{
					Name: " ls ", Arguments: json.RawMessage(`{"path":"."}`),
				}}}},
				{Respond: Respond{Text: "listed"}},
			},
			Expect: Expect{
				ToolCalls:    []string{"ls"},
				TextContains: []string{"listed"},
				Turns:        intPtr(2),
			},
		}
		var out bytes.Buffer
		report, err := Run(context.Background(), []Scenario{scenario}, fakeRun, Options{Out: &out})
		if err != nil {
			t.Fatalf("Run: %v\n%s", err, out.String())
		}
		if report.Passed != 1 {
			t.Fatalf("report = %+v\n%s", report, out.String())
		}
	})
}
