package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// ErrCompactUnsupported 表示提供方不支持原生压缩（没有返回替换窗口）。
// 调用方应静默回退文本压缩，绝不把该错误暴露给用户。
var ErrCompactUnsupported = errors.New("llm: provider does not support native compaction")

// Compacter 是 Chatter 之上的可选能力。三协议客户端都实现它；
// 不支持原生压缩的第三方 Chatter 只需不实现该接口（见 compactWith）。
type Compacter interface {
	// Compact 请求提供方把对话压缩为服务端保留的窗口。返回的 Message 只在
	// NativeWindow 非空时携带替换窗口；不支持时返回 ErrCompactUnsupported。
	Compact(ctx context.Context, model string, messages []Message) (Message, error)
}

var (
	_ Compacter = (*Client)(nil)
	_ Compacter = (*AnthropicClient)(nil)
	_ Compacter = (*ResponsesClient)(nil)
)

// compactResponse 是三协议共享的最小压缩响应解码结构。提供方可能用
// native_window 或 replacement（opencode 风格）两种键返回替换窗口。
type compactResponse struct {
	NativeWindow []Message `json:"native_window,omitempty"`
	Replacement  []Message `json:"replacement,omitempty"`
	Error        *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (r compactResponse) window() []Message {
	if len(r.NativeWindow) > 0 {
		return r.NativeWindow
	}
	return r.Replacement
}

// postCompact 发送一次非流式压缩请求并解码响应。压缩是辅助调用，
// 不复用 Chat 的重试/流管线，也不改写任何常规请求的字节。
func postCompact(ctx context.Context, client *http.Client, endpoint string, headers map[string]string, payload any) (compactResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return compactResponse{}, fmt.Errorf("llm: encode compact request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return compactResponse{}, fmt.Errorf("llm: build compact request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	ApplyExtraHeaders(req)
	httpClient := client
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return compactResponse{}, fmt.Errorf("llm: compact request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return compactResponse{}, fmt.Errorf("llm: compact request failed: %s", RedactError(providerError(resp)))
	}
	var decoded compactResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return compactResponse{}, fmt.Errorf("llm: decode compact response: %w", err)
	}
	if decoded.Error != nil && decoded.Error.Message != "" {
		return compactResponse{}, fmt.Errorf("llm: compact provider error: %s", RedactText(decoded.Error.Message))
	}
	return decoded, nil
}

// nativeWindowMessage 把替换窗口封装为携带 NativeWindow 的助手消息。
// 窗口为空即视为提供方不支持，触发静默回退。
func nativeWindowMessage(model string, window []Message) (Message, error) {
	if len(window) == 0 {
		return Message{}, ErrCompactUnsupported
	}
	return Message{Role: "assistant", NativeWindow: window, CompactModel: model}, nil
}

// Compact 让 OpenAI 兼容 chat completions 端点执行原生压缩：stream:false
// 加上 compact 标记。响应中的替换窗口随 Message 返回，由上层持久化。
func (c *Client) Compact(ctx context.Context, model string, messages []Message) (Message, error) {
	headers := map[string]string{}
	if c.APIKey != "" {
		headers["Authorization"] = "Bearer " + c.APIKey
	}
	payload := struct {
		Model    string        `json:"model"`
		Messages []chatMessage `json:"messages"`
		Stream   bool          `json:"stream"`
		Compact  bool          `json:"compact"`
	}{
		Model:    model,
		Messages: wireMessages(messages, false, nil, c.Options.PreserveReasoning),
		Stream:   false,
		Compact:  true,
	}
	decoded, err := postCompact(ctx, c.HTTP, c.BaseURL+"/chat/completions", headers, payload)
	if err != nil {
		return Message{}, err
	}
	return nativeWindowMessage(model, decoded.window())
}

// Compact 让 Anthropic Messages 端点执行原生压缩：stream:false 加上 compact 标记。
func (c *AnthropicClient) Compact(ctx context.Context, model string, messages []Message) (Message, error) {
	headers := map[string]string{"anthropic-version": c.version()}
	if c.APIKey != "" {
		headers["x-api-key"] = c.APIKey
	}
	base := c.buildRequest(model, messages, nil)
	base.Stream = false
	payload := struct {
		anthropicRequest
		Compact bool `json:"compact"`
	}{anthropicRequest: base, Compact: true}
	decoded, err := postCompact(ctx, c.HTTP, c.endpoint(), headers, payload)
	if err != nil {
		return Message{}, err
	}
	return nativeWindowMessage(model, decoded.window())
}

// Compact 让 OpenAI Responses 端点执行原生压缩：stream:false 加上 compact 标记。
func (c *ResponsesClient) Compact(ctx context.Context, model string, messages []Message) (Message, error) {
	headers := map[string]string{}
	if c.APIKey != "" {
		headers["Authorization"] = "Bearer " + c.APIKey
	}
	base := c.buildRequest(model, messages, nil)
	base.Stream = false
	payload := struct {
		responsesRequest
		Compact bool `json:"compact"`
	}{responsesRequest: base, Compact: true}
	decoded, err := postCompact(ctx, c.HTTP, c.endpoint(), headers, payload)
	if err != nil {
		return Message{}, err
	}
	return nativeWindowMessage(model, decoded.window())
}

// compactWith 通过可选 Compacter 能力执行原生压缩。Chatter 未实现该接口、
// 提供方返回错误或没有替换窗口时返回 (nil, false)，调用方据此静默回退文本压缩。
func compactWith(ctx context.Context, chatter Chatter, model string, messages []Message) ([]Message, bool) {
	compacter, ok := chatter.(Compacter)
	if !ok {
		return nil, false
	}
	reply, err := compacter.Compact(ctx, model, messages)
	if err != nil || len(reply.NativeWindow) == 0 || reply.CompactModel != model {
		return nil, false
	}
	return reply.NativeWindow, true
}

// replayNativeWindow 在消息列表中找到与原模型匹配的原生替换窗口并展开。
// 没有窗口或模型不一致时原样返回，由调用方走文本压缩回退路径。
// 它只认可产生窗口时的同一模型，避免把加密/服务端状态错误的窗口发给别的模型。
func replayNativeWindow(model string, msgs []Message) []Message {
	for _, message := range msgs {
		if len(message.NativeWindow) > 0 && message.CompactModel == model {
			return append([]Message(nil), message.NativeWindow...)
		}
	}
	return msgs
}
