package session

// 本文件提供一组只读的薄导出封装，把包内已有的压缩保留/投影能力暴露给同仓库的
// 调用方（R4 压缩保真学习报告步骤 A2）。它们只做委托与投影，不改变任何既有
// 函数的行为，也不产生任何写入：EntryFromRecord 直接复用 messageEntry，
// RetainedForCompaction 直接复用 retainedForCompaction，
// FlaggedToolResults 复用 GetRetention 与 messageEntry 来筛选活动分支上的工具结果。

// RetainedForCompaction 返回 entries 中必须在压缩后逐字保留的记录。
// 它是 retainedForCompaction(entries, len(entries)) 的薄封装：把整个条目切片
// 都当作“被丢弃前缀”，因此带保留标记的记录及其完整工具对都会被返回。
// 返回的记录只携带 Kind 与 Data（与 retainedForCompaction 一致）。
func RetainedForCompaction(entries []Entry) []Record {
	return retainedForCompaction(entries, len(entries))
}

// FlaggedToolResults 收集活动分支上带有压缩保留标记的工具结果，按 ToolCallID 索引。
// 只保留同时满足以下条件的记录：能投影为 tool 角色、带非空 ToolCallID、且
// Data 中的保留提示能在压缩后存活（GetRetention(...).SurvivesCompaction()）。
//
// 同一 ToolCallID 的重试按活动分支顺序取最后一条（较新的结果覆盖较旧的），
// 属于“过包含安全”：宁可保留较新的一条，也不丢失需要的上下文。
// 非活动分支上的记录不会被读取，因此该方法不会把旁支结果混入索引。
// 该方法全程只读，既不修改会话文件，也不修改活动叶节点。
func (s *Session) FlaggedToolResults() map[string]Record {
	records, err := s.Records()
	if err != nil {
		return nil
	}
	path := s.activePath(records)
	out := make(map[string]Record)
	for _, record := range path {
		entry, ok := messageEntry(record)
		if !ok {
			continue
		}
		if entry.Role != "tool" || entry.ToolCallID == "" {
			continue
		}
		if !GetRetention(entry.Data).SurvivesCompaction() {
			continue
		}
		out[entry.ToolCallID] = record
	}
	return out
}

// EntryFromRecord 将一条记录投影为对话条目；当记录是元数据、事件或压缩标记时
// 返回 false。它是 messageEntry 的薄封装，便于调用方在不访问包内私有函数的前提下
// 复用完全相同的投影规则（包括 _retention 提示随 Data 逐字保留的行为）。
func EntryFromRecord(record Record) (Entry, bool) {
	return messageEntry(record)
}
