package agent

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// 并行批次的部分回执：让模型和用户在批次失败时看清失败前哪些调用已经
// 产生副作用，从而避免把整批调用盲目重发。
//
// 本文件的核心 partialBatchReceipt 是纯函数：只读参数，不访问时钟、环境或全局状态。
// 接线刻意做成 opt-in（见 WithBatchReceipt），默认路径逐字节不变。

const (
	// receiptMaxRunes 是整份回执的 rune 上限。超过时在 rune 边界截断并加省略号，
	// 使回放给模型/前端的文本始终有界。
	receiptMaxRunes = 200
	// receiptNameMaxRunes 是单个工具名的 rune 上限。
	receiptNameMaxRunes = 32
	// receiptErrMaxRunes 是单个错误文本的 rune 上限。
	receiptErrMaxRunes = 48

	// receiptDonePrefix 与 receiptNoUndo 构成回执的固定骨架。
	// 文案指向"已生效调用不撤销、勿重发"这一安全语义。
	receiptDonePrefix = "已完成 "
	receiptCountMid   = " 个："
	receiptNoUndo     = "。失败前已生效的调用列出，不撤销；请勿盲目重发。"
)

// CallStatus 是一次工具调用在批次结算后的结果分类。
// 它是字符串别名，因此调用方既能使用下面的具名常量，也能直接写字符串字面量。
type CallStatus string

const (
	// CallSucceeded 表示工具执行成功，其副作用已经生效。
	CallSucceeded CallStatus = "成功"
	// CallFailed 表示工具执行失败；失败之前批次中更早的成功调用不被撤销。
	CallFailed CallStatus = "失败"
	// CallSkipped 表示调用未真正执行（例如被防护阻断或批次已取消），没有产生副作用。
	CallSkipped CallStatus = "未执行"
)

// CallRecord 描述批次中一次调用的结算信息，是 partialBatchReceipt 的输入。
// Duration 仅作记录，不进入回执文本；Err 在非空时附在状态之后。
type CallRecord struct {
	Name     string
	Status   CallStatus
	Duration time.Duration
	Err      string
}

// partialBatchReceipt 根据一次批次的调用记录生成可读的文本回执。
//
// 格式为「已完成 N 个：name(status)；…。失败前已生效的调用列出，不撤销；请勿盲目重发。」。
// 所有工具名和错误文本都会被折叠成单行、按 rune 边界截断，因此结果始终是合法 UTF-8、
// CJK 安全且有界的。它是纯函数：相同输入永远得到相同输出。
func partialBatchReceipt(calls []CallRecord) string {
	var b strings.Builder
	b.WriteString(receiptDonePrefix)
	b.WriteString(strconv.Itoa(len(calls)))
	b.WriteString(receiptCountMid)
	for i, call := range calls {
		if i > 0 {
			b.WriteString("；")
		}
		status := string(call.Status)
		if status == "" {
			status = string(CallSkipped)
		}
		entry := receiptTruncate(receiptCleanLine(call.Name), receiptNameMaxRunes) + "(" + status
		if call.Err != "" {
			entry += ": " + receiptTruncate(receiptCleanLine(call.Err), receiptErrMaxRunes)
		}
		b.WriteString(entry + ")")
	}
	b.WriteString(receiptNoUndo)
	return receiptTruncate(b.String(), receiptMaxRunes)
}

// receiptTruncate 把 s 限制在 maxRunes 个 rune 以内；超出时保留前 maxRunes-1 个
// rune 并追加省略号。range 在 rune 边界给出下标，因此截断处绝不切断 UTF-8 序列，
// CJK 等多字节字符保持完整。
func receiptTruncate(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	kept := 0
	for pos := range s {
		if kept == maxRunes-1 {
			return s[:pos] + "…"
		}
		kept++
	}
	return s
}

// receiptCleanLine 把文本中的控制字符（含换行、ANSI ESC 等）折叠为空格，
// 防止回执注入额外的记录行或转义码。逐 rune 处理，因此 CJK 字符原样保留，
// 非法 UTF-8 字节也会被写成合法的替换字符。
func receiptCleanLine(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// batchReceiptEmitterKey 是 context 中可选回执发出器的私有键类型。
type batchReceiptEmitterKey struct{}

// WithBatchReceipt 在 ctx 中安装一个回执发出器。只有显式调用它的调用方，
// 才会在并行批次结算后收到 partialBatchReceipt 生成的文本；未安装时
// runParallelCalls 的事件与历史记录完全不变（逐字节兼容既有测试）。
// emit 为 nil 时原样返回 ctx。
func WithBatchReceipt(ctx context.Context, emit func(string)) context.Context {
	if emit == nil {
		return ctx
	}
	return context.WithValue(ctx, batchReceiptEmitterKey{}, emit)
}

// callRecordFrom 把一次已结算的调用映射为回执记录。
// 真正执行且失败的调用记为 CallFailed；未运行（审批失败、防护阻断、取消）的调用
// 记为 CallSkipped，表示没有产生副作用；其余记为 CallSucceeded。
func callRecordFrom(call llm.ToolCall, res callResult) CallRecord {
	status := CallSucceeded
	switch {
	case !res.ran:
		status = CallSkipped
	case res.isError:
		status = CallFailed
	}
	errText := ""
	if status == CallFailed {
		errText = res.output
	}
	return CallRecord{Name: call.Function.Name, Status: status, Duration: res.duration, Err: errText}
}
