package llm

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// 本文件提供"图像输入"的最小原语：把上层易于构造的 ImageInput 转成
// llm 包内已有的 ImagePart，并借 OpenAIContentParts 序列化为带图像 part
// 的原始 JSON，放进 Message.ContentParts 旁路字段，由 chat 线协议的
// chatMessage.MarshalJSON 原样作为 content 输出。它只做纯数据转换，
// 不改动 Chatter/Chat 的现有签名（接线由后续改动完成）。
//
// 设计取舍：Message.Content 是 string，无法无损承载 []contentPart；
// 旧实现把 OpenAIContentParts 产出的 JSON 数组转成字符串再塞进 Content，
// 导致 wire 编码时二次 JSON 编码，提供方收到的是字面文本而不是图像 part。
// 现在带图时把原始数组放进 ContentParts，Content 留空；无图时 Content
// 保持原始文本，与既有 text-only 路径逐字节一致。

// ImageInput 是调用方构造图像输入时使用的轻量结构。
// Data 是已经解码好的图片字节（不是 base64 文本），因此本结构不依赖
// 任何文件读取或解码逻辑，便于在 agent/session 层自由构造。
type ImageInput struct {
	MimeType string
	Data     []byte
	Detail   string
}

// unsupportedImagePlaceholder 是不支持图像 part 的协议（Anthropic Messages /
// OpenAI Responses）收到带图消息时使用的占位文本：既不静默丢弃整条消息，
// 也不假装这些协议真的能承载图片。
const unsupportedImagePlaceholder = "[image]"

// messageTextForProtocol 返回协议可安全编码的文本。纯文本消息原样返回；
// ContentParts 非空的带图消息在不支持图像 part 的协议上退化为占位文本。
func messageTextForProtocol(m Message) string {
	if len(m.ContentParts) > 0 {
		return unsupportedImagePlaceholder
	}
	return m.Content
}

// BuildImageMessage 构造一条带图像 part 的用户消息。
//
//   - images 为空（nil 或长度 0）时返回纯文本 Message，Content 与 text
//     逐字节相同，ContentParts 为 nil，不经任何 JSON 编码；
//   - images 非空时把每个 ImageInput 映射为 ImagePart，再交给
//     OpenAIContentParts 生成 OpenAI 风格 content part 数组的原始 JSON，
//     存入 ContentParts，Content 留空，避免任何形式的二次编码。
//
// 函数只读入参：不修改 text、images 及其中的字节切片；CJK 文本原样保留；
// 相同入参多次调用得到完全相同的结果（确定性）。
func BuildImageMessage(text string, images []ImageInput) Message {
	if len(images) == 0 {
		return Message{Role: "user", Content: text}
	}
	parts := make([]ImagePart, 0, len(images))
	for _, img := range images {
		parts = append(parts, ImagePart{
			MIMEType: img.MimeType,
			Data:     img.Data,
			Detail:   img.Detail,
		})
	}
	return Message{Role: "user", ContentParts: OpenAIContentParts(text, parts)}
}

// ImageInputsSupported 报告 protocol 能否承载图像输入。
//
// 判定依据来自实际 wire 编码能力：OpenAI chat completions 把 ContentParts
// 原始数组直接作为 content 发出；Anthropic Messages 把带图 user 消息映射为
// 原生 image content block；Responses 把图片映射为 input_image 分段。
// 三者都返回 true。未知协议（含空串）返回 false。
func ImageInputsSupported(protocol string) bool {
	switch protocol {
	case ProtocolChatCompletions, ProtocolAnthropicMessages, ProtocolResponses:
		return true
	default:
		return false
	}
}

// contentPartsImage 是从 OpenAI 风格 ContentParts 解码出的单张图片。
// Data 是 data URL 中的 base64 负载，可直接作为 Anthropic image source 的 data。
type contentPartsImage struct {
	MediaType string
	Data      string
}

// decodeContentPartsForImageInput 解析带图消息的 ContentParts，提取文本与
// 按输入顺序排列的图片。ok 为 false 表示 JSON 结构非法或存在无法解析的图片
// URL；调用方应据此回退到占位文本，而不是报错或发送无法解析的内容。
//
// 这里复用 image_parts.go 的 normalizeImageMIME 与标准库 base64 校验，
// 不引入任何依赖，也不修改入参。
func decodeContentPartsForImageInput(parts json.RawMessage) (text string, images []contentPartsImage, ok bool) {
	if len(parts) == 0 {
		return "", nil, false
	}
	var wire []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(parts, &wire); err != nil {
		return "", nil, false
	}

	var texts []string
	for _, part := range wire {
		switch part.Type {
		case "text":
			texts = append(texts, part.Text)
		case "image_url":
			if part.ImageURL == nil {
				return "", nil, false
			}
			mediaType, payload, valid := splitImageDataURL(part.ImageURL.URL)
			if !valid {
				return "", nil, false
			}
			images = append(images, contentPartsImage{MediaType: mediaType, Data: payload})
		}
	}
	return strings.Join(texts, ""), images, true
}

// splitImageDataURL 解析 data:<mime>;base64,<payload> 形式的 URL，返回
// 规范化媒体类型与 base64 负载。负载用标准库校验合法性，避免把损坏的
// base64 发给提供方；媒体类型走 normalizeImageMIME，与编码侧保持一致。
func splitImageDataURL(url string) (mediaType, payload string, ok bool) {
	if !strings.HasPrefix(url, "data:") {
		return "", "", false
	}
	meta, payload, found := strings.Cut(strings.TrimPrefix(url, "data:"), ",")
	if !found {
		return "", "", false
	}
	mediaType, marker, found := strings.Cut(meta, ";")
	if !found || !strings.EqualFold(marker, "base64") {
		return "", "", false
	}
	if _, err := base64.StdEncoding.DecodeString(payload); err != nil {
		return "", "", false
	}
	return normalizeImageMIME(mediaType), payload, true
}
