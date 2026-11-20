package llm_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func retryHeaderNoWait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

// TestRetryHeadersShouldRetryOverridesStatus checks the observable decision of
// the retry loop: a provider verdict wins over the HTTP status, while an
// unrecognised value defers to the status as before.
func TestRetryHeadersShouldRetryOverridesStatus(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		verdict   string
		wantSends int
	}{
		{"false is terminal on a retryable 503", 503, "false", 1},
		{"true retries a non-retryable 400", 400, "true", 2},
		{"unknown defers to retryable status", 503, "maybe", 2},
		{"unknown defers to non-retryable status", 400, "maybe", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sends := 0
			policy := llm.RetryPolicy{MaxAttempts: 3, Sleep: retryHeaderNoWait}
			resp, err := policy.Do(context.Background(), func(context.Context) (*http.Response, error) {
				sends++
				if sends == 1 {
					return &http.Response{
						StatusCode: tc.status,
						Status:     "provider response",
						Header:     http.Header{"X-Should-Retry": []string{tc.verdict}},
						Body:       io.NopCloser(strings.NewReader("original body")),
					}, nil
				}
				return &http.Response{StatusCode: 200, Status: "OK", Body: io.NopCloser(http.NoBody)}, nil
			})
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			if sends != tc.wantSends {
				t.Fatalf("sends = %d, want %d", sends, tc.wantSends)
			}
			if resp == nil {
				t.Fatal("Do returned a nil response")
			}
			defer resp.Body.Close()
			// When the verdict stops the retry, the provider's body must stay
			// readable. On a retry path the last response is the (empty) 200.
			if tc.wantSends == 1 {
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatalf("read body: %v", err)
				}
				if string(body) != "original body" {
					t.Fatalf("body = %q, want the provider's original body", body)
				}
			}
		})
	}
}

// TestRetryHeadersDelayPriority checks which header actually sets the sleep:
// retry-after-ms in milliseconds must win over a seconds-valued Retry-After.
func TestRetryHeadersDelayPriority(t *testing.T) {
	cases := []struct {
		name    string
		headers http.Header
		want    time.Duration
	}{
		{
			name:    "retry-after-ms wins over retry-after",
			headers: http.Header{"Retry-After-Ms": []string{"700"}, "Retry-After": []string{"12"}},
			want:    700 * time.Millisecond,
		},
		{
			name:    "retry-after seconds used when ms absent",
			headers: http.Header{"Retry-After": []string{"2"}},
			want:    2 * time.Second,
		},
		{
			name:    "malformed ms falls back to retry-after",
			headers: http.Header{"Retry-After-Ms": []string{"nope"}, "Retry-After": []string{"3"}},
			want:    3 * time.Second,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var delays []time.Duration
			policy := llm.RetryPolicy{
				MaxAttempts: 2,
				Sleep: func(ctx context.Context, d time.Duration) error {
					delays = append(delays, d)
					return ctx.Err()
				},
			}
			sends := 0
			_, err := policy.Do(context.Background(), func(context.Context) (*http.Response, error) {
				sends++
				if sends == 1 {
					return &http.Response{
						StatusCode: 429,
						Status:     "429 Too Many Requests",
						Header:     tc.headers,
						Body:       io.NopCloser(strings.NewReader("rate limited")),
					}, nil
				}
				return &http.Response{StatusCode: 200, Status: "OK", Body: io.NopCloser(http.NoBody)}, nil
			})
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			if len(delays) != 1 || delays[0] != tc.want {
				t.Fatalf("sleeps = %v, want one sleep of %v", delays, tc.want)
			}
		})
	}
}

// TestRetryPolicyDeclinesOverCapProviderDelay checks that an absurd provider
// hint is not silently shortened into a too-early retry: the original response
// (status and full body) is returned untouched and no retry is attempted.
func TestRetryPolicyDeclinesOverCapProviderDelay(t *testing.T) {
	cases := []struct {
		name    string
		headers http.Header
	}{
		{"retry-after-ms over cap", http.Header{"Retry-After-Ms": []string{"999999999"}}},
		{"retry-after over cap", http.Header{"Retry-After": []string{"3600"}}},
		// A seconds value that overflows time.Duration used to wrap negative,
		// bypass this decline check and trigger a retry. It must saturate.
		{"retry-after seconds overflow", http.Header{"Retry-After": []string{"9223372037"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sends, sleeps := 0, 0
			policy := llm.RetryPolicy{
				MaxAttempts: 3,
				MaxDelay:    500 * time.Millisecond, // ceiling 1.5s
				Sleep: func(ctx context.Context, _ time.Duration) error {
					sleeps++
					return ctx.Err()
				},
			}
			resp, err := policy.Do(context.Background(), func(context.Context) (*http.Response, error) {
				sends++
				return &http.Response{
					StatusCode: 429,
					Status:     "429 Too Many Requests",
					Header:     tc.headers,
					Body:       io.NopCloser(strings.NewReader("rate limited")),
				}, nil
			})
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			if sends != 1 || sleeps != 0 {
				t.Fatalf("sends = %d, sleeps = %d, want a single send and no retry", sends, sleeps)
			}
			if resp == nil || resp.StatusCode != 429 {
				t.Fatalf("resp = %#v, want the original 429", resp)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if string(body) != "rate limited" {
				t.Fatalf("body = %q, want the original body", body)
			}
		})
	}
}
