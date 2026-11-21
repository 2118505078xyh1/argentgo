package session

// CompactionSnapshot 是 buildCompactionSummary 重建前的压缩摘要快照。
//
// 重建（buildCompactionSummary 按结构化字段重写摘要）会把 Goal 硬编码为 (none)，
// 且完全丢弃 Summarize 写入的 Tool activity 段。快照在重建前捕获原始摘要文本以及
// 这两段是否存在的标记，供重建后调用 Restore 原样恢复。
//
// OriginalSummary 是 Summarize 的原始摘要（按值持有，Go 字符串不可变）。
// DroppedCount / KeptCount 分别记录被丢弃与保留的消息数，便于接线方核对重建结果。
// HasGoal / HasActivity 来自 R13 的 ParseSummarySections，标记原始摘要中是否出现
// 对应章节（空章节也算存在）。
type CompactionSnapshot struct {
	OriginalSummary string
	DroppedCount    int
	KeptCount       int
	HasGoal         bool
	HasActivity     bool
}

// SnapshotBeforeRebuild 在重建前捕获压缩快照。
//
// 它只读取 compaction 的 Summary / Dropped / Keep，不修改入参（包括其中的 Files /
// Failures / NextSteps 切片与 Archive 指针）。章节标记通过 ParseSummarySections 获得，
// 该函数按行切分，因此 CJK 内容不会被按字节截断。纯函数，相同入参总得到相同结果。
func SnapshotBeforeRebuild(compaction Compaction) CompactionSnapshot {
	sections := ParseSummarySections(compaction.Summary)
	return CompactionSnapshot{
		OriginalSummary: compaction.Summary,
		DroppedCount:    compaction.Dropped,
		KeptCount:       compaction.Keep,
		HasGoal:         sections.HasGoal,
		HasActivity:     sections.HasActivity,
	}
}

// Restore 把快照中的原始 Goal / Tool activity 段恢复回 rebuilt。
//
// 直接委托给 R13 的 PreserveSections：已存在同名章节则整段替换，缺失则追加到末尾，
// 原始摘要不含这两段时原样返回 rebuilt。因此本方法天然具备以下性质：
//   - 幂等：对结果再次调用得到相同字符串（PreserveSections 已保证）；
//   - 零值安全：CompactionSnapshot{} 的原始摘要为空，Restore 返回 rebuilt 原样；
//   - 纯函数：值接收者，不修改 s 与 rebuilt，CJK 文本按行处理不受影响。
func (s CompactionSnapshot) Restore(rebuilt string) string {
	return PreserveSections(rebuilt, s.OriginalSummary)
}
