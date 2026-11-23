package llm

import (
	"context"
	"errors"
	"io"
	"net"
	"regexp"
)

// ActionableError 报告的错误家族。字符串值是稳定的，因此
// 调用方无需导入本包的类型即可持久化或比较它们。
const (
	FamilyNotConfigured   = "not_configured"
	FamilyAuth            = "auth"
	FamilyQuotaExhausted  = "quota_exhausted"
	FamilyRateLimit       = "rate_limit"
	FamilyContentFilter   = "content_filter"
	FamilyContextOverflow = "context_overflow"
	FamilyModelNotFound   = "model_not_found"
	FamilyNetwork         = "network"
	FamilyServer          = "server"
	FamilyUnknown         = "unknown"
)

// ActionableError 是规范化后的提供方失败，包含一个错误家族、
// 一段面向用户的简短解释，以及运维人员可采取的一个具体下一步。
//
// 这与 retry.go 中的重试分类刻意不同：
// 重试决策回答"我该再发一次吗？"，而这个类型回答
// "用户现在该做什么？"。例如硬性配额响应虽然不可重试，
// 但仍有可采取行动的下一步（"等待或更换模型"）。
//
// Message 和 NextStep 是简体中文的 UI 文案。集成层可以通过
// i18n 映射它们；这里不引入 i18n 依赖，因为 llm 保持
// 仅依赖标准库。Message 携带原始错误的、有界且脱敏的摘录，
// 因此凭据绝不会通过这个类型进入日志或屏幕。
type ActionableError struct {
	Family   string
	Message  string
	NextStep string
}

// Error 使 ActionableError 可作为 error 使用。它只报告
// 解释，绝不报告下一步，使意外的 fmt 输出保持简洁。
func (a *ActionableError) Error() string {
	if a == nil {
		return ""
	}
	return a.Message
}

// apiErrorFamily 保存一个错误家族的简体中文文案。
type apiErrorFamily struct {
	name     string
	message  string
	nextStep string
}

var (
	familyNotConfigured = apiErrorFamily{FamilyNotConfigured,
		"提供方尚未配置（缺少 API Key 或凭据）", "先运行 login 登录或配置 API Key，再重试"}
	familyAuth = apiErrorFamily{FamilyAuth,
		"认证失败（API Key 无效或已过期）", "重新登录（login / /login）后重试"}
	familyQuotaExhausted = apiErrorFamily{FamilyQuotaExhausted,
		"额度或配额已用尽", "等待配额重置后重试，或用 --model <id> 换模型（先用 argentgo models 查看可用模型）"}
	familyRateLimit = apiErrorFamily{FamilyRateLimit,
		"请求过于频繁（触发限流）", "稍后重试（客户端会自动退避），或换模型重试"}
	familyContentFilter = apiErrorFamily{FamilyContentFilter,
		"内容被安全策略拦截", "修改提示词内容后重试"}
	familyContextOverflow = apiErrorFamily{FamilyContextOverflow,
		"上下文窗口已用尽（提示词或历史对话超出模型上限）", "压缩历史对话或缩短提示词后重试，或换用上下文更大的模型"}
	familyModelNotFound = apiErrorFamily{FamilyModelNotFound,
		"模型不存在或不可用", "用 --model <id> 选择可用模型后重试（先用 argentgo models 查看）"}
	familyNetwork = apiErrorFamily{FamilyNetwork,
		"网络连接失败", "检查网络或代理设置后重试"}
	familyServer = apiErrorFamily{FamilyServer,
		"提供方服务端暂时不可用", "稍后重试；若持续失败可换模型"}
	familyUnknown = apiErrorFamily{FamilyUnknown,
		"请求失败（未识别的错误类型）", "查看原始错误信息后重试，或用 --model <id> 换模型（先用 argentgo models 查看）"}
)

// 家族模式在脱敏后的错误文本上运行。它们放在这里，而不是
// 复用 retry.go 的 nonRetryableQuotaRE，使这个分类器保持为纯
// 增量：retry.go 属于另一位工作者，可能独立变化。
// 状态码采用宽松匹配，因为 providerError 会在响应体前
// 加上状态行，如 "provider returned 429 Too Many Requests: ..."。
var (
	notConfiguredRE = regexp.MustCompile(`(?i)(not[_\s-]?configured|not[_\s-]?set|no[_\s-]?(?:api[_\s-]?)?key|api[_\s-]?key[_\s-]?(?:is[_\s-]?)?required|missing[_\s-]?(?:api[_\s-]?)?key|no[_\s-]?credentials|provider[_\s-]?not[_\s-]?configured|configure[_\s-]+(?:an?[_\s-]+)?(?:api[_\s-]?)?key|set[_\s-]+(?:the[_\s-]+)?(?:api[_\s-]?)?key|尚未配置)`)
	// authRE 覆盖相邻形式（"invalid_api_key"）以及提供方实际返回的
	// 两种带间隔的措辞："api key" 前的形容词
	// （"Incorrect API key provided: ..."，OpenAI），以及其后的谓语
	// （"Your API key is invalid"、"api key has been revoked"）。
	// 间隔是有界的，因此仅仅提到 api key 的配额/限流消息
	// 不会被拉入 auth 家族；notConfiguredRE 仍
	// 先运行，把 "no api key"/"api key is required" 归入 config。
	authRE = regexp.MustCompile(`(?i)(\b401\b|\b403\b|unauthori[sz]ed|invalid[_\s-]?api[_\s-]?key|invalid[_\s-]?token|expired[_\s-]?token|authentication[_\s-]?(?:failed|error|required)|forbidden|login[_\s-]?required|sign[_\s-]?in[_\s-]?required|invalid[_\s-]?credentials?|api[_\s-]?key[_\s-]?invalid|(?:incorrect|wrong|unknown|unrecogni[sz]ed|expired|revoked)[^\n]{0,24}?api[_\s-]?key|api[_\s-]?key[^\n]{0,32}?(?:invalid|incorrect|wrong|expired|revoked|unknown|unrecogni[sz]ed|rejected|not[_\s]+valid)|认证失败|登录已过期)`)
	// quotaRE 匹配硬性的、不可重试的配额/计费限额。通用
	// 词（billing、配额、额度）刻意不是裸出现的：短暂的限流
	// 通常用它们表述（"请求过于频繁，配额将在 30 秒后重置"、
	// "rate limit exceeded, check your billing settings"）。若允许裸词
	// 在此匹配，这类回复会在 rateLimitRE 运行之前就被归类为 quota_exhausted
	// ——不可重试并路由到设置——即使 retry.go 有意
	// 把相同文本视为可重试。要求附近有耗尽短语；显式错误码
	// （insufficient_quota、quota_exceeded、402 等）本身已
	// 足够。
	quotaRE         = regexp.MustCompile(`(?i)(\b402\b|insufficient[_\s-]?quota|quota[_\s-]?(?:exceeded|exhausted|limit|reached)|exceeded[_\s-]+(?:your[_\s-]+)?(?:current[_\s-]+)?quota|exhausted[_\s-]+(?:its[_\s-]+|your[_\s-]+|the[_\s-]+)?quota|usage[_\s-]?limit|plan[_\s-]?limit|subscription[_\s-]?(?:limit|quota)|out[_\s-]?of[_\s-]?(?:quota|credit|balance)|credit[_\s-]?balance|insufficient[_\s-]?(?:credit|credits|funds|balance)|payment[_\s-]?required|hard[_\s-]?limit|(?:hard[_\s-]?limit|usage[_\s-]?limit|payment[_\s-]?required)[^\n]{0,80}?billing|billing[^\n]{0,80}?(?:hard[_\s-]?limit|usage[_\s-]?limit|exceeded|exhausted|insufficient|depleted|quota|payment[_\s-]?required|limit[_\s-]?reached)|(?:配额|额度|订阅额度|订阅限额)[^\n]{0,24}?(?:用尽|耗尽|不足|已用完|用罄))`)
	rateLimitRE     = regexp.MustCompile(`(?i)(\b429\b|too[_\s-]?many[_\s-]?requests|rate[_\s-]?limit|throttl|请求过于频繁|限流)`)
	contentFilterRE = regexp.MustCompile(`(?i)(content[_\s-]?filter|content[_\s-]?policy|safety[_\s-]?(?:filter|check|system)|moderation|blocked[_\s-]?by|policy[_\s-]?violation|prohibited[_\s-]?content|内容审核|安全策略)`)
	modelNotFoundRE = regexp.MustCompile(`(?i)(\b404\b|model[_\s-]?not[_\s-]?found|unknown[_\s-]?model|invalid[_\s-]?model|model[_\s-]?(?:does[_\s-]?not[_\s-]?exist|not[_\s-]?exist)|no[_\s-]?such[_\s-]?model|unsupported[_\s-]?model|模型不存在)`)
	networkRE       = regexp.MustCompile(`(?i)(connection[_\s-]?refused|dial[_\s-]?tcp|no[_\s-]?such[_\s-]?host|network[_\s-]?is[_\s-]?unreachable|no[_\s-]?route[_\s-]?to[_\s-]?host|host[_\s-]?is[_\s-]?unreachable|connection[_\s-]?reset|unexpected[_\s-]?eof|\beof\b|tls[:_\s-]|certificate|timeout|timed[_\s-]?out|context[_\s-]?deadline|网络)`)
	serverRE        = regexp.MustCompile(`(?i)(\b5\d\d\b|internal[_\s-]?server[_\s-]?error|bad[_\s-]?gateway|service[_\s-]?unavailable|gateway[_\s-]?timeout|overloaded|server[_\s-]?error|服务不可用|服务器错误)`)
	// contextOverflowRE 刻意很窄：只匹配显式的 "context window
	// exceeded" 措辞（以及 Anthropic 和 OpenAI 中等价的
	// 提示/消息长度短语）。通用的 "limit"/"exceeded" 错误和
	// 输出 token 上限被排除，因此硬性配额、限流和普通
	// 长度错误保持其现有家族。该模式与 ClassifyRetryError 共享，
	// 使超大请求绝不会被盲目重放。
	contextOverflowRE = regexp.MustCompile(`(?i)(exceed(?:s|ed)?[_\s-]+(?:the[_\s-]+)?context[_\s-]?(?:window|length)|context[_\s-]+(?:window|length)[_\s-]+(?:is[_\s-]+)?(?:exceeded|exceeds|overflow|too[_\s-]+long|limit)|(?:max|maximum)[_\s-]+context[_\s-]+length|(?:prompt|input|messages?)[_\s-]+(?:is[_\s-]+)?too[_\s-]+(?:long|large)|(?:tokens?|messages?|input)[_\s-]+exceed(?:s|ed)?[_\s-]+(?:the[_\s-]+)?limit|reduce[_\s-]+the[_\s-]+length[_\s-]+of[_\s-]+(?:the[_\s-]+)?messages|上下文(?:窗口|长度)[^\n]{0,16}?(?:超出|超过|用尽|耗尽|溢出|过长|限制|上限)|(?:提示词|输入(?:内容|文本)?|消息)(?:过长|超出(?:上下文|长度|窗口)?|太久)|超出(?:上下文|模型)(?:窗口|长度))`)
)

// ClassifyAPIError 把任意错误规范化为可采取行动的提示。
//
// 行为：
//   - nil 返回 nil。
//   - *ActionableError 原样返回，即使用 %w
//     包装也是如此，因此对于可能多次分类的调用方，
//     分类是幂等的。
//   - 任何其他非 nil 错误都会产生一个家族。先通过 errors.Is/errors.As
//     哨兵（网络/context/流失败）选择可识别的家族，
//     再根据 RedactText(err.Error()) 上的包级模式选择。来自
//     本包之外的错误不会被拒绝：没有匹配模式时它们
//     会得到 FamilyUnknown，显示它仍然是安全的。
//
// Message 中嵌入的错误文本总是经过 RedactError 且长度有界，
// 因此凭据和超大的提供方响应体不会泄露。
// 取消（context.Canceled）没有专用家族，有意
// 落入 FamilyUnknown；这是有意的停止，而非失败。
func ClassifyAPIError(err error) *ActionableError {
	if err == nil {
		return nil
	}
	// errors.As（而非直接类型断言）使分类能穿过线客户端
	// 在已分类错误外添加的 %w 包装，保持幂等。没有它，
	// 同一个 ActionableError 会从自己脱敏后的消息中重新派生，
	// 重复细节，并可能在消息不再匹配原始信号时
	// 改变家族。
	var own *ActionableError
	if errors.As(err, &own) {
		return own
	}
	text := RedactError(err)
	family := classifyAPIErrorFamily(err, text)
	detail := clipRedacted(text, 160)
	if detail != "" {
		family.message += ": " + detail
	}
	return &ActionableError{Family: family.name, Message: family.message, NextStep: family.nextStep}
}

// classifyAPIErrorFamily 先按哨兵匹配、再按文本模式选择家族，
// 因此文本恰好提到状态码的 net.Error 仍会被
// 报告为网络失败。
func classifyAPIErrorFamily(err error, text string) apiErrorFamily {
	switch {
	case errors.Is(err, ErrStreamError):
		return apiErrorFamily{FamilyNetwork, "流式响应中断", "检查网络后重试本次请求"}
	case errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, net.ErrClosed),
		errors.Is(err, io.EOF),
		errors.Is(err, io.ErrUnexpectedEOF):
		return familyNetwork
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return familyNetwork
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return familyNetwork
	}

	switch {
	case notConfiguredRE.MatchString(text):
		return familyNotConfigured
	case authRE.MatchString(text):
		return familyAuth
	case quotaRE.MatchString(text):
		return familyQuotaExhausted
	case rateLimitRE.MatchString(text):
		return familyRateLimit
	case contentFilterRE.MatchString(text):
		return familyContentFilter
	case modelNotFoundRE.MatchString(text):
		return familyModelNotFound
	case serverRE.MatchString(text):
		return familyServer
	case networkRE.MatchString(text):
		return familyNetwork
	case contextOverflowRE.MatchString(text):
		// 有意放在最后：同时带有更强既有信号
		// （auth/quota/server 等）的错误保留那个家族，因此添加这个
		// 分支不会遮蔽任何已有的分类。
		return familyContextOverflow
	default:
		return familyUnknown
	}
}

// clipRedacted 按字符数限制脱敏摘录的长度，绝不拆分 UTF-8
// 序列，因此 UI 输出不会在字符中间被截断。
func clipRedacted(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit-1]) + "…"
}
