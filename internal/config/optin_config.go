package config

import (
	"fmt"
	"sort"
	"strings"
)

// 本文件提供 R14 交互优化所需的 opt-in 配置读取与展示原语。它刻意保持
// stdlib-only、零调用方：只读取调用方传入的 map，不触碰磁盘、环境或任何
// 全局状态，接线由 internal/app 等上层按需完成。默认全部关闭（fail-closed）。

// optInBlockKey 是配置中承载 opt-in 开关的子对象名。
const optInBlockKey = "optins"

// OptInDisplay 是一个 opt-in 开关的展示投影。
//
// Key 是稳定的配置词汇表键名；Label 是可直接展示的中文标签；Enabled 表示
// 配置是否显式开启该开关（缺省或非布尔值一律为 false）；Category 是中文
// 归类，便于界面分组展示。所有字段都不带 omitempty 语义的省略约定，调用方
// 可安全持有副本。
type OptInDisplay struct {
	Key      string
	Label    string
	Enabled  bool
	Category string
}

// optInDefinition 描述一个受支持 opt-in 键的静态元数据。
type optInDefinition struct {
	key      string
	label    string
	category string
}

// optInDefinitions 是键白名单的唯一事实来源：只有这里列出的键才会出现在
// 展示列表中，配置里出现的任何其他键一律忽略（fail-closed）。标签与归类
// 均为完整 UTF-8 的中文文本，不会被按字节截断。
var optInDefinitions = []optInDefinition{
	{key: "cli_hints", label: "CLI 提示", category: "交互"},
	{key: "apply_patch", label: "补丁应用", category: "交互"},
	{key: "grep_ex", label: "增强搜索", category: "交互"},
	{key: "context_pressure", label: "上下文压力", category: "上下文"},
	{key: "attempt_isolation", label: "失败尝试隔离", category: "上下文"},
	{key: "keep_implicit_context", label: "保留隐式上下文", category: "上下文"},
	{key: "preserve_summary_sections", label: "保留摘要章节", category: "上下文"},
	{key: "refusal_reason", label: "拒绝理由", category: "交互"},
	{key: "untrusted_result", label: "不可信结果", category: "上下文"},
	{key: "untrusted_boundary", label: "不可信边界", category: "上下文"},
	{key: "compaction_fidelity", label: "压缩保真", category: "上下文"},
	{key: "tool_error_reflection", label: "工具错误反射", category: "交互"},
}

// optInBool 把任意配置值收敛为布尔：只有类型为 bool 且值为 true 才视为开启，
// 其余（字符串 "true"、数字、nil、映射等）一律为 false，实现 fail-closed。
func optInBool(value any) bool {
	enabled, ok := value.(bool)
	return ok && enabled
}

// optInRawBlock 从配置 map 中定位 opt-in 子块。
//
// 规则：
//   - raw 为 nil 时返回 nil；
//   - 若存在 "optins" 键且其值为 map[string]any，则返回该子块；
//   - 若 "optins" 键存在但类型不符，返回 nil（fail-closed，不猜测）；
//   - 若没有 "optins" 键，则把 raw 本身当作 opt-in 映射（直接形式），
//     白名单之外的值会在 ParseOptInDisplay 中被忽略。
//
// 该函数只读入参，不修改任何嵌套结构。
func optInRawBlock(raw map[string]any) map[string]any {
	if raw == nil {
		return nil
	}
	if block, ok := raw[optInBlockKey].(map[string]any); ok {
		return block
	}
	if _, exists := raw[optInBlockKey]; exists {
		return nil
	}
	return raw
}

// ParseOptInDisplay 从配置 map 读取 opt-in 块，产出按 Key 升序稳定排序的
// 展示列表。每个白名单键都恰好出现一次：配置显式设为 true 时 Enabled 为
// true，缺键、非布尔值或整个子块缺失时均为默认关闭。
//
// 它是纯函数：不修改 raw 及其嵌套值，不读取全局状态，结果只由入参决定。
// 未知键被忽略；标签为完整 CJK 文本，全程按字符串处理，不会切坏 UTF-8。
// 返回值是新建切片，调用方可自由修改而不影响后续调用。
func ParseOptInDisplay(raw map[string]any) []OptInDisplay {
	block := optInRawBlock(raw)
	items := make([]OptInDisplay, 0, len(optInDefinitions))
	for _, def := range optInDefinitions {
		items = append(items, OptInDisplay{
			Key:      def.key,
			Label:    def.label,
			Category: def.category,
			Enabled:  optInBool(block[def.key]),
		})
	}
	// 即便白名单表被重排，也按 Key 升序稳定排序，保证输出逐字节一致。
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Key < items[j].Key
	})
	return items
}

// OptInDisplaySummary 把展示列表汇总为稳定的一行中文摘要，列出所有已开启
// 的开关。
//
// 稳定性体现在：先按 Key 升序排序并去重，因此无论传入顺序如何，同一组条目
// 都得到逐字节一致的文本；未启用任何开关时返回固定短句。它不修改 items，
// 也不读取全局状态；标签与全角括号均为完整 UTF-8，不会被按字节截断。
func OptInDisplaySummary(items []OptInDisplay) string {
	enabled := make([]OptInDisplay, 0, len(items))
	for _, item := range items {
		if item.Enabled {
			enabled = append(enabled, item)
		}
	}
	if len(enabled) == 0 {
		return "未启用任何 opt-in。"
	}

	sort.SliceStable(enabled, func(i, j int) bool {
		return enabled[i].Key < enabled[j].Key
	})
	// 相同 Key 的条目排序后相邻，去重保留首个，避免摘要重复。
	unique := enabled[:0]
	for _, item := range enabled {
		if len(unique) > 0 && unique[len(unique)-1].Key == item.Key {
			continue
		}
		unique = append(unique, item)
	}

	parts := make([]string, 0, len(unique))
	for _, item := range unique {
		parts = append(parts, fmt.Sprintf("%s（%s）", item.Label, item.Key))
	}
	return "已启用的 opt-in：" + strings.Join(parts, "、") + "。"
}
