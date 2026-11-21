package session

// RebuildInput 汇总压缩摘要重建所需的输入上下文。
//
// 三个字段各自对应一条 wire 的数据来源：
//   - OriginalSummary 是重建前的原始摘要，由 R14 的 SnapshotBeforeRebuild 捕获后传入，
//     作为 preserve wire 的 orig 参数；
//   - Dropped 是本次压缩被丢弃的条目，作为 enrich wire 的输入；
//   - Compaction 是重建后的压缩记录（其 Summary 为 buildCompactionSummary 的新结果），
//     作为上下文载荷随输入一起携带，便于接线方从同一处取用重建后的计数、改动文件等
//     信息来自行构造闭包。本编排函数自身不读取它。
//
// 结构体按值持有；Dropped 切片仅被透传给回调，编排函数不会改动其中任何条目。
type RebuildInput struct {
	Compaction      Compaction
	OriginalSummary string
	Dropped         []Entry
}

// RebuildSummaryWithWires 按固定顺序把三个 wire 闭包应用到重建摘要上，是 R15 压缩 wire
// threading 在 session 侧的纯编排原语。
//
// 应用顺序固定为：
//  1. preserve(rebuilt, in.OriginalSummary) —— 恢复原始章节（R14/R13 的 Restore /
//     PreserveSections 等）；
//  2. enrich(上一步结果, in.Dropped) —— 从丢弃条目注入隐式上下文等；
//  3. fidelity(上一步结果) 返回非空告警时，追加 "\n" + warning。
//
// 任一回调为 nil 时跳过对应步骤，因此三者全为 nil 时逐字节返回 rebuilt，默认行为完全不变。
// 本函数是纯编排：不修改 in（包括 Dropped 切片）与 rebuilt，只做透传与字符串拼接；全程
// 不涉及按字节切片，故对 CJK / emoji 安全。
//
// 幂等性：本函数本身是确定性纯函数；当注入的 wire 幂等（R13/R14 的 wire 均靠章节标记或
// 哨兵串去重）时，整体结果幂等。fidelity wire 也应据此在告警已存在时返回空串，以保证
// 重复调用不会重复追加告警。
//
// 选择保留 fidelity 而非更精简的两回调版本，是因为测试要求覆盖"告警追加"这一环节，
// 且 R15 需要在 session 侧一处定序"恢复 → 增强 → 保真告警"并与压缩重建实现解耦。
func RebuildSummaryWithWires(
	rebuilt string,
	in RebuildInput,
	preserve func(string, string) string,
	enrich func(string, []Entry) string,
	fidelity func(string) string,
) string {
	result := rebuilt
	if preserve != nil {
		result = preserve(result, in.OriginalSummary)
	}
	if enrich != nil {
		result = enrich(result, in.Dropped)
	}
	if fidelity != nil {
		if warning := fidelity(result); warning != "" {
			result += "\n" + warning
		}
	}
	return result
}
