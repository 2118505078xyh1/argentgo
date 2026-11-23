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
// 字段保存规范化后的取值，由各协议客户端按线上协议读取适用项：
// chat completions 读取 Effort 作为 reasoning_effort。让 Options.Thinking
// 为 nil 时客户端不发出推理参数，使每个请求字节与早期构建完全一致。
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
	// SessionID 是续接（--session/--resume）中的会话 ID。非空时
	// Responses 协议用它派生跨 turn/fork 稳定的 prompt_cache_key
	// （见 sessionCacheKey）；空值表示不参与派生，仍回退到逐字节
	// 不变的 deriveCacheKey，使无 ID 请求的线协议字节与接线前一致。
	// 它是纯路由元数据，不进入请求体。
	SessionID string `json:"-"`
	// PreserveReasoning 选择加入在后续请求中重放助手轮次的推理：
	// chat-completions 上是 reasoning_content，Anthropic Messages 上是 thinking 块。
	// 默认关闭，使发送到上游的字节——以及所有现有的线协议格式测试——
	// 保持与之前完全一致。
	PreserveReasoning bool
	// Thinking 携带规范化后的推理控制，供线客户端按协议读取。
	// 它默认为 nil，此时客户端不序列化任何推理参数，
	// 因此默认请求字节与接线前完全一致。
	Thinking *ThinkingConfig `json:"thinking,omitempty"`
	// ReasoningSummary 在 Responses 协议上请求推理摘要
	// （reasoning.summary，如 "auto"/"concise"/"detailed"）。
	// 空值时不发送该字段，保持默认请求字节不变。
	ReasoningSummary string `json:"reasoning_summary,omitempty"`
	// StoreResponses 选择加入 Responses 协议的 store=true，
	// 让提供方保存响应以便后续引用。默认 false，请求字节与接线前一致。
	StoreResponses bool `json:"store_responses,omitempty"`
	// Verbosity 在 Responses 协议上映射为顶层 verbosity
	// （low/medium/high）。空值时省略该字段，保持默认字节不变。
	Verbosity string `json:"verbosity,omitempty"`
	// StrictStreamTerminator 选择加入严格的流终止符校验：流式答复已输出文本
	// 却未到达协议终止符时返回 ErrStreamError，使 "文本中途 TCP 关闭"
	// 这类截断能触发 agent 层的回补门。默认 false，容忍截断并返回半截文本，
	// 逐字节保持历史行为。
	StrictStreamTerminator bool
	// AttemptHook 为会话层接收脱敏后的尝试记录。
	AttemptHook func(AttemptRecord)
	// Steer 是可选的流中纠正源（live steering，opt-in ARGENT_LIVE_STEER=1）。
	// readStream 在每个 SSE 事件边界调用它，返回自上次调用以来排队的纠正文本
	// （应当是一次性消费，语义对齐 agent 层 steering.drain）。返回的文本不改写
	// 任何线协议字节，而是经 SteerHook 以 accepted/deferred 决策上报，由上层在
	// 下一轮合法边界作为用户消息注入。nil 时该机制整体短路，零开销。
	Steer func() []string
	// SteerHook 可选地接收每条 steering 决策（accepted/deferred）。
	// 仅在 Steer 非 nil 且 opt-in 开启时被调用；nil 时决策只被丢弃。
	SteerHook func(SteerDecision)
	// ContextWindow 是当前模型的上下文窗口（token）。大于 0 时 Chat/ChatOnce
	// 会把输出上限拟合进剩余窗口（见 FitOutputTokens），避免提示接近窗口时
	// 因 max_tokens 过大被 400 拒绝。零值（默认）表示未知窗口，请求字节不变。
	ContextWindow int `json:"-"`
}

// DefaultChatMaxTokens 是 chat completions 请求默认的输出上限，供输出拟合使用。
// 默认请求不发送该字段；仅在拟合后确实需要收紧时才写入线协议。
const DefaultChatMaxTokens = 8192

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

// sessionCacheKeyMaxLen 是会话派生缓存键的长度上限（按 rune 计）。
// 上限让键保持可读，也避免过长的会话 ID 或覆盖值进入上游请求日志。
const sessionCacheKeyMaxLen = 128

// sessionCacheKey 按三态策略派生跨 turn/fork 稳定的 Responses 缓存键：
//   - override 非空（忽略首尾空白）时原样采用调用方的显式覆盖值；
//   - 否则 sessionID 非空时返回 "session:"+sessionID，使同一会话在
//     不同轮次与分叉之间复用同一份缓存；
//   - 两者都为空时返回空串，让线协议通过 omitempty 省略 prompt_cache_key。
//
// 返回值按 rune 有界截断，因此 CJK 等多字节字符不会被从中间切断；
// 相同输入始终得到相同输出。该函数是纯函数，不读取任何全局状态。
func sessionCacheKey(sessionID, override string) string {
	if key := strings.TrimSpace(override); key != "" {
		return truncateRunes(key, sessionCacheKeyMaxLen)
	}
	if id := strings.TrimSpace(sessionID); id != "" {
		return truncateRunes("session:"+id, sessionCacheKeyMaxLen)
	}
	return ""
}

// truncateRunes 按 rune 把 s 截断到最多 max 个字符。按 rune 而非字节
// 切片，保证截断不会落在某个多字节字符的中间（CJK 安全）。
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
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

// mayContainCredential 是 RedactText 的廉价守卫。五条正则中，前三条分别需要
// ':'/'='（字段赋值）、'-'/'_'（密钥前缀分隔符）、'@'（邮箱）；只有 hexBlobRE
// 不需要任何分隔符，因此单独线性扫描 32 位连续 hex。普通源码行一次扫描即可
// 跳过全部正则，避免每行都跑五遍 ReplaceAllString。
func mayContainCredential(text string) bool {
	if strings.ContainsAny(text, ":=@-_") {
		return true
	}
	if len(text) < 32 {
		return false
	}
	run := 0
	for i := 0; i < len(text); i++ {
		c := text[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			run++
			if run >= 32 {
				return true
			}
		} else {
			run = 0
		}
	}
	return false
}

// RedactText 在提供方文本被记录或附加到尝试记录之前，移除其中
// 凭据形态的值。普通的错误措辞会保留。
func RedactText(text string) string {
	if !mayContainCredential(text) {
		return text
	}
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
