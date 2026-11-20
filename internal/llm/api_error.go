package llm

import (
	"context"
	"errors"
	"io"
	"net"
	"regexp"
)

// Error families reported by ActionableError. The string values are stable so
// callers can persist or compare them without importing this package's types.
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

// ActionableError is a provider failure normalized into a family, a short
// human-facing explanation and one concrete next step the operator can take.
//
// This is deliberately different from the retry classification in retry.go:
// a retry decision answers "should I send this again?", while this type answers
// "what should the user do now?". A hard quota response, for example, is not
// retryable and still has an actionable next step ("wait or switch models").
//
// Message and NextStep are zh-CN UI copy. The integration layer may map them
// through i18n; no i18n dependency is introduced here because llm stays
// stdlib-only. Message carries a bounded, redacted excerpt of the original
// error, so credentials never reach logs or the screen through this type.
type ActionableError struct {
	Family   string
	Message  string
	NextStep string
}

// Error makes ActionableError usable as an error. It reports only the
// explanation, never the next step, so accidental fmt output stays terse.
func (a *ActionableError) Error() string {
	if a == nil {
		return ""
	}
	return a.Message
}

// apiErrorFamily holds the zh-CN copy for one family.
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
		"额度或配额已用尽", "等待配额重置后重试，或用 /model 换一个模型"}
	familyRateLimit = apiErrorFamily{FamilyRateLimit,
		"请求过于频繁（触发限流）", "稍后重试（客户端会自动退避），或换模型重试"}
	familyContentFilter = apiErrorFamily{FamilyContentFilter,
		"内容被安全策略拦截", "修改提示词内容后重试"}
	familyContextOverflow = apiErrorFamily{FamilyContextOverflow,
		"上下文窗口已用尽（提示词或历史对话超出模型上限）", "压缩历史对话或缩短提示词后重试，或换用上下文更大的模型"}
	familyModelNotFound = apiErrorFamily{FamilyModelNotFound,
		"模型不存在或不可用", "用 /model 选择可用模型后重试"}
	familyNetwork = apiErrorFamily{FamilyNetwork,
		"网络连接失败", "检查网络或代理设置后重试"}
	familyServer = apiErrorFamily{FamilyServer,
		"提供方服务端暂时不可用", "稍后重试；若持续失败可换模型"}
	familyUnknown = apiErrorFamily{FamilyUnknown,
		"请求失败（未识别的错误类型）", "查看原始错误信息后重试，或用 /model 换模型"}
)

// Family patterns run on the redacted error text. They are kept here instead
// of reusing retry.go's nonRetryableQuotaRE so this classifier stays a pure
// addition: retry.go belongs to another worker and may change independently.
// Status codes are matched loosely because providerError prefixes the body
// with a status line such as "provider returned 429 Too Many Requests: ...".
var (
	notConfiguredRE = regexp.MustCompile(`(?i)(not[_\s-]?configured|not[_\s-]?set|no[_\s-]?(?:api[_\s-]?)?key|api[_\s-]?key[_\s-]?(?:is[_\s-]?)?required|missing[_\s-]?(?:api[_\s-]?)?key|no[_\s-]?credentials|provider[_\s-]?not[_\s-]?configured|configure[_\s-]+(?:an?[_\s-]+)?(?:api[_\s-]?)?key|set[_\s-]+(?:the[_\s-]+)?(?:api[_\s-]?)?key|尚未配置)`)
	authRE          = regexp.MustCompile(`(?i)(\b401\b|\b403\b|unauthori[sz]ed|invalid[_\s-]?api[_\s-]?key|invalid[_\s-]?token|expired[_\s-]?token|authentication[_\s-]?(?:failed|error|required)|forbidden|login[_\s-]?required|sign[_\s-]?in[_\s-]?required|invalid[_\s-]?credentials?|api[_\s-]?key[_\s-]?invalid|认证失败|登录已过期)`)
	// quotaRE matches hard, non-retryable quota/billing limits. The generic
	// words (billing, 配额, 额度) are deliberately NOT bare: a transient rate
	// limit is usually phrased with them ("请求过于频繁，配额将在 30 秒后重置",
	// "rate limit exceeded, check your billing settings"). Letting a bare word
	// match here would classify such a reply as quota_exhausted — non-retryable
	// and routed to settings — before rateLimitRE ever runs, even though retry.go
	// deliberately treats the same text as retryable. Require an exhaustion
	// phrase nearby; explicit codes (insufficient_quota, quota_exceeded, 402,
	// ...) stay sufficient on their own.
	quotaRE         = regexp.MustCompile(`(?i)(\b402\b|insufficient[_\s-]?quota|quota[_\s-]?(?:exceeded|exhausted|limit|reached)|exceeded[_\s-]+(?:your[_\s-]+)?(?:current[_\s-]+)?quota|exhausted[_\s-]+(?:its[_\s-]+|your[_\s-]+|the[_\s-]+)?quota|usage[_\s-]?limit|plan[_\s-]?limit|subscription[_\s-]?(?:limit|quota)|out[_\s-]?of[_\s-]?(?:quota|credit|balance)|credit[_\s-]?balance|insufficient[_\s-]?(?:credit|credits|funds|balance)|payment[_\s-]?required|hard[_\s-]?limit|(?:hard[_\s-]?limit|usage[_\s-]?limit|payment[_\s-]?required)[^\n]{0,80}?billing|billing[^\n]{0,80}?(?:hard[_\s-]?limit|usage[_\s-]?limit|exceeded|exhausted|insufficient|depleted|quota|payment[_\s-]?required|limit[_\s-]?reached)|(?:配额|额度|订阅额度|订阅限额)[^\n]{0,24}?(?:用尽|耗尽|不足|已用完|用罄))`)
	rateLimitRE     = regexp.MustCompile(`(?i)(\b429\b|too[_\s-]?many[_\s-]?requests|rate[_\s-]?limit|throttl|请求过于频繁|限流)`)
	contentFilterRE = regexp.MustCompile(`(?i)(content[_\s-]?filter|content[_\s-]?policy|safety[_\s-]?(?:filter|check|system)|moderation|blocked[_\s-]?by|policy[_\s-]?violation|prohibited[_\s-]?content|内容审核|安全策略)`)
	modelNotFoundRE = regexp.MustCompile(`(?i)(\b404\b|model[_\s-]?not[_\s-]?found|unknown[_\s-]?model|invalid[_\s-]?model|model[_\s-]?(?:does[_\s-]?not[_\s-]?exist|not[_\s-]?exist)|no[_\s-]?such[_\s-]?model|unsupported[_\s-]?model|模型不存在)`)
	networkRE       = regexp.MustCompile(`(?i)(connection[_\s-]?refused|dial[_\s-]?tcp|no[_\s-]?such[_\s-]?host|network[_\s-]?is[_\s-]?unreachable|no[_\s-]?route[_\s-]?to[_\s-]?host|host[_\s-]?is[_\s-]?unreachable|connection[_\s-]?reset|unexpected[_\s-]?eof|\beof\b|tls[:_\s-]|certificate|timeout|timed[_\s-]?out|context[_\s-]?deadline|网络)`)
	serverRE        = regexp.MustCompile(`(?i)(\b5\d\d\b|internal[_\s-]?server[_\s-]?error|bad[_\s-]?gateway|service[_\s-]?unavailable|gateway[_\s-]?timeout|overloaded|server[_\s-]?error|服务不可用|服务器错误)`)
	// contextOverflowRE is deliberately narrow: only explicit "context window
	// exceeded" wording (plus the equivalent prompt/message length phrases from
	// Anthropic and OpenAI) matches. Generic "limit"/"exceeded" errors and
	// output-token caps are excluded so hard quota, rate-limit and ordinary
	// length errors keep their existing families. This pattern is shared with
	// ClassifyRetryError so an oversized request is never blindly replayed.
	contextOverflowRE = regexp.MustCompile(`(?i)(exceed(?:s|ed)?[_\s-]+(?:the[_\s-]+)?context[_\s-]?(?:window|length)|context[_\s-]+(?:window|length)[_\s-]+(?:is[_\s-]+)?(?:exceeded|exceeds|overflow|too[_\s-]+long|limit)|(?:max|maximum)[_\s-]+context[_\s-]+length|(?:prompt|input|messages?)[_\s-]+(?:is[_\s-]+)?too[_\s-]+(?:long|large)|(?:tokens?|messages?|input)[_\s-]+exceed(?:s|ed)?[_\s-]+(?:the[_\s-]+)?limit|reduce[_\s-]+the[_\s-]+length[_\s-]+of[_\s-]+(?:the[_\s-]+)?messages|上下文(?:窗口|长度)[^\n]{0,16}?(?:超出|超过|用尽|耗尽|溢出|过长|限制|上限)|(?:提示词|输入(?:内容|文本)?|消息)(?:过长|超出(?:上下文|长度|窗口)?|太久)|超出(?:上下文|模型)(?:窗口|长度))`)
)

// ClassifyAPIError normalizes any error into an actionable hint.
//
// Behavior:
//   - nil returns nil.
//   - An *ActionableError is returned unchanged, so classification is
//     idempotent for callers that may classify an error more than once.
//   - Any other non-nil error yields a family. Recognized families are chosen
//     by errors.Is/errors.As sentinels first (net/context/stream failures),
//     then by package-level patterns on RedactText(err.Error()). Errors from
//     outside this package are not rejected: with no matching pattern they
//     get FamilyUnknown, which is still safe to display.
//
// The error text embedded in Message always goes through RedactError and is
// length-bounded, so credentials and oversized provider bodies cannot leak.
// Cancellation (context.Canceled) has no dedicated family and deliberately
// falls through to FamilyUnknown; it is an intentional stop, not a failure.
func ClassifyAPIError(err error) *ActionableError {
	if err == nil {
		return nil
	}
	if own, ok := err.(*ActionableError); ok {
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

// classifyAPIErrorFamily picks a family using sentinel matching before text
// patterns, so a net.Error whose text happens to mention a status code is
// still reported as a network failure.
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
		// Last on purpose: an error that also carries a stronger existing
		// signal (auth/quota/server/...) keeps that family, so adding this
		// case cannot shadow any pre-existing classification.
		return familyContextOverflow
	default:
		return familyUnknown
	}
}

// clipRedacted bounds a redacted excerpt by runes, never splitting a UTF-8
// sequence, so UI output cannot be truncated mid-rune.
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
