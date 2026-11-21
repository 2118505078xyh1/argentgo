package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// responsesTestItem 是解码 responsesRequest.input 中 message 项的最小镜像。
type responsesTestItem struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL string `json:"image_url"`
	} `json:"content"`
	CallID string `json:"call_id"`
	Name   string `json:"name"`
	Output string `json:"output"`
}

// marshalResponsesInput 把 buildRequest 的结果编码后再解出 input 项，
// 便于逐字段检查带图消息的 wire 结构。
func marshalResponsesInput(t *testing.T, req responsesRequest) []responsesTestItem {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var top struct {
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	var items []responsesTestItem
	if err := json.Unmarshal(top.Input, &items); err != nil {
		t.Fatalf("unmarshal input: %v", err)
	}
	return items
}

// TestResponsesImageWireStructure 覆盖单图、多图、图文混合与 CJK+图四种形态，
// 验证 user 消息的 content 是 Responses 原生分段：input_text 后跟 input_image，
// image_url 是直接的 base64 data URL 字符串而非嵌套对象。
func TestResponsesImageWireStructure(t *testing.T) {
	cases := []struct {
		name      string
		message   Message
		wantText  string
		wantURLs  []string
		wantOrder []string // content 分段的类型序列
	}{
		{
			name: "单图",
			message: BuildImageMessage("看这张图", []ImageInput{
				{MimeType: "image/png", Data: []byte("x")},
			}),
			wantText:  "看这张图",
			wantURLs:  []string{"data:image/png;base64,eA=="},
			wantOrder: []string{"input_text", "input_image"},
		},
		{
			name: "多图",
			message: BuildImageMessage("两张图", []ImageInput{
				{MimeType: "image/png", Data: []byte("hello")},
				{MimeType: "image/jpeg", Data: []byte("world")},
			}),
			wantText: "两张图",
			wantURLs: []string{
				"data:image/png;base64,aGVsbG8=",
				"data:image/jpeg;base64,d29ybGQ=",
			},
			wantOrder: []string{"input_text", "input_image", "input_image"},
		},
		{
			name: "图文混合保留顺序",
			message: Message{Role: "user", ContentParts: json.RawMessage(`[
				{"type":"text","text":"前"},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,eA=="}},
				{"type":"text","text":"后"},
				{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,eA=="}}
			]`)},
			wantText:  "后",
			wantURLs:  []string{"data:image/png;base64,eA==", "data:image/jpeg;base64,eA=="},
			wantOrder: []string{"input_text", "input_image", "input_text", "input_image"},
		},
		{
			name: "CJK 与 emoji 带图",
			message: BuildImageMessage("山色空蒙雨亦奇 🏞", []ImageInput{
				{MimeType: "image/png", Data: []byte("hi")},
			}),
			wantText:  "山色空蒙雨亦奇 🏞",
			wantURLs:  []string{"data:image/png;base64,aGk="},
			wantOrder: []string{"input_text", "input_image"},
		},
		{
			name: "MIME 规范化与空文本",
			message: BuildImageMessage("", []ImageInput{
				{MimeType: "IMAGE/PNG", Data: []byte("\x01\x02")},
			}),
			wantText:  "",
			wantURLs:  []string{"data:image/png;base64,AQI="},
			wantOrder: []string{"input_image"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := (&ResponsesClient{}).buildRequest("m", []Message{tc.message}, nil)
			items := marshalResponsesInput(t, req)
			if len(items) != 1 {
				t.Fatalf("input 项数 = %d, want 1: %+v", len(items), items)
			}
			item := items[0]
			if item.Type != "message" || item.Role != "user" {
				t.Fatalf("消息项 type/role = %q/%q, want message/user", item.Type, item.Role)
			}
			if len(item.Content) != len(tc.wantOrder) {
				t.Fatalf("content 分段数 = %d, want %d: %+v", len(item.Content), len(tc.wantOrder), item.Content)
			}
			var gotURLs []string
			for i, part := range item.Content {
				if part.Type != tc.wantOrder[i] {
					t.Errorf("content[%d].type = %q, want %q", i, part.Type, tc.wantOrder[i])
				}
				if part.Type == "input_image" {
					gotURLs = append(gotURLs, part.ImageURL)
				}
			}
			if len(gotURLs) != len(tc.wantURLs) {
				t.Fatalf("图片分段数 = %d, want %d: %v", len(gotURLs), len(tc.wantURLs), gotURLs)
			}
			for i, want := range tc.wantURLs {
				if gotURLs[i] != want {
					t.Errorf("image_url[%d] = %q, want %q", i, gotURLs[i], want)
				}
			}
			// 文本 part 必须保留原文（混合场景检查最后一个文本段）。
			if tc.wantText != "" {
				var found bool
				for _, part := range item.Content {
					if part.Type == "input_text" && part.Text == tc.wantText {
						found = true
					}
				}
				if !found {
					t.Errorf("缺少文本分段 %q: %+v", tc.wantText, item.Content)
				}
			}
			// image_url 必须是字符串，不能被编码成 {"url": ...} 嵌套对象。
			raw, _ := json.Marshal(req)
			if strings.Contains(string(raw), `"image_url":{`) {
				t.Errorf("image_url 被编码为嵌套对象: %s", raw)
			}
		})
	}
}

// TestResponsesNoImageByteBaseline 锁定无图请求的线上字节：
// 新增的图像映射不改变任何既有纯文本请求，包括兜底 Continue 路径。
func TestResponsesNoImageByteBaseline(t *testing.T) {
	cases := []struct {
		name     string
		messages []Message
		want     string
	}{
		{
			name: "system 与 user",
			messages: []Message{
				{Role: "system", Content: "rules"},
				{Role: "user", Content: "hi"},
			},
			want: `{"model":"m","instructions":"rules","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"max_output_tokens":8192,"stream":true,"store":false}`,
		},
		{
			name:     "空输入兜底",
			messages: nil,
			want:     `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Continue."}]}],"max_output_tokens":8192,"stream":true,"store":false}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 关闭提示缓存，避免缓存键字段干扰纯文本字节基线的对比。
			client := &ResponsesClient{Options: Options{NoPromptCache: true}}
			raw, err := json.Marshal(client.buildRequest("m", tc.messages, nil))
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != tc.want {
				t.Fatalf("无图请求字节变化:\n got  %s\n want %s", raw, tc.want)
			}
		})
	}
}

// TestResponsesInvalidImagePartsDegrade 覆盖非法 data URL、非法 JSON、
// 非图片 part 与缺失 image_url：都必须退化为占位文本，绝不把可疑内容发给提供方。
func TestResponsesInvalidImagePartsDegrade(t *testing.T) {
	cases := []struct {
		name  string
		parts json.RawMessage
	}{
		{"非法 JSON", json.RawMessage(`{bad`)},
		{"非图片 part", json.RawMessage(`[{"type":"unknown"}]`)},
		{"缺失 image_url", json.RawMessage(`[{"type":"image_url"}]`)},
		{"非 data URL", json.RawMessage(`[{"type":"image_url","image_url":{"url":"https://example.com/x.png"}}]`)},
		{"非法 base64", json.RawMessage(`[{"type":"image_url","image_url":{"url":"data:image/png;base64,@@@"}}]`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := (&ResponsesClient{}).buildRequest("m", []Message{{Role: "user", ContentParts: tc.parts}}, nil)
			items := marshalResponsesInput(t, req)
			if len(items) != 1 || len(items[0].Content) != 1 {
				t.Fatalf("降级后应只有一条文本分段: %+v", items)
			}
			if got := items[0].Content[0]; got.Type != "input_text" || got.Text != unsupportedImagePlaceholder {
				t.Fatalf("降级分段 = %+v, want input_text/%q", got, unsupportedImagePlaceholder)
			}
			raw, _ := json.Marshal(req)
			if strings.Contains(string(raw), "base64") {
				t.Errorf("降级请求不应携带 base64: %s", raw)
			}
		})
	}
}
