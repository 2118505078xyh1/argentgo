package llm_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestResponsesImageWireOverHTTP 从真实 HTTP 请求体验证带图消息同时被
// 流式 Chat 与非流式 ChatOnce 以 Responses 原生分段发出：两条路径都复用
// buildRequest，因此都携带 input_image，只有 stream 标志不同。
func TestResponsesImageWireOverHTTP(t *testing.T) {
	cases := []struct {
		name       string
		stream     bool
		wantStream bool
		respond    func(w http.ResponseWriter)
	}{
		{
			name:       "Chat streaming",
			stream:     true,
			wantStream: true,
			respond: func(w http.ResponseWriter) {
				writeSSE(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
			},
		},
		{
			name:       "ChatOnce non-streaming",
			wantStream: false,
			respond: func(w http.ResponseWriter) {
				io.WriteString(w, `{"id":"r","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`)
			},
		},
	}

	imageMsg := llm.BuildImageMessage("识别这张图", []llm.ImageInput{
		{MimeType: "image/png", Data: []byte("x")},
	})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ = io.ReadAll(r.Body)
				tc.respond(w)
			}))
			defer server.Close()

			client := llm.NewResponses(server.URL, "")
			var err error
			if tc.stream {
				_, err = client.Chat(context.Background(), "gpt-test", []llm.Message{imageMsg}, nil, nil)
			} else {
				_, err = client.ChatOnce(context.Background(), "gpt-test", []llm.Message{imageMsg}, nil)
			}
			if err != nil {
				t.Fatalf("请求失败: %v", err)
			}

			var decoded struct {
				Stream bool `json:"stream"`
				Input  []struct {
					Type    string `json:"type"`
					Role    string `json:"role"`
					Content []struct {
						Type     string `json:"type"`
						Text     string `json:"text"`
						ImageURL string `json:"image_url"`
					} `json:"content"`
				} `json:"input"`
			}
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Fatalf("解码请求体失败: %v; body=%s", err, body)
			}
			if decoded.Stream != tc.wantStream {
				t.Errorf("stream = %v, want %v; body=%s", decoded.Stream, tc.wantStream, body)
			}
			if len(decoded.Input) != 1 {
				t.Fatalf("input 项数 = %d, want 1; body=%s", len(decoded.Input), body)
			}
			item := decoded.Input[0]
			if item.Type != "message" || item.Role != "user" {
				t.Errorf("输入项 type/role = %q/%q, want message/user", item.Type, item.Role)
			}
			if len(item.Content) != 2 {
				t.Fatalf("content 分段数 = %d, want 2; body=%s", len(item.Content), body)
			}
			if item.Content[0].Type != "input_text" || item.Content[0].Text != "识别这张图" {
				t.Errorf("文本分段 = %+v", item.Content[0])
			}
			if item.Content[1].Type != "input_image" ||
				item.Content[1].ImageURL != "data:image/png;base64,eA==" {
				t.Errorf("图片分段 = %+v", item.Content[1])
			}
			if strings.Contains(string(body), `"image_url":{`) {
				t.Errorf("image_url 被编码为嵌套对象: %s", body)
			}
		})
	}
}
