package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// BatchEntry 是一次原子提交中的一条记录载荷。kind 非空；data 原样存放，
// 调用方（如压缩路径）可传 json.RawMessage 保留 CJK 原文与字段顺序。
type BatchEntry struct {
	Kind string
	Data any
}

// AppendBatch 把一组记录作为**一个原子单元**提交：N 条记录只在跨进程写锁内
// 一次性 prepare id/parent，单次 fd.Write 落盘，要么全部出现在转录里，要么
// 一条都不出现（清单 #188）。
//
// 这是压缩这类多事件操作的正确提交方式：旧实现逐条 Append，若在第 k 条失败，
// 磁盘上会留下"compaction 已写、restored 半写"的不完整状态，读侧无法区分
// 这是一次失败压缩还是合法历史。批量提交从根上消除这种中间态。
//
// 限制：批大小为 1 时退化为 AppendRecord 的等价路径；空批返回空切片。
func (s *Session) AppendBatch(entries []BatchEntry) ([]Record, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	for i, entry := range entries {
		if strings.TrimSpace(entry.Kind) == "" {
			return nil, fmt.Errorf("session: batch entry %d: empty kind", i)
		}
	}

	// 预编码：编码失败必须在触碰磁盘前发生，否则会留下半批字节。
	data := make([][]byte, len(entries))
	for i, entry := range entries {
		encoded, err := encodePayload(entry.Data)
		if err != nil {
			return nil, fmt.Errorf("session: batch entry %d: %w", i, err)
		}
		data[i] = StampIfImportant(entry.Kind, encoded)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var lastErr error
	for attempt := 0; attempt < appendMaxAttempts; attempt++ {
		records, err := s.appendBatchOnce(data, kindsOf(entries))
		if !errors.Is(err, errAppendRetry) {
			return records, err
		}
		lastErr = err
	}
	return nil, fmt.Errorf("session: batch append: state kept changing after %d attempts: %w",
		appendMaxAttempts, lastErr)
}

func kindsOf(entries []BatchEntry) []string {
	kinds := make([]string, len(entries))
	for i, entry := range entries {
		kinds[i] = strings.TrimSpace(entry.Kind)
	}
	return kinds
}

// appendBatchOnce 在跨进程锁内为整批生成链式 id/parent，单次写入，失败整体回滚。
func (s *Session) appendBatchOnce(data [][]byte, kinds []string) ([]Record, error) {
	var records []Record
	err := withAppendLock(s.appendLockPathLocked(), func() error {
		prevNextSeq := s.nextSeq
		prevActive := s.active
		prevLastID := s.lastID
		addedIDs := make([]string, 0, len(kinds))

		// 为每条记录 prepare 一条：parent 顺着批内前一条链式递增。
		records = make([]Record, 0, len(kinds))
		var buf []byte
		for i, kind := range kinds {
			parent, id, err := s.prepareAppend()
			if err != nil {
				rollbackBatch(s, prevNextSeq, prevActive, prevLastID, addedIDs)
				return err
			}
			record := Record{
				ID: id, Parent: parent, TS: nextRecordTS(), Kind: kind, Data: data[i],
			}
			line, err := json.Marshal(record)
			if err != nil {
				rollbackBatch(s, prevNextSeq, prevActive, prevLastID, addedIDs)
				return fmt.Errorf("session: encode batch record: %w", err)
			}
			buf = append(buf, line...)
			buf = append(buf, '\n')
			records = append(records, record)
			addedIDs = append(addedIDs, id)
			// 批内链式推进：下一条记录的 parent 是本条 id。失败时整体回滚到批前状态。
			s.active = id
			s.lastID = id
		}

		if appendPrepareHookForTest != nil {
			appendPrepareHookForTest()
		}
		if err := s.writeAppendBatch(records[len(records)-1].ID, buf, addedIDs); err != nil {
			rollbackBatch(s, prevNextSeq, prevActive, prevLastID, nil)
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.active = records[len(records)-1].ID
	return records, nil
}

// writeAppendBatch 复用单行写的 TOCTOU 校验与 fd 管理，把整批字节一次写出。
// addedIDs 只在全部成功后写入内存索引；失败由调用方整体回滚。
func (s *Session) writeAppendBatch(lastID string, buf []byte, addedIDs []string) error {
	if info, statErr := os.Stat(s.path()); statErr != nil || info.Size() != s.fdSize {
		s.invalidateAppendState()
		return errAppendRetry
	}
	fd := s.fd
	temporary := fd == nil
	if temporary {
		var err error
		fd, err = s.openAppendFile()
		if err != nil {
			return err
		}
	}
	n, err := fd.Write(buf)
	if temporary {
		fd.Close()
	}
	if err != nil {
		s.closeAppendFD()
		s.ids = nil
		return fmt.Errorf("session: batch write %s: %w", s.path(), err)
	}
	if n != len(buf) {
		s.closeAppendFD()
		s.ids = nil
		return fmt.Errorf("session: batch write %s: short write", s.path())
	}
	for _, id := range addedIDs {
		s.ids[id] = struct{}{}
	}
	s.lastID = lastID

	expected := s.fdSize + int64(len(buf))
	pathInfo, err := os.Stat(s.path())
	if err != nil || s.lastInfo == nil ||
		!os.SameFile(pathInfo, s.lastInfo) || pathInfo.Size() != expected {
		s.invalidateAppendState()
		return errAppendRetry
	}
	s.fdSize = expected
	s.fdMTime = pathInfo.ModTime()
	s.lastInfo = pathInfo
	return nil
}

func rollbackBatch(s *Session, nextSeq int, active, lastID string, addedIDs []string) {
	s.nextSeq = nextSeq
	s.active = active
	s.lastID = lastID
	if s.ids != nil {
		for _, id := range addedIDs {
			delete(s.ids, id)
		}
	}
}
