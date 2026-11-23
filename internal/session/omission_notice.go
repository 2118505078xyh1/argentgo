package session

import (
	"encoding/json"
	"strings"
)

// OmissionNoticeID 是下一轮告示条目的稳定 id。投影按 id 去重，保证同一视图里
// 告示恰好出现一次；它不写入磁盘，因此旧转录与干净会话逐字节不变。
const OmissionNoticeID = "omission-authority-notice"

// OmissionNoticeText 是固定、有界、带标记的告示。它只陈述"缺失不构成授权"，
// 不含任何被省略的原文、路径或审批细节，因此不会把裁掉的策略内容重新带入上下文。
const OmissionNoticeText = "<omission_authority_notice>" +
	"注意：此前上下文裁剪、压缩或省略可能移除了部分历史；被省略的内容可能包含限制或审批规则。" +
	"缺失的证据不构成授权，也不覆盖仍然生效的审批与策略；执行前请依据当前仍可见的规则判断。" +
	"</omission_authority_notice>"

// omissionInfo 汇总本轮确实发生的省略类型。任何一项为真都足以注入一次告示。
type omissionInfo struct {
	pruned    bool
	compacted bool
	hidden    bool
	projected bool
}

func (i omissionInfo) any() bool {
	return i.pruned || i.compacted || i.hidden || i.projected
}

// projectionOmission 从活动分支记录与投影结果判断本轮是否真的丢了消息。
// 它只看稳定的结构化信号（裁剪标记、压缩保留数、隐藏编辑、投影长度），
// 不读取被省略的原文；干净路径返回零值，withOmissionNotice 因此逐字节不变。
func projectionOmission(records, path []Record, entries []Entry) omissionInfo {
	var info omissionInfo
	if toolPruneEnabled() {
		for _, entry := range entries {
			if strings.Contains(entry.Content, pruneMarkerToken) {
				info.pruned = true
				break
			}
		}
	}
	latest := -1
	for i := range path {
		if path[i].Kind == KindCompaction {
			latest = i
		}
	}
	if latest >= 0 {
		var compaction Compaction
		if err := json.Unmarshal(path[latest].Data, &compaction); err == nil &&
			compaction.Keep >= 0 && compaction.Keep < countMessageRecords(path[:latest]) {
			info.compacted = true
		}
	}
	for _, record := range path {
		if record.Kind != KindContextEdit {
			continue
		}
		var edit ContextEdit
		if json.Unmarshal(record.Data, &edit) == nil && edit.Text == "" {
			info.hidden = true
		}
	}
	// 预算投影把整条消息移出视图：仅在无压缩（否则摘要本身就会减少条目数）
	// 且投影后条目数确实少于原始消息记录数时判定为省略。
	if budgetProjectionEnabled() && latest < 0 && len(entries) < countMessageRecords(path) {
		info.projected = true
	}
	return info
}

// countMessageRecords 统计会投影为消息的原始记录数（元数据与事件不计）。
// 它只用于"条目数是否减少"的判断，不做任何内容读取。
func countMessageRecords(records []Record) int {
	n := 0
	for _, record := range records {
		switch record.Kind {
		case KindUser, KindAssistant, KindTool, KindMessage, KindSummary,
			"prompt", "model", "reply", "tool_result", "tool-result", "toolresult":
			n++
		}
	}
	return n
}

// withOmissionNotice 在有省略时把告示作为最后一条用户消息注入投影视图。
// 已存在同 id 的告示时原样返回（幂等、恰好一次）；无省略时返回同一个切片。
func withOmissionNotice(entries []Entry, info omissionInfo) []Entry {
	if !info.any() {
		return entries
	}
	for _, entry := range entries {
		if entry.ID == OmissionNoticeID {
			return entries
		}
	}
	out := make([]Entry, 0, len(entries)+1)
	out = append(out, entries...)
	out = append(out, Entry{
		ID:      OmissionNoticeID,
		Role:    "user",
		Content: OmissionNoticeText,
	})
	return out
}
