package session

import "fmt"

// Checkout 把会话的活动叶节点就地切换到 entryID 指向的记录（分支切换）。
// 它复用 Path 的校验：entryID 必须是该会话树中真实存在的记录 id，且从它回溯
// 到根的链上不能出现环；未知 id、空 id 与环都会被拒绝。
//
// 切换只修改内存中的活动指针，不写入任何记录，也不复制历史。因此随后的
// Append 会把新记录挂到 entryID 之下，形成一条新分支，而原有分支保持不变。
// 这在语义上等价于 Fork 到中间记录，但省去了创建新会话文件与复制记录的开销。
func (s *Session) Checkout(entryID string) error {
	path, err := s.Path(entryID)
	if err != nil {
		return err
	}
	if len(path) == 0 {
		return fmt.Errorf("session: entry %q is not on a path", entryID)
	}
	// Path 内部会经 Records -> Flush 获取 mu，因此不能在持锁状态下调用；
	// 这里只把最终的指针写入放在 mu 下，保证与并发的 Append/读取互不竞争。
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = entryID
	return nil
}
