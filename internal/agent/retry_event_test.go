package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestEventRetryCarriesFullInfo checks that EventRetry carries every field a
// frontend needs to draw retry progress, and that the payload uses stable
// snake_case wire names rather than Go field names.
func TestEventRetryCarriesFullInfo(t *testing.T) {
	t.Parallel()
	ev := Event{Kind: EventRetry, Turn: 2, Retry: &RetryInfo{
		Attempt:     2,
		MaxAttempts: 4,
		DelayMS:     1500,
		Reason:      "status",
		Model:       "backup-model",
	}}
	if ev.Kind != EventRetry || ev.Retry == nil {
		t.Fatalf("event = %+v, want a retry event with a payload", ev)
	}
	if ev.IsTerminal() {
		t.Fatal("EventRetry must not be a run terminal")
	}

	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Kind  EventKind `json:"Kind"`
		Turn  int       `json:"Turn"`
		Retry struct {
			Attempt     int    `json:"attempt"`
			MaxAttempts int    `json:"max_attempts"`
			DelayMS     int64  `json:"delay_ms"`
			Reason      string `json:"reason"`
			Model       string `json:"model"`
		} `json:"retry"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Kind != EventRetry || wire.Turn != 2 {
		t.Fatalf("wire header = %+v, want retry/2", wire)
	}
	got := wire.Retry
	if got.Attempt != 2 || got.MaxAttempts != 4 || got.DelayMS != 1500 || got.Reason != "status" || got.Model != "backup-model" {
		t.Fatalf("wire retry payload = %+v, want attempt=2 max=4 delay=1500 reason=status model=backup-model", got)
	}
	// A round trip must preserve the payload, not just marshal it.
	var back Event
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Retry == nil || *back.Retry != *ev.Retry {
		t.Fatalf("round trip = %+v, want %+v", back.Retry, ev.Retry)
	}
}

// TestEventRetryNilKeepsLegacyJSON proves the field is purely additive: a nil
// Retry is omitted, so existing events keep byte-identical wire output and old
// JSON without a "retry" key still decodes to Retry == nil.
func TestEventRetryNilKeepsLegacyJSON(t *testing.T) {
	t.Parallel()
	ev := Event{Kind: EventError, Turn: 3, Text: "t", Err: "boom", Reason: StopProviderError}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "retry") || strings.Contains(string(data), "Retry") {
		t.Fatalf("nil Retry leaked into JSON: %s", data)
	}

	// The pre-Retry shape with every field in the same order. Byte equality is
	// the strongest form of the "no change when nil" guarantee. Failure and
	// Retry are both nil+omitempty, so neither appears in either encoding.
	type retryLegacyEvent struct {
		Kind            EventKind
		Turn            int
		Depth           int
		Text            string
		ToolCallID      string
		ToolName        string
		Args            string
		Intent          string
		Output          string
		IsError         bool
		DurationMS      int64
		Usage           llm.Usage
		TotalUsage      llm.Usage
		Err             string
		Reason          string
		PendingMessages int
		Checkpoint      *Checkpoint
	}
	legacy := retryLegacyEvent{Kind: ev.Kind, Turn: ev.Turn, Text: ev.Text, Err: ev.Err, Reason: ev.Reason}
	legacyJSON, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(legacyJSON) {
		t.Fatalf("nil-Retry JSON changed:\n got %s\nwant %s", data, legacyJSON)
	}

	var back Event
	if err := json.Unmarshal([]byte(`{"Kind":"notice","Text":"legacy"}`), &back); err != nil {
		t.Fatal(err)
	}
	if back.Retry != nil {
		t.Fatalf("legacy decode set Retry: %+v", back.Retry)
	}
}

// TestFallbackSwitchEmitsEventRetry checks the agent-visible retry point: a
// provider switch publishes a structured EventRetry, with a wire-safe reason
// and no provider text. The legacy notice stays so old consumers are unchanged.
func TestFallbackSwitchEmitsEventRetry(t *testing.T) {
	t.Parallel()
	const secret = "sk-proj-SUPERSECRET123"
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return llm.Message{}, errors.New("HTTP 429: too many requests; key=" + secret)
		}
		return textReply("ok from backup"), nil
	}}
	sink := &eventSink{}
	runner := &Runner{Opts: Options{
		Model:          "primary",
		FallbackModels: []string{"primary", "backup"},
		Chatter:        chat,
		Emit:           sink.emit,
	}}
	res, err := runner.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Stopped != StopComplete {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopComplete)
	}

	var retry Event
	found := false
	for _, ev := range sink.events {
		if ev.Kind == EventRetry {
			retry = ev
			found = true
		}
	}
	if !found {
		t.Fatalf("no EventRetry emitted; kinds = %v", sink.kinds())
	}
	if retry.Retry == nil {
		t.Fatal("EventRetry has no Retry payload")
	}
	r := retry.Retry
	if r.Attempt != 2 || r.MaxAttempts != 2 || r.Model != "backup" || r.Reason != "status" {
		t.Fatalf("retry payload = %+v, want attempt=2 max=2 model=backup reason=status", r)
	}
	if r.DelayMS != 0 {
		t.Fatalf("DelayMS = %d, want 0 (the agent fallback switch has no backoff wait)", r.DelayMS)
	}
	if !sink.has(EventNotice) {
		t.Fatal("legacy switch notice was dropped")
	}
	data, _ := json.Marshal(retry)
	if strings.Contains(string(data), secret) || strings.Contains(string(data), "sk-") {
		t.Fatalf("retry event leaked provider text: %s", data)
	}
}

// TestEventRetryFieldsAreRedacted pins the safety promise at both producers:
// retryReason emits only closed tokens, and RetryInfoFromAttempt copies only
// the Reason class -- never llm's free-form Cause, which can carry a key.
func TestEventRetryFieldsAreRedacted(t *testing.T) {
	t.Parallel()
	const secret = "sk-proj-SUPERSECRET123"
	reasons := []struct {
		name string
		err  error
	}{
		{"status with key", errors.New("HTTP 429: too many requests; Authorization: Bearer " + secret)},
		{"server with key", errors.New("provider returned 503: backend said " + secret)},
		{"timeout with key", errors.New("dial tcp: i/o timeout using " + secret)},
		{"cancellation with key", errors.New("context canceled after sending " + secret)},
	}
	known := map[string]bool{
		string(llm.ReasonNone): true, string(llm.ReasonStatus): true,
		string(llm.ReasonNotSent): true, string(llm.ReasonUnreachable): true,
		string(llm.ReasonTimeout): true,
	}
	for _, tc := range reasons {
		t.Run(tc.name, func(t *testing.T) {
			got := retryReason(tc.err)
			if !known[got] {
				t.Fatalf("retryReason = %q, want a closed llm reason token", got)
			}
			if strings.Contains(got, secret) || strings.Contains(got, "sk-") {
				t.Fatalf("reason leaked a credential: %q", got)
			}
		})
	}

	// The llm bridge: Cause holds redacted-but-free-form text. The converter
	// must drop it entirely and keep only the fixed reason class.
	attempt := llm.RetryAttempt{Attempt: 2, StatusCode: 429, Cause: "provider said " + secret, Reason: llm.ReasonStatus}
	info := RetryInfoFromAttempt(attempt, "backup", 3, 750*time.Millisecond)
	if info.Attempt != 2 || info.MaxAttempts != 3 || info.DelayMS != 750 || info.Reason != "status" || info.Model != "backup" {
		t.Fatalf("converted payload = %+v, want attempt=2 max=3 delay=750 reason=status model=backup", info)
	}
	data, err := json.Marshal(Event{Kind: EventRetry, Retry: &info})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) || strings.Contains(string(data), "Cause") {
		t.Fatalf("converted payload leaked Cause/credential: %s", data)
	}
}
