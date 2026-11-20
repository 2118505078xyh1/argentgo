package llm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RetryPolicy 的默认值。可重试的失败通过
// 指数退避等待解决，提供方 Retry-After 头部更长时优先。
const (
	DefaultMaxAttempts = 3
	DefaultBaseDelay   = 500 * time.Millisecond
	DefaultMaxDelay    = 20 * time.Second

	// MaxRetryAfter 限制提供方提供的延迟：恶意或有 bug 的
	// Retry-After 不得让一次调用停滞数小时。
	MaxRetryAfter = 15 * time.Minute
	// MaxTimeoutRetries 把超时重试次数限制得远低于一般尝试预算。
	// 超时的尝试在失败前已经等待过。
	MaxTimeoutRetries = 3
	// UnreachableWindow 是针对在到主机的网络路径建立之前
	// 就发生的失败的独立重试窗口。它们不消耗 MaxAttempts。
	UnreachableWindow = time.Hour
	// UnreachableMaxDelay 限制该窗口中重试之间的间隔。
	UnreachableMaxDelay = 30 * time.Second

	// retryJitterRatio 把重试分散到计算出的退避窗口的下四分之一，
	// 使命中同一限流提供方的多个会话不会
	// 在同一时刻全部醒来。
	retryJitterRatio = 0.25
)

// Reason 是失败尝试的重试类别。它上报到
// RetryAttempt，使会话层无需响应体就能解释一次重试。
type Reason string

const (
	ReasonNone        Reason = ""            // 不可重试（取消）
	ReasonStatus      Reason = "status"      // 可重试的 HTTP 状态 / Retry-After
	ReasonNotSent     Reason = "not_sent"    // 在收到响应之前传输失败
	ReasonUnreachable Reason = "unreachable" // 无 DNS 应答或无到主机的路由
	ReasonTimeout     Reason = "timeout"     // 截止时间或 i/o 超时
)

// ClassifyRetryError 把传输失败映射到重试类别。DNS 错误
// 和路由缺失是 "unreachable"；截止时间和 net.Error 超时是
// "timeout"；其他在响应之前失败的都是 "not_sent"。
// 取消是 ReasonNone，因为调用方要求停止。显式的
// 上下文窗口溢出也是 ReasonNone：重试同样的超大
// 请求不可能成功，因此调用方必须先压缩或缩短它。
func ClassifyRetryError(err error) Reason {
	if err == nil || errors.Is(err, context.Canceled) {
		return ReasonNone
	}
	// 上下文窗口溢出不会自行消除：重放同样的
	// 超大请求会再次失败。调用方必须先压缩/缩短它，
	// 因此应报告为不可重试，而非传输层的 "not_sent"。
	var actionable *ActionableError
	if errors.As(err, &actionable) && actionable.Family == FamilyContextOverflow {
		return ReasonNone
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ReasonTimeout
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return ReasonUnreachable
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if errors.Is(opErr.Err, context.DeadlineExceeded) || opErr.Timeout() {
			return ReasonTimeout
		}
		if isUnreachableNetworkError(opErr.Err) {
			return ReasonUnreachable
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ReasonTimeout
	}
	// 文本检查放在最后，使上面权威的传输类别仍然优先。
	// 普通的提供方错误（无哨兵）带有溢出措辞，会落
	// 到这里；它们不得被报告为可重试的 "not_sent"。
	if contextOverflowRE.MatchString(RedactError(err)) {
		return ReasonNone
	}
	return ReasonNotSent
}

// isUnreachableNetworkError 识别 EHOSTUNREACH
// 和 ENETDOWN 背后的平台错误，而不依赖特定于 OS 的 syscall 常量。
func isUnreachableNetworkError(err error) bool {
	if err == nil {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"network is unreachable", "no route to host", "network is down", "host is unreachable"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// RetryPolicy 重试短暂的提供方失败（429 和 5xx）以及传输
// 失败。状态错误和普通传输错误消耗 MaxAttempts；超时
// 单独限制，无法访问的主机有自己更长的窗口。
//
// Sleep 可覆盖，使测试无需等待即可运行；nil 表示真实的、
// 感知 context 的定时器等待。
type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	Sleep       func(ctx context.Context, d time.Duration) error
	// Jitter 返回 [0, 1) 区间的值。它可覆盖，使测试能注入
	// 确定性的来源；nil 表示 math/rand/v2.Float64。Jitter 只
	// 应用于指数退避，绝不应用于显式的提供方 Retry-After。
	Jitter func() float64
	// OnRetry 设置后会收到脱敏的重试决策。会话层
	// 可以把它们变成重试尝试记录，而无需看到响应体。
	OnRetry func(RetryAttempt)
}

// DefaultRetryPolicy 返回各协议客户端使用的策略。
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts: DefaultMaxAttempts,
		BaseDelay:   DefaultBaseDelay,
		MaxDelay:    DefaultMaxDelay,
	}
}

// IsZero 报告是否没有任何字段被设置，使客户端可以回退到
// DefaultRetryPolicy。RetryPolicy 持有函数，无法比较。
// 函数字段也要算：只配置了 Jitter 来源或
// OnRetry 钩子的调用方也算选择了加入，用默认值替换整个策略
// 会悄悄丢弃那个回调。因此这里必须检查
// 每一个字段。
func (p RetryPolicy) IsZero() bool {
	return p.MaxAttempts == 0 && p.BaseDelay == 0 && p.MaxDelay == 0 &&
		p.Sleep == nil && p.Jitter == nil && p.OnRetry == nil
}

// Do 发送请求并重试短暂的失败。
//
// send 必须在每次调用时构建新的请求（流式 body 无法
// 重放）。当尝试预算在可重试状态上耗尽时，它返回最后一个响应，
// 使调用方能报告提供方的错误体；只有
// 传输失败和 context 取消才会作为错误浮现。
func (p RetryPolicy) Do(ctx context.Context, send func(context.Context) (*http.Response, error)) (*http.Response, error) {
	p = p.withDefaults()

	var (
		lastErr          error
		normalAttempts   int // 从 MaxAttempts 预算中消耗的尝试次数
		timeouts         int
		unreachableSince time.Time
		unreachableGaps  int
	)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		resp, err := send(ctx)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if err != nil {
			lastErr = err
			reason := ClassifyRetryError(err)
			switch reason {
			case ReasonNone:
				return nil, fmt.Errorf("request failed: %w", err)
			case ReasonUnreachable:
				if unreachableSince.IsZero() {
					unreachableSince = time.Now()
				}
				if time.Since(unreachableSince) >= UnreachableWindow {
					return nil, p.exhausted(lastErr)
				}
				p.notify(RetryAttempt{
					Attempt: unreachableGaps + 1,
					Cause:   clipErrorClass(RedactError(err)),
					Reason:  reason,
				})
				delay := unreachableDelay(unreachableGaps)
				unreachableGaps++
				if waitErr := p.sleep(ctx, delay); waitErr != nil {
					return nil, waitErr
				}
				continue
			case ReasonTimeout:
				if timeouts >= MaxTimeoutRetries {
					return nil, p.exhausted(lastErr)
				}
				timeouts++
			}
			// not_sent 和 timeout 失败消耗通用预算；上面的
			// unreachable 失败则有意不消耗。
			normalAttempts++
			if normalAttempts >= p.MaxAttempts {
				return nil, p.exhausted(lastErr)
			}
			p.notify(RetryAttempt{
				Attempt: normalAttempts,
				Cause:   clipErrorClass(RedactError(err)),
				Reason:  reason,
			})
			if waitErr := p.wait(ctx, normalAttempts, 0); waitErr != nil {
				return nil, waitErr
			}
			continue
		}

		normalAttempts++
		hints := parseRetryHeaders(resp.Header)
		shouldRetry := IsRetryableStatus(resp.StatusCode)
		// x-should-retry 在错误响应上是权威的：网关可以
		// 把某个 5xx 标记为终态，或要求对我们的默认集合
		// 未覆盖的状态重试。缺失或无法识别的值仍按
		// 状态码处理。成功的（2xx）响应绝不重放，即使
		// 网关转发或误配置了 "true" 判定：第一次
		// 调用已经完成，重放会重复计费和
		// 所有副作用，并丢弃调用方需要的成功结果。
		if hints.shouldRetrySet && resp.StatusCode >= 300 {
			shouldRetry = hints.shouldRetry
		}
		if !shouldRetry || normalAttempts >= p.MaxAttempts {
			return resp, nil
		}
		// 窥视一个有界前缀，以区分提供方的硬性限额与
		// 短暂的限流。前缀会被放回，使调用方仍得到
		// 完整的响应体；只有下面的重试路径才会消费它。
		if resp.Body != nil {
			prefix, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body = bodyWithPrefix(prefix, resp.Body)
			// 终态的提供方结果（会话/支出预算、用量
			// 计费失败、hook 拒绝等）无法通过
			// 重试解决，因此也原样返回带体的响应。
			if IsNonRetryableQuotaResponse(resp.StatusCode, prefix) || IsTerminalProviderOutcome(prefix, resp.StatusCode) {
				return resp, nil
			}
		}
		// 长于上限的提供方提示会被拒绝，而不是缩短：
		// 只等待所请求窗口的一个封顶比例，只会再次撞上
		// 同一个限制并浪费一次尝试。原样呈现原始响应，
		// 让调用方自己的恢复逻辑来决定。
		if hints.delay > p.delayCeiling() {
			return resp, nil
		}
		p.notify(RetryAttempt{
			Attempt:    normalAttempts,
			StatusCode: resp.StatusCode,
			Cause:      clipErrorClass(RedactText(resp.Status)),
			Reason:     ReasonStatus,
		})
		drainAndClose(resp.Body)
		if waitErr := p.wait(ctx, normalAttempts, hints.delay); waitErr != nil {
			return nil, waitErr
		}
	}
}

// Delay 是下一次尝试前的等待（attempt 从 1 开始，因此 attempt 2
// 在第一次重试之后）。任何显式的提供方 Retry-After 都是权威的：
// 服务器要求一个最小等待，因此绝不会被 jitter 减少。它同时受
// 3 倍 MaxDelay 和 MaxRetryAfter 限制。没有 Retry-After 时，
// 指数退避会最多向下抖动 retryJitterRatio，使
// 并发会话不会步调一致地醒来。
func (p RetryPolicy) Delay(attempt int, retryAfter time.Duration) time.Duration {
	p = p.withDefaults()
	base := p.baseDelay(attempt, retryAfter)
	// 正的 Retry-After 即使小于指数退避也是权威的：
	// base 已经至少等待那么久，而对其
	// 抖动可能低于提供方要求的最小值。
	if retryAfter > 0 {
		return base
	}
	return time.Duration(float64(base) * (1 - retryJitterRatio*p.Jitter()))
}

// baseDelay 计算不含抖动的精确等待：指数退避以 MaxDelay 为上限，
// 当提供方要求更长等待时，被 Retry-After 覆盖并钳制到 3 倍 MaxDelay
// 和 MaxRetryAfter 之内。
func (p RetryPolicy) baseDelay(attempt int, retryAfter time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := p.BaseDelay
	if base <= 0 {
		base = DefaultBaseDelay
	}
	delay := base
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= p.MaxDelay {
			delay = p.MaxDelay
			break
		}
	}
	if delay > p.MaxDelay {
		delay = p.MaxDelay
	}
	if retryAfter > delay {
		delay = retryAfter
		if cap := p.delayCeiling(); delay > cap {
			delay = cap
		}
	}
	return delay
}

// delayCeiling 是重试循环愿意接受的、提供方请求的最长延迟。
// 更长的提示会被 Do 拒绝（原样呈现响应），
// 而不是悄悄缩短。它与 baseDelay 为直接调用 Delay 的调用方
// 钳制延迟时使用的上限相同。
func (p RetryPolicy) delayCeiling() time.Duration {
	ceiling := p.MaxDelay * 3
	if ceiling <= 0 {
		ceiling = DefaultMaxDelay * 3
	}
	if ceiling > MaxRetryAfter {
		ceiling = MaxRetryAfter
	}
	return ceiling
}

// unreachableDelay 在长窗口中从 2 秒增长到 UnreachableMaxDelay。
func unreachableDelay(gap int) time.Duration {
	delay := 2 * time.Second
	for i := 0; i < gap && delay < UnreachableMaxDelay; i++ {
		delay *= 2
	}
	if delay > UnreachableMaxDelay {
		delay = UnreachableMaxDelay
	}
	return delay
}

func (p RetryPolicy) withDefaults() RetryPolicy {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = DefaultMaxAttempts
	}
	if p.BaseDelay <= 0 {
		p.BaseDelay = DefaultBaseDelay
	}
	if p.MaxDelay <= 0 {
		p.MaxDelay = DefaultMaxDelay
	}
	if p.MaxDelay < p.BaseDelay {
		p.MaxDelay = p.BaseDelay
	}
	if p.Jitter == nil {
		p.Jitter = rand.Float64
	}
	return p
}

func (p RetryPolicy) exhausted(lastErr error) error {
	if lastErr == nil {
		lastErr = errors.New("no attempt was made")
	}
	return fmt.Errorf("request failed after %d attempts: %w", p.MaxAttempts, lastErr)
}

func (p RetryPolicy) notify(attempt RetryAttempt) {
	if p.OnRetry != nil {
		p.OnRetry(attempt)
	}
}

func (p RetryPolicy) wait(ctx context.Context, attempt int, retryAfter time.Duration) error {
	return p.sleep(ctx, p.Delay(attempt, retryAfter))
}

func (p RetryPolicy) sleep(ctx context.Context, delay time.Duration) error {
	if delay < 0 {
		delay = 0
	}
	if p.Sleep != nil {
		return p.Sleep(ctx, delay)
	}
	if delay == 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// IsRetryableStatus 报告某个状态是否值得重试：429 表示限流，
// 5xx 表示提供方自身的失败。408/409/425 以及
// Cloudflare 网关错误码也包含在内，因为提供方在负载下
// 会返回它们。
func IsRetryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooEarly,
		http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable,
		http.StatusGatewayTimeout, 522, 524, 529:
		return true
	}
	return false
}

// nonRetryableQuotaRE 匹配描述硬性账户限额（配额、计费、订阅/套餐）的
// 提供方错误载荷，而非短暂的限流。在包级只编译一次；匹配在脱敏后的
// 文本上进行，因此凭据形态的值无法影响判定。
// text so credential-shaped values cannot influence the decision.
//
// 配额/计费词刻意不是裸出现的："请求过于频繁，配额将在
// 30 秒后重置" 和 "check your billing settings" 描述的是重试
// 可消除的限流。中文配额表述附近需要有耗尽短语，
// "billing" 附近需要有硬性限额/用量短语；显式错误码
// （如 insufficient_quota 和 quota_exceeded）本身已足够。
var nonRetryableQuotaRE = regexp.MustCompile(`(?i)(insufficient[_-]?quota|quota[_-]?(?:exceeded|exhausted|limit|reached)|exceeded[_\s]+(?:your[_\s]+)?(?:current[_\s]+)?quota|exhausted[_\s]+(?:its[_\s]+|your[_\s]+|the[_\s]+)?quota|usage[_\s]?limit|plan[_\s]?limit|subscription[_\s]?(?:limit|quota)|out[_\s]+of[_\s]+(?:quota|credit|balance)|credit[_\s]+balance|insufficient[_\s]+(?:credit|credits|funds|balance)|payment[_\s]+required|hard[_\s]+limit|(?:hard[_\s-]?limit|usage[_\s-]?limit|payment[_\s]+required)[^\n]{0,80}?billing|billing[^\n]{0,80}?(?:hard[_\s-]?limit|usage[_\s-]?limit|exceeded|exhausted|insufficient|depleted|quota|payment[_\s]+required|limit[_\s-]?reached)|(?:配额|额度|订阅额度|订阅限额)[^\n]{0,24}?(?:用尽|耗尽|不足|已用完|用罄))`)

// IsNonRetryableQuotaResponse 报告某提供方响应是否是硬性
// 配额/计费/订阅限额，而非短暂限流。这类
// 响应永远不会成功，因此必须立即原样交回调用方，
// 而不是重试等待。
// 只考虑 429 和 402；body 应为响应载荷的
// 有界前缀。
func IsNonRetryableQuotaResponse(status int, body []byte) bool {
	if status != http.StatusTooManyRequests && status != http.StatusPaymentRequired {
		return false
	}
	if len(body) == 0 {
		return false
	}
	return nonRetryableQuotaRE.MatchString(RedactText(string(body)))
}

// bodyWithPrefix 把窥视到的前缀放回响应体之前。
// 保留原始的 Closer，使关闭仍能释放连接。
// 这使分类后调用方仍能完整读取载荷。
func bodyWithPrefix(prefix []byte, original io.ReadCloser) io.ReadCloser {
	return struct {
		io.Reader
		io.Closer
	}{
		Reader: io.MultiReader(bytes.NewReader(prefix), original),
		Closer: original,
	}
}

// maxRetrySeconds 是能乘以 time.Duration 而不溢出 int64 的最大
// 整秒数。超过它的 Retry-After 会饱和
// （与 parseRetryAfterMS 一样），使恶意头部无法回绕为
// 负数：负值提示会绕过 Do 的 "delay > ceiling" 检查，
// 被当作"无提示"，从而重放本应拒绝的响应。
const maxRetrySeconds = int64(^uint64(0)>>1) / int64(time.Second)

// ParseRetryAfter 读取 Retry-After 头部值，可以是增量秒数或
// HTTP 日期。头部缺失或无法解析时返回零。超过 time.Duration
// 所能表示的整秒值会饱和到
// 最大时长，而不会溢出为负的延迟。
func ParseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		if int64(seconds) > maxRetrySeconds {
			return time.Duration(^uint64(0) >> 1)
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if d := time.Until(when); d > 0 {
			return d
		}
	}
	return 0
}

// retryHeaders 是从提供方响应头部提取的重试决策。
// Anthropic 等提供方和带重试的网关发送这些头部，以
// 覆盖状态码启发式并请求精确的等待时间。
type retryHeaders struct {
	// shouldRetrySet 报告 x-should-retry 是否带有可识别的判定。
	// 设置时，shouldRetry 覆盖状态码决策。
	shouldRetrySet bool
	shouldRetry    bool
	// delay 是请求的等待时间：retry-after-ms（毫秒）优先于
	// Retry-After（增量秒数或 HTTP 日期）。零表示无可用提示。
	delay time.Duration
}

// parseRetryHeaders 从提供方响应中读取重试决策。
// x-should-retry 的 true/false 直接决定是否重试；延迟优先取
// 毫秒提示，否则回退到 Retry-After。
func parseRetryHeaders(h http.Header) retryHeaders {
	var out retryHeaders
	switch strings.ToLower(strings.TrimSpace(h.Get("X-Should-Retry"))) {
	case "true":
		out.shouldRetrySet, out.shouldRetry = true, true
	case "false":
		out.shouldRetrySet, out.shouldRetry = true, false
	}
	if ms, ok := parseRetryAfterMS(h.Get("Retry-After-Ms")); ok {
		out.delay = ms
	} else {
		out.delay = ParseRetryAfter(h.Get("Retry-After"))
	}
	return out
}

// maxRetryMilliseconds 是能乘以 time.Millisecond 转换成 time.Duration
// 而不溢出的最大毫秒值。
const maxRetryMilliseconds = int64(^uint64(0)>>1) / int64(time.Millisecond)

// parseRetryAfterMS 解析 retry-after-ms 提示。布尔值区分
// 存在的值（即使是零）与缺失或格式错误的值，使调用方可以
// 回退到 Retry-After。超出范围的正值会饱和而非
// 溢出：它仍是显式的、超上限的提示，Do 必须拒绝。
func parseRetryAfterMS(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	ms, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) && !strings.HasPrefix(value, "-") {
			return time.Duration(^uint64(0) >> 1), true
		}
		return 0, false
	}
	if ms < 0 {
		return 0, false
	}
	if ms > maxRetryMilliseconds {
		return time.Duration(^uint64(0) >> 1), true
	}
	return time.Duration(ms) * time.Millisecond, true
}

// providerError 把非 2xx 响应转换为带状态码和
// 有界、脱敏响应体片段的错误。凭据形态的值绝不会进入
// 日志或尝试记录。
func providerError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	message := RedactText(strings.TrimSpace(string(body)))
	if message == "" {
		return fmt.Errorf("provider returned %s", resp.Status)
	}
	return fmt.Errorf("provider returned %s: %s", resp.Status, message)
}

func drainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 64*1024))
	_ = body.Close()
}
