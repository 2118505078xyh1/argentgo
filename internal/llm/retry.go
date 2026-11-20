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

// Defaults for RetryPolicy. A retryable failure is waited out with
// exponential backoff and a provider Retry-After header wins when larger.
const (
	DefaultMaxAttempts = 3
	DefaultBaseDelay   = 500 * time.Millisecond
	DefaultMaxDelay    = 20 * time.Second

	// MaxRetryAfter bounds a provider-supplied delay: a hostile or buggy
	// Retry-After must not stall a call for hours.
	MaxRetryAfter = 15 * time.Minute
	// MaxTimeoutRetries caps timeouts well below the general attempt budget.
	// A timed-out attempt already waited before it failed.
	MaxTimeoutRetries = 3
	// UnreachableWindow is the separate retry window for failures that happen
	// before a network path to the host exists. They do not spend MaxAttempts.
	UnreachableWindow = time.Hour
	// UnreachableMaxDelay caps the gap between retries in that window.
	UnreachableMaxDelay = 30 * time.Second

	// retryJitterRatio spreads retries over the lower quarter of the computed
	// backoff window, so sessions hitting the same rate-limited provider do not
	// all wake up at the same instant.
	retryJitterRatio = 0.25
)

// Reason is the retry class of a failed attempt. It is reported on
// RetryAttempt so the session layer can explain a retry without bodies.
type Reason string

const (
	ReasonNone        Reason = ""            // not retryable (cancellation)
	ReasonStatus      Reason = "status"      // retryable HTTP status / Retry-After
	ReasonNotSent     Reason = "not_sent"    // transport failed before a response
	ReasonUnreachable Reason = "unreachable" // no DNS answer or no route to host
	ReasonTimeout     Reason = "timeout"     // deadline or i/o timeout
)

// ClassifyRetryError maps a transport failure onto a retry class. DNS errors
// and a missing route are "unreachable"; deadlines and net.Error timeouts are
// "timeout"; anything else that failed before a response is "not_sent".
// Cancellation is ReasonNone because the caller asked to stop. An explicit
// context-window overflow is also ReasonNone: retrying the same oversized
// request cannot succeed, so the caller must compact or shorten it first.
func ClassifyRetryError(err error) Reason {
	if err == nil || errors.Is(err, context.Canceled) {
		return ReasonNone
	}
	// A context-window overflow cannot clear on its own: replaying the same
	// oversized request fails again. The caller must compact/shorten it first,
	// so report it as non-retryable instead of a transport "not_sent".
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
	// Text check is last so an authoritative transport class above still wins.
	// Plain provider errors (no sentinel) carry the overflow wording and land
	// here; they must not be reported as a retryable "not_sent".
	if contextOverflowRE.MatchString(RedactError(err)) {
		return ReasonNone
	}
	return ReasonNotSent
}

// isUnreachableNetworkError recognizes the platform errors behind EHOSTUNREACH
// and ENETDOWN without depending on OS-specific syscall constants.
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

// RetryPolicy retries transient provider failures (429 and 5xx) and transport
// failures. Statuses and ordinary transport errors spend MaxAttempts; timeouts
// are capped separately and unreachable hosts get their own longer window.
//
// Sleep is overridable so tests can run without waiting; nil means a real
// context-aware timer wait.
type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	Sleep       func(ctx context.Context, d time.Duration) error
	// Jitter returns a value in [0, 1). It is overridable so tests can inject a
	// deterministic source; nil means math/rand/v2.Float64. Jitter is applied
	// only to exponential backoff, never to an explicit provider Retry-After.
	Jitter func() float64
	// OnRetry, when set, receives redacted retry decisions. The session layer
	// can turn these into retry attempt records without seeing response bodies.
	OnRetry func(RetryAttempt)
}

// DefaultRetryPolicy returns the policy used by the protocol clients.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts: DefaultMaxAttempts,
		BaseDelay:   DefaultBaseDelay,
		MaxDelay:    DefaultMaxDelay,
	}
}

// IsZero reports whether no field was set, so clients can fall back to
// DefaultRetryPolicy. RetryPolicy holds a func and cannot be compared.
func (p RetryPolicy) IsZero() bool {
	return p.MaxAttempts == 0 && p.BaseDelay == 0 && p.MaxDelay == 0 && p.Sleep == nil
}

// Do sends a request and retries transient failures.
//
// send must build a fresh request on every call (streaming bodies cannot be
// replayed). It returns the last response when the attempt budget runs out on
// a retryable status, so the caller can report the provider's body; only
// transport failures and context cancellation surface as errors.
func (p RetryPolicy) Do(ctx context.Context, send func(context.Context) (*http.Response, error)) (*http.Response, error) {
	p = p.withDefaults()

	var (
		lastErr          error
		normalAttempts   int // attempts spent from the MaxAttempts budget
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
			// not_sent and timeout failures spend the general budget; an
			// unreachable failure above deliberately did not.
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
		// x-should-retry is authoritative when the provider sent a verdict: a
		// gateway can mark a 5xx terminal, or ask for a retry on a status our
		// default set does not cover. Absent or unrecognised values defer to
		// the status code as before.
		if hints.shouldRetrySet {
			shouldRetry = hints.shouldRetry
		}
		if !shouldRetry || normalAttempts >= p.MaxAttempts {
			return resp, nil
		}
		// Peek at a bounded prefix to tell a hard provider limit from a
		// transient rate limit. The prefix is put back so the caller still gets
		// a complete body; only the retry path below drains it.
		if resp.Body != nil {
			prefix, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body = bodyWithPrefix(prefix, resp.Body)
			// A terminal provider outcome (session/spend budget, usage
			// accounting failure, hook denial, ...) cannot be settled by a
			// retry, so return the response with its body intact as well.
			if IsNonRetryableQuotaResponse(resp.StatusCode, prefix) || IsTerminalProviderOutcome(prefix, resp.StatusCode) {
				return resp, nil
			}
		}
		// A provider hint longer than the ceiling is declined, not shortened:
		// waiting a capped fraction of the requested window would only hit the
		// same block again and burn an attempt. Surface the original response
		// intact so the caller's own recovery can decide.
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

// Delay is the wait before the next attempt (attempt is 1-based, so attempt 2
// follows the first retry). Any explicit provider Retry-After is authoritative:
// the server asks for a minimum wait, so it is never reduced by jitter. It is
// bounded by both 3x MaxDelay and MaxRetryAfter. Without Retry-After,
// exponential backoff is jittered downwards by up to retryJitterRatio so
// concurrent sessions do not wake in lockstep.
func (p RetryPolicy) Delay(attempt int, retryAfter time.Duration) time.Duration {
	p = p.withDefaults()
	base := p.baseDelay(attempt, retryAfter)
	// A positive Retry-After is authoritative even when it is smaller than the
	// exponential backoff: base already waits at least that long, and jittering
	// it could fall below the provider's requested minimum.
	if retryAfter > 0 {
		return base
	}
	return time.Duration(float64(base) * (1 - retryJitterRatio*p.Jitter()))
}

// baseDelay computes the exact wait without jitter: exponential backoff capped
// at MaxDelay, overridden by Retry-After clamped to both 3x MaxDelay and
// MaxRetryAfter when the provider asks for a longer wait.
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

// delayCeiling is the longest provider-requested delay the retry loop will
// honour. A longer hint is declined by Do (the response is surfaced intact)
// rather than silently shortened. It is the same cap baseDelay uses when it
// clamps a delay for callers of Delay directly.
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

// unreachableDelay grows from 2s to UnreachableMaxDelay for the long window.
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

// IsRetryableStatus reports whether a status is worth retrying: 429 for rate
// limiting and 5xx for the provider's own failure. 408/409/425 and the
// Cloudflare gateway codes are included because providers return them under
// load.
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

// nonRetryableQuotaRE matches provider error payloads that describe a hard
// account limit (quota, billing, subscription/plan) rather than a transient
// rate limit. Compiled once at package scope; matching happens on redacted
// text so credential-shaped values cannot influence the decision.
//
// The quota/billing words are deliberately NOT bare: "请求过于频繁，配额将在
// 30 秒后重置" and "check your billing settings" describe a rate limit that a
// retry can clear. Chinese quota mentions require an exhaustion phrase nearby,
// and "billing" requires a hard-limit/usage phrase nearby; explicit error codes
// such as insufficient_quota and quota_exceeded stay sufficient on their own.
var nonRetryableQuotaRE = regexp.MustCompile(`(?i)(insufficient[_-]?quota|quota[_-]?(?:exceeded|exhausted|limit|reached)|exceeded[_\s]+(?:your[_\s]+)?(?:current[_\s]+)?quota|exhausted[_\s]+(?:its[_\s]+|your[_\s]+|the[_\s]+)?quota|usage[_\s]?limit|plan[_\s]?limit|subscription[_\s]?(?:limit|quota)|out[_\s]+of[_\s]+(?:quota|credit|balance)|credit[_\s]+balance|insufficient[_\s]+(?:credit|credits|funds|balance)|payment[_\s]+required|hard[_\s]+limit|(?:hard[_\s-]?limit|usage[_\s-]?limit|payment[_\s]+required)[^\n]{0,80}?billing|billing[^\n]{0,80}?(?:hard[_\s-]?limit|usage[_\s-]?limit|exceeded|exhausted|insufficient|depleted|quota|payment[_\s]+required|limit[_\s-]?reached)|(?:配额|额度|订阅额度|订阅限额)[^\n]{0,24}?(?:用尽|耗尽|不足|已用完|用罄))`)

// IsNonRetryableQuotaResponse reports whether a provider response is a hard
// quota/billing/subscription limit rather than a transient rate limit. Such a
// response will never start succeeding, so it must be handed back to the caller
// immediately with its body intact instead of being retried and waited out.
// Only 429 and 402 are considered; body is expected to be a bounded prefix of
// the response payload.
func IsNonRetryableQuotaResponse(status int, body []byte) bool {
	if status != http.StatusTooManyRequests && status != http.StatusPaymentRequired {
		return false
	}
	if len(body) == 0 {
		return false
	}
	return nonRetryableQuotaRE.MatchString(RedactText(string(body)))
}

// bodyWithPrefix puts a peeked prefix back in front of a response body. The
// original Closer is retained so closing still releases the connection. This
// keeps the payload fully readable by the caller after classification.
func bodyWithPrefix(prefix []byte, original io.ReadCloser) io.ReadCloser {
	return struct {
		io.Reader
		io.Closer
	}{
		Reader: io.MultiReader(bytes.NewReader(prefix), original),
		Closer: original,
	}
}

// ParseRetryAfter reads a Retry-After header value, either delta-seconds or an
// HTTP date. It returns zero when the header is absent or unparseable.
func ParseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
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

// retryHeaders is the retry decision extracted from a provider response's
// headers. Providers such as Anthropic and retrying gateways send these to
// override the status-code heuristic and to request an exact wait.
type retryHeaders struct {
	// shouldRetrySet reports whether x-should-retry held a recognised verdict.
	// When set, shouldRetry overrides the status-code decision.
	shouldRetrySet bool
	shouldRetry    bool
	// delay is the requested wait: retry-after-ms (milliseconds) wins over
	// Retry-After (delta-seconds or HTTP date). Zero means no usable hint.
	delay time.Duration
}

// parseRetryHeaders reads the retry decision from a provider response.
// x-should-retry true/false decides retry outright; the delay prefers the
// millisecond hint and falls back to Retry-After.
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

// maxRetryMilliseconds is the largest millisecond value that can be turned
// into a time.Duration without overflowing when multiplied by time.Millisecond.
const maxRetryMilliseconds = int64(^uint64(0)>>1) / int64(time.Millisecond)

// parseRetryAfterMS parses a retry-after-ms hint. The bool distinguishes a
// present (even zero) value from an absent or malformed one so the caller can
// fall back to Retry-After. An out-of-range positive value saturates instead of
// overflowing: it is still an explicit, over-cap hint that Do must decline.
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

// providerError turns a non-2xx response into an error carrying the status and
// a bounded, redacted slice of the body. Credential-shaped values never reach
// logs or attempt records.
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
