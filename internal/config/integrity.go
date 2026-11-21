package config

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// integrityOK 是完整性审计无缺失时返回的固定摘要字面量。
const integrityOK = "integrity: ok"

// integrityNewlineReplacer 把 Reason 中内嵌的换行压成单个空格，
// 保证"一项一行"的契约。它按完整 UTF-8 字节序列匹配换行符，
// 不会切坏文本里的 CJK 字符。
var integrityNewlineReplacer = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ")

// MissingEntry 是配置完整性审计发现的一个缺失项。
//
// Entry 是类似 "$.model" 或 "$.providers.openai.apiKey" 的条目名，
// 便于调用方定位与去重；Reason 用一句话解释为什么它是缺失的；
// Fatal 表示该项是否会阻断启动（调用方据此决定是否 fail closed）。
type MissingEntry struct {
	Entry  string `json:"entry"`
	Reason string `json:"reason"`
	Fatal  bool   `json:"fatal"`
}

// CheckIntegrity 对已解析的配置做一次完整性审计，一次返回所有缺失项的
// 聚合清单，而不是在第一个问题处停止。它检查三类条目：
//
//   - 必需字段：model、baseURL、protocol；
//   - 工作区路径：cwd、storageDir；
//   - provider 凭证：能否解析出提供方，以及该方是否配置了凭据。
//
// 它是一个纯函数：不读写磁盘、不修改接收者、不访问全局状态；env 由
// 调用方注入，因此相同输入总是产生相同输出（与 Load 的环境注入方式
// 保持一致，便于测试隔离）。它有意不检查路径是否真实存在——那需要 I/O，
// 交给使用方在启动时处理。
//
// 该函数默认不会被 Load/Merge/Validate 等任何调用点触发，仅作为显式
// 的启动前审计或 doctor 类工具按需使用，对现有加载行为零影响。
func (c Config) CheckIntegrity(env func(string) string) []MissingEntry {
	var out []MissingEntry
	add := func(entry, reason string, fatal bool) {
		out = append(out, MissingEntry{Entry: entry, Reason: reason, Fatal: fatal})
	}

	// 必需字段：缺任何一个都无法构造出可用的 LLM 请求。
	if strings.TrimSpace(c.Model) == "" {
		add("$.model", "model is required", true)
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		add("$.baseURL", "baseURL is required", true)
	}
	if strings.TrimSpace(c.Protocol) == "" {
		add("$.protocol", "protocol is required", true)
	}

	// 工作区路径：会话与状态需要落盘位置；这里只校验已配置，
	// 不验证目录是否存在，以保持纯函数语义。
	if strings.TrimSpace(c.CWD) == "" {
		add("$.cwd", "working directory is required", true)
	}
	if strings.TrimSpace(c.StorageDir) == "" {
		add("$.storageDir", "storage directory is required", true)
	}

	// provider 凭证：先确认能解析出提供方，再判断它期望的凭据是否
	// 有来源。解析不到提供方时无法发起任何请求，属于致命项。
	provider, ok := c.Provider("")
	if !ok {
		add("$.provider", "no provider is configured or resolvable", true)
		return out
	}

	// 只有声明了环境变量键的提供方才需要凭据（例如本地 ollama 不需要）。
	// 顶层 apiKey、提供方 apiKey 以及注入 env 中任一有值即视为已配置。
	// 即使这里都缺失，调用方仍可能通过命令行 flag 提供密钥，
	// 因此这一项是非致命的提示，而非阻断启动的硬失败。
	if provider.APIKeyEnv != "" {
		hasKey := strings.TrimSpace(c.APIKey) != "" || strings.TrimSpace(provider.APIKey) != ""
		if !hasKey && env != nil {
			hasKey = strings.TrimSpace(env(provider.APIKeyEnv)) != ""
		}
		if !hasKey {
			add(fmt.Sprintf("$.providers.%s.apiKey", provider.Name),
				fmt.Sprintf("provider %q has no credential: set apiKey or provide %s at runtime",
					provider.Name, provider.APIKeyEnv), false)
		}
	}

	return out
}

// MissingEntryLess 按 Entry 字段对两个缺失项做字典序比较，供调用方配合
// sort.SliceStable 对 MissingEntry 切片做稳定排序。只比较 Entry：Entry 相等
// 时返回 false，使比较满足严格弱序，相同条目的相对顺序交由稳定排序保留。
func MissingEntryLess(a, b MissingEntry) bool {
	return a.Entry < b.Entry
}

// IntegritySummary 对当前配置做一次完整性审计，并把缺失项渲染为稳定摘要，
// 供 doctor 等启动前审计工具直接展示。它等价于对 CheckIntegrity(c, os.Getenv)
// 的结果调用 formatIntegritySummary：不修改接收者、不写磁盘；环境变量仅通过
// os.Getenv 只读读取，不改动任何全局状态。
func (c Config) IntegritySummary() string {
	return formatIntegritySummary(c.CheckIntegrity(os.Getenv))
}

// formatIntegritySummary 把缺失项渲染为稳定文本摘要。输出遵循以下约定：
//   - 无缺失（nil 或空切片）返回固定字面量 integrityOK；
//   - 有缺失先输出一行计数 "integrity: N missing"，再逐项输出，
//     每项一行，格式为 "  [级别] Entry: Reason"；
//   - 条目先按 Entry 字典序稳定排序，故相同输入集合无论初始顺序如何
//     都得到逐字节一致的文本；Entry 相同时保留原始相对顺序；
//   - 致命项级别为 "fatal"，非致命项为 "non-fatal"，据此携带 Fatal 标记；
//   - Reason 中的 CR/LF 被压成空格，保证一项恰好一行，CJK 文本保持完整；
//   - 排序只读取切片，不修改调用方持有的元素顺序。
func formatIntegritySummary(missing []MissingEntry) string {
	if len(missing) == 0 {
		return integrityOK
	}

	// 复制后排序，避免改变调用方持有的切片元素顺序。
	ordered := append([]MissingEntry(nil), missing...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return MissingEntryLess(ordered[i], ordered[j])
	})

	var b strings.Builder
	fmt.Fprintf(&b, "integrity: %d missing\n", len(ordered))
	for _, m := range ordered {
		level := "non-fatal"
		if m.Fatal {
			level = "fatal"
		}
		fmt.Fprintf(&b, "  [%s] %s: %s\n", level, m.Entry,
			integrityNewlineReplacer.Replace(m.Reason))
	}
	return strings.TrimSuffix(b.String(), "\n")
}
