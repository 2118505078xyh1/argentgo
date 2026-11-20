package session

import (
	"fmt"
	"os"
)

// Sync 把会话文件的缓冲写入刷新到稳定存储。它可以在新打开的句柄上工作，
// 不触碰追加状态，也不截断文件，因此调用方可以在昂贵请求之前把它当作
// "持久性屏障"使用（见 agent 包中的请求前钩子）。
//
// 文件首先以只读方式打开，因为这样可移植且避免获取写锁。如果平台拒绝
// 在只读句柄上执行 Sync（尤其是 Windows，其 FlushFileBuffers 可能需要
// 写权限），则会用 O_RDWR 重新打开句柄（这绝不会截断文件）并再次尝试 Sync。
// 最后一次尝试的任何失败都会被包装后返回。
func (s *Session) Sync() error {
	path := s.path()
	fd, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("session: sync open %s: %w", path, err)
	}
	if err := fd.Sync(); err == nil {
		_ = fd.Close()
		return nil
	}
	// 并非每个平台都接受只读的 Sync；用 O_RDWR 重试。
	if err := fd.Close(); err != nil {
		return fmt.Errorf("session: sync close %s: %w", path, err)
	}
	rw, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("session: sync reopen %s: %w", path, err)
	}
	defer rw.Close()
	if err := rw.Sync(); err != nil {
		return fmt.Errorf("session: sync %s: %w", path, err)
	}
	return nil
}

// SyncProgress 追加一个进度检查点，然后把转录刷新到稳定存储，
// 使持久性屏障直接与检查点绑定。当不提供 Progress 时，它对已经用
// AppendProgress 追加的进度充当普通的持久性屏障（等价于 Sync）；
// 这使无参数的调用形式仍然有用且安全。
//
// Sync 在新打开的句柄上工作，不触碰追加状态，也不截断文件，
// 因此可以在追加之后从任何 goroutine 调用。追加失败会在任何 Sync
// 尝试之前返回。
func (s *Session) SyncProgress(progress ...Progress) error {
	if len(progress) > 0 {
		if err := s.AppendProgress(progress[0]); err != nil {
			return err
		}
	}
	return s.Sync()
}
