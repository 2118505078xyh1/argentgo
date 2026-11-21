package session

// ActiveLeafID 返回活动叶节点的记录 id，语义与 RecordCheckpoint 设置检查点
// Parent 时完全一致：优先使用内存中的活动 id（分叉/恢复后它可能指向文件中间
// 的记录，未必是文件顺序最后一条），仅当该 id 不在文件中时回退到最后一条记录。
// 第二返回值报告是否找到（空会话或读取失败为 false）。
func (s *Session) ActiveLeafID() (string, bool) {
	records, err := s.Records()
	if err != nil || len(records) == 0 {
		return "", false
	}
	path := s.activePath(records)
	if len(path) == 0 {
		return "", false
	}
	return path[len(path)-1].ID, true
}
