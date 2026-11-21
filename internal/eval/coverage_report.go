package eval

// 本文件提供"场景覆盖度诊断"的原语：按调用方给出的维度标签映射，统计
// 每个失效模式维度被多少场景覆盖，并把零覆盖维度显式列为 gaps（监控盲区）。
// 这是纯函数：不修改入参、不读时钟/网络/全局状态，也不接线到 CLI。
// 与 quality_report.go / compare_report.go 一脉相承——可测性做进结构里。

import "sort"

// CoverageReport 是一轮场景集的覆盖度汇总。字段都是纯数据，
// 测试可直接断言，无需解析渲染后的文本（可测性做进结构里）。
type CoverageReport struct {
	TotalScenarios int            // 参与统计的场景总数
	ByDimension    map[string]int // 维度 → 覆盖该维度的场景数（含零覆盖维度）
	Gaps           []string       // 零覆盖的维度名，按 sort.Strings 升序，保证确定性
}

// AnalyzeCoverage 统计各失效模式维度的场景覆盖数并识别零覆盖维度。
//
// dimensionTags 是"维度 → 场景名标签列表"的映射：当 scenarios 中某场景的
// Name 出现在某维度的标签列表中，该场景即计入该维度的覆盖数。标签列表内的
// 重复项先去重，因此同一标签不会把单个场景重复计数；scenarios 中的多个
// 同名场景仍按各自独立计数（它们本就是不同场景）。
//
// 该函数是纯函数：不修改 scenarios 与 dimensionTags，返回的 ByDimension
// 与 Gaps 都是新分配的数据结构，不与入参或任何全局状态共享。ByDimension
// 包含 dimensionTags 中的所有维度（覆盖数为 0 的也保留），Gaps 收录其中
// 零覆盖的维度名并经排序，故相同入参的多次调用结果逐字节一致。
func AnalyzeCoverage(scenarios []Scenario, dimensionTags map[string][]string) CoverageReport {
	report := CoverageReport{
		TotalScenarios: len(scenarios),
		ByDimension:    make(map[string]int, len(dimensionTags)),
		Gaps:           make([]string, 0),
	}
	for dimension, tags := range dimensionTags {
		// 标签去重：同一标签只代表一个命名场景，避免列表内重复夸大覆盖数。
		tagSet := make(map[string]struct{}, len(tags))
		for _, tag := range tags {
			tagSet[tag] = struct{}{}
		}
		covered := 0
		for i := range scenarios {
			if _, ok := tagSet[scenarios[i].Name]; ok {
				covered++
			}
		}
		report.ByDimension[dimension] = covered
		if covered == 0 {
			report.Gaps = append(report.Gaps, dimension)
		}
	}
	// map 遍历顺序随机，显式排序让 Gaps 跨调用稳定（CJK 名按 UTF-8 字节序）。
	sort.Strings(report.Gaps)
	return report
}
