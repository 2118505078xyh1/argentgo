package llm_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func TestRedactTextMasksCredentialsButKeepsErrorWording(t *testing.T) {
	const secret = "sk_abcdefghijklmnopqrstuvwxyz"
	cases := []struct {
		name     string
		in       string
		mustHide []string
		mustKeep []string
	}{
		{
			name:     "JSON error body",
			in:       fmt.Sprintf(`provider returned 401: {"api_key":"%s","email":"alice@example.com"}`, secret),
			mustHide: []string{secret, "alice@example.com"},
			mustKeep: []string{"provider returned 401"},
		},
		{
			name:     "bearer header",
			in:       "Authorization: Bearer " + secret,
			mustHide: []string{secret},
			mustKeep: []string{"Authorization"},
		},
		{
			name:     "long hex blob",
			in:       "invalid signature 0123456789abcdef0123456789abcdef",
			mustHide: []string{"0123456789abcdef0123456789abcdef"},
			mustKeep: []string{"invalid signature"},
		},
		{
			name:     "ordinary error",
			in:       "provider returned 400: model not found",
			mustKeep: []string{"provider returned 400: model not found"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := llm.RedactText(tc.in)
			for _, hidden := range tc.mustHide {
				if strings.Contains(got, hidden) {
					t.Fatalf("RedactText leaked %q: %q", hidden, got)
				}
			}
			for _, kept := range tc.mustKeep {
				if !strings.Contains(got, kept) {
					t.Fatalf("RedactText lost %q: %q", kept, got)
				}
			}
		})
	}
}

func TestProviderErrorRedactsTheBody(t *testing.T) {
	const secret = "sk_abcdefghijklmnopqrstuvwxyz"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":{"message":"bad key %s for alice@example.com"}}`, secret)
	}))
	defer server.Close()

	_, err := llm.NewAnthropic(server.URL, "k").Chat(context.Background(), "m", nil, nil, nil)
	if err == nil {
		t.Fatal("Chat returned no error")
	}
	text := err.Error()
	if strings.Contains(text, secret) || strings.Contains(text, "alice@example.com") {
		t.Fatalf("error body was not redacted: %q", text)
	}
	if !strings.Contains(text, "401") || !strings.Contains(text, "bad key") {
		t.Fatalf("error wording was lost: %q", text)
	}
}

func TestClassifyError(t *testing.T) {
	cases := []struct {
		err  error
		want llm.AttemptKind
	}{
		{nil, llm.AttemptSuccess},
		{context.Canceled, llm.AttemptCancelled},
		{context.DeadlineExceeded, llm.AttemptCancelled},
		{fmt.Errorf("wrapped: %w", llm.ErrStreamError), llm.AttemptStreamError},
		{errors.New("plain failure"), llm.AttemptFailed},
	}
	for _, tc := range cases {
		if got := llm.ClassifyError(tc.err); got != tc.want {
			t.Errorf("ClassifyError(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestNewAttemptRecordClipsErrorClassByRune(t *testing.T) {
	// A byte-based clip used to cut this message in the middle of a rune.
	long := strings.Repeat("中文", 200)
	record := llm.NewAttemptRecord(llm.AttemptFailed, llm.ProtocolChatCompletions, "m", errors.New(long))
	if !utf8.ValidString(record.ErrorClass) {
		t.Fatalf("ErrorClass is not valid UTF-8: %q", record.ErrorClass)
	}
	if got := utf8.RuneCountInString(record.ErrorClass); got != 256 {
		t.Fatalf("ErrorClass rune count = %d, want 256 (255 + ellipsis)", got)
	}
	if !strings.HasSuffix(record.ErrorClass, "…") {
		t.Fatalf("ErrorClass = %q, want an ellipsis suffix", record.ErrorClass)
	}

	short := llm.NewAttemptRecord(llm.AttemptFailed, llm.ProtocolChatCompletions, "m", errors.New("中文"))
	if short.ErrorClass != "中文" {
		t.Fatalf("short ErrorClass = %q, want 中文", short.ErrorClass)
	}
}

func TestAttemptHookRecordsRetriesAndSuccess(t *testing.T) {
	const secret = "sk_abcdefghijklmnopqrstuvwxyz"
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprintf(w, `{"message":"slow down %s"}`, secret)
			return
		}
		writeSSE(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	var records []llm.AttemptRecord
	client := llm.NewAnthropic(server.URL, "k")
	client.Retry = llm.RetryPolicy{Sleep: noWait}
	client.Options.AttemptHook = func(record llm.AttemptRecord) { records = append(records, record) }
	if _, err := client.Chat(context.Background(), "m", nil, nil, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(records) != 2 || records[0].Kind != llm.AttemptRetry || records[1].Kind != llm.AttemptSuccess {
		t.Fatalf("records = %+v", records)
	}
	if records[0].StatusCode != http.StatusTooManyRequests || records[0].Protocol != llm.ProtocolAnthropicMessages {
		t.Fatalf("retry record = %+v", records[0])
	}
	if strings.Contains(fmt.Sprint(records), secret) {
		t.Fatalf("attempt records leaked the secret: %+v", records)
	}
}

func TestAttemptHookRecordsRedactedStreamError(t *testing.T) {
	const secret = "sk_abcdefghijklmnopqrstuvwxyz"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, fmt.Sprintf(`data: {"type":"error","error":{"message":"overloaded for %s"}}`+"\n\n", secret))
	}))
	defer server.Close()

	var records []llm.AttemptRecord
	client := llm.NewAnthropic(server.URL, "k")
	client.Options.AttemptHook = func(record llm.AttemptRecord) { records = append(records, record) }
	if _, err := client.Chat(context.Background(), "m", nil, nil, nil); err == nil {
		t.Fatal("a stream error was not reported")
	}
	if len(records) != 1 || records[0].Kind != llm.AttemptStreamError {
		t.Fatalf("records = %+v", records)
	}
	if strings.Contains(records[0].ErrorClass, secret) || !strings.Contains(records[0].ErrorClass, "overloaded") {
		t.Fatalf("error class = %q", records[0].ErrorClass)
	}
}

func TestAttemptHookRecordsCancellationWithoutAnErrorClass(t *testing.T) {
	var records []llm.AttemptRecord
	client := llm.NewResponses("http://127.0.0.1:1", "k")
	client.Options.AttemptHook = func(record llm.AttemptRecord) { records = append(records, record) }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Chat(ctx, "m", nil, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(records) != 1 || records[0].Kind != llm.AttemptCancelled || records[0].ErrorClass != "" {
		t.Fatalf("records = %+v", records)
	}
}

func TestProviderHeadersCarryAuthOnlyWhenConfigured(t *testing.T) {
	cases := []struct {
		name   string
		build  func(url, key string) llm.Chatter
		header string
		want   string
	}{
		{"chat completions", func(url, key string) llm.Chatter { return llm.New(url, key) }, "Authorization", "Bearer secret"},
		{"anthropic messages", func(url, key string) llm.Chatter { return llm.NewAnthropic(url, key) }, "x-api-key", "secret"},
		{"responses", func(url, key string) llm.Chatter { return llm.NewResponses(url, key) }, "Authorization", "Bearer secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var header http.Header
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				header = r.Header.Clone()
				switch tc.name {
				case "chat completions":
					io.WriteString(w, "data: [DONE]\n\n")
				case "anthropic messages":
					writeSSE(w, "data: {\"type\":\"message_stop\"}\n\n")
				case "responses":
					writeSSE(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
				}
			}))
			defer server.Close()

			if _, err := tc.build(server.URL, "secret").Chat(context.Background(), "m", nil, nil, nil); err != nil {
				t.Fatalf("Chat with key: %v", err)
			}
			if got := header.Get(tc.header); got != tc.want {
				t.Fatalf("%s = %q, want %q", tc.header, got, tc.want)
			}
			if got := header.Get("Accept"); got != "text/event-stream" {
				t.Fatalf("Accept = %q", got)
			}
			if got := header.Get("anthropic-beta"); got != "" {
				t.Fatalf("an unexpected cache beta header was sent: %q", got)
			}

			if _, err := tc.build(server.URL, "").Chat(context.Background(), "m", nil, nil, nil); err != nil {
				t.Fatalf("Chat without key: %v", err)
			}
			if got := header.Get(tc.header); got != "" {
				t.Fatalf("%s without a key = %q, want empty", tc.header, got)
			}
		})
	}
}
