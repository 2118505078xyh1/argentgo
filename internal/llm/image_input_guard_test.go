package llm_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// 本文件对图像输入原语做属性守护：从外部契约验证空输入的逐字节一致性、
// 单图/多图编码、CJK 文本保留、协议能力判定、入参不被持有或修改，以及
// 确定性。测试侧用独立的 base64 计算与 JSON 解构，不复用被测实现细节。

// guardWirePart 仅按 OpenAI chat completions 字段读取 Content，与被测包内部
// 类型无关。
type guardWirePart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL *struct {
		URL    string `json:"url"`
		Detail string `json:"detail"`
	} `json:"image_url"`
}

// guardDecodeParts 把带图消息的 ContentParts 原始 JSON 解析为 part 数组。
func guardDecodeParts(t *testing.T, raw json.RawMessage) []guardWirePart {
	t.Helper()
	if !json.Valid(raw) {
		t.Fatalf("ContentParts 不是合法 JSON: %s", raw)
	}
	var parts []guardWirePart
	if err := json.Unmarshal(raw, &parts); err != nil {
		t.Fatalf("解析 content part 失败: %v; raw=%s", err, raw)
	}
	return parts
}

// guardDataURL 用测试侧独立的 base64 计算期望 data URL。
func guardDataURL(mime string, data []byte) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// TestGuardBuildImageMessageEmptyIsPlainText 空 images（nil 与空切片）必须
// 产出纯文本 Message：Content 与 text 逐字节一致，不经任何 JSON 编码。
func TestGuardBuildImageMessageEmptyIsPlainText(t *testing.T) {
	texts := []struct {
		name string
		text string
	}{
		{"ascii", "hello world"},
		{"empty", ""},
		{"cjk", "你好，世界"},
		{"emoji", "👋🏞"},
		{"needs escape", "a\"b\nc\t\\d"},
	}
	for _, tc := range texts {
		for _, variant := range []struct {
			name   string
			images []llm.ImageInput
		}{
			{"nil", nil},
			{"empty", []llm.ImageInput{}},
		} {
			t.Run(tc.name+"/"+variant.name, func(t *testing.T) {
				msg := llm.BuildImageMessage(tc.text, variant.images)
				if msg.Role != "user" {
					t.Errorf("Role = %q, want user", msg.Role)
				}
				if msg.Content != tc.text {
					t.Fatalf("Content = %q, want %q（逐字节不变）", msg.Content, tc.text)
				}
			})
		}
	}
}

// TestGuardBuildImageMessageSingleAndMultiple 覆盖单图与多图：part 顺序、
// 文本位置、data URL（独立 base64 计算）与 detail 都需要正确。
func TestGuardBuildImageMessageSingleAndMultiple(t *testing.T) {
	t.Run("single", func(t *testing.T) {
		data := []byte("hello")
		msg := llm.BuildImageMessage("看这张图", []llm.ImageInput{
			{MimeType: "image/png", Data: data, Detail: "high"},
		})
		if msg.Role != "user" {
			t.Errorf("Role = %q, want user", msg.Role)
		}
		parts := guardDecodeParts(t, msg.ContentParts)
		if len(parts) != 2 {
			t.Fatalf("part 数 = %d, want 2; content=%s", len(parts), msg.ContentParts)
		}
		if parts[0].Type != "text" || parts[0].Text != "看这张图" || parts[0].ImageURL != nil {
			t.Errorf("parts[0] = %+v, want text part", parts[0])
		}
		if parts[1].Type != "image_url" || parts[1].ImageURL == nil {
			t.Fatalf("parts[1] = %+v, want image_url part", parts[1])
		}
		if want := guardDataURL("image/png", data); parts[1].ImageURL.URL != want {
			t.Errorf("URL = %q, want %q", parts[1].ImageURL.URL, want)
		}
		if parts[1].ImageURL.Detail != "high" {
			t.Errorf("detail = %q, want high", parts[1].ImageURL.Detail)
		}
	})

	t.Run("multiple", func(t *testing.T) {
		d0 := []byte{0x01, 0x02}
		d1 := []byte{0x03}
		msg := llm.BuildImageMessage("A", []llm.ImageInput{
			{MimeType: "image/jpeg", Data: d0, Detail: "low"},
			{MimeType: "image/webp", Data: d1},
		})
		parts := guardDecodeParts(t, msg.ContentParts)
		if len(parts) != 3 {
			t.Fatalf("part 数 = %d, want 3; content=%s", len(parts), msg.ContentParts)
		}
		if parts[0].Type != "text" || parts[0].Text != "A" {
			t.Errorf("parts[0] = %+v, want text A", parts[0])
		}
		if parts[1].Type != "image_url" || parts[1].ImageURL == nil ||
			parts[1].ImageURL.URL != guardDataURL("image/jpeg", d0) || parts[1].ImageURL.Detail != "low" {
			t.Errorf("parts[1] = %+v, want jpeg/low image_url", parts[1])
		}
		if parts[2].Type != "image_url" || parts[2].ImageURL == nil ||
			parts[2].ImageURL.URL != guardDataURL("image/webp", d1) || parts[2].ImageURL.Detail != "" {
			t.Errorf("parts[2] = %+v, want webp image_url with empty detail", parts[2])
		}
	})
}

// TestGuardBuildImageMessageCJKPreserved CJK 文本走带图路径后必须原样保留，
// 不被切片或额外转义破坏；空文本配图时只产出图片 part。
func TestGuardBuildImageMessageCJKPreserved(t *testing.T) {
	text := "这是一张图片：山色空蒙雨亦奇 🏞。"
	msg := llm.BuildImageMessage(text, []llm.ImageInput{
		{MimeType: "image/png", Data: []byte("x")},
	})
	parts := guardDecodeParts(t, msg.ContentParts)
	if len(parts) != 2 {
		t.Fatalf("part 数 = %d, want 2; content=%s", len(parts), msg.ContentParts)
	}
	if parts[0].Text != text {
		t.Errorf("文本 = %q, want %q", parts[0].Text, text)
	}

	empty := llm.BuildImageMessage("", []llm.ImageInput{
		{MimeType: "image/png", Data: []byte("x")},
	})
	onlyImage := guardDecodeParts(t, empty.ContentParts)
	if len(onlyImage) != 1 || onlyImage[0].Type != "image_url" {
		t.Errorf("空文本带图应只有 1 个 image_url part, got %+v", onlyImage)
	}
}

// TestGuardImageInputsSupported 三协议能力判定：只有 OpenAI chat
// completions 的 wire 能承载原始 part 数组；responses 与 Anthropic Messages
// 均不支持；未知与空协议同样返回 false。
func TestGuardImageInputsSupported(t *testing.T) {
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
		t.Run("protocol_"+tc.protocol, func(t *testing.T) {
			if got := llm.ImageInputsSupported(tc.protocol); got != tc.want {
				t.Errorf("ImageInputsSupported(%q) = %v, want %v", tc.protocol, got, tc.want)
			}
		})
	}
}

// TestGuardBuildImageMessageDoesNotTouchInput 入参不得被修改或被持有：
// 调用前后 Data 字节一致；构造完成后改动入参的字节与元数据，已生成的
// Content 必须保持不变（证明结果不是对入参切片的活引用）。
func TestGuardBuildImageMessageDoesNotTouchInput(t *testing.T) {
	d0 := []byte{0x01, 0x02, 0x03}
	d1 := []byte{0x04, 0x05}
	images := []llm.ImageInput{
		{MimeType: "image/jpeg", Data: d0, Detail: "low"},
		{MimeType: "image/png", Data: d1},
	}
	before := [][]byte{
		append([]byte(nil), d0...),
		append([]byte(nil), d1...),
	}

	msg := llm.BuildImageMessage("t", images)
	if !bytes.Equal(d0, before[0]) || !bytes.Equal(d1, before[1]) {
		t.Fatalf("构造过程修改了 Data: d0=%v d1=%v", d0, d1)
	}
	if images[0].MimeType != "image/jpeg" || images[1].MimeType != "image/png" ||
		images[0].Detail != "low" || images[1].Detail != "" {
		t.Fatalf("构造过程修改了元数据: %+v", images)
	}

	// 改动入参后，已构造的消息内容必须保持原样。
	snapshot := append(json.RawMessage(nil), msg.ContentParts...)
	d0[0], d0[1], d0[2] = 0xFF, 0xFE, 0xFD
	d1[0], d1[1] = 0xAA, 0xBB
	images[0].MimeType, images[0].Detail = "image/gif", "high"
	if !bytes.Equal(msg.ContentParts, snapshot) {
		t.Errorf("ContentParts 持有入参引用，改动 Data 后变了: got %s, want %s", msg.ContentParts, snapshot)
	}
	if want := guardDataURL("image/jpeg", before[0]); !bytes.Contains(msg.ContentParts, []byte(want)) {
		t.Errorf("ContentParts 未保留构造时的 base64 快照: content=%s want url=%s", msg.ContentParts, want)
	}
}

// TestGuardBuildImageMessageDeterministic 相同入参多次调用得到完全相同结果。
func TestGuardBuildImageMessageDeterministic(t *testing.T) {
	images := []llm.ImageInput{
		{MimeType: "image/png", Data: []byte{0xAA, 0xBB}, Detail: "auto"},
		{MimeType: "image/webp", Data: []byte{0xCC}},
	}
	a := llm.BuildImageMessage("稳定输出", images)
	for i := 0; i < 5; i++ {
		b := llm.BuildImageMessage("稳定输出", images)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("第 %d 次构造结果不一致: a=%+v, b=%+v", i, a, b)
		}
	}
}
