package i18n

import "fmt"

// auditSummaryOK 是 OK 报告的固定中文摘要，避免经由格式化产生细微差异。
const auditSummaryOK = "i18n 审计通过：键与占位符均一致"

// AuditReport 聚合 i18n 的一致性审计结果。
//
// KeyIssues 统计两份文案表的键对齐问题行数（en-US / zh-CN 两个缺失方向
// 各记一行）；PlaceholderIssues 统计占位符审计的 Problems 行数。OK 仅在
// 两类问题数都为零时为真。
type AuditReport struct {
	KeyIssues         int
	PlaceholderIssues int
	OK                bool
}

// countKeyAlignmentIssues 统计一份键对齐结果中的问题行数，口径与
// KeyAlignment.Problems 一致：每个缺失方向记一行。
func countKeyAlignmentIssues(a KeyAlignment) int {
	return len(a.MissingInEn) + len(a.MissingInZh)
}

// countPlaceholderIssues 统计占位符审计的 Problems 行数。同一条目若在两个
// 方向都不一致，会分别计数，与 PlaceholderIssue.Problems 的口径一致。
func countPlaceholderIssues(issues []PlaceholderIssue) int {
	total := 0
	for _, issue := range issues {
		total += len(issue.Problems())
	}
	return total
}

// newAuditReport 由各部分审计结果构造聚合报告。它是纯函数，也是 FullAudit
// 与测试共用的核心：测试借此用模拟结果验证统计口径与 OK 判定，无需改动
// 真实文案表。
func newAuditReport(catalog, errorMessages KeyAlignment, placeholders []PlaceholderIssue) AuditReport {
	keyIssues := countKeyAlignmentIssues(catalog) + countKeyAlignmentIssues(errorMessages)
	placeholderIssues := countPlaceholderIssues(placeholders)
	return AuditReport{
		KeyIssues:         keyIssues,
		PlaceholderIssues: placeholderIssues,
		OK:                keyIssues == 0 && placeholderIssues == 0,
	}
}

// FullAudit 聚合键对齐审计与占位符审计的完整结果。
//
// 它是纯函数：只读嵌入的文案表，不改变 T / errorMessage 的查找行为，不改
// 任何文案字节，也不向 Missing 记录任何内容。各部分审计本身稳定有序，故
// 同一份文案表上的重复调用返回完全相同的报告。
func FullAudit() AuditReport {
	return newAuditReport(
		AuditCatalogKeyAlignment(),
		AuditErrorMessagesKeyAlignment(),
		AuditPlaceholders(),
	)
}

// Summary 返回稳定的中文摘要。OK 时返回固定字面量；否则按固定顺序报告
// 两类问题数，不依赖遍历顺序，可安全用于 doctor 输出与断言。
func (r AuditReport) Summary() string {
	if r.OK {
		return auditSummaryOK
	}
	return fmt.Sprintf("i18n 审计未通过：键问题 %d 项，占位符问题 %d 项",
		r.KeyIssues, r.PlaceholderIssues)
}
