package llm

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
// 判定依据来自实际 wire 编码能力：只有 OpenAI chat completions 的
// chatMessage.MarshalJSON 会把 ContentParts 原始数组直接作为 content 发出，
// 故 chat completions 返回 true。Responses 使用带类型的 input_text /
// output_text 分段，Anthropic Messages 使用 media_type/data 的 image source，
// 两者均未实现原始 part 承载，返回 false 以免把图片误发给不支持的端点；
// 未知协议同样返回 false。
func ImageInputsSupported(protocol string) bool {
	switch protocol {
	case ProtocolChatCompletions:
		return true
	default:
		return false
	}
}
