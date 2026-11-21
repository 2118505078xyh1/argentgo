package session

import (
	"errors"
	"fmt"
)

// 失败尝试丢弃使用的哨兵错误。调用方可用 errors.Is 区分两种拒绝原因：
// 没有足够失败证据，以及分叉点不在活动路径上。
var (
	// ErrAttemptNotDiscardable 表示尝试没有失败证据，或混入了成功结果。
	ErrAttemptNotDiscardable = errors.New("session: attempt is not discardable")
	// ErrAttemptBranchOffPath 表示失败尝试的分叉点无法从当前活动叶节点回溯到，
	// 继续分叉会切到与当前对话无关的分支，因此必须拒绝。
	ErrAttemptBranchOffPath = errors.New("session: attempt branch point is not on the active path")
)

// DiscardFailedAttempt 把一次失败的尝试"丢弃"为一个新会话：在分叉点复制历史，
// 并把指向源会话的重试血缘追加到新会话。源会话的转录文件永远不会被修改。
//
// 这是显式方法，默认没有任何调用路径自动触发它。只有 MarkFailure 给出的标记
// 全部为失败（CanDiscardAttempt 为真）、且分叉点位于活动路径上时才真正分叉；
// 否则 fail-closed，返回哨兵错误，不触碰任何文件。
func (s *Session) DiscardFailedAttempt(attemptID string) (*Session, error) {
	records, err := s.Records()
	if err != nil {
		return nil, err
	}
	markers := MarkFailure(records, attemptID)
	if !CanDiscardAttempt(markers, attemptID) {
		return nil, fmt.Errorf("%w: %q", ErrAttemptNotDiscardable, attemptID)
	}
	branchPoint, ok := BranchPointFor(records, attemptID)
	if !ok {
		return nil, fmt.Errorf("%w: no branch point for %q", ErrAttemptNotDiscardable, attemptID)
	}
	// 防跨分支误切：分叉点必须能由当前活动叶节点沿 parent 链回溯到。
	onPath := false
	for _, record := range s.activePath(records) {
		if record.ID == branchPoint {
			onPath = true
			break
		}
	}
	if !onPath {
		return nil, fmt.Errorf("%w: %q (active tip %q)", ErrAttemptBranchOffPath, branchPoint, s.activeSnapshot())
	}

	fork, err := s.Fork(branchPoint)
	if err != nil {
		return nil, err
	}
	sourceRecord := attemptRootRecordID(records, attemptID)
	if _, err := fork.AppendAttempt(Attempt{
		Reason:        AttemptRetried,
		SourceSession: s.ID(),
		SourceRecord:  sourceRecord,
	}); err != nil {
		return nil, err
	}
	return fork, nil
}

// attemptRootRecordID 返回 attemptID 对应的 KindAttempt 记录 id，供血缘引用。
// 优先精确相等，其次用 id 前缀归属；都没有时原样返回传入的 id。
func attemptRootRecordID(records []Record, attemptID string) string {
	for _, record := range records {
		if record.Kind == KindAttempt && record.ID == attemptID {
			return record.ID
		}
	}
	for _, record := range records {
		if record.Kind == KindAttempt && idInAttempt(record.ID, attemptID) {
			return record.ID
		}
	}
	return attemptID
}
