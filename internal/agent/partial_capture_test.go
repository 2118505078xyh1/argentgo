package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// streamChatter simulates a provider that streams deltas through onText and
// then returns a reply or an error. It records how many requests it saw so the
// retry/cancellation assertions can be made without a network.
type streamChatter struct {
	mu    sync.Mutex
	calls int
	fn    func(n int, onText func(string)) (llm.Message, error)
}

func (s *streamChatter) Chat(_ context.Context, _ string, _ []llm.Message, _ []llm.ToolSpec, onText func(string)) (llm.Message, error) {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.mu.Unlock()
	return s.fn(n, onText)
}

func (s *streamChatter) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// collectPartials returns every EventPartial a sink collected, in emit order.
func collectPartials(s *eventSink) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Event
	for _, ev := range s.events {
		if ev.Kind == EventPartial {
			out = append(out, ev)
		}
	}
	return out
}

// allEventText joins the Text of every emitted event, for "never leaked" checks.
func allEventText(s *eventSink) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	for _, ev := range s.events {
		b.WriteString(ev.Text)
	}
	return b.String()
}

// TestPartialCaptureSuccessPathUnchanged proves the default path is untouched:
// a streaming success emits only EventTextDelta deltas, never a partial event.
func TestPartialCaptureSuccessPathUnchanged(t *testing.T) {
	sink := &eventSink{}
	chat := &streamChatter{fn: func(_ int, onText func(string)) (llm.Message, error) {
		onText("hel")
		onText("lo")
		return textReply("hello"), nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Emit: sink.emit}}
	res, err := runner.Run(context.Background(), "hi")
	if err != nil || res.Text != "hello" || res.Stopped != StopComplete {
		t.Fatalf("result = %+v, err = %v, want a clean success", res, err)
	}
	if got := collectPartials(sink); len(got) != 0 {
		t.Fatalf("success emitted %d partial event(s), want 0: %+v", len(got), got)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var deltas string
	for _, ev := range sink.events {
		if ev.Kind == EventTextDelta {
			deltas += ev.Text
		}
	}
	if deltas != "hello" {
		t.Fatalf("text deltas = %q, want %q", deltas, "hello")
	}
}

// TestPartialCaptureOnProviderError keeps streamed text at the failure boundary.
func TestPartialCaptureOnProviderError(t *testing.T) {
	sink := &eventSink{}
	chat := &streamChatter{fn: func(_ int, onText func(string)) (llm.Message, error) {
		onText("half ")
		onText("written")
		return llm.Message{}, errors.New("provider failed")
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Emit: sink.emit}}
	res, err := runner.Run(context.Background(), "hi")
	if err == nil || res.Stopped != StopProviderError {
		t.Fatalf("result = %+v, err = %v, want a provider error", res, err)
	}
	partials := collectPartials(sink)
	if len(partials) != 1 {
		t.Fatalf("partial events = %d, want 1: %+v", len(partials), partials)
	}
	p := partials[0]
	if p.Text != "half written" || !p.Partial || p.PartialStopReason != StopProviderError ||
		p.PartialModel != "m" || p.Turn != 0 || p.PartialTruncated {
		t.Fatalf("partial = %+v, want the captured prefix with provider_error provenance", p)
	}
	assertPartialBeforeStop(t, sink)
}

// TestPartialCaptureOnCancellation keeps streamed text when the run is cancelled
// mid-stream, and does not retry the cancelled attempt.
func TestPartialCaptureOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &eventSink{}
	chat := &streamChatter{fn: func(_ int, onText func(string)) (llm.Message, error) {
		onText("half ")
		onText("said")
		cancel()
		return llm.Message{}, errors.New("HTTP 429: too many requests") // retryable, but cancel wins
	}}
	runner := &Runner{Opts: Options{
		FallbackModels: []string{"a", "b"}, Chatter: chat, Emit: sink.emit,
	}}
	res, err := runner.Run(ctx, "hi")
	if !errors.Is(err, context.Canceled) || res.Stopped != StopCancelled {
		t.Fatalf("result = %+v, err = %v, want cancellation", res, err)
	}
	if chat.count() != 1 {
		t.Fatalf("requests = %d, want 1 (cancellation must not retry)", chat.count())
	}
	partials := collectPartials(sink)
	if len(partials) != 1 {
		t.Fatalf("partial events = %d, want 1: %+v", len(partials), partials)
	}
	p := partials[0]
	if p.Text != "half said" || !p.Partial || p.PartialStopReason != StopCancelled || p.PartialModel != "a" {
		t.Fatalf("partial = %+v, want the cancelled prefix with cancelled provenance", p)
	}
	assertPartialBeforeStop(t, sink)
}

// TestPartialCaptureNoTextEmitsNoEvent keeps a textless failure byte-identical:
// only the legacy error + stop events are emitted.
func TestPartialCaptureNoTextEmitsNoEvent(t *testing.T) {
	sink := &eventSink{}
	chat := &streamChatter{fn: func(int, func(string)) (llm.Message, error) {
		return llm.Message{}, errors.New("boom")
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Emit: sink.emit}}
	if _, err := runner.Run(context.Background(), "hi"); err == nil {
		t.Fatal("expected failure")
	}
	if got := collectPartials(sink); len(got) != 0 {
		t.Fatalf("textless failure emitted %d partial event(s), want 0", len(got))
	}
}

// TestPartialCaptureRuneSafeBoundary checks the cap never cuts a UTF-8 rune in
// half, both through the helper and through a real capture.
func TestPartialCaptureRuneSafeBoundary(t *testing.T) {
	for _, tc := range []struct {
		name  string
		s     string
		max   int
		want  int
		wantS string
	}{
		{"ascii", "abcdef", 4, 4, "abcd"},
		{"cjk mid-rune", "你好世界", 4, 3, "你"},
		{"emoji mid-rune", "😀😀", 7, 4, "😀"},
		{"emoji too big", "😀😀", 3, 0, ""},
		{"ascii then emoji", "abc😀", 5, 3, "abc"},
		{"whole string", "你好", 99, 6, "你好"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := runeSafePrefixLen(tc.s, tc.max)
			if n != tc.want || tc.s[:n] != tc.wantS {
				t.Fatalf("prefix len = %d (%q), want %d (%q)", n, tc.s[:n], tc.want, tc.wantS)
			}
			if !utf8.ValidString(tc.s[:n]) {
				t.Fatalf("prefix %q is not valid UTF-8", tc.s[:n])
			}
		})
	}

	// First delta fits, second crosses the cap in the middle of 世 (3 bytes).
	c := newPartialCapture("m", 0, 5)
	c.Add("a")
	c.Add("世界")
	s := c.Snapshot(StopProviderError)
	if s.Text != "a世" || !utf8.ValidString(s.Text) || !s.Truncated {
		t.Fatalf("snapshot = %+v, want %q (valid UTF-8, truncated)", s, "a世")
	}
}

// TestPartialCaptureByteCap enforces the cap across many deltas, keeps CJK and
// emoji intact, and carries the source model / step / stop reason.
func TestPartialCaptureByteCap(t *testing.T) {
	c := newPartialCapture("model-x", 3, 7)
	c.Add("你好") // 6 bytes, fits
	c.Add("世")  // 3 bytes, only 1 byte left -> rune cannot fit, nothing added
	c.Add("🎉")  // 4 bytes, still cannot fit in 1 byte -> nothing added
	s := c.Snapshot(StopCancelled)
	if s.Text != "你好" || len(s.Text) != 6 || !utf8.ValidString(s.Text) {
		t.Fatalf("text = %q (%d bytes), want %q (6 bytes, valid UTF-8)", s.Text, len(s.Text), "你好")
	}
	if !s.Truncated || s.Model != "model-x" || s.Turn != 3 || s.StopReason != StopCancelled {
		t.Fatalf("snapshot = %+v, want truncated with source model-x / step 3 / cancelled", s)
	}

	// A default capture stays within the advertised byte limit.
	d := newPartialCapture("m", 0, 0)
	d.Add(strings.Repeat("🙂", defaultPartialByteLimit))
	got := d.Snapshot("").Text
	if len(got) > defaultPartialByteLimit || !utf8.ValidString(got) || !d.Snapshot("").Truncated {
		t.Fatalf("default capture text = %d bytes, want <= %d with valid UTF-8 and truncation",
			len(got), defaultPartialByteLimit)
	}
}

// TestPartialCaptureFallbackRetryUsesFinalAttempt proves a failed-then-retried
// attempt's prefix is discarded, while the final failure's prefix wins.
func TestPartialCaptureFallbackRetryUsesFinalAttempt(t *testing.T) {
	sink := &eventSink{}
	chat := &streamChatter{fn: func(n int, onText func(string)) (llm.Message, error) {
		switch n {
		case 1:
			onText("from first ")
			return llm.Message{}, errors.New("HTTP 500: server error") // retryable
		case 2:
			onText("from second")
			return llm.Message{}, errors.New("fatal error") // final
		default:
			t.Fatalf("unexpected request %d", n)
			return llm.Message{}, nil
		}
	}}
	runner := &Runner{Opts: Options{
		FallbackModels: []string{"first", "second"}, Chatter: chat, Emit: sink.emit,
	}}
	_, _ = runner.Run(context.Background(), "hi")
	partials := collectPartials(sink)
	if len(partials) != 1 {
		t.Fatalf("partial events = %d, want 1 (from the final attempt only): %+v", len(partials), partials)
	}
	if partials[0].Text != "from second" || partials[0].PartialModel != "second" {
		t.Fatalf("partial = %+v, want the second model's prefix", partials[0])
	}
	if strings.Contains(allEventText(sink), "from first") {
		t.Fatal("a retried attempt's partial text leaked into the event stream")
	}
}

// TestPartialCaptureFallbackSuccessDiscardsFailedAttempt proves the fallback
// success path is byte-identical: only the winning model's deltas are emitted.
func TestPartialCaptureFallbackSuccessDiscardsFailedAttempt(t *testing.T) {
	sink := &eventSink{}
	chat := &streamChatter{fn: func(n int, onText func(string)) (llm.Message, error) {
		if n == 1 {
			onText("discard me")
			return llm.Message{}, errors.New("HTTP 429: rate limit")
		}
		onText("final")
		return textReply("final"), nil
	}}
	runner := &Runner{Opts: Options{
		FallbackModels: []string{"first", "second"}, Chatter: chat, Emit: sink.emit,
	}}
	res, err := runner.Run(context.Background(), "hi")
	if err != nil || res.Text != "final" || res.Stopped != StopComplete {
		t.Fatalf("result = %+v, err = %v, want a clean fallback success", res, err)
	}
	if got := collectPartials(sink); len(got) != 0 {
		t.Fatalf("fallback success emitted %d partial event(s), want 0", len(got))
	}
	if text := allEventText(sink); strings.Contains(text, "discard") || !strings.Contains(text, "final") {
		t.Fatalf("event text = %q, want only the winning model's answer", text)
	}
}

// TestPartialEventJSONIsAdditive locks in the wire contract: a normal event has
// no new keys, a partial event carries all of them, and legacy JSON decodes to
// zero-valued additive fields.
func TestPartialEventJSONIsAdditive(t *testing.T) {
	errEvent, err := json.Marshal(Event{Kind: EventError, Err: "boom", Reason: StopProviderError})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"partial":`, `"partial_stop_reason"`, `"partial_model"`, `"partial_truncated"`} {
		if strings.Contains(string(errEvent), key) {
			t.Fatalf("normal event JSON leaked %s: %s", key, errEvent)
		}
	}

	partial, err := json.Marshal(Event{
		Kind: EventPartial, Turn: 3, Text: "半", Partial: true,
		PartialStopReason: StopCancelled, PartialModel: "m", PartialTruncated: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"Kind":"partial"`, `"Text":"半"`, `"partial":true`,
		`"partial_stop_reason":"cancelled"`, `"partial_model":"m"`, `"partial_truncated":true`} {
		if !strings.Contains(string(partial), key) {
			t.Fatalf("partial event JSON missing %s: %s", key, partial)
		}
	}

	var back Event
	if err := json.Unmarshal([]byte(`{"Kind":"error","Err":"legacy"}`), &back); err != nil {
		t.Fatal(err)
	}
	if back.Partial || back.PartialStopReason != "" || back.PartialModel != "" || back.PartialTruncated {
		t.Fatalf("legacy decode set additive fields: %+v", back)
	}
	if (Event{Kind: EventPartial}).IsTerminal() {
		t.Fatal("EventPartial must not be terminal; EventStop stays the single run terminal")
	}
}

// TestPartialCaptureConcurrentAddSnapshot exercises the mutex under -race.
func TestPartialCaptureConcurrentAddSnapshot(t *testing.T) {
	c := newPartialCapture("m", 0, 1<<20)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				c.Add("x")
				_ = c.Snapshot(StopCancelled)
			}
		}()
	}
	wg.Wait()
	if got := len(c.Snapshot("").Text); got != 800 {
		t.Fatalf("captured %d bytes, want 800", got)
	}
}

func assertPartialBeforeStop(t *testing.T, s *eventSink) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	partialAt, stopAt := -1, -1
	for i, ev := range s.events {
		if ev.Kind == EventPartial && partialAt < 0 {
			partialAt = i
		}
		if ev.Kind == EventStop && stopAt < 0 {
			stopAt = i
		}
	}
	if partialAt < 0 || stopAt < 0 || partialAt >= stopAt {
		t.Fatalf("want EventPartial before EventStop, got partial@%d stop@%d", partialAt, stopAt)
	}
}
