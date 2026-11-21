package config

import (
	"fmt"
	"strings"
)

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
