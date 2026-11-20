package mcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// defaultDiscoveryTimeoutStdio bounds discovery against local stdio
	// servers, which may need time to boot their runtime before answering
	// initialize / tools-list.
	defaultDiscoveryTimeoutStdio = 30 * time.Second
	// defaultDiscoveryTimeoutRemote bounds discovery against HTTP/SSE servers;
	// a remote that cannot answer within this window is treated as unavailable.
	defaultDiscoveryTimeoutRemote = 5 * time.Second
	// minDiscoveryTimeout and maxDiscoveryTimeout bound any per-server
	// discovery timeout override so a bad config cannot disable or stall
	// discovery.
	minDiscoveryTimeout = time.Second
	maxDiscoveryTimeout = 2 * time.Minute
)

// DiscoveryTimeouts is the graded discovery-timeout policy. A zero-valued
// field falls back to its package default, so callers may override only the
// parts they care about.
type DiscoveryTimeouts struct {
	Stdio  time.Duration
	Remote time.Duration
	Min    time.Duration
	Max    time.Duration
}

// DefaultDiscoveryTimeouts returns the shipped policy: 30s for stdio, 5s for
// remote, with per-server overrides clamped to [1s, 2m].
func DefaultDiscoveryTimeouts() DiscoveryTimeouts {
	return DiscoveryTimeouts{
		Stdio:  defaultDiscoveryTimeoutStdio,
		Remote: defaultDiscoveryTimeoutRemote,
		Min:    minDiscoveryTimeout,
		Max:    maxDiscoveryTimeout,
	}
}

// clamp bounds an explicit per-server override. A non-positive override means
// "no override" and is returned unchanged so the graded default applies.
func (d DiscoveryTimeouts) clamp(override time.Duration) time.Duration {
	if override <= 0 {
		return 0
	}
	min, max := d.Min, d.Max
	if min <= 0 {
		min = minDiscoveryTimeout
	}
	if max <= 0 {
		max = maxDiscoveryTimeout
	}
	if max < min {
		max = min
	}
	if override < min {
		return min
	}
	if override > max {
		return max
	}
	return override
}

// Effective returns the discovery timeout for transport (a value accepted by
// Spec.transportKind) given an optional per-server override, which is clamped
// to the configured bounds.
func (d DiscoveryTimeouts) Effective(transport string, override time.Duration) time.Duration {
	if clamped := d.clamp(override); clamped > 0 {
		return clamped
	}
	stdio, remote := d.Stdio, d.Remote
	if stdio <= 0 {
		stdio = defaultDiscoveryTimeoutStdio
	}
	if remote <= 0 {
		remote = defaultDiscoveryTimeoutRemote
	}
	if strings.EqualFold(strings.TrimSpace(transport), "stdio") {
		return stdio
	}
	return remote
}

// transportKind mirrors the inference used by Connect: an explicit transport
// wins, otherwise a command means stdio and a URL means remote.
func (s Spec) transportKind() string {
	kind := strings.ToLower(strings.TrimSpace(s.Transport))
	if kind != "" {
		return kind
	}
	if strings.TrimSpace(s.Command) != "" {
		return "stdio"
	}
	return "http"
}

// EffectiveDiscoveryTimeout returns the discovery timeout for this server:
// the clamped per-server DiscoveryTimeout override when set, otherwise the
// graded default selected by transport kind (30s stdio / 5s remote).
//
// It is deliberately independent of Spec.Timeout, which keeps its original
// meaning as a fallback bound for ordinary requests.
func (s Spec) EffectiveDiscoveryTimeout() time.Duration {
	return DefaultDiscoveryTimeouts().Effective(s.transportKind(), s.DiscoveryTimeout)
}

// HTTPStatusError carries an HTTP response status code so the retry policy
// can classify a failure without parsing error strings. Transports should wrap
// non-2xx responses in this type when they adopt the retry policy.
type HTTPStatusError struct {
	StatusCode int
	Message    string
}

func (e *HTTPStatusError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Message)
}

// IsRetryable reports whether err is a transient failure worth backing off and
// retrying. Only a narrow allowlist is retryable:
//
//   - network resets and timeouts (ECONNRESET, ETIMEDOUT, net timeout errors);
//   - HTTP 502, 503 and 504.
//
// Everything else is permanent and must fail immediately. In particular 401,
// 403 and JSON-RPC method-not-found (-32601) are never retryable.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	// Permanent JSON-RPC errors win over any other signal in the chain.
	var rpcErr *rpcErrorBody
	if errors.As(err, &rpcErr) && rpcErr.Code == -32601 {
		return false
	}
	// HTTP status: the typed error first, then the "HTTP %d" form emitted by
	// the current HTTP transport so the policy is useful before transports
	// adopt the typed error.
	if status := httpStatusFrom(err); status > 0 {
		return status == 502 || status == 503 || status == 504
	}
	// Network-level transient conditions.
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ETIMEDOUT) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// httpStatusFrom extracts an HTTP status code from err, either from a typed
// HTTPStatusError or from the "HTTP <code>" text the HTTP transport emits.
// It returns 0 when no status code is present.
func httpStatusFrom(err error) int {
	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) {
		return statusErr.StatusCode
	}
	for cur := err; cur != nil; cur = errors.Unwrap(cur) {
		const prefix = "HTTP "
		text := cur.Error()
		if !strings.HasPrefix(text, prefix) {
			continue
		}
		rest := text[len(prefix):]
		if i := strings.IndexAny(rest, " :"); i >= 0 {
			rest = rest[:i]
		}
		if code, convErr := strconv.Atoi(rest); convErr == nil {
			return code
		}
	}
	return 0
}
