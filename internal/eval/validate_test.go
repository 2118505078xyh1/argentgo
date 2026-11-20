package eval

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateRejectsInvalidScriptedTurns 在边界覆盖脚本化步骤检查：无法产生可用回复的轮次必须在任何场景运行前被拒绝。
// 在边界：无法产生可用回复的轮次必须被拒绝
// 在任何场景运行之前。
func TestValidateRejectsInvalidScriptedTurns(t *testing.T) {
	tests := []struct {
		name string
		turn ScriptTurn
		want string
		ok   bool
	}{
		{
			name: "text-only turn is valid",
			turn: ScriptTurn{Respond: Respond{Text: "hello"}},
			ok:   true,
		},
		{
			name: "tool call without arguments is valid",
			turn: ScriptTurn{Respond: Respond{ToolCalls: []ToolCall{{Name: "ls"}}}},
			ok:   true,
		},
		{
			name: "object arguments are valid",
			turn: ScriptTurn{Respond: Respond{ToolCalls: []ToolCall{{
				Name: "read", Arguments: json.RawMessage(`{"path":"a"}`),
			}}}},
			ok: true,
		},
		{
			name: "one cache expectation is valid",
			turn: ScriptTurn{ExpectRequestCached: true, Respond: Respond{Text: "warm"}},
			ok:   true,
		},
		{
			name: "contradictory cache expectations",
			turn: ScriptTurn{
				ExpectRequestCached:    true,
				ExpectRequestCacheFree: true,
				Respond:                Respond{Text: "impossible"},
			},
			want: "mutually exclusive",
		},
		{
			name: "tool call with a blank name",
			turn: ScriptTurn{Respond: Respond{ToolCalls: []ToolCall{{Name: " \t "}}}},
			want: "empty name",
		},
		{
			name: "arguments that are an array",
			turn: ScriptTurn{Respond: Respond{ToolCalls: []ToolCall{{
				Name: "read", Arguments: json.RawMessage(`["a"]`),
			}}}},
			want: "arguments are not a JSON object",
		},
		{
			name: "arguments that are not JSON at all",
			turn: ScriptTurn{Respond: Respond{ToolCalls: []ToolCall{{
				Name: "read", Arguments: json.RawMessage(`oops`),
			}}}},
			want: "arguments are not a JSON object",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Scenario{Name: "case", Provider: []ScriptTurn{tt.turn}}.Validate()
			if tt.ok {
				if err != nil {
					t.Fatalf("Validate rejected a valid turn: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Validate accepted an invalid scripted turn")
			}
			if !errors.Is(err, ErrInvalidScript) {
				t.Errorf("Validate error %q does not wrap ErrInvalidScript", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Validate error %q does not mention %q", err, tt.want)
			}
		})
	}
}

// TestValidateReportsScenarioAndTurn 检查诊断上下文：
// TestValidateReportsScenarioAndTurn 检查诊断上下文：当文档省略 "name" 时使用生成的名称。
func TestValidateReportsScenarioAndTurn(t *testing.T) {
	scenario := Scenario{Provider: []ScriptTurn{
		{Respond: Respond{Text: "first"}},
		{Respond: Respond{ToolCalls: []ToolCall{{Name: "read"}}}},
		{Respond: Respond{ToolCalls: []ToolCall{{}}}},
	}}
	// 模拟 Parse 在验证之前所做的事。
	scenario.normalize(0)
	err := scenario.Validate()
	if err == nil {
		t.Fatal("Validate accepted an empty tool name in turn 3")
	}
	for _, want := range []string{`scenario "scenario 1"`, "turn 3", "tool call 1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate error %q does not mention %q", err, want)
		}
	}
}

// TestParseRejectsInvalidScriptedSteps 验证校验被接入场景加载，而不仅是作为独立辅助函数可用。
// 场景加载，而不仅是作为独立辅助函数可用。
func TestParseRejectsInvalidScriptedSteps(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want string
	}{
		{
			name: "blank tool name",
			doc:  `{"scenarios":[{"name":"bad","prompt":"p","provider":[{"respond":{"tool_calls":[{"name":"   "}]}}]}]}`,
			want: "empty name",
		},
		{
			name: "malformed arguments",
			doc:  `{"scenarios":[{"name":"bad","prompt":"p","provider":[{"respond":{"tool_calls":[{"name":"read","arguments":"oops"}]}}]}]}`,
			want: "arguments are not a JSON object",
		},
		{
			name: "contradictory cache expectations",
			doc:  `{"scenarios":[{"name":"bad","prompt":"p","provider":[{"expect_request_cached":true,"expect_request_cache_free":true,"respond":{"text":"x"}}]}]}`,
			want: "mutually exclusive",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.doc))
			if err == nil {
				t.Fatal("Parse accepted an invalid scripted step")
			}
			if !errors.Is(err, ErrInvalidScript) {
				t.Errorf("Parse error %q does not wrap ErrInvalidScript", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Parse error %q does not mention %q", err, tt.want)
			}
		})
	}
}

// TestParseAcceptsStringWrappedArguments 检查解包发生在
// TestParseAcceptsStringWrappedArguments 检查解包发生在验证之前：内含对象的 JSON 字符串是合法的脚本语法。
func TestParseAcceptsStringWrappedArguments(t *testing.T) {
	doc := `{"scenarios":[{"name":"ok","prompt":"p","provider":[
		{"respond":{"tool_calls":[{"name":"read","arguments":"{\"path\":\"a\"}"}]}}
	]}]}`
	if _, err := Parse([]byte(doc)); err != nil {
		t.Fatalf("Parse rejected string-wrapped arguments: %v", err)
	}
}

// TestLoadRejectsInvalidScriptedSteps 确认错误到达 Load 的调用方，
// TestLoadRejectsInvalidScriptedSteps 确认错误连同文档路径到达 Load 的调用方，使坏 fixture 在加载时而非套件中途被捕获。
// 加载时而非套件中途。
func TestLoadRejectsInvalidScriptedSteps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.json")
	doc := `{"scenarios":[{"name":"broken","prompt":"p","provider":[{"respond":{"tool_calls":[{"name":"read","arguments":[1]}]}}]}]}`
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load accepted an invalid scripted step")
	}
	if !errors.Is(err, ErrInvalidScript) {
		t.Errorf("Load error %q does not wrap ErrInvalidScript", err)
	}
	for _, want := range []string{path, `scenario "broken"`, "arguments are not a JSON object"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Load error %q does not mention %q", err, want)
		}
	}
}
