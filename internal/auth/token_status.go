package auth

import (
	"net/http"
	"strings"
)

// TokenState describes how usable a token deadline is, independent of any
// store or clock. It is the pure counterpart of Status: TokenStatus works on
// raw Unix seconds so callers can classify without loading a credential file
// and can choose their own refresh margin.
type TokenState string

// TokenState values reported by TokenStatus.
const (
	TokenValid    TokenState = "valid"
	TokenExpiring TokenState = "expiring"
	TokenExpired  TokenState = "expired"
	// TokenUnknown means no deadline is known (the on-disk zero sentinel).
	TokenUnknown TokenState = "unknown"
)

// String implements fmt.Stringer.
func (s TokenState) String() string { return string(s) }

// TokenStatus classifies an access-token deadline. expiresAt and now are Unix
// seconds; refreshMargin is the number of seconds before expiry at which a
// still-valid token is considered close enough to warrant a proactive refresh.
//
// A non-positive expiresAt is the "no expiry" sentinel and yields TokenUnknown.
// A negative refreshMargin is treated as zero (refresh only once expired). The
// comparison is overflow-safe: deadlines far outside the int64 subtraction
// range are reported as TokenValid rather than wrapping into the past.
func TokenStatus(expiresAt, now, refreshMargin int64) TokenState {
	if expiresAt <= 0 {
		return TokenUnknown
	}
	if expiresAt <= now {
		return TokenExpired
	}
	// expiresAt > here, so a non-positive difference means expiresAt-now
	// wrapped: the true deadline is effectively infinite and still healthy.
	left := expiresAt - now
	if left <= 0 {
		return TokenValid
	}
	if refreshMargin < 0 {
		refreshMargin = 0
	}
	if left <= refreshMargin {
		return TokenExpiring
	}
	return TokenValid
}

// tokenRefreshMarkers are the token-invalidity phrases that make a refresh
// worthwhile. Deliberately narrower than the general auth-error classification
// in internal/llm: a 403 or an invalid API key cannot be fixed by refreshing.
// Matching is case-insensitive; CJK markers are unaffected by lowering.
var tokenRefreshMarkers = []string{
	"invalid_token", "invalid token", "invalid-token",
	"expired_token", "expired token", "expired-token",
	"token_expired", "token expired", "token has expired",
	"invalid access token", "access token expired",
	"令牌已过期", "令牌过期", "token已过期", "token 已过期",
	"无效令牌", "无效的令牌", "令牌无效",
}

// ShouldRefreshOnError reports whether a provider failure is a token-expiry
// signal worth refreshing for. Only an explicit 401 or an invalid/expired-token
// message qualifies; every other status or message returns false so transient
// or permission errors never trigger a needless refresh. The decision is a pure
// function of its two arguments and visible to callers (transparent).
func ShouldRefreshOnError(statusCode int, errBody string) bool {
	if statusCode == http.StatusUnauthorized {
		return true
	}
	lower := strings.ToLower(errBody)
	for _, marker := range tokenRefreshMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
