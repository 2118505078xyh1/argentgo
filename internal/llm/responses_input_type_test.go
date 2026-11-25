package llm_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestResponsesInputMessagesCarryType 验证带类型分段的消息项携带 type:"message"。
// Responses API 以 type 判别项类型；content 使用 input_text/output_text 分段时
// 缺少该字段会被服务端拒绝，因此每条文本消息都必须显式声明它。
func TestResponsesInputMessagesCarryType(t *testing.T) {
	// 收集客户端发出的请求体，逐项检查 input 数组的判别字段。
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("解码请求体失败: %v", err)
		}
		writeSSE(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer server.Close()

	client := llm.NewResponses(server.URL, "")
	messages := []llm.Message{
		{Role: "system", Content: "简短回答"},
		{Role: "user", Content: "读文件"},
		{Role: "assistant", Content: "好的"},
	}
	if _, err := client.Chat(context.Background(), "gpt-test", messages, nil, nil); err != nil {
		t.Fatalf("chat 失败: %v", err)
	}

	input, ok := body["input"].([]any)
	if !ok || len(input) != 2 {
		t.Fatalf("input = %#v，期望两条消息项", body["input"])
	}
	wantRoles := []string{"user", "assistant"}
	for i, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("input[%d] = %#v，期望对象", i, raw)
		}
		if got := item["type"]; got != "message" {
			t.Errorf("input[%d].type = %#v，期望 message", i, got)
		}
		if got := item["role"]; got != wantRoles[i] {
			t.Errorf("input[%d].role = %#v，期望 %s", i, got, wantRoles[i])
		}
		parts, _ := item["content"].([]any)
		if len(parts) != 1 {
			t.Fatalf("input[%d].content = %#v，期望一个分段", i, item["content"])
		}
		part, _ := parts[0].(map[string]any)
		wantPartType := "input_text"
		if i == 1 {
			wantPartType = "output_text"
		}
		if got := part["type"]; got != wantPartType {
			t.Errorf("input[%d].content[0].type = %#v，期望 %s", i, got, wantPartType)
		}
	}
}

// TestResponsesFallbackInputMessageCarriesType 验证空输入时补发的 Continue 消息同样
// 带有 type:"message"，避免仅因触发兜底路径而生成服务端拒绝的请求。
func TestResponsesFallbackInputMessageCarriesType(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("解码请求体失败: %v", err)
		}
		writeSSE(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer server.Close()

	client := llm.NewResponses(server.URL, "")
	if _, err := client.Chat(context.Background(), "gpt-test", nil, nil, nil); err != nil {
		t.Fatalf("chat 失败: %v", err)
	}

	input, _ := body["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("input = %#v，期望一条兜底消息", body["input"])
	}
	item, _ := input[0].(map[string]any)
	if got := item["type"]; got != "message" {
		t.Fatalf("兜底消息 type = %#v，期望 message", got)
	}
	if got := item["role"]; got != "user" {
		t.Fatalf("兜底消息 role = %#v，期望 user", got)
	}
}
