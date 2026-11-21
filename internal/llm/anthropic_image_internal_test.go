package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 本文件为 Anthropic Messages 的图像输入接线提供 wire 层测试：从用户构造
// 的带图 Message 出发，验证 buildRequest 产出的原生 content block 结构、
// base64/media_type、CJK 文本、非法数据降级，以及 ChatOnce 非流式路径。
// 测试用手独立的解码结构，不复用被测实现细节。

// anthropicWireBlock 是测试侧对 Anthropic content block 的最小镜像，
// 字段与线协议一致，用于验证 source 的嵌套结构。
type anthropicWireBlock struct {
	Type   string `json:"type"`
	Text   string `json:"text,omitempty"`
	Source *struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	} `json:"source,omitempty"`
}

// decodeAnthropicWireMessages 把 buildRequest 的结果序列化再解码为镜像结构，
// 确保断言覆盖真实 JSON 字段名与嵌套关系，而不只是内存中的结构体。
func decodeAnthropicWireMessages(t *testing.T, req anthropicRequest) []struct {
	Role    string               `json:"role"`
	Content []anthropicWireBlock `json:"content"`
} {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var decoded struct {
		Messages []struct {
			Role    string               `json:"role"`
			Content []anthropicWireBlock `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal request: %v; raw=%s", err, raw)
	}
	return decoded.Messages
}

// TestAnthropicImageWireSingle 验证单图：text 块在前、image 块在后，
// source.type=base64、media_type 与 base64 负载精确正确。
func TestAnthropicImageWireSingle(t *testing.T) {
	data := []byte("hello")
	msg := BuildImageMessage("看这张图", []ImageInput{
		{MimeType: "image/png", Data: data},
	})
	msgs := decodeAnthropicWireMessages(t, (&AnthropicClient{}).buildRequest("claude-3", []Message{msg}, nil))
	if len(msgs) != 1 || msgs[0].Role != "user" {
		t.Fatalf("messages = %+v, want one user message", msgs)
	}
	blocks := msgs[0].Content
	if len(blocks) != 2 {
		t.Fatalf("content blocks = %d, want 2; %+v", len(blocks), blocks)
	}
	if blocks[0].Type != "text" || blocks[0].Text != "看这张图" || blocks[0].Source != nil {
		t.Errorf("blocks[0] = %+v, want text block", blocks[0])
	}
	image := blocks[1]
	if image.Type != "image" || image.Source == nil {
		t.Fatalf("blocks[1] = %+v, want image block", image)
	}
	if image.Source.Type != "base64" {
		t.Errorf("source.type = %q, want base64", image.Source.Type)
	}
	if image.Source.MediaType != "image/png" {
		t.Errorf("media_type = %q, want image/png", image.Source.MediaType)
	}
	if want := base64.StdEncoding.EncodeToString(data); image.Source.Data != want {
		t.Errorf("source.data = %q, want %q", image.Source.Data, want)
	}
}

// TestAnthropicImageWireMultiple 验证多图按输入顺序编码，各图 MIME 独立，
// 且文本块位于所有图片之前。
func TestAnthropicImageWireMultiple(t *testing.T) {
	d0, d1 := []byte{0x01, 0x02}, []byte("second")
	msg := BuildImageMessage("A", []ImageInput{
		{MimeType: "image/jpeg", Data: d0},
		{MimeType: "image/webp", Data: d1},
	})
	msgs := decodeAnthropicWireMessages(t, (&AnthropicClient{}).buildRequest("m", []Message{msg}, nil))
	blocks := msgs[0].Content
	if len(blocks) != 3 {
		t.Fatalf("content blocks = %d, want 3; %+v", len(blocks), blocks)
	}
	if blocks[0].Type != "text" || blocks[0].Text != "A" {
		t.Errorf("blocks[0] = %+v, want text A", blocks[0])
	}
	wants := []struct {
		mediaType string
		data      []byte
	}{
		{"image/jpeg", d0},
		{"image/webp", d1},
	}
	for i, want := range wants {
		block := blocks[i+1]
		if block.Type != "image" || block.Source == nil {
			t.Fatalf("blocks[%d] = %+v, want image block", i+1, block)
		}
		if block.Source.Type != "base64" ||
			block.Source.MediaType != want.mediaType ||
			block.Source.Data != base64.StdEncoding.EncodeToString(want.data) {
			t.Errorf("blocks[%d].Source = %+v, want %s/%s", i+1, block.Source,
				want.mediaType, base64.StdEncoding.EncodeToString(want.data))
		}
	}
}

// TestAnthropicImageWireCJKText 验证 CJK 文本与 emoji 在带图路径下原样到达
// wire，且图片负载仍正确。
func TestAnthropicImageWireCJKText(t *testing.T) {
	text := "这是一张图片：山色空蒙雨亦奇 🏞。"
	msg := BuildImageMessage(text, []ImageInput{
		{MimeType: "image/png", Data: []byte("x")},
	})
	msgs := decodeAnthropicWireMessages(t, (&AnthropicClient{}).buildRequest("m", []Message{msg}, nil))
	blocks := msgs[0].Content
	if len(blocks) != 2 || blocks[0].Type != "text" || blocks[0].Text != text {
		t.Fatalf("text block 未保留 CJK: %+v", blocks)
	}
	if blocks[1].Source == nil || blocks[1].Source.Data != "eA==" {
		t.Errorf("image block 异常: %+v", blocks[1])
	}
}

// TestAnthropicImageWireInvalidFallsBack 覆盖非法图片降级：非法 JSON、
// 缺失 image_url、非 data URL、损坏 base64 都退化为 [image] 占位文本，
// 不报错、不发 base64、也不把原始 JSON 当文本发出。
func TestAnthropicImageWireInvalidFallsBack(t *testing.T) {
	cases := []struct {
		name  string
		parts json.RawMessage
	}{
		{"invalid json", json.RawMessage(`not-json`)},
		{"missing image_url", json.RawMessage(`[{"type":"image_url"}]`)},
		{"non data url", json.RawMessage(`[{"type":"image_url","image_url":{"url":"https://x/y.png"}}]`)},
		{"broken base64", json.RawMessage(`[{"type":"image_url","image_url":{"url":"data:image/png;base64,@@@"}}]`)},
		{"no base64 marker", json.RawMessage(`[{"type":"image_url","image_url":{"url":"data:image/png,abc"}}]`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := Message{Role: "user", ContentParts: tc.parts}
			req := (&AnthropicClient{}).buildRequest("m", []Message{msg}, nil)
			msgs := decodeAnthropicWireMessages(t, req)
			blocks := msgs[0].Content
			if len(blocks) != 1 || blocks[0].Type != "text" || blocks[0].Text != unsupportedImagePlaceholder {
				t.Fatalf("未降级为占位文本: %+v", blocks)
			}
			raw, _ := json.Marshal(req)
			if strings.Contains(string(raw), "source") ||
				strings.Contains(string(raw), "data:image") {
				t.Errorf("降级请求携带了图片结构: %s", raw)
			}
		})
	}
}

// TestAnthropicImageWireNoPartsUnchanged 验证无 ContentParts 时走原路径：
// 请求字节与图像接线之前逐字节一致。
func TestAnthropicImageWireNoPartsUnchanged(t *testing.T) {
	req := (&AnthropicClient{Options: Options{NoPromptCache: true}}).buildRequest("m", []Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "hi"},
	}, nil)
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"m","max_tokens":8192,"system":"rules","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"stream":true}`
	if string(raw) != want {
		t.Fatalf("无图请求字节变化: got %s, want %s", raw, want)
	}
}

// TestAnthropicChatOnceImage 验证非流式 ChatOnce 复用 buildRequest，
// 同样把带图消息映射为原生 image block。
func TestAnthropicChatOnceImage(t *testing.T) {
	var body struct {
		Stream   bool `json:"stream"`
		Messages []struct {
			Role    string               `json:"role"`
			Content []anthropicWireBlock `json:"content"`
		} `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body: %v; raw=%s", err, raw)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"content":[{"type":"text","text":"ok"}]}`)
	}))
	defer server.Close()

	client := NewAnthropic(server.URL, "k")
	msg := BuildImageMessage("看这张图", []ImageInput{
		{MimeType: "image/png", Data: []byte("hello")},
	})
	reply, err := client.ChatOnce(context.Background(), "m", []Message{msg}, nil)
	if err != nil {
		t.Fatalf("ChatOnce: %v", err)
	}
	if reply.Content != "ok" {
		t.Fatalf("content = %q, want ok", reply.Content)
	}
	if body.Stream {
		t.Error("ChatOnce 请求 stream 应为 false")
	}
	if len(body.Messages) != 1 || len(body.Messages[0].Content) != 2 {
		t.Fatalf("messages = %+v, want one user message with 2 blocks", body.Messages)
	}
	blocks := body.Messages[0].Content
	if blocks[0].Type != "text" || blocks[0].Text != "看这张图" {
		t.Errorf("text block = %+v", blocks[0])
	}
	if blocks[1].Type != "image" || blocks[1].Source == nil {
		t.Fatalf("image block = %+v", blocks[1])
	}
	if blocks[1].Source.Type != "base64" ||
		blocks[1].Source.MediaType != "image/png" ||
		blocks[1].Source.Data != base64.StdEncoding.EncodeToString([]byte("hello")) {
		t.Errorf("image source = %+v", blocks[1].Source)
	}
}
