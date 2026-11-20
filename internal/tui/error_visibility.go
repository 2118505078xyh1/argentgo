package tui

import (
	"fmt"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// 流错误可见性辅助函数（goose 研究项 3 和 5）。
//
// 渲染器已经知道如何给任何 AttemptKind 加徽章（见
// Model.attemptLinesLocked），区域设置表也已经带有
// attempt_stream_error / attempt_retried 标签。缺少的是一个生产者：
// 流错误到达 HandleEvent 后却被忽略，而退避根本没有
// 任何事件。这些辅助函数构建该生产者所需的记录条目。
// 它们是纯函数 —— 无 I/O、无模型修改 —— 因此调用方决定何时追加。

const (
	// maxStreamErrorDetail 限制流错误通知中保留的文本。
	// 渲染器会再次按终端宽度裁剪，但模型不能把
	// 任意大的 provider 错误钉在上下文里直到进程退出。
	maxStreamErrorDetail = 240
	// maxRetryReason 限制重试通知中的自由格式原因，使恶意的
	// 或格式错误的生产者无法用单个事件撑宽记录。
	maxRetryReason = 120
)

// StreamErrorEntry 为失败的 LLM 流构建未提交的通知。
//
// 详情被折叠为一行，用提供的函数清洗
// （通常是 llm.RedactText），并限制在 maxStreamErrorDetail 个字素簇内。
// nil 的清洗函数让文本不被清洗，这使该辅助函数
// 便于在表驱动测试中使用。AttemptStreamError 驱动现有的
// 徽章渲染器；不涉及新的绘制路径。
func StreamErrorEntry(detail string, redact func(string) string) Entry {
	if redact != nil {
		detail = redact(detail)
	}
	return Entry{
		Kind:    EntryNotice,
		Attempt: AttemptStreamError,
		Text:    Truncate(collapseSpace(detail), maxStreamErrorDetail),
	}
}

// RetryNotice 格式化一行实时的退避反馈：
//
//	⟳ retry <reason> in <waitMS>ms (attempt/max)
//
// 原因通过 llm.RedactText 清洗、折叠并限制长度；
// 计数被钳制为非负值，使字符串对
// 格式错误的调用方也稳定。前导标记是文本的一部分，因为该通知
// 作为普通 EntryNotice 追加：渲染器为每条记录画一个徽章，
// 这里再来一个尝试徽章会重复 "⟳" 字形。
func RetryNotice(reason string, attempt, max int, waitMS int64) string {
	reason = Truncate(collapseSpace(llm.RedactText(reason)), maxRetryReason)
	if reason == "" {
		reason = "stream"
	}
	if attempt < 0 {
		attempt = 0
	}
	if max < 0 {
		max = 0
	}
	if waitMS < 0 {
		waitMS = 0
	}
	return fmt.Sprintf("⟳ retry %s in %dms (%d/%d)", reason, waitMS, attempt, max)
}
