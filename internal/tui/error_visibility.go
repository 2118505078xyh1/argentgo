package tui

import (
	"context"
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
	// maxErrorDetailFull 限制错误通知可展开详情的长度。它比一行摘要宽，
	// 但仍设上界，防止异常 provider 把巨量文本钉进 transcript。
	maxErrorDetailFull = 8 * maxStreamErrorDetail
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

// RetryNotice 用产品默认中文格式化一行实时退避反馈。它是兼容旧 4 参调用方
// （含内部 app 测试）的入口；带 locale 的路径由 retryNotice 提供。
func RetryNotice(reason string, attempt, max int, waitMS int64) string {
	return retryNotice("zh", reason, attempt, max, waitMS)
}

// retryNotice 按 locale 格式化退避反馈，文案与 reason 短句都走本包文案表，
// 不在代码里写死英文。reason 先经 llm.RedactText 清洗、折叠、限长，再按
// 封闭标记映射成本地化短句（未知标记原样保留）；计数钳制为非负，使字符串
// 对格式错误的调用方也稳定。前导 "⟳" 是文本的一部分，因为该通知作为普通
// EntryNotice 追加，渲染器再画尝试徽章会重复该字形。
func retryNotice(locale, reason string, attempt, max int, waitMS int64) string {
	reason = Truncate(collapseSpace(llm.RedactText(reason)), maxRetryReason)
	reason = localRetryReason(locale, reason)
	if attempt < 0 {
		attempt = 0
	}
	if max < 0 {
		max = 0
	}
	if waitMS < 0 {
		waitMS = 0
	}
	return fmt.Sprintf(tuiText(locale, "retry_line"), reason, waitMS, attempt, max)
}

// errorNoticeEntry 为回合结束错误构建一条"可展开 + 带下一步动作"的通知。
// 摘要行始终可独立阅读并附 /retry 动作提示；完整（脱敏、限长）文本存入
// Detail，只有点击展开时才渲染。sticky 标记它不随状态行 TTL 退休。
func errorNoticeEntry(locale string, err error) Entry {
	if err == context.Canceled {
		return Entry{Kind: EntryNotice, Text: text(locale, "cancelled")}
	}
	full := Truncate(collapseSpace(llm.RedactText(err.Error())), maxErrorDetailFull)
	short := Truncate(full, maxStreamErrorDetail)
	return Entry{
		Kind:   EntryNotice,
		Text:   text(locale, "error") + ": " + short + " · " + tuiText(locale, "error_action"),
		Detail: full,
		sticky: true,
	}
}
