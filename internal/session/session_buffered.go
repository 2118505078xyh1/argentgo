package session

import (
	"fmt"
	"os"
)

// sessionSync 是对 (*Session).Sync 的引用。Close 通过它调用持久性屏障；生产环境
// 始终指向真实 Sync，测试可在同包内临时替换以统计调用次数。放在本文件是为了
// 不改动 session 的其它文件；读取该变量是并发安全的，测试替换时无需并行。
var sessionSync = (*Session).Sync

// SetBufferedWrite 开启或关闭批量（缓冲）写模式，默认关闭。
// 开启后 AppendRecord 不再逐行写盘，而是把记录暂存在内存中，直到调用
// Flush（或 Close）才把暂存行一次性写入文件；这能把大量小记录的多次 IO
// 合并为一次写入。关闭时如有暂存数据会先 Flush，保证切换前的数据不丢失。
// JSONL 的一行一条、id/parent 相连语义在两种模式下完全一致。
func (s *Session) SetBufferedWrite(enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bufferedWrite == enabled {
		return nil
	}
	if !enabled {
		if err := s.flushLocked(); err != nil {
			return err
		}
	}
	s.bufferedWrite = enabled
	return nil
}

// BufferedWrite 报告当前是否处于批量写模式。
func (s *Session) BufferedWrite() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bufferedWrite
}

// PendingBytes 返回尚未落盘的暂存字节数，供诊断与测试观测缓冲状态。
func (s *Session) PendingBytes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}

// Flush 把暂存行一次性写入磁盘。非批量模式或没有暂存数据时是空操作，
// 因此默认路径不会因调用它而产生任何额外 IO。
func (s *Session) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushLocked()
}

// Close 刷新暂存数据、把转录同步到稳定存储，然后释放追加 fd。持有 fd 的平台
// 借此彻底交还文件句柄；之后如需继续追加，fd 会按需重新打开。
//
// 在关闭前调用一次 Sync，确保正常退出不会丢掉最后一次答复后、下一个请求前
// 尚未越过持久性屏障的 assistant/turn。Sync 在新打开的句柄上工作且不触碰追加
// 状态，因此可以在持有 mu 时安全调用。
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.flushLocked(); err != nil {
		return err
	}
	// 即使 Sync 失败也要释放 fd，避免泄漏句柄；但把同步错误交给调用方。
	syncErr := sessionSync(s)
	s.closeAppendFD()
	return syncErr
}

// flushLocked 在持有 mu 的前提下把暂存行一次写入并刷新追加状态索引。
// 调用方必须持有 s.mu。
func (s *Session) flushLocked() error {
	if len(s.pending) == 0 {
		return nil
	}
	buf := s.pending
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
		// 写入失败时保留暂存数据与内存索引，便于调用方重试 Flush。
		return fmt.Errorf("session: flush %s: %w", s.path(), err)
	}
	if n != len(buf) {
		return fmt.Errorf("session: flush %s: short write", s.path())
	}
	// 写入成功：清空暂存区，并复用 writeAppend 的同步检查，确认写入落在
	// 当前路径所指向的文件上，再推进大小/修改时间快照。
	s.pending = s.pending[:0]
	expected := s.fdSize + int64(len(buf))
	pathInfo, statErr := os.Stat(s.path())
	if statErr != nil || s.lastInfo == nil ||
		!os.SameFile(pathInfo, s.lastInfo) || pathInfo.Size() != expected {
		// 写入期间文件在背后被替换/改动：丢弃缓存，让下一次追加重建状态。
		s.invalidateAppendState()
		return nil
	}
	s.fdSize = expected
	s.fdMTime = pathInfo.ModTime()
	s.lastInfo = pathInfo
	return nil
}
