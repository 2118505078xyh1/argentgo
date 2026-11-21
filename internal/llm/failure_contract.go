package llm

import (
	"context"
	"errors"
	"net/http"
)

// 本文件把散落在 llm 包内的失败分类、可重试判断与溢出识别收敛为一组导出的
// 纯函数，作为 agent 与 app 的单一入口，使它们不必各自重复实现哨兵、状态码
// 与文本模式的组合逻辑。
//
// 约定：这里只做"组合与转发"，权威逻辑仍属于原有分类器
// （ClassifyAPIError / ClassifyRetryError / IsRetryableStatus 等），
// 因此不会改变任何 wire 协议行为或输出字节；所有函数只依赖入参，
// 不读写可变状态、不产生副作用，且保持 llm 零 internal 依赖。

// IsContextOverflow 报告 err 是否是明确的上下文窗口溢出：模型请求因
// 提示词或历史对话超出上下文上限而被拒绝。
//
// 它复用 ClassifyAPIError 的权威顺序：net/context 哨兵与流错误先于
// 通用文本模式，因此一个恰好提到 "context window exceeded" 的 DNS
// 失败仍是网络失败，而不是溢出。nil 返回 false。
// 结果不含任何提供方文本或凭据。
func IsContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	actionable := ClassifyAPIError(err)
	return actionable != nil && actionable.Family == FamilyContextOverflow
}

// IsByteSizeOverflow 报告一段错误文本是否明确表示"请求体按字节计超过
// 上限"（HTTP 413 / request body too large 等）。它是 isByteSizeOverflow
// 的导出入口，供 agent/app 复用；大小写不敏感、支持中文、nil 语义为空串。
//
// 字节级溢出与上下文溢出是两个不同概念：前者重发同样的请求不会成功，
// 但压缩历史对话也无法修复，因此调用方应分别处理。
func IsByteSizeOverflow(msg string) bool {
	return isByteSizeOverflow(msg)
}

// IsRecoverableStreamError 报告一段错误文本是否描述"可重试的流中断"：
// 提供方在开始输出后被中途切断，重放同一请求通常能成功。
// 它是 isRecoverableStreamError 的导出入口；只做文本识别，不读写状态。
func IsRecoverableStreamError(msg string) bool {
	return isRecoverableStreamError(msg)
}

// IsRetryableError 报告一个传输/提供方错误是否值得原样重试，是
// ClassifyRetryError 的布尔门面。取消、上下文溢出与无法识别的失败都返回
// false；超时、不可达主机与普通传输失败返回 true。nil 返回 false。
func IsRetryableError(err error) bool {
	return ClassifyRetryError(err) != ReasonNone
}

// RetryReasonForResponse 报告一次已收到 HTTP 响应的失败属于哪个重试类别。
// 它组合了 llm 内既有的三类终态判定（终态提供方结果、硬性配额、上下文溢出）
// 与状态码判定：终态一律 ReasonNone，可重试状态返回 ReasonStatus，
// 成功（2xx）与不可重试状态返回 ReasonNone。
//
// body 应为响应体的有界、已脱敏前缀；status 为 0 表示没有响应，返回 ReasonNone。
func RetryReasonForResponse(status int, body []byte) Reason {
	if status == 0 {
		return ReasonNone
	}
	// 终态结果、硬性配额与上下文溢出都无法靠等待消除，必须先于状态码判定。
	if IsTerminalProviderOutcome(body, status) || IsNonRetryableQuotaResponse(status, body) {
		return ReasonNone
	}
	if len(body) > 0 && IsContextOverflow(errors.New(string(body))) {
		return ReasonNone
	}
	if status >= http.StatusMultipleChoices && IsRetryableStatus(status) {
		return ReasonStatus
	}
	return ReasonNone
}

// ClassifyRetry 是把一次失败的全部可用信号合并为单个重试类别的统一入口。
// err 是传输/提供方错误，status 与 body 是可选的 HTTP 响应信息：
//   - err 非 nil 时以 ClassifyRetryError 为准（其中已包含取消与上下文溢出的
//     权威排除），status/body 被忽略；
//   - err 为 nil 且 status 非 0 时以 RetryReasonForResponse 为准；
//   - 二者皆无时返回 ReasonNone。
//
// 调用方据此决定"是否重试/是否换模型"，无需在 agent/app 重复实现状态码、
// 终态文本与传输哨兵的并集判断。该函数是纯函数：只依赖入参，无副作用。
func ClassifyRetry(err error, status int, body []byte) Reason {
	if err != nil {
		// context.Canceled 在 ClassifyRetryError 中已是 ReasonNone；这里
		// 显式再判一次，确保取消永远优先于任何响应信号。
		if errors.Is(err, context.Canceled) {
			return ReasonNone
		}
		return ClassifyRetryError(err)
	}
	return RetryReasonForResponse(status, body)
}
