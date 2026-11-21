package llm

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// reasoningMessages 是一段混合对话：只有助手轮次携带
// 推理，且其中一个助手轮次带工具调用而非文本。
func reasoningMessages() []Message {
	return []Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "answer", Reasoning: "思考过程"},
		{Role: "tool", ToolCallID: "c1", Content: "result"},
		{
			Role:      "assistant",
			ToolCalls: []ToolCall{{ID: "c1", Type: "function", Function: ToolCallFunction{Name: "read", Arguments: `{}`}}},
			Reasoning: "再想想",
		},
	}
}

// TestWireMessagesDefaultOmitsReasoning 锁定硬性要求：
// PreserveReasoning 关闭时，wireMessages 产生的 []chatMessage 字节
// 与推理字段出现之前完全一致。
func TestWireMessagesDefaultOmitsReasoning(t *testing.T) {
	messages := reasoningMessages()
	got := wireMessages(messages, false, nil, false)
	want := []chatMessage{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "answer"},
		{Role: "tool", ToolCallID: "c1", Content: "result"},
		{
			Role:      "assistant",
			ToolCalls: []ToolCall{{ID: "c1", Type: "function", Function: ToolCallFunction{Name: "read", Arguments: `{}`}}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wireMessages(default) = %#v, want %#v", got, want)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "reasoning") {
		t.Fatalf("default wire bytes contain reasoning: %s", raw)
	}
}

// TestWireMessagesPreserveReasoningOptIn 检查只有助手轮次获得
// reasoning_content，user/tool/system 轮次永远没有，且 CJK
// 推理文本被原样复制。
func TestWireMessagesPreserveReasoningOptIn(t *testing.T) {
	got := wireMessages(reasoningMessages(), false, nil, true)
	if got[2].ReasoningContent != "思考过程" {
		t.Fatalf("assistant reasoning_content = %q, want 思考过程", got[2].ReasoningContent)
	}
	if got[4].ReasoningContent != "再想想" {
		t.Fatalf("tool-assistant reasoning_content = %q, want 再想想", got[4].ReasoningContent)
	}
	for _, i := range []int{0, 1, 3} {
		if got[i].ReasoningContent != "" {
			t.Fatalf("message %d (%s) must not carry reasoning, got %q", i, got[i].Role, got[i].ReasoningContent)
		}
	}
}

// TestAnthropicBuildRequestReasoningOptIn 验证思考块只在
// PreserveReasoning 开启时发出，且默认请求与没有推理的构建
// 字节完全相同。
func TestAnthropicBuildRequestReasoningOptIn(t *testing.T) {
	messages := reasoningMessages()
	// 重放思考块现在要求同时具备推理文本与签名；为参与断言的助手轮次补上签名。
	messages[2].ReasoningSignature = "sig-1"
	messages[4].ReasoningSignature = "sig-2"
	client := &AnthropicClient{}

	defaultReq := client.buildRequest("m", messages, nil)
	defaultJSON, err := json.Marshal(defaultReq)
	if err != nil {
		t.Fatalf("marshal default: %v", err)
	}
	if strings.Contains(string(defaultJSON), "thinking") {
		t.Fatalf("default Anthropic request contains thinking: %s", defaultJSON)
	}

	client.Options.PreserveReasoning = true
	req := client.buildRequest("m", messages, nil)
	// 助手轮次是 req.Messages[1] 和 [3]；每个都必须以携带精确
	// 推理文本的思考块开头。
	for _, tc := range []struct {
		index int
		want  string
	}{{1, "思考过程"}, {3, "再想想"}} {
		blocks := req.Messages[tc.index].Content
		if len(blocks) == 0 || blocks[0].Type != "thinking" || blocks[0].Thinking != tc.want {
			t.Fatalf("assistant %d blocks = %+v, want leading thinking %q", tc.index, blocks, tc.want)
		}
	}
	for _, index := range []int{0, 2} {
		for _, block := range req.Messages[index].Content {
			if block.Type == "thinking" {
				t.Fatalf("message %d must not carry a thinking block", index)
			}
		}
	}
}
