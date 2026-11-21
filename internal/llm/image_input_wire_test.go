package llm_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// wirePart 是测试侧用来解构 Message.ContentParts 中 content part 数组的镜像结构。
// 它不依赖被测包内部类型，只按 OpenAI chat completions 的字段读取。
type wirePart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL *struct {
		URL    string `json:"url"`
		Detail string `json:"detail"`
	} `json:"image_url"`
}

// decodeWireParts 把带图消息的 ContentParts 原始 JSON 解析为 part 数组，格式非法时直接失败测试。
func decodeWireParts(t *testing.T, raw json.RawMessage) []wirePart {
	t.Helper()
	if !json.Valid(raw) {
		t.Fatalf("ContentParts 不是合法 JSON: %s", raw)
	}
	var parts []wirePart
	if err := json.Unmarshal(raw, &parts); err != nil {
		t.Fatalf("解析 content part 数组失败: %v; raw=%s", err, raw)
	}
	return parts
}

// TestBuildImageMessageEmptyIsByteIdenticalText 确认 nil 与空切片都产出纯文本
// Message：Content 与 text 逐字节相同，且角色为用户。覆盖 ASCII、空串、
// CJK、emoji 与需要转义的字符。
func TestBuildImageMessageEmptyIsByteIdenticalText(t *testing.T) {
	texts := []struct {
		name string
		text string
	}{
		{"ascii", "hello"},
		{"empty", ""},
		{"cjk", "你好，世界"},
		{"emoji", "👋🏞"},
		{"needs escaping", "a\"b\nc\t\\d"},
	}
	for _, tc := range texts {
		for _, variant := range []struct {
			name   string
			images []llm.ImageInput
		}{
			{"nil", nil},
			{"empty slice", []llm.ImageInput{}},
		} {
			t.Run(tc.name+"/"+variant.name, func(t *testing.T) {
				msg := llm.BuildImageMessage(tc.text, variant.images)
				if msg.Content != tc.text {
					t.Fatalf("Content = %q, want %q（逐字节不变）", msg.Content, tc.text)
				}
				if msg.ContentParts != nil {
					t.Fatalf("无图消息的 ContentParts 必须为空，got %s", msg.ContentParts)
				}
				if msg.Role != "user" {
					t.Errorf("Role = %q, want user", msg.Role)
				}
			})
		}
	}
}

// TestBuildImageMessageSingleImage 验证单图时的 part 顺序、文本、data URL
// 与 detail 字段。
func TestBuildImageMessageSingleImage(t *testing.T) {
	msg := llm.BuildImageMessage("看这张图", []llm.ImageInput{
		{MimeType: "image/png", Data: []byte("hello"), Detail: "high"},
	})
	if msg.Role != "user" {
		t.Errorf("Role = %q, want user", msg.Role)
	}
	if msg.Content != "" {
		t.Fatalf("带图消息的 Content 必须留空，got %q", msg.Content)
	}
	parts := decodeWireParts(t, msg.ContentParts)
	if len(parts) != 2 {
		t.Fatalf("part 数 = %d, want 2; content=%s", len(parts), msg.ContentParts)
	}
	if parts[0].Type != "text" || parts[0].Text != "看这张图" || parts[0].ImageURL != nil {
		t.Errorf("第一个 part = %+v, want text part", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL == nil {
		t.Fatalf("第二个 part = %+v, want image_url part", parts[1])
	}
	if got, want := parts[1].ImageURL.URL, "data:image/png;base64,aGVsbG8="; got != want {
		t.Errorf("data URL = %q, want %q", got, want)
	}
	if got, want := parts[1].ImageURL.Detail, "high"; got != want {
		t.Errorf("detail = %q, want %q", got, want)
	}
}

// TestBuildImageMessageMultipleImages 验证多图按输入顺序编码，各图 MIME/detail
// 独立，且 detail 为空时字段省略。
func TestBuildImageMessageMultipleImages(t *testing.T) {
	msg := llm.BuildImageMessage("A", []llm.ImageInput{
		{MimeType: "image/jpeg", Data: []byte{0x01, 0x02}, Detail: "low"},
		{MimeType: "image/webp", Data: []byte{0x03}},
	})
	parts := decodeWireParts(t, msg.ContentParts)
	if len(parts) != 3 {
		t.Fatalf("part 数 = %d, want 3; content=%s", len(parts), msg.ContentParts)
	}
	if parts[0].Type != "text" || parts[0].Text != "A" {
		t.Errorf("第一个 part = %+v, want text \"A\"", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL == nil ||
		parts[1].ImageURL.URL != "data:image/jpeg;base64,AQI=" {
		t.Errorf("第二个 part = %+v, want jpeg image_url", parts[1])
	}
	if parts[1].ImageURL.Detail != "low" {
		t.Errorf("第一张图 detail = %q, want low", parts[1].ImageURL.Detail)
	}
	if parts[2].Type != "image_url" || parts[2].ImageURL == nil ||
		parts[2].ImageURL.URL != "data:image/webp;base64,Aw==" {
		t.Errorf("第三个 part = %+v, want webp image_url", parts[2])
	}
	if parts[2].ImageURL.Detail != "" {
		t.Errorf("第二张图 detail = %q, want 省略（空串）", parts[2].ImageURL.Detail)
	}
	// detail 为空时不应出现在该图自己的 image_url 对象中。
	var rawParts []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(msg.ContentParts), &rawParts); err != nil {
		t.Fatal(err)
	}
	var imageURL map[string]json.RawMessage
	if err := json.Unmarshal(rawParts[2]["image_url"], &imageURL); err != nil {
		t.Fatal(err)
	}
	if _, ok := imageURL["detail"]; ok {
		t.Errorf("空 detail 不应输出 detail 字段: %s", rawParts[2]["image_url"])
	}
}

// TestBuildImageMessageCJKText 确认 CJK 与 emoji 文本在带图路径下原样保留，
// 不被字节切片或转义破坏。
func TestBuildImageMessageCJKText(t *testing.T) {
	text := "这是一张图片：山色空蒙雨亦奇 🏞。"
	msg := llm.BuildImageMessage(text, []llm.ImageInput{
		{MimeType: "image/png", Data: []byte("x")},
	})
	parts := decodeWireParts(t, msg.ContentParts)
	if len(parts) != 2 {
		t.Fatalf("part 数 = %d, want 2; content=%s", len(parts), msg.ContentParts)
	}
	if parts[0].Text != text {
		t.Errorf("文本 = %q, want %q", parts[0].Text, text)
	}
}

// TestBuildImageMessageMimeTypeValidation 覆盖合法与不合法 MIME：函数不得
// panic，不合法时回退到默认图片类型，产出仍是可解析的 data URL。
func TestBuildImageMessageMimeTypeValidation(t *testing.T) {
	cases := []struct {
		name    string
		mime    string
		wantURL string
	}{
		{"empty", "", "data:image/png;base64,eA=="},
		{"no slash", "not-a-mime", "data:image/png;base64,eA=="},
		{"empty subtype", "image/", "data:image/png;base64,eA=="},
		{"valid lowercase", "image/gif", "data:image/gif;base64,eA=="},
		{"valid uppercase with param", "IMAGE/GIF; charset=binary", "data:image/gif;base64,eA=="},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := llm.BuildImageMessage("t", []llm.ImageInput{
				{MimeType: tc.mime, Data: []byte("x")},
			})
			parts := decodeWireParts(t, msg.ContentParts)
			if len(parts) != 2 {
				t.Fatalf("part 数 = %d, want 2; content=%s", len(parts), msg.ContentParts)
			}
			if got := parts[1].ImageURL.URL; got != tc.wantURL {
				t.Errorf("data URL = %q, want %q", got, tc.wantURL)
			}
		})
	}
}

// TestBuildImageMessageEmptyTextWithImage 确认文本为空但有图片时只产出图片 part。
func TestBuildImageMessageEmptyTextWithImage(t *testing.T) {
	msg := llm.BuildImageMessage("", []llm.ImageInput{
		{MimeType: "image/png", Data: []byte("x")},
	})
	parts := decodeWireParts(t, msg.ContentParts)
	if len(parts) != 1 {
		t.Fatalf("part 数 = %d, want 1; content=%s", len(parts), msg.ContentParts)
	}
	if parts[0].Type != "image_url" {
		t.Errorf("part 类型 = %q, want image_url", parts[0].Type)
	}
}

// TestBuildImageMessageDoesNotMutateInput 确认调用前后入参（含 MimeType、
// Detail 与 Data 字节）完全不变，且结果不持有入参切片的活引用。
func TestBuildImageMessageDoesNotMutateInput(t *testing.T) {
	images := []llm.ImageInput{
		{MimeType: "image/jpeg", Data: []byte{0x01, 0x02, 0x03}, Detail: "low"},
		{MimeType: "", Data: []byte{0x04}, Detail: "high"},
	}
	before := make([]llm.ImageInput, len(images))
	for i, im := range images {
		before[i] = llm.ImageInput{
			MimeType: im.MimeType,
			Data:     append([]byte(nil), im.Data...),
			Detail:   im.Detail,
		}
	}
	msg := llm.BuildImageMessage("t", images)
	for i := range images {
		if images[i].MimeType != before[i].MimeType || images[i].Detail != before[i].Detail ||
			!bytes.Equal(images[i].Data, before[i].Data) {
			t.Errorf("images[%d] 被修改: got %+v, want %+v", i, images[i], before[i])
		}
	}
	// 改入参后，已构造的 ContentParts 必须保持原样。
	snapshot := append(json.RawMessage(nil), msg.ContentParts...)
	images[0].Data[0], images[0].MimeType = 0xFF, "image/gif"
	if !bytes.Equal(msg.ContentParts, snapshot) {
		t.Errorf("ContentParts 持有入参引用，改动 Data 后变了: got %s, want %s", msg.ContentParts, snapshot)
	}
}

// TestBuildImageMessageDeterministic 确认相同入参多次调用得到完全相同的结果。
func TestBuildImageMessageDeterministic(t *testing.T) {
	images := []llm.ImageInput{
		{MimeType: "image/png", Data: []byte{0xAA, 0xBB}, Detail: "auto"},
		{MimeType: "image/webp", Data: []byte{0xCC}},
	}
	a := llm.BuildImageMessage("稳定输出", images)
	b := llm.BuildImageMessage("稳定输出", images)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("两次构造结果不一致: a=%+v, b=%+v", a, b)
	}
}

// TestImageInputsSupported 确认能力判定：只有 chat completions 的 wire
// 能承载原始 part 数组；responses 因分段编码形态不同、anthropic 因使用
// 独立 image source，均不支持；未知与空协议同样返回 false。
func TestImageInputsSupported(t *testing.T) {
	cases := []struct {
		protocol string
		want     bool
	}{
		{llm.ProtocolChatCompletions, true},
		{llm.ProtocolResponses, false},
		{llm.ProtocolAnthropicMessages, false},
		{"", false},
		{"unknown-protocol", false},
	}
	for _, tc := range cases {
		t.Run(tc.protocol, func(t *testing.T) {
			if got := llm.ImageInputsSupported(tc.protocol); got != tc.want {
				t.Errorf("ImageInputsSupported(%q) = %v, want %v", tc.protocol, got, tc.want)
			}
		})
	}
}

// TestBuildImageMessageRawPartsNotString 锁定 R17 双重编码 bug 的修复：
// ContentParts 必须是原生 JSON 数组，而不是被转成 JSON 字符串。
func TestBuildImageMessageRawPartsNotString(t *testing.T) {
	msg := llm.BuildImageMessage("看这张图", []llm.ImageInput{
		{MimeType: "image/png", Data: []byte("x")},
	})
	if len(msg.ContentParts) == 0 || msg.ContentParts[0] != '[' {
		t.Fatalf("ContentParts 应以 [ 开头的数组，got %s", msg.ContentParts)
	}
	var asString string
	if err := json.Unmarshal(msg.ContentParts, &asString); err == nil {
		t.Fatalf("ContentParts 被编码为 JSON 字符串（双重编码回归）: %s", msg.ContentParts)
	}
	if msg.Content != "" {
		t.Fatalf("带图消息 Content 必须留空，got %q", msg.Content)
	}
}
