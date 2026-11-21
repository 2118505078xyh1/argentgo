package llm

import (
	"bytes"
	"encoding/json"
	"testing"
)

// TestChatMessageMarshalJSONTextOnlyBaseline 锁定委托路径：Parts 为 nil 时
// 编码结果与新增旁路字段之前逐字节一致，且不会发出 parts 字段。
func TestChatMessageMarshalJSONTextOnlyBaseline(t *testing.T) {
	cases := []struct {
		name string
		msg  chatMessage
		want string
	}{
		{
			name: "simple",
			msg:  chatMessage{Role: "user", Content: "hi"},
			want: `{"role":"user","content":"hi"}`,
		},
		{
			name: "empty content",
			msg:  chatMessage{Role: "user"},
			want: `{"role":"user"}`,
		},
		{
			name: "all fields",
			msg: chatMessage{
				Role:    "assistant",
				Content: "answer",
				ToolCalls: []ToolCall{{
					ID: "c1", Type: "function",
					Function: ToolCallFunction{Name: "read", Arguments: `{}`},
				}},
				ReasoningContent: "think",
			},
			want: `{"role":"assistant","content":"answer","tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{}"}}],"reasoning_content":"think"}`,
		},
		{
			name: "cjk",
			msg:  chatMessage{Role: "user", Content: "你好，世界"},
			want: `{"role":"user","content":"你好，世界"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.msg)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("MarshalJSON = %s, want %s", got, tc.want)
			}
			if bytes.Contains(got, []byte("parts")) || bytes.Contains(got, []byte("Parts")) {
				t.Fatalf("text-only 编码不应发出 parts 字段: %s", got)
			}
		})
	}
}

// TestChatMessageMarshalJSONRawParts 确认有图时 content 是原始 part 数组，
// 而不是把 JSON 再字符串化的双重编码；其他字段依然保留。
func TestChatMessageMarshalJSONRawParts(t *testing.T) {
	msg := chatMessage{
		Role: "user",
		Parts: OpenAIContentParts("看这张图", []ImagePart{
			{MIMEType: "image/png", Data: []byte("x"), Detail: "high"},
		}),
		ReasoningContent: "think",
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"content":"[`)) {
		t.Fatalf("content 被二次编码为字符串（R17 回归）: %s", raw)
	}
	var decoded struct {
		Role             string          `json:"role"`
		Content          json.RawMessage `json:"content"`
		ReasoningContent string          `json:"reasoning_content"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Role != "user" || decoded.ReasoningContent != "think" {
		t.Errorf("其他字段丢失: role=%q reasoning=%q; raw=%s", decoded.Role, decoded.ReasoningContent, raw)
	}
	if len(decoded.Content) == 0 || decoded.Content[0] != '[' {
		t.Fatalf("content 应为数组，got %s", decoded.Content)
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(decoded.Content, &parts); err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || parts[0].Type != "text" || parts[0].Text != "看这张图" ||
		parts[1].Type != "image_url" || parts[1].ImageURL == nil ||
		parts[1].ImageURL.URL != "data:image/png;base64,eA==" {
		t.Fatalf("content part 解码异常: %+v; raw=%s", parts, raw)
	}
}

// TestWireMessagesContentPartsPropagation 验证 wireMessages 把 ContentParts
// 传入 chatMessage.Parts；纯文本消息仍保持 Parts 为 nil。
func TestWireMessagesContentPartsPropagation(t *testing.T) {
	imageMsg := BuildImageMessage("看这张图", []ImageInput{
		{MimeType: "image/png", Data: []byte("x")},
	})
	got := wireMessages([]Message{
		{Role: "system", Content: "rules"},
		imageMsg,
	}, false, nil)
	if got[0].Parts != nil {
		t.Fatalf("纯文本消息不应携带 Parts: %s", got[0].Parts)
	}
	if !bytes.Equal(got[1].Parts, imageMsg.ContentParts) {
		t.Fatalf("Parts 未传递 ContentParts: got %s, want %s", got[1].Parts, imageMsg.ContentParts)
	}
	if got[1].Content != "" {
		t.Fatalf("带图消息的 wire Content 必须为空: %q", got[1].Content)
	}
}

// TestChatRequestTextOnlyWireBytesUnchanged 锁定完整 chatRequest 的
// text-only 线上字节：新增旁路字段与 MarshalJSON 不得改变既有请求形态。
func TestChatRequestTextOnlyWireBytesUnchanged(t *testing.T) {
	req := chatRequest{
		Model: "m",
		Messages: wireMessages([]Message{
			{Role: "system", Content: "rules"},
			{Role: "user", Content: "hi"},
		}, false, nil),
		Stream: true,
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"m","messages":[{"role":"system","content":"rules"},{"role":"user","content":"hi"}],"stream":true}`
	if string(raw) != want {
		t.Fatalf("text-only 请求字节变化: got %s, want %s", raw, want)
	}
}

// TestChatRequestImageContentIsArray 从完整 chatRequest 验证带图消息在线上
// 是 content 数组，且 CJK 文本、base64 data URL 都原样到达提供方。
func TestChatRequestImageContentIsArray(t *testing.T) {
	req := chatRequest{
		Model: "m",
		Messages: wireMessages([]Message{
			BuildImageMessage("山色空蒙雨亦奇 🏞", []ImageInput{
				{MimeType: "image/png", Data: []byte("hello"), Detail: "high"},
			}),
		}, false, nil),
		Stream: true,
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"content":"[`)) {
		t.Fatalf("带图请求被二次编码: %s", raw)
	}
	var decoded struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Messages) != 1 || decoded.Messages[0].Content[0] != '[' {
		t.Fatalf("线上 content 不是数组: %s", raw)
	}
	for _, want := range []string{"山色空蒙雨亦奇 🏞", "data:image/png;base64,aGVsbG8=", `"detail":"high"`} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Errorf("线上请求缺少 %q: %s", want, raw)
		}
	}
}

// TestProtocolsDegradeInvalidImageParts 验证 Anthropic 与 Responses 在
// ContentParts 无法解析为有效图片时都退化为占位文本：既不静默丢弃整条消息，
// 也不把可疑的 data URL 误当文本发给提供方。
// 正常的带图消息现由各协议以原生形态承载，另有各自的 wire 测试覆盖。
func TestProtocolsDegradeInvalidImageParts(t *testing.T) {
	bad := Message{Role: "user", ContentParts: json.RawMessage(`[{"type":"image_url","image_url":{"url":"not-a-data-url"}}]`)}

	anthropicRaw, err := json.Marshal((&AnthropicClient{}).buildRequest("m", []Message{bad}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(anthropicRaw, []byte(unsupportedImagePlaceholder)) {
		t.Errorf("Anthropic 请求缺少占位文本: %s", anthropicRaw)
	}
	if bytes.Contains(anthropicRaw, []byte("data:image/png;base64")) {
		t.Errorf("Anthropic 请求不应携带图像的 base64: %s", anthropicRaw)
	}

	responsesRaw, err := json.Marshal((&ResponsesClient{}).buildRequest("m", []Message{bad}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(responsesRaw, []byte(unsupportedImagePlaceholder)) {
		t.Errorf("Responses 请求缺少占位文本: %s", responsesRaw)
	}
	if bytes.Contains(responsesRaw, []byte("data:image/png;base64")) {
		t.Errorf("Responses 请求不应携带图像的 base64: %s", responsesRaw)
	}
}
