package agent

import "sync/atomic"

// retainGuidance 控制是否让 stuck/mistake 的纠正提示记录在压缩后存活。默认开启：
// 由 app 层按 ARGENT_RETAIN_GUIDANCE 统一设置，仅 =0 时紧急关闭；
// =0 关闭时 nudge 记录保持原有的字符串载荷，字节与既有运行完全一致。
var retainGuidance atomic.Bool

// SetRetainGuidance 打开或关闭纠正提示的压缩存活标记。
// =0 关闭时所有 nudge 记录走原始字符串载荷路径。
func SetRetainGuidance(enabled bool) {
	retainGuidance.Store(enabled)
}

// retainGuidanceEnabled 报告当前是否启用了纠正提示的压缩存活标记。
func retainGuidanceEnabled() bool {
	return retainGuidance.Load()
}

// 该常量镜像 session 包的 in_thread 线格式值。agent 不 import session，
// 保持低层包零依赖；载荷经 session.Append 时会由 StampIfImportant 识别该提示。
const guidanceRetentionValue = "in_thread"

// retainedGuidance 是开启时 nudge 记录的载荷。它被编码为带 _retention
// 提示的 JSON 对象：session 的 GetRetention 读到 in_thread，SurvivesCompaction
// 为真，记录因而能熬过压缩；同时不会泄漏到子会话。
type retainedGuidance struct {
	Text      string `json:"text"`
	Retention string `json:"_retention"`
}

// nudgePayload 按开关选择 nudge 记录的载荷形状。=0 关闭时返回原字符串，
// 与历史 run 的会话 JSONL 逐字节一致；默认开启时返回带保留提示的对象。
// 入参文本不被修改，编码交给 encoding/json，CJK 原样保留。
func nudgePayload(text string) any {
	if !retainGuidanceEnabled() {
		return text
	}
	return retainedGuidance{Text: text, Retention: guidanceRetentionValue}
}
