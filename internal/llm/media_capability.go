package llm

import (
	"fmt"
	"strings"
)

// mediaCapabilityNoteDefault 是当"模型不支持图像、内容却含图像"时给出的提示模板。
// %s 会被替换为调用方传入的原始模型名（按 rune 原样嵌入，不做字节切片，CJK 安全）。
// 提示强调"已用占位符替代"，使调用方明白不应再把 base64 静默塞进文本内容。
const mediaCapabilityNoteDefault = "模型 %s 不支持图像输入，已用占位符替代"

// visionModelMarkers 是已知支持图像输入的模型名片段（规范化为小写）。
// 命中任意一个即认为该模型天然支持图像，此时即便内容含图像也不给出提示。
// 列表按子串匹配、大小写不敏感；只在此处读取、从不修改，以保证函数纯度。
var visionModelMarkers = []string{
	"gpt-4o",       // 覆盖 gpt-4o、gpt-4o-mini 等（均支持视觉）
	"gpt-4-vision", // 早期视觉预览版
	"gpt-4.1",      // gpt-4.1 系列支持视觉
	"claude-3",     // Claude 3 系列支持视觉
	"claude-4",     // Claude 4 系列支持视觉
	"gemini",       // Gemini 全系支持视觉
	"qwen-vl",      // 通义千问视觉版
	"qwen2-vl",     // 通义千问 2 视觉版
	"qwen2.5-vl",   // 通义千问 2.5 视觉版
	"qvq",          // 通义千问视觉推理版
	"llava",        // LLaVA 视觉模型
	"vision",       // 通用视觉标记
	"文心一言-多模态",     // 多模态版本按视觉处理（纯文本版在 textOnly 列表）
}

// textOnlyModelMarkers 是已知不支持图像输入的模型名片段（规范化为小写）。
// 只有规范化后的模型名命中其中之一时，mediaCapabilityNote 才给出提示；
// "能力未知"的模型一律按保守策略处理——不提示，避免误伤未来支持视觉的模型。
// 列表按子串匹配、大小写不敏感；只在此处读取、从不修改，以保证函数纯度。
var textOnlyModelMarkers = []string{
	"gpt-3.5",           // GPT-3.5 全系为纯文本
	"gpt-4-0613",        // 早期 GPT-4 快照为纯文本
	"gpt-4-32k",         // 早期 GPT-4 长上下文快照为纯文本
	"o1-mini",           // o1-mini 不支持图像（o1 正版支持，未列入）
	"o3-mini",           // o3-mini 不支持图像（o3 正版支持，未列入）
	"deepseek-chat",     // DeepSeek 对话版为纯文本（deepseek-vl 未列入）
	"deepseek-reasoner", // DeepSeek 推理版为纯文本
	"deepseek-coder",    // DeepSeek 代码版为纯文本
	"text-embedding",    // 嵌入模型为纯文本
	"文心一言",              // 文心一言默认为纯文本（CJK，验证原样嵌入）
	"百川",                // 百川系列默认为纯文本
}

// mediaCapabilityNote 根据模型名与"内容是否含图像"生成一条可选的能力提示：
// 仅当 hasImage 为真、且模型名明确属于已知的不支持图像输入的模型时，
// 才返回形如"模型 X 不支持图像输入，已用占位符替代"的提示；
// 其余情况（无图像、模型支持图像、模型为空、能力未知）一律返回空串。
//
// VisionMarkerFor 报告模型名是否命中已知视觉模型标记。它是 provider 能力画像
// 与 llm 媒体提示共用的单一判定入口，避免两处各自维护一份漂移的标记表。
// 纯函数：只做 TrimSpace + ToLower 后子串匹配，CJK 安全，不改入参。
func VisionMarkerFor(model string) bool {
	name := strings.ToLower(strings.TrimSpace(model))
	if name == "" {
		return false
	}
	for _, marker := range visionModelMarkers {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}

// 这是一个纯函数：不读取全局可变状态（上面的切片只在此处只读遍历），
// 不修改任何参数，不发送事件，也不改变任何线协议行为；是否接入请求流程
// 由调用方依据产品策略决定。模型名仅做 TrimSpace 与 ToLower 后用于匹配，
// 嵌入提示时不做任何字节切片，因此 CJK 等多字节模型名不会被截断或破坏。
func mediaCapabilityNote(model string, hasImage bool) string {
	if !hasImage {
		return ""
	}
	display := strings.TrimSpace(model)
	if display == "" {
		return ""
	}
	name := strings.ToLower(display)

	// 视觉模型优先：部分模型名可能同时带有文本族前缀，
	// 先确认支持图像即可安全返回空串。
	for _, marker := range visionModelMarkers {
		if strings.Contains(name, marker) {
			return ""
		}
	}
	for _, marker := range textOnlyModelMarkers {
		if strings.Contains(name, marker) {
			// 直接以 rune 原样嵌入，CJK 安全。
			return fmt.Sprintf(mediaCapabilityNoteDefault, display)
		}
	}
	// 能力未知：保守起见不提示。
	return ""
}
