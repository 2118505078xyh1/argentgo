package app

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestNewChatterFactorySelection 验证每种协议及别名都解析到正确的客户端类型，
// 并覆盖大小写、首尾空白、未知协议回退与显式 default 等情形。
func TestNewChatterFactorySelection(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		wantType string
	}{
		{"anthropic", "anthropic", "*llm.AnthropicClient"},
		{"anthropic alias messages", "anthropic-messages", "*llm.AnthropicClient"},
		{"anthropic alias short", "messages", "*llm.AnthropicClient"},
		{"responses", "responses", "*llm.ResponsesClient"},
		{"responses alias", "openai-responses", "*llm.ResponsesClient"},
		{"default", "default", "*llm.Client"},
		{"chat", "chat", "*llm.Client"},
		{"uppercase", "ANTHROPIC", "*llm.AnthropicClient"},
		{"mixed case", "OpenAI-Responses", "*llm.ResponsesClient"},
		{"leading and trailing space", "  messages  ", "*llm.AnthropicClient"},
		{"unknown falls back", "totally-unknown", "*llm.Client"},
		{"empty falls back", "", "*llm.Client"},
	}
	// 使用一个非空的选项，确保工厂把选项原样写入客户端。
	opts := llm.Options{CacheRetention: "policy", IncludeUsage: true}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newChatter(tt.protocol, "https://example.com/v1/", "key", opts)
			if typ := fmt.Sprintf("%T", got); typ != tt.wantType {
				t.Fatalf("client type = %s, want %s", typ, tt.wantType)
			}
			var gotOpts llm.Options
			switch c := got.(type) {
			case *llm.AnthropicClient:
				gotOpts = c.Options
			case *llm.ResponsesClient:
				gotOpts = c.Options
			case *llm.Client:
				gotOpts = c.Options
			}
			if !reflect.DeepEqual(gotOpts, opts) {
				t.Fatalf("Options = %+v, want %+v", gotOpts, opts)
			}
		})
	}
}

// TestAnthropicFactoryKeepsDefaultMaxTokens 锁定改造前的细节：
// Anthropic 客户端必须带上默认的 max_tokens。
func TestAnthropicFactoryKeepsDefaultMaxTokens(t *testing.T) {
	got := newChatter("anthropic", "https://example.com/v1", "key", llm.Options{})
	client, ok := got.(*llm.AnthropicClient)
	if !ok {
		t.Fatalf("client type = %T, want *llm.AnthropicClient", got)
	}
	if client.MaxTokens != llm.DefaultAnthropicMaxTokens {
		t.Fatalf("MaxTokens = %d, want %d", client.MaxTokens, llm.DefaultAnthropicMaxTokens)
	}
}
