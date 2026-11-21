package session

import (
	"errors"
	"strings"
)

// 本文件提供"前置尝试根"原语：在运行/任务之前先写一条 KindAttempt 记录，
// 使后续工具记录自动以它为 parent，形成"根在前、失败工具在后"的可丢弃结构。
// 默认没有任何调用路径触发它；两个函数都是纯接线 helper，除 AppendRecord 外零 I/O，
// 且不修改 AppendRecord 本身。

// attemptBeginReason 是 BeginAttempt 在未指定原因时使用的默认原因。
// 它只是一条稳定文本，不参与任何标记判定。
const attemptBeginReason = "task_start"

// BeginAttempt 在运行/任务之前写入一条前置 KindAttempt 根记录，返回其记录 id。
//
// 它只通过现有的 AppendRecord 写入：parent 自动取当前活动叶，写入后活动叶前移，
// 因此紧随其后的工具记录会自然地把 parent 指向这个根。调用方把返回的 rootID 作为
// attemptID 传给 MarkFailure / DiscardFailedAttempt，就能把后续失败工具归属于本次尝试。
// 这是 R11 §2.3 方案 B 的会话侧原语：生产转录的 KindAttempt 根写在运行末尾，
// 失败工具是其祖先而非后代，无法被 MarkFailure 命中；前置根恰好反转这个先后关系。
func BeginAttempt(s *Session, reason string) (rootID string, err error) {
	if s == nil {
		return "", errors.New("session: nil session")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = attemptBeginReason
	}
	record, err := s.AppendRecord(KindAttempt, Attempt{Reason: reason})
	if err != nil {
		return "", err
	}
	return record.ID, nil
}

// EndAttempt 写入一条 KindAttempt 结果记录，reason 为调用方给出的 verdict
// （例如 "succeeded" / "failed"）。
//
// 它与生产 recordAttempt 的形态对齐：同样经 AppendRecord 以当前活动叶为 parent，
// 因此结果记录落在本次尝试子树内，既不影响 MarkFailure 的归属，也不被当作失败标记
// （KindAttempt 载荷没有 is_error，也没有 StopError 的最终性文本）。
// rootID 仅用于 fail-closed 校验：空根 id 直接拒绝写入，避免为不存在的尝试落结果。
// 不设置 SourceRecord：该字段专用于 retry/fork 血缘，给普通结果记录带上它会在树视图里
// 被误读成分叉链接。
func EndAttempt(s *Session, rootID string, verdict string) error {
	if s == nil {
		return errors.New("session: nil session")
	}
	if strings.TrimSpace(rootID) == "" {
		return errors.New("session: empty attempt root id")
	}
	verdict = strings.TrimSpace(verdict)
	if verdict == "" {
		return errors.New("session: empty attempt verdict")
	}
	_, err := s.AppendRecord(KindAttempt, Attempt{Reason: verdict})
	return err
}
