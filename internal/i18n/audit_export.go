package i18n

import "sort"

// AuditDetail 是一条结构化的审计明细。Table 标识来源表（catalog /
// error messages），Key 是文案条目键，Issue 是可读的问题描述。
//
// 把不同来源的问题统一成三元组，便于上层按表/键过滤与去重，而不用解析
// 各自的文字输出。
type AuditDetail struct {
	Table string
	Key   string
	Issue string
}

// keyAlignmentDetails 把一份键对齐结果按缺失方向展开为明细。table 是该
// 结果来源表的稳定标识；Issue 沿用 KeyAlignment.Problems 的文字口径，
// 使导出的明细与既有 doctor 输出逐字一致。
func keyAlignmentDetails(a KeyAlignment, table string) []AuditDetail {
	out := make([]AuditDetail, 0, len(a.MissingInEn)+len(a.MissingInZh))
	for _, key := range a.MissingInEn {
		out = append(out, AuditDetail{
			Table: table,
			Key:   key,
			Issue: "en-US missing key (extra in zh-CN): " + key,
		})
	}
	for _, key := range a.MissingInZh {
		out = append(out, AuditDetail{
			Table: table,
			Key:   key,
			Issue: "zh-CN missing key (extra in en-US): " + key,
		})
	}
	return out
}

// placeholderDetails 把一条占位符问题按 Problems 的每一行展开为明细。
// Table / Key 取自问题本身，Issue 保留 Problems 的完整描述（含表/键前缀），
// 这样每条明细都自带可读上下文。
func placeholderDetails(issue PlaceholderIssue) []AuditDetail {
	problems := issue.Problems()
	out := make([]AuditDetail, 0, len(problems))
	for _, problem := range problems {
		out = append(out, AuditDetail{
			Table: issue.Table,
			Key:   issue.Key,
			Issue: problem,
		})
	}
	return out
}

// newAuditDetails 由各部分审计结果构造统一的明细列表。它是纯函数，也是
// AuditDetails 与测试共用的核心：测试借此用模拟结果验证展开完整性与排序，
// 无需改动真实文案表。保留原签名使既有测试调用不变。
func newAuditDetails(catalog, errorMessages KeyAlignment, placeholders []PlaceholderIssue) []AuditDetail {
	return newAuditDetailsWithTables(catalog, errorMessages, nil, placeholders)
}

// newAuditDetailsWithTables 在原有两张表之外再展开一批带表名的键对齐结果。
// extra 为 nil 时与 newAuditDetails 完全等价。结果按 (Table, Key, Issue) 稳定
// 排序；无问题时返回 nil，与 FullAudit 的 OK 口径一致。
func newAuditDetailsWithTables(catalog, errorMessages KeyAlignment, extra []namedKeyAlignment, placeholders []PlaceholderIssue) []AuditDetail {
	out := make([]AuditDetail, 0)
	out = append(out, keyAlignmentDetails(catalog, placeholderTableCatalog)...)
	out = append(out, keyAlignmentDetails(errorMessages, placeholderTableErrorMessages)...)
	for _, alignment := range extra {
		out = append(out, keyAlignmentDetails(alignment.align, alignment.name)...)
	}
	for _, issue := range placeholders {
		out = append(out, placeholderDetails(issue)...)
	}
	if len(out) == 0 {
		return nil
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Table != b.Table {
			return a.Table < b.Table
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return a.Issue < b.Issue
	})
	return out
}

// AuditDetails 把键对齐审计与占位符审计展开为按 (Table, Key, Issue) 稳定
// 排序的结构化明细，覆盖 catalog、错误码表以及四张独立消息表。它是纯函数：
// 只读嵌入的文案表，不改变任何查找行为，不改任何文案字节，也不向 Missing
// 记录任何内容。无问题时返回 nil。
func AuditDetails() []AuditDetail {
	return newAuditDetailsWithTables(
		AuditCatalogKeyAlignment(),
		AuditErrorMessagesKeyAlignment(),
		AuditMessageTablesKeyAlignment(),
		AuditPlaceholders(),
	)
}

// GroupByTable 按 Table 分组明细。每个分组内保持输入的相对顺序（稳定），
// 返回全新的 map 与切片：修改结果不会影响入参，也不会在分组之间共享底层
// 数组。空输入返回空的非 nil map。
func GroupByTable(details []AuditDetail) map[string][]AuditDetail {
	out := make(map[string][]AuditDetail)
	for _, d := range details {
		out[d.Table] = append(out[d.Table], d)
	}
	return out
}
