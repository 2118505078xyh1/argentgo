// Package agent —— 编辑格式指引。
//
// omp 的特色是按模型调整编辑格式：通过提示告诉模型哪种编辑策略最适合其所属系列。
// argentgo 已经提供了 hashline 锚点（通过 edit 工具的 hashline 字段选择性启用）和
// 分级修复阶梯（repair:true），但系统提示从未提及它们。
// 本文件提供一段简短的、感知模型系列的文本块，可追加到系统提示中。
// 它刻意只使用标准库，并停留在 agent 提示层：不会泄漏任何提供方/协议细节。
//
// 系列调整通过一个小型配置来表达（见 editProfile）。配置声明 (a) 追加到提示中的提示行，
// 以及 (b) 一个建议性的"首选编辑工具"，供构建工具目录或 UI 提示的调用方使用。
// 该首选工具刻意不注入系统提示：这样现有提示的字节保持稳定，
// 且没有任何工具注册或执行依赖于它。
package agent

import "strings"

// editFormatMarker 让 AppendEditFormat 能识别自己添加的内容，
// 因此重复追加（例如 schema 重试）不会造成该文本块重复。
const editFormatMarker = "<!-- edit-format-guidance -->"

// commonEditFormat 对所有模型系列都包含。这两句话是关键的：
// 输出围栏代码块而不调用工具的 agent 会浪费轮次，而未先读取就进行的编辑没有可靠的锚点文本。
const commonEditFormat = "All changes must go through tools (write/edit/multi_edit); never output code blocks as edits. " +
	"Always read before edit to get exact anchor text."

// ModelFamily 是仅用于提示调整的粗略系列标签。它是字符串，以便在日志和测试中可打印；
// 调用方不得用它来分发工具（见 editProfile.preferredTool 的说明）。
type ModelFamily string

const (
	FamilyAnthropic ModelFamily = "anthropic"
	FamilyOpenAI    ModelFamily = "openai"
	FamilyGemini    ModelFamily = "gemini"
	FamilyDeepseek  ModelFamily = "deepseek"
	// FamilyHashlineChunked 涵盖那些共享同一提示行的较小"小分块 hashline"同系列
	// 模型（Grok/MiniMax/Qwen）。
	FamilyHashlineChunked ModelFamily = "hashline-chunked"
	FamilyUnknown         ModelFamily = "unknown"
)

// 提示行以常量形式保存，这样对某个系列重新分类时，不会悄悄改变现有系列的提示文本。
// 前四个字符串与旧版 strategyFor 的输出逐字节一致。
const (
	hintHashlineAnchor = "Claude/Anthropic: prefer hashline edits (hashline:true). Set anchor to the exact text you first read and " +
		"anchor_hash to the first 12 hex chars of sha256(anchor bytes). Keep anchors small and unique."
	hintEditRepair = "GPT/OpenAI: prefer edit with one unique old->new pair; on whitespace drift retry with repair:true. " +
		"Batch several changes with multi_edit."
	hintSmallChunks = "Grok/Gemini/MiniMax/Qwen: prefer hashline edits in small chunks - read a short unique anchor, then edit " +
		"with hashline:true and anchor_hash (12-hex sha256). Split large rewrites."
	hintDeepseek = "DeepSeek: prefer edit with one unique old->new pair; use repair:true when whitespace drifted and multi_edit " +
		"for several changes. Keep anchors short and unique."
	hintUnknown = "Unknown model: both styles are available - edit (unique old->new, repair:true for whitespace drift) or " +
		"hashline (hashline:true + anchor_hash = 12-hex sha256 of the text you read). Read first; choose the best fit."
)

// editProfile 是轻量的按系列声明。它刻意是只读且私有的：
// 只导出下面的检测和咨询辅助函数，这样任何调用方都无法依赖配置内部细节来分发工具。
type editProfile struct {
	family ModelFamily
	hint   string // 追加到系统提示的策略行
	// preferredTool 是一个真实注册的工具名，作为该系列的首选对外公布。
	// 空值表示"没有强烈偏好"。它是供构建工具描述/UI 提示的调用方使用的建议性元数据；
	// 它从不改变工具注册或执行，也从不进入提示。
	preferredTool string
}

// profileFor 把检测到的系列映射到其调整声明。未知系列回退到旧版通用提示，且不带工具偏好。
func profileFor(family ModelFamily) editProfile {
	switch family {
	case FamilyAnthropic:
		return editProfile{family, hintHashlineAnchor, "edit"}
	case FamilyOpenAI:
		return editProfile{family, hintEditRepair, "edit"}
	case FamilyGemini:
		// Gemini 原样保留旧版 Grok/Gemini/MiniMax/Qwen 文本。
		return editProfile{family, hintSmallChunks, "edit"}
	case FamilyDeepseek:
		return editProfile{family, hintDeepseek, "edit"}
	case FamilyHashlineChunked:
		return editProfile{family, hintSmallChunks, "edit"}
	default:
		return editProfile{FamilyUnknown, hintUnknown, ""}
	}
}

// DetectModelFamily 按优先级顺序，通过不区分大小写的子串匹配，把已解析的模型名
// 映射为粗略的系列。它是纯函数：无 I/O、不查询提供方。无法识别的名称返回 FamilyUnknown。
func DetectModelFamily(model string) ModelFamily {
	name := strings.ToLower(model)
	switch {
	case strings.Contains(name, "claude"), strings.Contains(name, "anthropic"):
		return FamilyAnthropic
	case strings.Contains(name, "gpt"), strings.Contains(name, "openai"),
		strings.Contains(name, "o1"), strings.Contains(name, "o3"):
		return FamilyOpenAI
	case strings.Contains(name, "gemini"):
		return FamilyGemini
	case strings.Contains(name, "deepseek"):
		return FamilyDeepseek
	case strings.Contains(name, "grok"), strings.Contains(name, "minimax"),
		strings.Contains(name, "qwen"):
		return FamilyHashlineChunked
	default:
		return FamilyUnknown
	}
}

// RecommendedEditTool 返回该模型系列通常首选且容易成功的注册工具名
// （对目前所有已配置的系列都是 "edit"），没有强烈偏好时返回 ""。
// 它仅供参考：调用方可以用它在目录或 UI 提示中对工具描述排序，
// 但工具注册和执行不受影响。
func RecommendedEditTool(model string) string {
	return profileFor(DetectModelFamily(model)).preferredTool
}

// EditToolPreference 返回一句简短的建议性语句，适用于工具"宣传"
// （目录简介、UI 提示）。未知模型返回 ""，且本包绝不会把它追加到系统提示中。
func EditToolPreference(model string) string {
	tool := RecommendedEditTool(model)
	if tool == "" {
		return ""
	}
	return "Preferred edit tool for this model: " + tool + "."
}

// EditFormatGuidance 返回一段简短（<=600 字节）、感知模型系列的编辑指引，
// 适合追加到系统提示中。
//
// 系列检测是启发式的且刻意简单：按优先级顺序对已解析的模型名做不区分大小写的子串匹配。
// 它只是提示性内容——没有任何行为依赖于它。
func EditFormatGuidance(model string) string {
	return editFormatMarker + "\n" + strategyFor(model) + "\n" + commonEditFormat
}

// AppendEditFormat 用空行作为分隔，把 EditFormatGuidance 追加到 system 之后。
// 它是幂等的：如果 system 已经带有该指引标记，就原样返回。
// 原始文本只会被扩展、绝不会被裁剪，因此锁定系统提示的调用方能保持其字节不变。
func AppendEditFormat(system, model string) string {
	if strings.Contains(system, editFormatMarker) {
		return system
	}
	if system == "" {
		return EditFormatGuidance(model)
	}
	sep := "\n\n"
	switch {
	case strings.HasSuffix(system, "\n\n"):
		sep = ""
	case strings.HasSuffix(system, "\n"):
		sep = "\n"
	}
	return system + sep + EditFormatGuidance(model)
}

// strategyFor 从检测到的系列配置中挑选策略行。提示字符串与之前的子串判断保持一致，
// 因此对每一个此前已识别的系列，现有提示字节都会保持不变。
func strategyFor(model string) string {
	return profileFor(DetectModelFamily(model)).hint
}
