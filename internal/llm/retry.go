package llm

import (
	"context"
	"fmt"
	"io"
	"net/http"
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
)

// RetryPolicy retries transient provider failures (429 and 5xx).
//
// Sleep is overridable so tests can run without waiting; nil means a real
// context-aware timer wait.
type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	Sleep       func(ctx context.Context, d time.Duration) error
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

// Do sends a request and retries rate limits and server errors.
//
// send must build a fresh request on every call (streaming bodies cannot be
// replayed). It returns the last response when the attempt budget runs out on
// a retryable status, so the caller can report the provider's body; only
// transport failures and context cancellation surface as errors.
func (p RetryPolicy) Do(ctx context.Context, send func(context.Context) (*http.Response, error)) (*http.Response, error) {
	p = p.withDefaults()

	var lastErr error
	for attempt := 1; attempt <= p.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		resp, err := send(ctx)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if err != nil {
			lastErr = err
			if attempt == p.MaxAttempts {
				break
			}
			if waitErr := p.wait(ctx, attempt, 0); waitErr != nil {
				return nil, waitErr
			}
			continue
		}
		if !IsRetryableStatus(resp.StatusCode) || attempt == p.MaxAttempts {
			return resp, nil
		}
		retryAfter := ParseRetryAfter(resp.Header.Get("Retry-After"))
		if p.OnRetry != nil {
			p.OnRetry(RetryAttempt{
				Attempt:    attempt,
				StatusCode: resp.StatusCode,
				Cause:      clipErrorClass(RedactText(resp.Status)),
			})
		}
		drainAndClose(resp.Body)
		if waitErr := p.wait(ctx, attempt, retryAfter); waitErr != nil {
			return nil, waitErr
		}
		continue
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no attempt was made")
	}
	return nil, fmt.Errorf("request failed after %d attempts: %w", p.MaxAttempts, lastErr)
}

// Delay is the wait before the next attempt (attempt is 1-based, so attempt 2
// follows the first retry). A provider Retry-After wins when it is larger.
func (p RetryPolicy) Delay(attempt int, retryAfter time.Duration) time.Duration {
	p = p.withDefaults()
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
		ceiling := p.MaxDelay * 3
		if ceiling <= 0 {
			ceiling = DefaultMaxDelay * 3
		}
		delay = retryAfter
		if delay > ceiling {
			delay = ceiling
		}
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
	return p
}

func (p RetryPolicy) wait(ctx context.Context, attempt int, retryAfter time.Duration) error {
	delay := p.Delay(attempt, retryAfter)
	if delay <= 0 {
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
