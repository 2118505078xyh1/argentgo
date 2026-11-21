package llm

import (
	"encoding/base64"
	"encoding/json"
	"mime"
	"strings"
)

// 本文件为"多模态内容通道"铺路：它只提供纯数据结构与序列化辅助，
// 不接入任何协议的 wire 编解码，也不改动 Chat/Message 的现有字段。
//
// 目前仅实现 OpenAI chat completions 一种 content part 形态；
// Anthropic Messages 与 OpenAI Responses 的对应形态有意留空，
// 待后续按各自协议单独实现。所有函数都是纯函数：只读入参、
// 不读写可变状态、不产生副作用，且保持 llm 零 internal 依赖。

// ImagePart 描述一段待编码为 content part 的图片负载。
//
// 它是纯数据结构，不持有连接、不缓存、也不依赖任何协议实现，
// 因此调用方可以在 agent/session 层自由构造它，而不把协议细节带进上层。
type ImagePart struct {
	// MIMEType 是图片的媒体类型（如 "image/png"）。为空或不合法时，
	// 编码会回退到默认类型，避免生成无法解析的 data URL。
	MIMEType string
	// Data 是原始图片字节；编码为 base64 后嵌入 data URL。
	Data []byte
	// Detail 对应 image_url.detail（"low"/"high"/"auto"）。为空时
	// 省略该字段，交由提供方使用其默认细节级别。
	Detail string
}

// defaultImageMIMEType 是 MIMEType 缺失或不合法时的回退类型。
// 选择 image/png 是因为它被绝大多数支持视觉的模型接受。
const defaultImageMIMEType = "image/png"

// contentPart 是 OpenAI chat completions 的单个 content part。
// 字段顺序即 JSON 输出顺序：type 在前，随后是 text 或 image_url。
type contentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *imageURLPart `json:"image_url,omitempty"`
}

// imageURLPart 是 image_url 类型 content part 的负载。
type imageURLPart struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// OpenAIContentParts 把文本与图片编码为 OpenAI chat completions 风格的
// content 值（json.RawMessage）。
//
// 行为约定：
//   - images 为空（nil 或长度 0）时，返回值与 json.Marshal(text) 逐字节
//     一致，即一个 JSON 字符串。这正是现有 text-only 请求的形态，
//     因此即便将来接入 wire，也不会改变任何现有字节；
//   - images 非空时，返回 content part 数组：text 非空则先放一个
//     {"type":"text","text":...}，随后每张图片对应一个
//     {"type":"image_url","image_url":{"url":"data:<mime>;base64,<...>","detail":...}}；
//   - text 为空但有图片时，数组中只有图片 part。
//
// MIME 为空或不合法时回退到 image/png；Detail 为空时省略 detail 字段。
// 本函数只依赖入参，不读全局可变状态，也不触碰任何协议实现。
// json.Marshal 对这里的具体类型（字符串与字符串字段的结构体）不会失败，
// 故忽略其 error 是安全的。
func OpenAIContentParts(text string, images []ImagePart) json.RawMessage {
	if len(images) == 0 {
		// 与现有 text-only 路径逐字节一致：content 是一个 JSON 字符串。
		raw, _ := json.Marshal(text)
		return raw
	}

	parts := make([]contentPart, 0, len(images)+1)
	if text != "" {
		parts = append(parts, contentPart{Type: "text", Text: text})
	}
	for _, img := range images {
		parts = append(parts, imageContentPart(img))
	}
	raw, _ := json.Marshal(parts)
	return raw
}

// imageContentPart 把单张 ImagePart 编码为 image_url 类型的 content part。
// 图片字节以 base64 编码后拼成 data:<mime>;base64,<payload> 形式的 URL。
func imageContentPart(img ImagePart) contentPart {
	url := "data:" + normalizeImageMIME(img.MIMEType) + ";base64," +
		base64.StdEncoding.EncodeToString(img.Data)
	return contentPart{
		Type: "image_url",
		ImageURL: &imageURLPart{
			URL:    url,
			Detail: img.Detail,
		},
	}
}

// normalizeImageMIME 规范化图片 MIME 类型：解析成功则取小写、
// 去掉参数的媒体类型；为空或不合法时回退到默认图片类型。
// 这一步保证 data URL 的 mediatype 段始终是一个 <type>/<subtype> 形式的媒体类型。
func normalizeImageMIME(mimeType string) string {
	if mimeType == "" {
		return defaultImageMIMEType
	}
	mediaType, _, err := mime.ParseMediaType(mimeType)
	if err != nil {
		return defaultImageMIMEType
	}
	// mime.ParseMediaType 会接受 "not-a-mime" 这类没有斜杠的裸 token，
	// 但它不是 data URL 允许的媒体类型，需额外校验 type/subtype 结构。
	typ, sub, ok := strings.Cut(mediaType, "/")
	if !ok || typ == "" || sub == "" {
		return defaultImageMIMEType
	}
	return mediaType
}
