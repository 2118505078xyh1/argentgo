package i18n

import (
	"fmt"
	"sort"
)

// opt-in 与可靠性提示消息键。这些常量定义了 OptInMessage 可识别的完整键名
// 集合，与 catalog 的 UI key 分开维护：opt-in 消息描述可选功能的激活状态，
// 可靠性提示描述去重、替换深度等告警，由调用方按需取用，各自演进、互不干扰。
const (
	OptInEnabled          = "optin.enabled"
	OptInDisabled         = "optin.disabled"
	OptInUnknown          = "optin.unknown"
	OptInActivationFailed = "optin.activation.failed"
	OptInDegraded         = "optin.degraded"

	ReliabilityGuidanceDedup        = "reliability.guidance.dedup"
	ReliabilityGuidanceTrim         = "reliability.guidance.trim"
	ReliabilityGuidanceCheckpoint   = "reliability.guidance.checkpoint"
	ReliabilitySubstitutionDepth    = "reliability.substitution.depth"
	ReliabilitySubstitutionFallback = "reliability.substitution.fallback"
	ReliabilitySubstitutionLimit    = "reliability.substitution.limit"
)

// optInMessageEntry 是单个 opt-in 消息键在两种语言环境下的文案。
type optInMessageEntry struct {
	en string
	zh string
}

// optInMessageTable 把 opt-in 与可靠性提示消息键映射到本地化文案。
//
// 每种语言的格式动词刻意保持一致，使同一组 args 在两种语言下都能正确
// 渲染。该表独立于 catalog 与 errorMessage，不参与现有文案审计，也不
// 改变任何既有文案字节。
var optInMessageTable = map[string]optInMessageEntry{
	OptInEnabled:                    {"opt-in %s enabled", "opt-in %s 已启用"},
	OptInDisabled:                   {"opt-in %s disabled", "opt-in %s 已停用"},
	OptInUnknown:                    {"opt-in status unknown for %s", "opt-in %s 的状态未知"},
	OptInActivationFailed:           {"failed to activate opt-in %s: %s", "激活 opt-in %s 失败：%s"},
	OptInDegraded:                   {"opt-in %s running in degraded mode", "opt-in %s 处于降级模式"},
	ReliabilityGuidanceDedup:        {"consider deduplicating %d repeated tool calls", "建议对 %d 次重复的工具调用进行去重"},
	ReliabilityGuidanceTrim:         {"consider trimming %d characters of history", "建议裁剪 %d 个字符的历史记录"},
	ReliabilityGuidanceCheckpoint:   {"consider saving a checkpoint before compaction", "建议在压缩前保存检查点"},
	ReliabilitySubstitutionDepth:    {"substitution depth limited to %d", "替换深度已限制为 %d"},
	ReliabilitySubstitutionFallback: {"substituted %s with %s", "已将 %s 替换为 %s"},
	ReliabilitySubstitutionLimit:    {"substitution limit reached at %d", "替换次数已达上限 %d"},
}

// missingOptInMessage 为未知键构造稳定的占位串。占位串包含原始键，
// 使尚未翻译的消息在 UI 中可见而非渲染为空白；同一键总是得到同一串。
// 使用字节拼接而非格式化，避免对键内容做任何解释，也不会 panic。
func missingOptInMessage(key string) string {
	return "<missing opt-in message: " + key + ">"
}

// OptInMessage 返回 opt-in 消息键在给定语言环境下的本地化文案。
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
func OptInMessage(lang, key string, args ...any) string {
	entry, ok := optInMessageTable[key]
	if !ok {
		return missingOptInMessage(key)
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

// OptInMessageKeys 返回所有 opt-in 消息键，按字典序稳定排序。
//
// 每次调用都从表中重新构造并排序，返回的是独立切片：调用方修改结果不会
// 影响表或后续调用。供审计遍历全部已定义键使用。
func OptInMessageKeys() []string {
	keys := make([]string, 0, len(optInMessageTable))
	for key := range optInMessageTable {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
