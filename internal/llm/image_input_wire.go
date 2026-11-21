package llm

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
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

// ImageInputsFromPrompt 从 prompt 中扫描 @<路径> 引用，读取对应图片文件并构造
// ImageInput 列表。它只是一个纯原语：只读 prompt/root、不改动入参、不缓存状态，
// 也不接入 agent/app 的接线（由上层按需调用）。
//
// 解析规则：
//   - 每个 '@' 后若紧跟连续非空白字符，视为一个候选路径；被引号（"、'、`）
//     包围时允许路径含空格；
//   - 扩展名必须是 png/jpg/jpeg/gif/webp 之一（大小写不敏感）；
//   - 路径必须是相对路径，且经 filepath.Clean/Join 解析后不得逃出 root；
//   - 文件大小不得超过 maxBytes（负数等价于跳过所有文件）。
//
// 任一候选失败（坏扩展名、绝对路径、逃逸、文件缺失、读取失败、超限）都静默
// 跳过该路径，不返回错误。返回顺序与候选在 prompt 中出现的顺序一致；同一路径
// 出现多次则按次数分别返回。CJK 路径按 UTF-8 原样处理；相同入参与文件状态
// 得到完全相同的结果。
func ImageInputsFromPrompt(prompt, root string, maxBytes int64) []ImageInput {
	var out []ImageInput
	base, err := filepath.Abs(root)
	if err != nil {
		return out
	}
	for _, raw := range imagePathRefs(prompt) {
		if !isAllowedImageExt(filepath.Ext(raw)) {
			continue
		}
		// 绝对路径（含 Windows 盘符相对路径）一律拒绝。
		if filepath.IsAbs(raw) || filepath.VolumeName(raw) != "" {
			continue
		}
		// Clean + Join 解析后，必须仍落在 root 之内。
		resolved := filepath.Join(base, filepath.Clean(raw))
		rel, err := filepath.Rel(base, resolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		// 先 stat 以便廉价地跳过超限文件；读取后再按实际字节数复核。
		info, err := os.Stat(resolved)
		if err != nil || info.IsDir() || info.Size() > maxBytes {
			continue
		}
		data, err := os.ReadFile(resolved)
		if err != nil || int64(len(data)) > maxBytes {
			continue
		}
		out = append(out, ImageInput{
			MimeType: http.DetectContentType(data),
			Data:     data,
		})
	}
	return out
}

// isAllowedImageExt 判断扩展名（含前导点）是否属于允许的图片类型。
// 这里先小写化再比较，因此 .PNG/.JpG 等大小写形式均被接受。
func isAllowedImageExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	default:
		return false
	}
}

// imagePathRefs 按出现顺序提取 prompt 中每个 '@' 后面的路径串。
// 裸路径是 '@' 后的连续非空白字符；被引号包围时允许路径含空格。扫描时会跳过
// 已消费的 token，避免把 token 内部的 '@' 误当作新的引用起点。
func imagePathRefs(prompt string) []string {
	var refs []string
	for i := 0; i < len(prompt); {
		r, size := utf8.DecodeRuneInString(prompt[i:])
		if r != '@' {
			i += size
			continue
		}
		token, consumed := imagePathToken(prompt[i+1:])
		if token != "" {
			refs = append(refs, token)
		}
		i += 1 + consumed
	}
	return refs
}

// imagePathToken 解析 '@' 之后的路径：若首字符是引号且存在闭合引号，返回引号
// 之间的内容（可含空格）；否则返回连续的非空白字符。consumed 是本次消费的
// 字节数（含成对引号），0 表示没有可用 token。
func imagePathToken(rest string) (token string, consumed int) {
	if rest == "" {
		return "", 0
	}
	first, firstSize := utf8.DecodeRuneInString(rest)
	if first == '"' || first == '\'' || first == '`' {
		if end := strings.IndexByte(rest[firstSize:], byte(first)); end >= 0 {
			return rest[firstSize : firstSize+end], firstSize + end + 1
		}
		return "", 0
	}
	n := 0
	for n < len(rest) {
		r, size := utf8.DecodeRuneInString(rest[n:])
		if unicode.IsSpace(r) {
			break
		}
		n += size
	}
	return rest[:n], n
}
