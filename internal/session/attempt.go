package session

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/security"
	"github.com/2118505078xyh1/argentgo/internal/textutil"
)

// Attempt 原因记录为仅追加的审计记录。它们永远不会进入模型可见的消息历史。
const (
	AttemptFailed      = "failed"
	AttemptRetried     = "retried"
	AttemptCancelled   = "cancelled"
	AttemptStreamError = "stream_error"
)

// Attempt 是 kind:"attempt" 记录的载荷。错误文本在写入前会被脱敏；
// 只有已提交的消息会被重建为模型历史。
type Attempt struct {
	Reason        string    `json:"reason"`
	Message       string    `json:"message,omitempty"`
	ErrorClass    string    `json:"errorClass,omitempty"`
	SourceSession string    `json:"sourceSession,omitempty"`
	SourceRecord  string    `json:"sourceRecord,omitempty"`
	Prompt        string    `json:"prompt,omitempty"`
	TS            time.Time `json:"-"`
}

// NewAttempt 根据错误构建一条尝试记录。nil 错误只记录原因；
// 非 nil 错误会贡献一条脱敏后的消息及其 Go 类型作为稳定的错误类别。
func NewAttempt(reason string, err error) Attempt {
	attempt := Attempt{Reason: strings.TrimSpace(reason)}
	if err == nil {
		return attempt
	}
	attempt.Message = err.Error()
	attempt.ErrorClass = fmt.Sprintf("%T", err)
	return attempt
}

// AppendAttempt 记录一次尝试并返回存储的记录。
func (s *Session) AppendAttempt(attempt Attempt) (Record, error) {
	attempt.Reason = strings.TrimSpace(attempt.Reason)
	if attempt.Reason == "" {
		return Record{}, errors.New("session: empty attempt reason")
	}
	attempt.Message = truncateRunes(security.Redact(attempt.Message), 240)
	attempt.ErrorClass = truncateRunes(security.Redact(attempt.ErrorClass), 80)
	attempt.Prompt = truncateRunes(security.Redact(attempt.Prompt), titleLimit)
	attempt.SourceSession = security.Redact(strings.TrimSpace(attempt.SourceSession))
	attempt.SourceRecord = security.Redact(strings.TrimSpace(attempt.SourceRecord))
	return s.AppendRecord(KindAttempt, attempt)
}

// RecordAttemptError 是 AppendAttempt 的错误形式便捷包装，
// 供只有错误需要报告的调用方使用。
func (s *Session) RecordAttemptError(reason string, err error) (Record, error) {
	return s.AppendAttempt(NewAttempt(reason, err))
}

// Attempts 列出活动分支上的尝试记录，最早的在前。
func (s *Session) Attempts() ([]Attempt, error) {
	records, err := s.Records()
	if err != nil {
		return nil, err
	}
	out := make([]Attempt, 0)
	for _, record := range s.activePath(records) {
		if record.Kind != KindAttempt {
			continue
		}
		var attempt Attempt
		if jsonUnmarshal(record.Data, &attempt) != nil {
			continue
		}
		attempt.TS = record.TS
		out = append(out, attempt)
	}
	return out, nil
}

// Snippet 是树视图和导出视图使用的简短脱敏文本。
func (a Attempt) Snippet() string {
	if text := truncateRunes(a.Message, 60); text != "" {
		return text
	}
	if a.ErrorClass != "" {
		return a.ErrorClass
	}
	return a.Reason
}

// kindProgress 是持久的 assistant 进度检查点所对应的记录 kind。
// 其他记录 Kind 常量位于 session.go，本次改动不拥有该文件，
// 因此进度 kind 被刻意保留在本文件内。
const kindProgress = "progress"

// 单个进度检查点的上限。每收到一个流式块或工具批次后都会写入检查点，
// 因此记录必须保持很小。限制以符文（而非字节）为单位，
// 确保多字节 UTF-8 符文永远不会被从中间切断。
const (
	maxProgressTextRunes = 8 << 10 // 每个检查点保留 8 KiB 的 assistant 文本
	maxProgressToolCalls = 64      // 每个检查点保留的工具调用数
	maxProgressToolRunes = 256     // 每个工具调用名称/参数保留的字符数
)

// Progress 是 kind:"progress" 记录的载荷：记录一个轮次之内已提交的进度，
// 使运行中途崩溃时可以从最后一个检查点恢复，而不只是从最后一条完整的 assistant 消息恢复。
// 与 Attempt 一样，它是持久性/审计记录，永远不会被重建为模型可见的历史。
//
// Turn 从 1 开始，应与 assistant 回复所属的轮次匹配。
// TS 在读取时由外层记录填充；存储的值只是为直接构造 Progress 的调用方提供便利。
type Progress struct {
	Turn          int       `json:"turn"`
	AssistantText string    `json:"assistantText,omitempty"`
	ToolCalls     []string  `json:"toolCalls,omitempty"`
	TS            time.Time `json:"ts"`
}

// capProgressRunes 将文本裁剪到 limit 个符文，其他方面不做改动（不 TrimSpace）。
// 切点交给 textutil.ClipRunes 吸附到字素簇边界，因此组合序列、
// ZWJ emoji 与旗帜不会被从中间劈开；符文预算语义不变。
func capProgressRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return textutil.ClipRunes(text, limit)
}

// sanitizeProgress 在进度载荷写入转录之前限制大小并脱敏。脱敏在截断之前进行，
// 这样跨越上限的凭据就不会以被切断的部分形式残留。工具调用在数量和长度上都受限，
// 以保持单条记录很小。
func sanitizeProgress(p Progress) Progress {
	p.AssistantText = capProgressRunes(security.Redact(p.AssistantText), maxProgressTextRunes)
	if len(p.ToolCalls) > maxProgressToolCalls {
		p.ToolCalls = append([]string(nil), p.ToolCalls[:maxProgressToolCalls]...)
	} else if p.ToolCalls != nil {
		p.ToolCalls = append([]string(nil), p.ToolCalls...)
	}
	for i, tool := range p.ToolCalls {
		p.ToolCalls[i] = capProgressRunes(security.Redact(tool), maxProgressToolRunes)
	}
	return p
}

// AppendProgress 通过普通追加路径追加一个持久的进度检查点，
// 仅在记录无法写入时返回错误。载荷会先被脱敏和限制大小，
// 因此调用方可以传入原始的流式文本。
func (s *Session) AppendProgress(p Progress) error {
	_, err := s.AppendRecord(kindProgress, sanitizeProgress(p))
	return err
}

// progressScope 返回进度查询应扫描的记录：可解析时返回活动分支，
// 否则按文件顺序返回所有记录。
func progressScope(s *Session, records []Record) []Record {
	if path := s.activePath(records); len(path) > 0 {
		return path
	}
	return records
}

// latestProgressRecord 返回按文件排序的切片中最后一个进度记录及其索引，
// 使调用方可以把它与已提交的 assistant 消息比较位置。布尔值报告是否找到了记录。
func latestProgressRecord(records []Record) (Progress, int, bool) {
	var found Progress
	idx := -1
	for i, record := range records {
		if record.Kind != kindProgress {
			continue
		}
		var p Progress
		if jsonUnmarshal(record.Data, &p) != nil {
			continue
		}
		p.TS = record.TS // 记录的时间戳在读取时具有权威性
		found, idx = p, i
	}
	return found, idx, idx >= 0
}

// LatestProgress 返回活动分支上最新的进度检查点
// （当无法解析活动分支时返回最新的记录）。布尔值报告是否找到任何检查点。
// 这是原始查询；如需带过期检查的崩溃恢复，请使用 RecoverProgress。
func (s *Session) LatestProgress() (Progress, bool) {
	records, err := s.Records()
	if err != nil {
		return Progress{}, false
	}
	progress, _, ok := latestProgressRecord(progressScope(s, records))
	return progress, ok
}

// ProgressCount 报告存储了多少个进度检查点，包括非活动分支上的检查点。
// 它用于测试和指标统计。
func (s *Session) ProgressCount() int {
	records, err := s.Records()
	if err != nil {
		return 0
	}
	count := 0
	for _, record := range records {
		if record.Kind == kindProgress {
			count++
		}
	}
	return count
}
