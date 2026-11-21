package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAnthropicThinkingRequiresSignature 验证思考块的重放条件：只有同时
// 具备推理文本与签名时才发出思考块，且签名随块写入；缺少任一项时降级为
// 不发思考块，避免被要求签名的提供方以 400 拒绝。
func TestAnthropicThinkingRequiresSignature(t *testing.T) {
	client := &AnthropicClient{}
	client.Options.PreserveReasoning = true

	cases := []struct {
		name      string
		reasoning string
		signature string
		wantBlock bool
	}{
		{"both", "思考", "sig-1", true},
		{"no signature", "思考", "", false},
		{"no reasoning", "", "sig-1", false},
		{"neither", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := client.buildRequest("m", []Message{{
				Role:               "assistant",
				Content:            "答案",
				Reasoning:          tc.reasoning,
				ReasoningSignature: tc.signature,
			}}, nil)
			blocks := req.Messages[0].Content
			var thinking *anthropicBlock
			for i := range blocks {
				if blocks[i].Type == "thinking" {
					thinking = &blocks[i]
				}
			}
			if !tc.wantBlock {
				if thinking != nil {
					t.Fatalf("unexpected thinking block: %+v", thinking)
				}
				return
			}
			if thinking == nil {
				t.Fatalf("want thinking block, got %+v", blocks)
			}
			if thinking.Thinking != tc.reasoning {
				t.Fatalf("thinking = %q, want %q", thinking.Thinking, tc.reasoning)
			}
			if thinking.Signature != tc.signature {
				t.Fatalf("signature = %q, want %q", thinking.Signature, tc.signature)
			}
			raw, err := json.Marshal(thinking)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !strings.Contains(string(raw), `"signature":"sig-1"`) {
				t.Fatalf("thinking json = %s, want signature field", raw)
			}
		})
	}
}

// TestMessageSignatureOmittedWhenEmpty 确认 llm.Message 在未设置签名时
// 的序列化字节与添加该字段之前完全一致，而非空签名能正确往返。
func TestMessageSignatureOmittedWhenEmpty(t *testing.T) {
	raw, err := json.Marshal(Message{Role: "assistant", Content: "hi", Reasoning: "think"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "reasoning_signature") {
		t.Fatalf("message json = %s, want no reasoning_signature", raw)
	}

	in := Message{Role: "assistant", Content: "hi", Reasoning: "think", ReasoningSignature: "sig"}
	raw, err = json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Message
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ReasoningSignature != in.ReasoningSignature {
		t.Fatalf("round-trip signature = %q, want %q", out.ReasoningSignature, in.ReasoningSignature)
	}
}

// TestWireMessagesDefaultOmitsSignature 确保即使 Message 携带签名，
// 默认线协议字节也不会出现 reasoning_signature。
func TestWireMessagesDefaultOmitsSignature(t *testing.T) {
	got := wireMessages([]Message{{
		Role:               "assistant",
		Content:            "answer",
		Reasoning:          "think",
		ReasoningSignature: "sig",
	}}, false, nil, false)
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "signature") {
		t.Fatalf("default wire bytes contain signature: %s", raw)
	}
}
