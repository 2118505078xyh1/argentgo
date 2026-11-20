package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/url"
	"regexp"
	"strings"
)

// 线协议名称。它们与 provider.Protocol 的字符串值一致，但 llm
// 保持独立于 provider 表。
const (
	ProtocolChatCompletions   = "openai-chat-completions"
	ProtocolAnthropicMessages = "anthropic-messages"
	ProtocolResponses         = "openai-responses"
)

// CacheControl 为支持显式提示缓存的提供方标记稳定的请求前缀（系统提示加工具 schema）。
// Type 通常为 "ephemeral"；
// 除非调用方要求保留提示，否则省略 TTL。
type CacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

// EphemeralCacheControl 是受支持线协议共享的标记。
func EphemeralCacheControl() *CacheControl {
	return &CacheControl{Type: "ephemeral"}
}

// ThinkingConfig 是 Options 携带的、与协议无关的推理控制。
// 它是纯数据载体：目前没有线客户端读取它，因此让
// Options.Thinking 为 nil 可使每个请求字节与早期构建完全一致。
// 三个字段覆盖厂商提供的三类旋钮：
//   - Effort 是命名级别（"low"/"medium"/"high" 或对应的中文），
//     由 OpenAI 风格协议映射到 reasoning_effort / reasoning.effort。
//   - BudgetTokens 是显式的思考 token 预算，映射到 Anthropic 的
//     扩展思考 budget_tokens。
//   - Enabled 是面向只有开关的厂商的普通布尔值。
//
// 零值字段表示"不覆盖此旋钮"；在选择该值适用的字段之前，
// 这是调用方的职责（目前是 internal/provider.ResolveThinking），
// 然后该值才会到达线客户端。
type ThinkingConfig struct {
	Effort       string `json:"effort,omitempty"`
	BudgetTokens int64  `json:"budget_tokens,omitempty"`
	Enabled      *bool  `json:"enabled,omitempty"`
}

// Options 是每个协议客户端共享的线协议级旋钮。
// 零值保持历史线协议格式，并
// 根据协议和端点自动探测缓存支持。
type Options struct {
	// NoPromptCache 对应 --no-prompt-cache：任何地方都不发出标记。
	// 它优先于 ForcePromptCache。
	NoPromptCache bool
	// ForcePromptCache 让原本无缓存的端点（本地服务器、
	// 开放兼容的 chat completions）选择发出稳定前缀标记。
	ForcePromptCache bool
	// CacheRetention 设置后，会给标记和 Responses 缓存键请求
	// 添加保留提示。
	CacheRetention string
	// IncludeUsage 要求 chat-completion 提供方提供最终用量分块。
	// 启用提示缓存即隐含启用它。
	IncludeUsage bool
	// PromptCacheKey 覆盖 Responses 协议中从稳定前缀派生的
	// 缓存键。
	PromptCacheKey string
	// PreserveReasoning 选择加入在后续请求中重放助手轮次的推理：
	// chat-completions 上是 reasoning_content，Anthropic Messages 上是 thinking 块。
	// 默认关闭，使发送到上游的字节——以及所有现有的线协议格式测试——
	// 保持与之前完全一致。
	PreserveReasoning bool
	// Thinking 携带规范化后的推理控制，供未来线客户端接线使用。
	// 它默认为 nil，且目前没有线客户端序列化它，
	// 因此添加此字段不会改变任何现有请求字节。
	Thinking *ThinkingConfig `json:"thinking,omitempty"`
	// AttemptHook 为会话层接收脱敏后的尝试记录。
	AttemptHook func(AttemptRecord)
}

// CacheCapability 描述 Chatter 实际有效的提示缓存支持。
type CacheCapability struct {
	Supported bool
	Protocol  string
	// Local 报告该端点是否为回环地址且默认无缓存。
	Local bool
}

// PromptCacheCapable 由能够声明是否发出缓存标记的客户端实现。
// 应使用 CacheSupport，而不要导入具体的客户端类型。
type PromptCacheCapable interface {
	PromptCacheCapability() CacheCapability
}

// CacheSupport 在 Chatter 边界上报告提示缓存支持。未实现
// PromptCacheCapable 的 Chatter 会被报告为无缓存，从而
// 使脚本化和第三方实现保持不变。
func CacheSupport(ch Chatter) CacheCapability {
	if capable, ok := ch.(PromptCacheCapable); ok {
		return capable.PromptCacheCapability()
	}
	return CacheCapability{}
}

func (o Options) cacheEnabled(protocol, baseURL string) bool {
	if o.NoPromptCache {
		return false
	}
	if o.ForcePromptCache {
		return true
	}
	switch protocol {
	case ProtocolAnthropicMessages, ProtocolResponses:
		return !isLocalEndpoint(baseURL)
	default:
		// OpenAI 兼容的 chat completions 没有标准标记；需显式选择加入，
		// 使本地和开放兼容端点保持不变。
		return false
	}
}

func (o Options) cacheControl() *CacheControl {
	control := EphemeralCacheControl()
	control.TTL = strings.TrimSpace(o.CacheRetention)
	return control
}

// normalizeCacheTTL 只接受缓存网关理解的保留档位。
// 匹配前会去除空白；空值表示"使用提供方默认值"，其他值
// 会被拒绝（返回 ""），而不是作为不支持的提示转发。
// 它从不修改 Options。
func normalizeCacheTTL(ttl string) string {
	trimmed := strings.TrimSpace(ttl)
	switch trimmed {
	case "", "5m", "30m", "1h", "24h":
		return trimmed
	default:
		return ""
	}
}

// normalizedCacheControl 为递增的会话尾部断点构建标记。
// 长期存在的稳定前缀标记继续使用 cacheControl，
// 不做改动，以保持其线协议格式。
func (o Options) normalizedCacheControl() *CacheControl {
	control := EphemeralCacheControl()
	control.TTL = normalizeCacheTTL(o.CacheRetention)
	return control
}

// isLocalEndpoint 把回环端点视为本地，即使提供方
// 模板使用的是支持缓存的协议。
func isLocalEndpoint(baseURL string) bool {
	value := strings.TrimSpace(baseURL)
	if value == "" {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return strings.HasSuffix(host, ".localhost")
}

// deriveCacheKey 把稳定前缀哈希为确定性的 Responses
// prompt_cache_key。短摘要使请求日志保持可读。
func deriveCacheKey(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = io.WriteString(hash, part)
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))[:32]
}

var (
	// authorizationRE 在一次匹配中吞掉 Authorization 风格字段的
	// 整个值：可选的方案词（Bearer、Basic 等）加上 token。
	// 同时匹配方案和 token 至关重要——只匹配字段名会脱敏 "Bearer"
	// 而留下真正的密钥。它在 credentialFieldRE 之前运行，使通用字段规则
	// 永远不会先对 Authorization 头部做部分
	// 匹配。
	authorizationRE = regexp.MustCompile(`(?i)(authorization["']?\s*[:=]\s*["']?)([^\s"',;}]+(?:\s+[^\s"',;}]+)?)`)
	// 字段名允许词之间有空格、'_' 或 '-'，因此 "API key"、
	// "api_key" 和 "api-key" 都能覆盖。结尾的 [:=] 要求
	// 防止普通文本被当作凭据字段。
	credentialFieldRE = regexp.MustCompile(`(?i)(api[\s_\-]*key|access[\s_\-]*token|refresh[\s_\-]*token|authorization|bearer|password|passwd|secret|token)(["']?\s*[:=]\s*["']?)([^\s"',;}]+)`)
	// 已知的密钥前缀用 '-' 或 '_' 作分隔符。值字符类也
	// 包含 '-'/'_'，因此 sk-ant-api03- 和 sk-proj- 这样的多段前缀
	// 无需列举每一种变体即可覆盖。
	knownSecretRE = regexp.MustCompile(`(?i)\b(?:sk|rk|pk|xoxb|ghp|gho|github_pat)[-_][A-Za-z0-9_\-]{12,}\b`)
	emailRE       = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	hexBlobRE     = regexp.MustCompile(`\b[A-Fa-f0-9]{32,}\b`)
)

// RedactText 在提供方文本被记录或附加到尝试记录之前，移除其中
// 凭据形态的值。普通的错误措辞会保留。
func RedactText(text string) string {
	text = authorizationRE.ReplaceAllString(text, "${1}[redacted]")
	text = credentialFieldRE.ReplaceAllString(text, "${1}${2}[redacted]")
	text = knownSecretRE.ReplaceAllString(text, "[redacted]")
	text = emailRE.ReplaceAllString(text, "[email]")
	text = hexBlobRE.ReplaceAllString(text, "[redacted]")
	return text
}

// RedactError 是面向错误的 RedactText；nil 错误渲染为空字符串。
func RedactError(err error) string {
	if err == nil {
		return ""
	}
	return RedactText(err.Error())
}
