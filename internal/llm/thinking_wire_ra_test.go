package llm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestResponsesBuildRequestThinking 锁定 Responses 推理控制的线格式：
// Options.Thinking 为 nil 时请求字节不含 reasoning；Effort 非空时
// 携带 reasoning.effort，且字段值原样透传。
func TestResponsesBuildRequestThinking(t *testing.T) {
	client := &ResponsesClient{}
	messages := []Message{{Role: "user", Content: "hi"}}

	// 默认请求不得出现 reasoning 字段。
	defaultReq := client.buildRequest("m", messages, nil)
	defaultJSON, err := json.Marshal(defaultReq)
	if err != nil {
		t.Fatalf("marshal default: %v", err)
	}
	if strings.Contains(string(defaultJSON), `"reasoning"`) {
		t.Fatalf("default Responses request contains reasoning: %s", defaultJSON)
	}
	if defaultReq.Reasoning != nil {
		t.Fatalf("default Reasoning = %+v, want nil", defaultReq.Reasoning)
	}

	// Effort 非空时携带命名推理级别。
	client.Options.Thinking = &ThinkingConfig{Effort: "high"}
	req := client.buildRequest("m", messages, nil)
	if req.Reasoning == nil || req.Reasoning.Effort != "high" {
		t.Fatalf("Reasoning = %+v, want effort=high", req.Reasoning)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal thinking: %v", err)
	}
	if !strings.Contains(string(raw), `"reasoning":{"effort":"high"}`) {
		t.Fatalf("thinking request missing reasoning.effort: %s", raw)
	}

	// Effort 为空时仍不发送，保持字节不变。
	client.Options.Thinking = &ThinkingConfig{}
	req = client.buildRequest("m", messages, nil)
	if req.Reasoning != nil {
		t.Fatalf("empty Effort must omit reasoning, got %+v", req.Reasoning)
	}
}

// TestAnthropicBuildRequestThinking 锁定 Anthropic 扩展思考的线格式：
// 默认请求不含 thinking 顶层字段；预算可用时携带 type=enabled 与
// 钳制后的 budget_tokens。
func TestAnthropicBuildRequestThinking(t *testing.T) {
	client := &AnthropicClient{MaxTokens: 8192}
	messages := []Message{{Role: "user", Content: "hi"}}

	// 默认请求不得出现顶层 thinking 字段。
	defaultReq := client.buildRequest("m", messages, nil)
	defaultJSON, err := json.Marshal(defaultReq)
	if err != nil {
		t.Fatalf("marshal default: %v", err)
	}
	if strings.Contains(string(defaultJSON), `"thinking"`) {
		t.Fatalf("default Anthropic request contains thinking: %s", defaultJSON)
	}
	if defaultReq.Thinking != nil {
		t.Fatalf("default Thinking = %+v, want nil", defaultReq.Thinking)
	}

	// 预算可用时携带启用配置。
	client.Options.Thinking = &ThinkingConfig{BudgetTokens: 2000}
	req := client.buildRequest("m", messages, nil)
	if req.Thinking == nil || req.Thinking.Type != "enabled" || req.Thinking.BudgetTokens != 2000 {
		t.Fatalf("Thinking = %+v, want enabled/2000", req.Thinking)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal thinking: %v", err)
	}
	if !strings.Contains(string(raw), `"thinking":{"type":"enabled","budget_tokens":2000}`) {
		t.Fatalf("thinking request missing thinking config: %s", raw)
	}
}

// TestAnthropicThinkingBudgetClamp 验证预算钳制边界 [1024, maxTokens-1]：
// 过大值被钳到 maxTokens-1，过小值被抬到 1024，maxTokens 太小时不发送。
func TestAnthropicThinkingBudgetClamp(t *testing.T) {
	cases := []struct {
		name       string
		maxTokens  int
		budget     int64
		configured bool
		want       int64
	}{
		{name: "nil config", maxTokens: 8192, configured: false, want: 0},
		{name: "zero budget", maxTokens: 8192, budget: 0, configured: true, want: 0},
		{name: "negative budget", maxTokens: 8192, budget: -1, configured: true, want: 0},
		{name: "clamp down to maxTokens-1", maxTokens: 1500, budget: 200000, configured: true, want: 1499},
		{name: "clamp up to 1024", maxTokens: 8192, budget: 16, configured: true, want: 1024},
		{name: "exact lower bound", maxTokens: 8192, budget: 1024, configured: true, want: 1024},
		{name: "maxTokens too small", maxTokens: 1024, budget: 1500, configured: true, want: 0},
		{name: "maxTokens tiny", maxTokens: 100, budget: 1500, configured: true, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &AnthropicClient{MaxTokens: tc.maxTokens}
			if tc.configured {
				client.Options.Thinking = &ThinkingConfig{BudgetTokens: tc.budget}
			}
			if got := client.thinkingBudget(); got != tc.want {
				t.Fatalf("thinkingBudget() = %d, want %d", got, tc.want)
			}
			// budget 为 0 时 buildRequest 不得发送 thinking。
			req := client.buildRequest("m", []Message{{Role: "user", Content: "hi"}}, nil)
			if tc.want == 0 && req.Thinking != nil {
				t.Fatalf("budget 0 must omit thinking, got %+v", req.Thinking)
			}
			if tc.want > 0 && (req.Thinking == nil || req.Thinking.BudgetTokens != tc.want) {
				t.Fatalf("Thinking = %+v, want budget %d", req.Thinking, tc.want)
			}
		})
	}
}

// TestAnthropicSignatureCapture 验证思考签名从 content_block_start 的
// 完整签名与后续 signature_delta 分片中被累积，并写入回复的
// ReasoningSignature 字段。
func TestAnthropicSignatureCapture(t *testing.T) {
	const script = `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":"sig_start_"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"inner thought"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"delta_1_"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"delta_2"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`
	client := &AnthropicClient{}
	msg, err := client.readStream(context.Background(), strings.NewReader(script), nil)
	if err != nil {
		t.Fatalf("readStream: %v", err)
	}
	if msg.Reasoning != "inner thought" {
		t.Fatalf("Reasoning = %q, want inner thought", msg.Reasoning)
	}
	const wantSignature = "sig_start_delta_1_delta_2"
	if msg.ReasoningSignature != wantSignature {
		t.Fatalf("ReasoningSignature = %q, want %q", msg.ReasoningSignature, wantSignature)
	}
}
