package llm

// 本文件提供"图像输入"的最小原语：把上层易于构造的 ImageInput 转成
// llm 包内已有的 ImagePart，并借 OpenAIContentParts 序列化为带图像 part
// 的 Message.Content。它只做纯数据转换，不接入任何协议编解码，也不改动
// Chatter/Chat 的现有签名（默认关闭，接线由后续改动完成）。
//
// 设计取舍：Message.Content 是 string，无法直接承载 []contentPart，
// 因此带图时把 OpenAIContentParts 产出的 JSON 数组以字符串形式放进 Content；
// 无图时 Content 保持原始文本，与既有 text-only 路径逐字节一致。

// ImageInput 是调用方构造图像输入时使用的轻量结构。
// Data 是已经解码好的图片字节（不是 base64 文本），因此本结构不依赖
// 任何文件读取或解码逻辑，便于在 agent/session 层自由构造。
type ImageInput struct {
	MimeType string
	Data     []byte
	Detail   string
}

// BuildImageMessage 构造一条带图像 part 的用户消息。
//
//   - images 为空（nil 或长度 0）时返回纯文本 Message，Content 与 text
//     逐字节相同，不经过任何 JSON 编码，与既有 text-only 路径一致；
//   - images 非空时把每个 ImageInput 映射为 ImagePart，再交给
//     OpenAIContentParts 生成 OpenAI 风格 content part 数组，其 JSON
//     以字符串形式存入 Content。
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
	return Message{Role: "user", Content: string(OpenAIContentParts(text, parts))}
}

// ImageInputsSupported 报告 protocol 能否承载图像输入。
//
// 判定依据来自 image_parts.go 的实际编码能力：它目前只实现了 OpenAI
// chat completions 风格的 content part（image_url + data URL）。chat
// completions 与 responses 同属 OpenAI 家族，图像输入均以 image_url 的
// data URL 形态承载，故两者返回 true。Anthropic Messages 使用与之不同的
// image source（media_type/data）结构，image_parts.go 并未实现，故返回
// false；未知协议同样返回 false，以免把图片发给不支持的端点。
func ImageInputsSupported(protocol string) bool {
	switch protocol {
	case ProtocolChatCompletions, ProtocolResponses:
		return true
	default:
		return false
	}
}
