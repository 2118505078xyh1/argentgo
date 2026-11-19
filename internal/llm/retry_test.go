package llm_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
	policy := llm.RetryPolicy{BaseDelay: 100 * time.Millisecond, MaxDelay: 500 * time.Millisecond}

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
