package llm_test

import (
	"encoding/json"
	"testing"

	"example.com/argentgo/internal/llm"
)

// decodedPart 是测试中用来解构 content part 数组的镜像结构。
// 它不依赖被测包内部类型，只按 OpenAI chat completions 的字段读取。
type decodedPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL *struct {
		URL    string `json:"url"`
		Detail string `json:"detail"`
	} `json:"image_url"`
}

// decodeParts 把 json.RawMessage 解析为 part 数组，并在格式非法时直接失败测试。
func decodeParts(t *testing.T, raw json.RawMessage) []decodedPart {
	t.Helper()
	if !json.Valid(raw) {
		t.Fatalf("输出不是合法 JSON: %s", raw)
	}
	var parts []decodedPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		t.Fatalf("解析 content part 数组失败: %v; raw=%s", err, raw)
	}
	return parts
}

// TestOpenAIContentPartsTextOnlyMatchesExistingShape 确认无图片时输出与
// json.Marshal(text) 逐字节一致（即现有 text-only 的 JSON 字符串形态），
// 且覆盖 ASCII、空串、CJK、emoji 与需要转义的字符。
func TestOpenAIContentPartsTextOnlyMatchesExistingShape(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"ascii", "hello"},
		{"empty", ""},
		{"cjk", "你好，世界"},
		{"emoji", "👋🏞"},
		{"needs escaping", "a\"b\nc\t\\d<&>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := llm.OpenAIContentParts(tc.text, nil)
			want, err := json.Marshal(tc.text)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Fatalf("text-only 输出 = %s, want %s", got, want)
			}
			// 必须是 JSON 字符串，不能退化成数组。
			var s string
			if err := json.Unmarshal(got, &s); err != nil {
				t.Fatalf("text-only 不是 JSON 字符串: %v; raw=%s", err, got)
			}
			if s != tc.text {
				t.Fatalf("解出文本 = %q, want %q", s, tc.text)
			}
		})
	}
}

// TestOpenAIContentPartsNilImagesReturnsOriginalText 确认 nil 与空切片都
// 返回原始 text 形态，调用方无需在入口处区分二者。
func TestOpenAIContentPartsNilImagesReturnsOriginalText(t *testing.T) {
	text := "原样文本，不要被改写"
	want, _ := json.Marshal(text)
	cases := []struct {
		name   string
		images []llm.ImagePart
	}{
		{"nil", nil},
		{"empty slice", []llm.ImagePart{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := llm.OpenAIContentParts(text, tc.images)
			if string(got) != string(want) {
				t.Fatalf("输出 = %s, want %s", got, want)
			}
		})
	}
}

// TestOpenAIContentPartsSingleImage 验证单图时的 part 顺序、类型、文本、
// base64 data URL 与 detail 字段。
func TestOpenAIContentPartsSingleImage(t *testing.T) {
	raw := llm.OpenAIContentParts("看这张图", []llm.ImagePart{
		{MIMEType: "image/png", Data: []byte("hello"), Detail: "high"},
	})
	parts := decodeParts(t, raw)
	if len(parts) != 2 {
		t.Fatalf("part 数 = %d, want 2; raw=%s", len(parts), raw)
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

// TestOpenAIContentPartsMultipleImages 验证多图按输入顺序编码，且各图的
// MIME/detail 独立；detail 为空时字段省略。
func TestOpenAIContentPartsMultipleImages(t *testing.T) {
	raw := llm.OpenAIContentParts("A", []llm.ImagePart{
		{MIMEType: "image/jpeg", Data: []byte{0x01, 0x02}, Detail: "low"},
		{MIMEType: "image/webp", Data: []byte{0x03}},
	})
	parts := decodeParts(t, raw)
	if len(parts) != 3 {
		t.Fatalf("part 数 = %d, want 3; raw=%s", len(parts), raw)
	}
	if parts[0].Type != "text" || parts[0].Text != "A" {
		t.Errorf("第一个 part = %+v, want text \"A\"", parts[0])
	}
	if got, want := parts[1].ImageURL.URL, "data:image/jpeg;base64,AQI="; parts[1].Type != "image_url" || got != want {
		t.Errorf("第二个 part = %+v, want image_url %q", parts[1], want)
	}
	if got, want := parts[1].ImageURL.Detail, "low"; got != want {
		t.Errorf("第一张图 detail = %q, want %q", got, want)
	}
	if got, want := parts[2].ImageURL.URL, "data:image/webp;base64,Aw=="; parts[2].Type != "image_url" || got != want {
		t.Errorf("第三个 part = %+v, want image_url %q", parts[2], want)
	}
	if got := parts[2].ImageURL.Detail; got != "" {
		t.Errorf("第二张图 detail = %q, want 省略（空串）", got)
	}
	// detail 为空时不应出现在该图自己的 image_url 对象中。
	var rawParts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawParts); err != nil {
		t.Fatal(err)
	}
	var imageURL map[string]json.RawMessage
	if err := json.Unmarshal(rawParts[2]["image_url"], &imageURL); err != nil {
		t.Fatal(err)
	}
	if _, ok := imageURL["detail"]; ok {
		t.Errorf("空 detail 不应输出 detail 字段; raw=%s", rawParts[2]["image_url"])
	}
}

// TestOpenAIContentPartsBase64DataURL 用表驱动确认 data URL 的精确拼装：
// MIME 小写化并去掉参数，负载为标准 base64。此测试用空文本隔离出纯图片数组。
func TestOpenAIContentPartsBase64DataURL(t *testing.T) {
	cases := []struct {
		name    string
		mime    string
		data    []byte
		wantURL string
	}{
		{"png lowercase", "image/png", []byte("hello"), "data:image/png;base64,aGVsbG8="},
		{"jpeg uppercase with param", "IMAGE/JPEG; charset=binary", []byte("hi"), "data:image/jpeg;base64,aGk="},
		{"empty bytes", "image/webp", nil, "data:image/webp;base64,"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := llm.OpenAIContentParts("", []llm.ImagePart{{MIMEType: tc.mime, Data: tc.data}})
			parts := decodeParts(t, raw)
			if len(parts) != 1 {
				t.Fatalf("part 数 = %d, want 1; raw=%s", len(parts), raw)
			}
			if got := parts[0].ImageURL.URL; got != tc.wantURL {
				t.Errorf("data URL = %q, want %q", got, tc.wantURL)
			}
		})
	}
}

// TestOpenAIContentPartsCJKText 确认 CJK 与 emoji 文本在带图路径下原样保留，
// 不被字节切片或转义破坏。
func TestOpenAIContentPartsCJKText(t *testing.T) {
	text := "这是一张图片：山色空蒙雨亦奇 🏞。"
	raw := llm.OpenAIContentParts(text, []llm.ImagePart{{MIMEType: "image/png", Data: []byte("x")}})
	parts := decodeParts(t, raw)
	if len(parts) != 2 {
		t.Fatalf("part 数 = %d, want 2; raw=%s", len(parts), raw)
	}
	if got := parts[0].Text; got != text {
		t.Errorf("文本 = %q, want %q", got, text)
	}
}

// TestOpenAIContentPartsInvalidMIME 覆盖各种不合法 MIME：函数不得 panic，
// 且必须回退到默认图片类型，产出仍是可解析的 data URL。
func TestOpenAIContentPartsInvalidMIME(t *testing.T) {
	cases := []struct {
		name string
		mime string
	}{
		{"empty", ""},
		{"no slash", "not-a-mime"},
		{"empty subtype", "image/"},
		{"empty type", "/png"},
		{"space instead of slash", "image png"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := llm.OpenAIContentParts("t", []llm.ImagePart{{MIMEType: tc.mime, Data: []byte("x")}})
			parts := decodeParts(t, raw)
			if len(parts) != 2 {
				t.Fatalf("part 数 = %d, want 2; raw=%s", len(parts), raw)
			}
			want := "data:image/png;base64,eA=="
			if got := parts[1].ImageURL.URL; got != want {
				t.Errorf("data URL = %q, want %q（回退到默认 MIME）", got, want)
			}
		})
	}
}

// TestOpenAIContentPartsEmptyTextWithImage 确认文本为空但有图片时只产出图片 part。
func TestOpenAIContentPartsEmptyTextWithImage(t *testing.T) {
	raw := llm.OpenAIContentParts("", []llm.ImagePart{{MIMEType: "image/png", Data: []byte("x")}})
	parts := decodeParts(t, raw)
	if len(parts) != 1 {
		t.Fatalf("part 数 = %d, want 1; raw=%s", len(parts), raw)
	}
	if parts[0].Type != "image_url" {
		t.Errorf("part 类型 = %q, want image_url", parts[0].Type)
	}
}
