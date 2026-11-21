package i18n

import (
	"fmt"
	"sort"
)

// auditDiffSummaryNoChange 是无差异时的固定中文摘要，避免经由格式化产生
// 细微差异，与 AuditReport.Summary 的 OK 字面量口径一致。
const auditDiffSummaryNoChange = "i18n 文案审计差异：无变化"

// sortAuditDiffDetails 按 (Table, Key, Issue) 字典序稳定排序一份差异明细。
// 排序口径与 newAuditDetails 一致，便于 doctor 输出与断言逐字比较；字符串
// 用 < 比较即按 UTF-8 字节序，CJK 键同样稳定。
func sortAuditDiffDetails(details []AuditDetail) {
	sort.SliceStable(details, func(i, j int) bool {
		a, b := details[i], details[j]
		if a.Table != b.Table {
			return a.Table < b.Table
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return a.Issue < b.Issue
	})
}

// AuditDiff 比较前后两批审计明细，返回各自的差异。
//
// 比较以 (Table, Key, Issue) 三元组为集合元素：同一个三元组在单侧重复出现
// 只视为一个成员。added 是 after 有而 before 没有的三元组，removed 是
// before 有而 after 没有的三元组；三元组包含 Issue，故 Table/Key 相同但
// Issue 不同的两条明细会被视为一增一删。
//
// 两批结果都按 (Table, Key, Issue) 稳定排序，且是全新的切片与明细副本，
// 修改结果不会改动入参。无差异的一侧返回 nil（与 newAuditDetails / FullAudit
// 的「干净即 nil」口径一致）。重复调用返回完全相同的结果。
func AuditDiff(before, after []AuditDetail) (added, removed []AuditDetail) {
	beforeSet := make(map[AuditDetail]struct{}, len(before))
	for _, d := range before {
		beforeSet[d] = struct{}{}
	}
	afterSet := make(map[AuditDetail]struct{}, len(after))
	for _, d := range after {
		afterSet[d] = struct{}{}
	}

	added = make([]AuditDetail, 0, len(afterSet))
	for d := range afterSet {
		if _, ok := beforeSet[d]; !ok {
			added = append(added, d)
		}
	}
	removed = make([]AuditDetail, 0, len(beforeSet))
	for d := range beforeSet {
		if _, ok := afterSet[d]; !ok {
			removed = append(removed, d)
		}
	}

	sortAuditDiffDetails(added)
	sortAuditDiffDetails(removed)

	if len(added) == 0 {
		added = nil
	}
	if len(removed) == 0 {
		removed = nil
	}
	return added, removed
}

// AuditDiffSummary 返回差异的稳定中文摘要。两侧都为空时返回固定字面量
// auditDiffSummaryNoChange；否则按固定顺序报告新增与移除计数，不依赖遍历
// 顺序，可安全用于 doctor 输出与断言。入参通常直接来自 AuditDiff；本函数
// 只读其长度，不排序、去重或修改。
func AuditDiffSummary(added, removed []AuditDetail) string {
	if len(added) == 0 && len(removed) == 0 {
		return auditDiffSummaryNoChange
	}
	return fmt.Sprintf("i18n 文案审计差异：新增 %d 项，移除 %d 项",
		len(added), len(removed))
}
