package llm

import (
	"net/http"
	"testing"
	"time"
)

func TestParseRetryAfterMS(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want time.Duration
		ok   bool
	}{
		{"absent", "", 0, false},
		{"whitespace only", "  ", 0, false},
		{"milliseconds", "700", 700 * time.Millisecond, true},
		{"trimmed", " 1500 ", 1500 * time.Millisecond, true},
		{"explicit zero is present", "0", 0, true},
		{"negative is invalid", "-5", 0, false},
		{"non numeric is invalid", "soon", 0, false},
		{"decimal is invalid", "1.5", 0, false},
		{"out of range saturates", "99999999999999999999", time.Duration(^uint64(0) >> 1), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseRetryAfterMS(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("parseRetryAfterMS(%q) = (%v, %v), want (%v, %v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestParseRetryHeaders(t *testing.T) {
	cases := []struct {
		name    string
		headers http.Header
		set     bool
		retry   bool
		delay   time.Duration
	}{
		{
			name:    "no headers",
			headers: http.Header{},
		},
		{
			name:    "should retry true",
			headers: http.Header{"X-Should-Retry": []string{"true"}},
			set:     true,
			retry:   true,
		},
		{
			name:    "should retry false case insensitive",
			headers: http.Header{"X-Should-Retry": []string{" FALSE "}},
			set:     true,
		},
		{
			name:    "unrecognised verdict is not authoritative",
			headers: http.Header{"X-Should-Retry": []string{"maybe"}},
		},
		{
			name:    "retry after seconds",
			headers: http.Header{"Retry-After": []string{"2"}},
			delay:   2 * time.Second,
		},
		{
			name: "retry after ms wins over seconds",
			headers: http.Header{
				"Retry-After-Ms": []string{"700"},
				"Retry-After":    []string{"12"},
			},
			delay: 700 * time.Millisecond,
		},
		{
			name: "malformed ms falls back to retry after",
			headers: http.Header{
				"Retry-After-Ms": []string{"nope"},
				"Retry-After":    []string{"3"},
			},
			delay: 3 * time.Second,
		},
		{
			name: "zero ms does not fall back to retry after",
			headers: http.Header{
				"Retry-After-Ms": []string{"0"},
				"Retry-After":    []string{"9"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseRetryHeaders(tc.headers)
			if got.shouldRetrySet != tc.set || got.shouldRetry != tc.retry || got.delay != tc.delay {
				t.Fatalf("parseRetryHeaders(%v) = %+v, want set=%v retry=%v delay=%v",
					tc.headers, got, tc.set, tc.retry, tc.delay)
			}
		})
	}
}
