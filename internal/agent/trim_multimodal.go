package agent

import (
	"encoding/json"
	"strings"
	"sync/atomic"

	"example.com/argentgo/internal/llm"
	"example.com/argentgo/internal/tokenize"
)

// multimodalTrim 控制是否在 token 估算中为带图消息计入 ContentParts 的开销。
// 默认开启、ARGENT_MULTIMODAL_TRIM=0 关闭：关闭时消息估算只累加
// Content/Reasoning/ToolCalls，历史逐字节不变。
var multimodalTrim atomic.Bool

// SetMultimodalTrim 由 app 按 ARGENT_MULTIMODAL_TRIM 接线。
func SetMultimodalTrim(enabled bool) { multimodalTrim.Store(enabled) }

// multimodalTrimEnabled 报告带图估算是否开启。
func multimodalTrimEnabled() bool { return multimodalTrim.Load() }

// 视觉 token 的保守估算。视觉模型通常对 low-detail 图按 ~85 token 计费，
// 对 high-detail 图按数百至 ~1700 token 计费；这里用固定上限保守估计，
// 宁可高估也不让带图会话低估上下文压力。
const (
	imageTokensLow  = 85
	imageTokensHigh = 1000
)

// contentPartProbe 只解析估算所需的字段，不触碰协议细节。
type contentPartProbe struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Detail   string `json:"detail"`
	ImageURL *struct {
		URL    string `json:"url"`
		Detail string `json:"detail"`
	} `json:"image_url"`
}

// estimateContentParts 估算 Message.ContentParts（OpenAI 风格 content part 数组）
// 的 token 开销：文本 part 计入字符估算，图像 part 按 detail 取固定上限，
// 未知 part 按其 JSON 字节的 1/4 保守计入。非法 JSON 时回退为整段的字符估算，
// 总之不会让带图消息估算为 0。
func estimateContentParts(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var parts []contentPartProbe
	if err := json.Unmarshal(raw, &parts); err != nil {
		return tokenize.Estimate(string(raw))
	}
	total := 0
	for _, part := range parts {
		switch {
		case strings.EqualFold(part.Type, "text"):
			total += tokenize.Estimate(part.Text)
		case strings.Contains(strings.ToLower(part.Type), "image"),
			part.ImageURL != nil:
			detail := part.Detail
			if part.ImageURL != nil && detail == "" {
				detail = part.ImageURL.Detail
			}
			// 默认按像素尺寸分块计价；尺寸未知时 estimateImageTokens 自行回退到
			// 原有的 85/1000 固定值，使坏 base64 与非 data URL 行为不变。
			url := ""
			if part.ImageURL != nil {
				url = part.ImageURL.URL
			}
			total += estimateImageTokens(detail, url)
		default:
			// 未知 part：按其原始 JSON 字节数保守估算，避免漏算。
			if encoded, err := json.Marshal(part); err == nil {
				total += tokenize.Estimate(string(encoded))
			}
		}
	}
	return total
}

// estimateMessageContentParts 是 EstimateMessage 中默认开启、
// ARGENT_MULTIMODAL_TRIM=0 关闭的扩展点。关闭时返回 0，原有估算字节逐值不变。
func estimateMessageContentParts(m llm.Message) int {
	if !multimodalTrimEnabled() || len(m.ContentParts) == 0 {
		return 0
	}
	return estimateContentParts(m.ContentParts)
}
