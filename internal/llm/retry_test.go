package llm_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func TestIsRetryableStatus(t *testing.T) {
	cases := map[int]bool{
		200: false,
		400: false,
		401: false,
		403: false,
		404: false,
		408: true,
		409: true,
		422: false,
		425: true,
		429: true,
		500: true,
		501: false,
		502: true,
		503: true,
		504: true,
		529: true,
	}
	for status, want := range cases {
		if got := llm.IsRetryableStatus(status); got != want {
			t.Fatalf("IsRetryableStatus(%d) = %v, want %v", status, got, want)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		value string
		want  time.Duration
	}{
		{"", 0},
		{"7", 7 * time.Second},
		{" 12 ", 12 * time.Second},
		{"0", 0},
		{"-3", 0},
		{"Wed, 21 Oct 2015 07:28:00 GMT", 0}, // in the past
		{"not a date", 0},
	}
	for _, tc := range cases {
		if got := llm.ParseRetryAfter(tc.value); got != tc.want {
			t.Fatalf("ParseRetryAfter(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
	if got := llm.ParseRetryAfter("Wed, 21 Oct 2037 07:28:00 GMT"); got <= 0 {
		t.Fatalf("future HTTP date = %v, want positive", got)
	}
}

func TestRetryPolicyDelayGrowsAndCaps(t *testing.T) {
	// Jitter returning 0 makes the jitter factor 1, keeping the exact values.
	policy := llm.RetryPolicy{
		BaseDelay: 100 * time.Millisecond,
		MaxDelay:  500 * time.Millisecond,
		Jitter:    func() float64 { return 0 },
	}

	if got := policy.Delay(1, 0); got != 100*time.Millisecond {
		t.Fatalf("delay(1) = %v", got)
	}
	if got := policy.Delay(2, 0); got != 200*time.Millisecond {
		t.Fatalf("delay(2) = %v", got)
	}
	if got := policy.Delay(3, 0); got != 400*time.Millisecond {
		t.Fatalf("delay(3) = %v", got)
	}
	if got := policy.Delay(9, 0); got != 500*time.Millisecond {
		t.Fatalf("delay(9) = %v, want cap", got)
	}
	if got := policy.Delay(1, time.Second); got != time.Second {
		t.Fatalf("retry-after should win, got %v", got)
	}
	if got := policy.Delay(1, 10*time.Second); got != 1500*time.Millisecond {
		t.Fatalf("retry-after should clamp to 3x max delay, got %v", got)
	}
}

func TestRetryPolicyDelayJitter(t *testing.T) {
	// Deterministic jitter source: [0, 0.5, 0.999] stays pinned at the last
	// value so a Retry-After test still sees the strongest possible jitter.
	seq := []float64{0, 0.5, 0.999}
	next := 0
	policy := llm.RetryPolicy{
		BaseDelay: 100 * time.Millisecond,
		MaxDelay:  500 * time.Millisecond,
		Jitter: func() float64 {
			v := seq[next]
			if next < len(seq)-1 {
				next++
			}
			return v
		},
	}

	cases := []struct {
		attempt int
		base    time.Duration
	}{
		{1, 100 * time.Millisecond},
		{2, 200 * time.Millisecond},
		{3, 400 * time.Millisecond},
	}
	for i, tc := range cases {
		got := policy.Delay(tc.attempt, 0)
		want := time.Duration(float64(tc.base) * (1 - 0.25*seq[i]))
		if got != want {
			t.Fatalf("Delay(%d, 0) = %v, want %v", tc.attempt, got, want)
		}
		lower := time.Duration(float64(tc.base) * 0.75)
		if got < lower || got > tc.base {
			t.Fatalf("Delay(%d, 0) = %v, want within [%v, %v]", tc.attempt, got, lower, tc.base)
		}
	}

	// An explicit Retry-After is authoritative: never jitter it, and keep the
	// 3x MaxDelay ceiling. The jitter source is still at 0.999 here, so an
	// accidental downward jitter would be clearly visible.
	if got := policy.Delay(1, 800*time.Millisecond); got != 800*time.Millisecond {
		t.Fatalf("Retry-After delay = %v, want unjittered 800ms", got)
	}
	if got := policy.Delay(1, 10*time.Second); got != 1500*time.Millisecond {
		t.Fatalf("Retry-After ceiling = %v, want 1500ms", got)
	}
}

func TestRetryPolicyDefaults(t *testing.T) {
	policy := llm.DefaultRetryPolicy()
	if policy.MaxAttempts != llm.DefaultMaxAttempts {
		t.Fatalf("MaxAttempts = %d", policy.MaxAttempts)
	}
	if policy.Delay(1, 0) <= 0 {
		t.Fatal("default delay should be positive")
	}
}

func TestRetryPolicyStopsOnCancellation(t *testing.T) {
	policy := llm.RetryPolicy{MaxAttempts: 5, BaseDelay: 10 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	sends := 0

	_, err := policy.Do(ctx, func(context.Context) (*http.Response, error) {
		sends++
		cancel()
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(http.NoBody)}, nil
	})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if sends != 1 {
		t.Fatalf("sends = %d, want 1", sends)
	}
}

func TestRetryPolicyReturnsLastRetryableResponse(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	policy := llm.RetryPolicy{MaxAttempts: 2, Sleep: noWait}
	resp, err := policy.Do(context.Background(), func(ctx context.Context) (*http.Response, error) {
		return http.Get(server.URL)
	})
	if err != nil {
		t.Fatalf("err = %v, want last response", err)
	}
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("resp = %#v", resp)
	}
	resp.Body.Close()
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestRetryPolicyRetriesTransportErrors(t *testing.T) {
	var attempts int
	policy := llm.RetryPolicy{MaxAttempts: 3, Sleep: noWait}
	_, err := policy.Do(context.Background(), func(context.Context) (*http.Response, error) {
		attempts++
		return nil, io.ErrUnexpectedEOF
	})
	if err == nil || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want wrapped transport error", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

type syntheticTimeoutError struct{}

func (syntheticTimeoutError) Error() string   { return "i/o timeout" }
func (syntheticTimeoutError) Timeout() bool   { return true }
func (syntheticTimeoutError) Temporary() bool { return true }

func TestClassifyRetryError(t *testing.T) {
	dnsErr := &net.DNSError{Err: "no such host", Name: "api.example.com"}
	cases := []struct {
		name string
		err  error
		want llm.Reason
	}{
		{"nil error", nil, llm.ReasonNone},
		{"cancellation", context.Canceled, llm.ReasonNone},
		{"wrapped cancellation", fmt.Errorf("abort: %w", context.Canceled), llm.ReasonNone},
		{"deadline exceeded", context.DeadlineExceeded, llm.ReasonTimeout},
		{"wrapped deadline", fmt.Errorf("read: %w", context.DeadlineExceeded), llm.ReasonTimeout},
		{"net timeout", syntheticTimeoutError{}, llm.ReasonTimeout},
		{"wrapped net timeout", fmt.Errorf("read: %w", syntheticTimeoutError{}), llm.ReasonTimeout},
		{"op error timeout", &net.OpError{Op: "read", Err: syntheticTimeoutError{}}, llm.ReasonTimeout},
		{"dns error", dnsErr, llm.ReasonUnreachable},
		{"wrapped dns error", fmt.Errorf("dial tcp: %w", dnsErr), llm.ReasonUnreachable},
		{"dial wraps dns error", &net.OpError{Op: "dial", Net: "tcp", Err: dnsErr}, llm.ReasonUnreachable},
		{"no route", &net.OpError{Op: "dial", Err: errors.New("connect: network is unreachable")}, llm.ReasonUnreachable},
		{"ordinary transport", io.ErrUnexpectedEOF, llm.ReasonNotSent},
		{"op error refused", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, llm.ReasonNotSent},
		{"context overflow", errors.New("context window exceeded"), llm.ReasonNone},
		{"wrapped context overflow", fmt.Errorf("chat: %w", errors.New("context_length_exceeded")), llm.ReasonNone},
		{"transport sentinel wins over overflow wording", fmt.Errorf("context window exceeded: %w", dnsErr), llm.ReasonUnreachable},
		{"deadline wins over overflow wording", fmt.Errorf("context window exceeded: %w", context.DeadlineExceeded), llm.ReasonTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := llm.ClassifyRetryError(tc.err); got != tc.want {
				t.Fatalf("ClassifyRetryError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestRetryPolicyUnreachableDoesNotSpendAttemptBudget(t *testing.T) {
	const unreachableSends = 5
	var sends int
	var reasons []llm.Reason
	policy := llm.RetryPolicy{
		MaxAttempts: 1, // a single ordinary failure would end the loop
		Sleep:       noWait,
		OnRetry:     func(attempt llm.RetryAttempt) { reasons = append(reasons, attempt.Reason) },
	}
	dnsErr := &net.DNSError{Err: "no such host", Name: "api.example.com"}

	resp, err := policy.Do(context.Background(), func(context.Context) (*http.Response, error) {
		sends++
		if sends <= unreachableSends {
			return nil, fmt.Errorf("dial tcp: %w", dnsErr)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(http.NoBody)}, nil
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp == nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("resp = %#v", resp)
	}
	resp.Body.Close()
	if sends != unreachableSends+1 {
		t.Fatalf("sends = %d, want %d", sends, unreachableSends+1)
	}
	if len(reasons) != unreachableSends {
		t.Fatalf("reasons = %v, want %d unreachable decisions", reasons, unreachableSends)
	}
	for _, reason := range reasons {
		if reason != llm.ReasonUnreachable {
			t.Fatalf("reason = %q, want unreachable", reason)
		}
	}
	if got := llm.UnreachableWindow; got < time.Minute {
		t.Fatalf("UnreachableWindow = %v, want a long window", got)
	}
}

func TestRetryPolicyCapsTimeoutRetries(t *testing.T) {
	var sends int
	var reasons []llm.Reason
	policy := llm.RetryPolicy{
		MaxAttempts: 10, // the timeout cap, not the general budget, must stop us
		Sleep:       noWait,
		OnRetry:     func(attempt llm.RetryAttempt) { reasons = append(reasons, attempt.Reason) },
	}

	_, err := policy.Do(context.Background(), func(context.Context) (*http.Response, error) {
		sends++
		return nil, fmt.Errorf("read: %w", context.DeadlineExceeded)
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want wrapped DeadlineExceeded", err)
	}
	if sends != llm.MaxTimeoutRetries+1 {
		t.Fatalf("sends = %d, want %d", sends, llm.MaxTimeoutRetries+1)
	}
	if len(reasons) != llm.MaxTimeoutRetries {
		t.Fatalf("reasons = %v, want %d timeout decisions", reasons, llm.MaxTimeoutRetries)
	}
	for _, reason := range reasons {
		if reason != llm.ReasonTimeout {
			t.Fatalf("reason = %q, want timeout", reason)
		}
	}
}

func TestRetryAttemptReportsNotSentAndStatusReasons(t *testing.T) {
	var transportReasons, statusReasons []llm.Reason
	transportPolicy := llm.RetryPolicy{
		MaxAttempts: 2,
		Sleep:       noWait,
		OnRetry:     func(a llm.RetryAttempt) { transportReasons = append(transportReasons, a.Reason) },
	}
	sends := 0
	if _, err := transportPolicy.Do(context.Background(), func(context.Context) (*http.Response, error) {
		sends++
		if sends == 1 {
			return nil, io.ErrUnexpectedEOF
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(http.NoBody)}, nil
	}); err != nil {
		t.Fatalf("transport Do: %v", err)
	}
	if len(transportReasons) != 1 || transportReasons[0] != llm.ReasonNotSent {
		t.Fatalf("transport reasons = %v", transportReasons)
	}

	statusPolicy := llm.RetryPolicy{
		MaxAttempts: 2,
		Sleep:       noWait,
		OnRetry:     func(a llm.RetryAttempt) { statusReasons = append(statusReasons, a.Reason) },
	}
	sends = 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends++
		if sends == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	resp, err := statusPolicy.Do(context.Background(), func(ctx context.Context) (*http.Response, error) {
		return http.Get(server.URL)
	})
	if err != nil {
		t.Fatalf("status Do: %v", err)
	}
	resp.Body.Close()
	if len(statusReasons) != 1 || statusReasons[0] != llm.ReasonStatus {
		t.Fatalf("status reasons = %v", statusReasons)
	}
}

func TestDelayClampsRetryAfterToFifteenMinutes(t *testing.T) {
	if llm.MaxRetryAfter != 15*time.Minute {
		t.Fatalf("MaxRetryAfter = %v", llm.MaxRetryAfter)
	}
	policy := llm.RetryPolicy{MaxDelay: 30 * time.Minute}
	if got := policy.Delay(1, time.Hour); got != llm.MaxRetryAfter {
		t.Fatalf("Delay(1, 1h) = %v, want %v", got, llm.MaxRetryAfter)
	}
	if got := policy.Delay(1, 5*time.Minute); got != 5*time.Minute {
		t.Fatalf("Delay(1, 5m) = %v", got)
	}
}

func TestChatterInterface(t *testing.T) {
	var chatter llm.Chatter = llm.New("http://127.0.0.1:1", "")
	if chatter == nil {
		t.Fatal("openai client should satisfy Chatter")
	}
	chatter = llm.NewAnthropic("http://127.0.0.1:1", "")
	if chatter == nil {
		t.Fatal("anthropic client should satisfy Chatter")
	}
	chatter = llm.NewResponses("http://127.0.0.1:1", "")
	if chatter == nil {
		t.Fatal("responses client should satisfy Chatter")
	}
}

func TestIsNonRetryableQuotaResponse(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"insufficient_quota", 429, `{"error":{"code":"insufficient_quota","message":"You exceeded your current quota"}}`, true},
		{"quota_exceeded", 429, `{"error":"quota_exceeded"}`, true},
		{"billing hard limit", 429, `{"error":{"type":"billing","message":"hard limit reached"}}`, true},
		{"usage limit", 429, `usage_limit reached`, true},
		{"plan limit", 429, `You have hit your plan limit`, true},
		{"subscription quota zh", 429, `订阅额度已用尽`, true},
		{"quota zh", 429, `配额已耗尽`, true},
		{"payment required 402", 402, `payment required: billing details`, true},
		{"plain rate limit", 429, `rate limit exceeded, please retry`, false},
		{"too many requests", 429, `too many requests`, false},
		{"quota wording on 500 is not a quota response", 500, `insufficient_quota`, false},
		{"empty body", 429, ``, false},
		// A credential-shaped value is redacted before matching, so a secret
		// containing a pattern word cannot influence the decision.
		{"credential-shaped value is redacted before matching", 429, `api_key=billing_secret_value1234567890`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := llm.IsNonRetryableQuotaResponse(tc.status, []byte(tc.body)); got != tc.want {
				t.Fatalf("IsNonRetryableQuotaResponse(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
			}
		})
	}
}

func TestRetryPolicyDoesNotRetryQuotaResponses(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"insufficient_quota", `{"error":{"code":"insufficient_quota","message":"You exceeded your current quota. Check your plan and billing details."}}`},
		{"quota_exceeded", `{"error":"quota_exceeded"}`},
		{"billing", `{"error":"billing_hard_limit_reached"}`},
		{"usage limit with credential", `usage_limit reached for api_key=sk_secretvalue1234567890`},
		{"plan limit", `plan limit reached`},
		{"订阅额度", `订阅额度已用尽`},
		{"配额", `配额已耗尽`},
		{"body larger than peek window", `{"error":"insufficient_quota","message":"` + strings.Repeat("x", 8192) + `"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sends int
			var attempts []llm.RetryAttempt
			policy := llm.RetryPolicy{
				MaxAttempts: 3,
				Sleep:       noWait,
				OnRetry:     func(a llm.RetryAttempt) { attempts = append(attempts, a) },
			}
			resp, err := policy.Do(context.Background(), func(context.Context) (*http.Response, error) {
				sends++
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Status:     "429 Too Many Requests",
					Header:     http.Header{"Retry-After": []string{"1"}},
					Body:       io.NopCloser(strings.NewReader(tc.body)),
				}, nil
			})
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			if sends != 1 {
				t.Fatalf("sends = %d, want 1", sends)
			}
			if len(attempts) != 0 {
				t.Fatalf("retry attempts = %v, want none for a quota limit", attempts)
			}
			if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
				t.Fatalf("resp = %#v", resp)
			}
			defer resp.Body.Close()
			got, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if string(got) != tc.body {
				t.Fatalf("body = %q, want the full original body", got)
			}
			// No retry record and no error text is produced for a quota limit,
			// so credential-shaped values in the body cannot leak via Do.
			for _, a := range attempts {
				if strings.Contains(a.Cause, "sk_secretvalue") {
					t.Fatalf("retry attempt leaked credential: %+v", a)
				}
			}
		})
	}
}

func TestRetryPolicyStillRetriesRateLimitResponses(t *testing.T) {
	var sends int
	policy := llm.RetryPolicy{MaxAttempts: 2, Sleep: noWait}
	resp, err := policy.Do(context.Background(), func(context.Context) (*http.Response, error) {
		sends++
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Status:     "429 Too Many Requests",
			Body:       io.NopCloser(strings.NewReader("rate limit exceeded")),
		}, nil
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if sends != 2 {
		t.Fatalf("sends = %d, want 2 for a plain rate limit", sends)
	}
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("resp = %#v", resp)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(got) != "rate limit exceeded" {
		t.Fatalf("body = %q, want full body", got)
	}
}
