package auth

import "testing"

func TestTokenStatus(t *testing.T) {
	const now = int64(1_800_000_000)
	cases := []struct {
		name    string
		expires int64
		margin  int64
		want    TokenState
	}{
		{"zero sentinel is unknown", 0, 60, TokenUnknown},
		{"negative sentinel is unknown", -1, 60, TokenUnknown},
		{"far future is valid", now + 3600, 60, TokenValid},
		{"far future with zero margin is valid", now + 3600, 0, TokenValid},
		{"just over margin is valid", now + 61, 60, TokenValid},
		{"exactly the margin is expiring", now + 60, 60, TokenExpiring},
		{"inside the margin is expiring", now + 1, 60, TokenExpiring},
		{"expires now is expired", now, 60, TokenExpired},
		{"already past is expired", now - 1, 60, TokenExpired},
		{"zero margin leaves no expiring band", now + 1, 0, TokenValid},
		{"negative margin behaves like zero", now + 1, -5, TokenValid},
		{"large margin covers the deadline", now + 300, 300, TokenExpiring},
	}
	for _, test := range cases {
		if got := TokenStatus(test.expires, now, test.margin); got != test.want {
			t.Fatalf("%s: TokenStatus(%d, %d, %d) = %q, want %q",
				test.name, test.expires, now, test.margin, got, test.want)
		}
	}
}

func TestTokenStatusOverflowStaysValid(t *testing.T) {
	// expiresAt-now would wrap negative; the deadline is effectively infinite,
	// so it must not be classified as expired or expiring.
	got := TokenStatus(int64(1<<63-1), int64(-1<<63), 60)
	if got != TokenValid {
		t.Fatalf("TokenStatus(MaxInt64, MinInt64, 60) = %q, want valid", got)
	}
}

func TestTokenStatusUsesNowArgument(t *testing.T) {
	// The same deadline moves from valid to expiring to expired as now advances.
	expires := int64(1_800_000_600)
	if got := TokenStatus(expires, 1_800_000_000, 60); got != TokenValid {
		t.Fatalf("early now = %q, want valid", got)
	}
	if got := TokenStatus(expires, 1_800_000_541, 60); got != TokenExpiring {
		t.Fatalf("near now = %q, want expiring", got)
	}
	if got := TokenStatus(expires, 1_800_000_600, 60); got != TokenExpired {
		t.Fatalf("at deadline = %q, want expired", got)
	}
}

func TestTokenStateString(t *testing.T) {
	if got := TokenExpiring.String(); got != "expiring" {
		t.Fatalf("String = %q, want expiring", got)
	}
}

func TestShouldRefreshOnError(t *testing.T) {
	cases := []struct {
		name string
		code int
		body string
		want bool
	}{
		{"401 always refreshes", 401, `{"error":"server error"}`, true},
		{"401 with empty body refreshes", 401, "", true},
		{"403 never refreshes on its own", 403, `{"error":"forbidden"}`, false},
		{"400 does not refresh", 400, `{"error":"bad request"}`, false},
		{"429 does not refresh", 429, `{"error":"rate limit exceeded"}`, false},
		{"500 does not refresh", 500, `{"error":"internal error"}`, false},
		{"200 does not refresh", 200, "", false},
		{"unknown code with invalid_token refreshes", 0, `{"error":"invalid_token"}`, true},
		{"expired_token refreshes", 0, `{"error":"expired_token"}`, true},
		{"token has expired refreshes", 0, "token has expired", true},
		{"invalid access token refreshes", 0, "invalid access token", true},
		{"separator variant refreshes", 0, "invalid-token", true},
		{"uppercase marker refreshes", 0, "INVALID_TOKEN", true},
		{"CJK expired token refreshes", 0, "令牌已过期", true},
		{"CJK invalid token refreshes", 0, "无效的令牌", true},
		{"CJK token invalid refreshes", 0, "令牌无效", true},
		{"plain text token expiration refreshes", 0, "token 已过期", true},
		{"unrelated text does not refresh", 0, "rate limit exceeded", false},
		{"empty body does not refresh", 0, "", false},
		{"non-token auth wording does not refresh", 0, "invalid api key", false},
		{"403 with token marker refreshes on body evidence", 403, "invalid_token", true},
	}
	for _, test := range cases {
		if got := ShouldRefreshOnError(test.code, test.body); got != test.want {
			t.Fatalf("%s: ShouldRefreshOnError(%d, %q) = %v, want %v",
				test.name, test.code, test.body, got, test.want)
		}
	}
}
