package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrStreamError 标记在提供方接受请求之后、答复仍在流式传输时
// 发生的失败。会话层用它记录一次流错误尝试，
// 而非请求失败。
var ErrStreamError = errors.New("provider stream error")

// AttemptKind 是附加到提供方尝试记录上的稳定原因。
type AttemptKind string

const (
	AttemptSuccess     AttemptKind = "success"
	AttemptFailed      AttemptKind = "failed"
	AttemptRetry       AttemptKind = "retry"
	AttemptCancelled   AttemptKind = "cancelled"
	AttemptStreamError AttemptKind = "stream_error"
)

// AttemptRecord 是给会话层的脱敏尝试通知。它
// 不含提示文本、响应体或凭据材料。
type AttemptRecord struct {
	Kind       AttemptKind
	Protocol   string
	Model      string
	Attempt    int
	StatusCode int
	ErrorClass string
}

// RetryAttempt 描述 RetryPolicy.Do 内部的一次重试决策。Reason
// 解释为何重试该尝试；它从不携带响应体或
// 凭据。
type RetryAttempt struct {
	Attempt    int
	StatusCode int
	Cause      string
	Reason     Reason
}

// ClassifyError 把提供方错误映射到尝试类别。取消优先于
// 流错误，使中途被中止的读取不被算作失败的
// 模型答复。
func ClassifyError(err error) AttemptKind {
	switch {
	case err == nil:
		return AttemptSuccess
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return AttemptCancelled
	case errors.Is(err, ErrStreamError):
		return AttemptStreamError
	default:
		return AttemptFailed
	}
}

// NewAttemptRecord 构建一个 ErrorClass 已脱敏
// 并缩短的记录，使 hooks 可以直接记录它。
func NewAttemptRecord(kind AttemptKind, protocol, model string, err error) AttemptRecord {
	record := AttemptRecord{Kind: kind, Protocol: protocol, Model: model}
	// 取消是停止原因，而非提供方错误类别。
	if err != nil && kind != AttemptCancelled {
		record.ErrorClass = clipErrorClass(RedactError(err))
	}
	return record
}

func emitAttempt(hook func(AttemptRecord), record AttemptRecord) {
	if hook != nil {
		hook(record)
	}
}

// clipErrorClass 按字符数限制错误类别的长度。按字节切片可能把
// 多字节消息（如中文）从 UTF-8 序列中间切断。
func clipErrorClass(text string) string {
	return clipRedacted(text, 256)
}

// streamIntegrity 跟踪答复是否到达其协议终止符，以及
// 是否有工具调用因流式参数为空或格式错误，
// 而不得不用空参数对象补全。在此状态下到达 EOF 意味着提供方
// 在工具调用中途截断了答复：分发它会用捏造的 "{}" 参数
// 运行真实工具。纯文本截断仍然容忍，
// 使省略终止符的网关能继续工作。
type streamIntegrity struct {
	terminated     bool
	incompleteTool bool
}

// toolArguments 补全流式参数，标记曾回退到 "{}"，使
// 未终止的流可以拒绝该答复，而不是用提供方从未发完的
// 参数分发工具。
func (s *streamIntegrity) toolArguments(arguments string) string {
	if strings.TrimSpace(arguments) == "" || !json.Valid([]byte(arguments)) {
		s.incompleteTool = true
		return "{}"
	}
	return arguments
}

// eofError 报告必须让尝试失败的截断。marker 是
// 协议自身的终止符，包含在错误中以便诊断。
func (s *streamIntegrity) eofError(marker string) error {
	if s.terminated || !s.incompleteTool {
		return nil
	}
	return streamError("stream ended without %s while a tool call was incomplete", marker)
}

// streamError 用会话层会归类为流错误尝试的哨兵错误
// 包装一次流中途失败。
func streamError(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrStreamError}, args...)...)
}

// uniqueToolCallID 返回一个不会与提供方提供的任何 id
// （或另一个生成的 id）冲突的生成 id。used 必须包含回复中每个提供方 id；
// 返回的 id 会被插入其中，使使用相同种子的连续调用仍得到不同的 id。
// 重复的调用 id 会让 agent 拒绝整个批次，因此每个协议客户端都必须
// 让其回退 id 经过这里，
// 而不是根据流索引格式化一个名字。
func uniqueToolCallID(used map[string]struct{}, prefix string, seed int) string {
	for n := seed; ; n++ {
		candidate := fmt.Sprintf("%s%d", prefix, n)
		if _, taken := used[candidate]; !taken {
			used[candidate] = struct{}{}
			return candidate
		}
	}
}
