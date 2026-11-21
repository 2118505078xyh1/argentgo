package eval

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// 本文件是 R18 真实评测（L2，真实 API）的纯门禁原语：默认断网、无 key 明确报错、key 不落盘。
// 它不导入 app（避免 import 环），环境变量一律通过注入的 env 函数读取，因此既不读进程环境，
// 也不碰文件系统；APIKey 只在内存中流动，Redacted 是唯一允许进入报告的出口。

const (
	envLiveEnabled = "ARGENT_EVAL_LIVE" // 精确等于 "1" 才开启，其余任何值都视为关闭
	envProtocol    = "ARGENT_PROTOCOL"
	envModel       = "ARGENT_MODEL"
	envBaseURL     = "ARGENT_BASE_URL"
	envAPIKey      = "ARGENT_API_KEY"
	envOpenAIKey   = "OPENAI_API_KEY"
)

var (
	// ErrLiveRequiresKey 表示开启了真实评测却没有任何可用 API key。
	ErrLiveRequiresKey = errors.New("eval: live evaluation requires an API key (set ARGENT_API_KEY or OPENAI_API_KEY)")
	// ErrLiveInvalidProtocol 表示协议名不在支持的别名白名单内。
	ErrLiveInvalidProtocol = errors.New("eval: invalid live protocol")
)

// LiveConfig 是一次真实评测所需的全部连接参数。
// APIKey 只存在于内存；调用方不得把它写进报告或日志。
type LiveConfig struct {
	Enabled                          bool
	Protocol, Model, BaseURL, APIKey string
}

// ReadLiveConfig 从注入的 env 读取真实评测配置。
//
// 只有 ARGENT_EVAL_LIVE 精确等于 "1" 才开启真实评测；未开启时返回零值配置且无错误（默认断网）。
// 开启后 ARGENT_PROTOCOL / ARGENT_MODEL / ARGENT_BASE_URL 决定连接方式，API key 依次取
// ARGENT_API_KEY、OPENAI_API_KEY 中第一个非空值；两者皆空时返回 ErrLiveRequiresKey。
//
// env 为 nil 时等价于所有变量缺失；本函数只读 env、不改入参，也绝不读取进程环境。
func ReadLiveConfig(env func(string) string) (LiveConfig, error) {
	get := func(key string) string {
		if env == nil {
			return ""
		}
		return env(key)
	}
	if get(envLiveEnabled) != "1" {
		return LiveConfig{}, nil
	}
	cfg := LiveConfig{
		Enabled:  true,
		Protocol: get(envProtocol),
		Model:    get(envModel),
		BaseURL:  get(envBaseURL),
		APIKey:   strings.TrimSpace(liveFirstNonEmpty(get(envAPIKey), get(envOpenAIKey))),
	}
	if cfg.APIKey == "" {
		return cfg, ErrLiveRequiresKey
	}
	return cfg, nil
}

// Redacted 返回可安全写入报告的脱敏摘要。
// APIKey 只以占位符加字符数呈现，任何情况下都不会泄漏原文。
func (c LiveConfig) Redacted() string {
	mask := "***"
	if c.APIKey == "" {
		mask = "<empty>"
	} else {
		mask = fmt.Sprintf("***(len=%d)", utf8.RuneCountInString(c.APIKey))
	}
	return fmt.Sprintf("enabled=%t protocol=%q model=%q base_url=%q api_key=%s",
		c.Enabled, c.Protocol, c.Model, c.BaseURL, mask)
}

// Validate 校验配置是否可用：协议必须落在别名白名单内，开启时必须有 key。
func (c LiveConfig) Validate() error {
	if !isLiveProtocol(c.Protocol) {
		return fmt.Errorf("%w: %q", ErrLiveInvalidProtocol, c.Protocol)
	}
	if c.Enabled && strings.TrimSpace(c.APIKey) == "" {
		return ErrLiveRequiresKey
	}
	return nil
}

// CanonicalProtocol 把别名映射到 llm 的线协议名称，供调用方构造 provider 客户端。
// 未知值原样返回（仅去首尾空白），与 Validate 的白名单保持一致。
func (c LiveConfig) CanonicalProtocol() string {
	switch strings.ToLower(strings.TrimSpace(c.Protocol)) {
	case "", "chat", "openai", "openai-chat-completions":
		return "openai-chat-completions"
	case "anthropic", "anthropic-messages", "messages":
		return "anthropic-messages"
	case "responses", "openai-responses":
		return "openai-responses"
	default:
		return strings.TrimSpace(c.Protocol)
	}
}

// isLiveProtocol 是协议别名的字符串白名单，刻意不导入 app 以保持 eval 的依赖方向。
func isLiveProtocol(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "chat", "openai", "openai-chat-completions",
		"anthropic", "anthropic-messages", "messages",
		"responses", "openai-responses":
		return true
	default:
		return false
	}
}

func liveFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
