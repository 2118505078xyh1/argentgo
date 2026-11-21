package i18n

import (
	"fmt"
	"sort"
)

// 可靠性消息键。这些常量定义了 ReliabilityMessage 可识别的完整键名集合，
// 与 catalog 的 UI key 分开维护：可靠性消息描述压缩保真、上下文压力、
// 审批审计与输出裁剪等原语告警，由调用方按需取用，二者各自演进、互不干扰。
const (
	ReliabilityCompactionLossy  = "reliability.compaction.lossy"
	ReliabilityCompactionImpact = "reliability.compaction.impact"
	ReliabilityFocusWandering   = "reliability.focus.wandering"
	ReliabilityFocusPressure    = "reliability.focus.pressure"
	ReliabilityFocusTrimmed     = "reliability.focus.trimmed"
	ReliabilityApprovalDenied   = "reliability.approval.denied"
	ReliabilityApprovalPending  = "reliability.approval.pending"
	ReliabilityApprovalAudit    = "reliability.approval.audit"
	ReliabilityOutputClipped    = "reliability.output.clipped"
	ReliabilityOutputTruncated  = "reliability.output.truncated"
	ReliabilityDedupeSuppressed = "reliability.dedupe.suppressed"
	ReliabilitySteeringDiverted = "reliability.steering.diverted"
)

// reliabilityMessageEntry 是单个可靠性消息键在两种语言环境下的文案。
type reliabilityMessageEntry struct {
	en string
	zh string
}

// reliabilityMessageTable 把可靠性消息键映射到本地化文案。
//
// 每种语言的格式动词刻意保持一致，使同一组 args 在两种语言下都能正确
// 渲染。该表独立于 catalog 与 errorMessage，不参与现有文案审计，也不
// 改变任何既有文案字节。
var reliabilityMessageTable = map[string]reliabilityMessageEntry{
	ReliabilityCompactionLossy:  {"compaction discarded %d messages; details may be lost", "压缩已丢弃 %d 条消息，部分细节可能丢失"},
	ReliabilityCompactionImpact: {"compaction reduced context from %d to %d tokens", "压缩将上下文从 %d 个 token 降至 %d"},
	ReliabilityFocusWandering:   {"topic drifting: %d steps since last user instruction", "主题漂移：距上次用户指令已 %d 步"},
	ReliabilityFocusPressure:    {"context pressure high: %d%% of budget used", "上下文压力偏高：已使用预算的 %d%%"},
	ReliabilityFocusTrimmed:     {"trimmed %d characters of history", "已裁剪 %d 个字符的历史记录"},
	ReliabilityApprovalDenied:   {"approval denied for %s", "操作 %s 的审批被拒绝"},
	ReliabilityApprovalPending:  {"waiting for approval: %s", "等待审批：%s"},
	ReliabilityApprovalAudit:    {"audit logged for %s", "已为 %s 记录审计"},
	ReliabilityOutputClipped:    {"output clipped to %d bytes", "输出已裁剪至 %d 字节"},
	ReliabilityOutputTruncated:  {"output truncated: %s", "输出已截断：%s"},
	ReliabilityDedupeSuppressed: {"suppressed %d duplicate tool calls", "已抑制 %d 次重复工具调用"},
	ReliabilitySteeringDiverted: {"steering diverted %d in-flight steps", "转向已改道 %d 个进行中的步骤"},
}

// missingReliabilityMessage 为未知键构造稳定的占位串。占位串包含原始键，
// 使尚未翻译的消息在 UI 中可见而非渲染为空白；同一键总是得到同一串。
// 使用字节拼接而非格式化，避免对键内容做任何解释，也不会 panic。
func missingReliabilityMessage(key string) string {
	return "<missing reliability message: " + key + ">"
}

// ReliabilityMessage 返回可靠性消息键在给定语言环境下的本地化文案。
//
// lang 经 Normalize 解析，因此 "zh"、"zh_cn"、"中文" 等别名与 "ZH-CN"
// 等大小写变体都得到中文文案；未知或空的语言环境回退到英文。提供 args
// 时用 fmt.Sprintf 格式化模板，未提供时原样返回模板。
//
// key 未命中映射表时返回一个包含该键的稳定占位串，而非 panic 或返回空串。
// 查找与返回都是整串复制，不做逐字节切分，所以含 CJK 的文案与键始终
// 保持完整的 UTF-8 字节序列。
//
// 它是纯函数：不读取配置或全局状态，不向 Missing 记录任何内容，也不修改
// 传入的 args。
func ReliabilityMessage(lang, key string, args ...any) string {
	entry, ok := reliabilityMessageTable[key]
	if !ok {
		return missingReliabilityMessage(key)
	}
	template := entry.en
	if Normalize(lang) == ZhCN {
		template = entry.zh
	}
	if len(args) > 0 {
		return fmt.Sprintf(template, args...)
	}
	return template
}

// ReliabilityMessageKeys 返回所有可靠性消息键，按字典序稳定排序。
//
// 每次调用都从表中重新构造并排序，返回的是独立切片：调用方修改结果不会
// 影响表或后续调用。供审计遍历全部已定义键使用。
func ReliabilityMessageKeys() []string {
	keys := make([]string, 0, len(reliabilityMessageTable))
	for key := range reliabilityMessageTable {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
