package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// blockingRunner parks a prompt until its ctx is cancelled or it is released,
// so cancellation tests can observe the in-flight prompt's completion.
type blockingRunner struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingRunner() *blockingRunner {
	return &blockingRunner{started: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockingRunner) Prompt(ctx context.Context, text string, onText func(string)) (string, error) {
	b.once.Do(func() { close(b.started) })
	select {
	case <-ctx.Done():
		return "partial", ctx.Err()
	case <-b.release:
		return "done", nil
	}
}

func (b *blockingRunner) Abort() {}

func readJSON(t *testing.T, dec *json.Decoder) map[string]any {
	t.Helper()
	var message map[string]any
	if err := dec.Decode(&message); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	return message
}

func waitFor(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("transport returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("transport did not exit after stdin closed")
	}
}

func TestRPCFraming(t *testing.T) {
	runner := &fakeRunner{reply: "hello", chunks: []string{"hel", "lo"}}
	sessions := fakeSessions{
		infos:  []SessionInfo{{ID: "s1", Title: "first"}},
		events: []SessionEvent{{ID: "e1", Kind: "user"}},
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunRPC(context.Background(), inR, outW, Config{Runner: runner, Sessions: sessions})
	}()

	enc := json.NewEncoder(inW)
	dec := json.NewDecoder(outR)

	if err := enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "prompt",
		"params": map[string]any{"text": "hi"},
	}); err != nil {
		t.Fatalf("encode prompt: %v", err)
	}

	delta := readJSON(t, dec)
	if delta["method"] != "prompt/delta" {
		t.Fatalf("first message = %v, want prompt/delta", delta)
	}
	if params, _ := delta["params"].(map[string]any); params["text"] != "hel" {
		t.Fatalf("delta params = %v", delta["params"])
	}
	readJSON(t, dec) // second chunk
	result := readJSON(t, dec)
	if result["id"] != float64(1) {
		t.Fatalf("result id = %v", result["id"])
	}
	body, _ := result["result"].(map[string]any)
	if body["text"] != "hello" {
		t.Fatalf("prompt result = %v", result)
	}

	if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session.list"}); err != nil {
		t.Fatalf("encode session.list: %v", err)
	}
	listResult := readJSON(t, dec)
	listBody, _ := listResult["result"].(map[string]any)
	list, _ := listBody["sessions"].([]any)
	if len(list) != 1 {
		t.Fatalf("sessions = %v", listBody["sessions"])
	}

	if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "abort"}); err != nil {
		t.Fatalf("encode abort: %v", err)
	}
	abortResult := readJSON(t, dec)
	abortBody, _ := abortResult["result"].(map[string]any)
	if abortBody["ok"] != true {
		t.Fatalf("abort result = %v", abortResult)
	}
	if !runner.abortedNow() {
		t.Fatal("runner was not aborted")
	}

	if _, err := io.WriteString(inW, "not json\n"); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	parseErr := readJSON(t, dec)
	errBody, _ := parseErr["error"].(map[string]any)
	if errBody["code"] != float64(-32700) {
		t.Fatalf("parse error = %v", parseErr)
	}

	if err := inW.Close(); err != nil {
		t.Fatalf("close stdin: %v", err)
	}
	waitFor(t, done)
}

func TestRPCSessionMethods(t *testing.T) {
	sessions := fakeSessions{events: []SessionEvent{{ID: "e1", Kind: "user"}}}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunRPC(context.Background(), inR, outW, Config{Runner: &fakeRunner{}, Sessions: sessions})
	}()

	enc := json.NewEncoder(inW)
	dec := json.NewDecoder(outR)

	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "nope"})
	unknown := readJSON(t, dec)
	if errBody, _ := unknown["error"].(map[string]any); errBody["code"] != float64(-32601) {
		t.Fatalf("unknown method response = %v", unknown)
	}

	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session.load", "params": map[string]any{"id": "x"}})
	loaded := readJSON(t, dec)
	loadedBody, _ := loaded["result"].(map[string]any)
	if loadedBody["id"] != "x" {
		t.Fatalf("session.load response = %v", loaded)
	}
	if events, _ := loadedBody["events"].([]any); len(events) != 1 {
		t.Fatalf("session.load events = %v", loadedBody["events"])
	}

	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "session.load", "params": map[string]any{}})
	noID := readJSON(t, dec)
	if errBody, _ := noID["error"].(map[string]any); errBody["code"] != float64(-32602) {
		t.Fatalf("missing id response = %v", noID)
	}

	_ = inW.Close()
	waitFor(t, done)
}

func TestRPCMissingSessionStore(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunRPC(context.Background(), inR, outW, Config{Runner: &fakeRunner{}})
	}()

	enc := json.NewEncoder(inW)
	dec := json.NewDecoder(outR)
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "session.list"})
	response := readJSON(t, dec)
	if errBody, _ := response["error"].(map[string]any); errBody["code"] != float64(-32000) {
		t.Fatalf("response = %v", response)
	}
	_ = inW.Close()
	waitFor(t, done)
}

func TestRPCPromptWithEmptyTextIsRejected(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunRPC(context.Background(), inR, outW, Config{Runner: &fakeRunner{}})
	}()

	enc := json.NewEncoder(inW)
	dec := json.NewDecoder(outR)
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "prompt", "params": map[string]any{}})
	response := readJSON(t, dec)
	if errBody, _ := response["error"].(map[string]any); errBody["code"] != float64(-32602) {
		t.Fatalf("response = %v", response)
	}
	_ = inW.Close()
	waitFor(t, done)
}

func TestRPCReturnsWhenContextCancelledWhileWaitingOnStdin(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer inW.Close()
	defer outW.Close()
	defer outR.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunRPC(ctx, inR, outW, Config{Runner: &fakeRunner{}})
	}()

	// inR has no data, so scanLines is blocked in Read. Cancelling must still
	// unblock the consumer without needing EOF or a kill.
	cancel()
	waitFor(t, done)
}

func TestRPCInFlightPromptFinishesOnCancel(t *testing.T) {
	runner := newBlockingRunner()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer inW.Close()
	defer outW.Close()
	defer outR.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunRPC(ctx, inR, outW, Config{Runner: runner})
	}()

	enc := json.NewEncoder(inW)
	dec := json.NewDecoder(outR)
	_ = enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "prompt",
		"params": map[string]any{"text": "hi"},
	})
	<-runner.started // the prompt is now in flight

	cancel()
	resp := readJSON(t, dec)
	body, _ := resp["result"].(map[string]any)
	if body["stopReason"] != "cancelled" {
		t.Fatalf("response = %v, want stopReason cancelled", resp)
	}
	waitFor(t, done)
}

func TestScanLinesTrimsAndSkipsBlankLines(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := scanLines(ctx, strings.NewReader("  first  \n\n\t\nsecond\n"))
	var got []string
	for res := range results {
		if res.err != nil {
			t.Fatalf("scan error: %v", res.err)
		}
		got = append(got, string(res.line))
	}
	if joined := strings.Join(got, ","); joined != "first,second" {
		t.Fatalf("lines = %q, want %q", joined, "first,second")
	}
}

func TestScanLinesDropsOversizedFrameAndContinues(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := strings.Repeat("x", maxRPCFrame+1) + "\n" + "second\n"
	results := scanLines(ctx, strings.NewReader(input))
	var errs []error
	var lines []string
	for res := range results {
		if res.err != nil {
			errs = append(errs, res.err)
			continue
		}
		lines = append(lines, string(res.line))
	}
	if len(errs) != 1 || !errors.Is(errs[0], errFrameTooLong) {
		t.Fatalf("errors = %v, want exactly one errFrameTooLong", errs)
	}
	if len(lines) != 1 || lines[0] != "second" {
		t.Fatalf("lines = %q, want [second]", lines)
	}
}

func TestScanLinesDropsOversizedFinalFrameWithoutNewline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := "first\n" + strings.Repeat("x", maxRPCFrame+1) // no trailing newline
	results := scanLines(ctx, strings.NewReader(input))
	var errs []error
	var lines []string
	for res := range results {
		if res.err != nil {
			errs = append(errs, res.err)
			continue
		}
		lines = append(lines, string(res.line))
	}
	if len(errs) != 1 || !errors.Is(errs[0], errFrameTooLong) {
		t.Fatalf("errors = %v, want exactly one errFrameTooLong", errs)
	}
	if len(lines) != 1 || lines[0] != "first" {
		t.Fatalf("lines = %q, want [first]", lines)
	}
}

func TestScanLinesAcceptsFrameAtLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	want := strings.Repeat("x", maxRPCFrame)
	results := scanLines(ctx, strings.NewReader(want+"\n"))
	res, ok := <-results
	if !ok {
		t.Fatal("no frame was returned")
	}
	if res.err != nil {
		t.Fatalf("unexpected error: %v", res.err)
	}
	if string(res.line) != want {
		t.Fatalf("frame length = %d, want %d", len(res.line), len(want))
	}
	if res, ok := <-results; ok {
		t.Fatalf("unexpected extra result: %+v", res)
	}
}

func TestRPCContinuesAfterOversizedFrame(t *testing.T) {
	var in, out bytes.Buffer
	in.WriteString(strings.Repeat("x", maxRPCFrame+1) + "\n")
	in.WriteString(`{"jsonrpc":"2.0","id":2,"method":"nope"}` + "\n")

	// No network: both ends are in-memory buffers. Only synchronous methods
	// are used so out is written solely from RunRPC's own goroutine.
	if err := RunRPC(context.Background(), &in, &out, Config{}); err != nil {
		t.Fatalf("RunRPC returned a terminal error for an oversized frame: %v", err)
	}

	frames := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(frames) != 2 {
		t.Fatalf("got %d frames (%q), want 2", len(frames), out.String())
	}
	var tooLong, unknown map[string]any
	if err := json.Unmarshal([]byte(frames[0]), &tooLong); err != nil {
		t.Fatalf("decode first frame: %v", err)
	}
	if err := json.Unmarshal([]byte(frames[1]), &unknown); err != nil {
		t.Fatalf("decode second frame: %v", err)
	}
	if eb, _ := tooLong["error"].(map[string]any); eb["code"] != float64(codeParseError) {
		t.Fatalf("oversized-frame reply = %v, want parse error", tooLong)
	} else if msg, _ := eb["message"].(string); !strings.Contains(msg, "too long") {
		t.Fatalf("oversized-frame message = %q", msg)
	}
	if eb, _ := unknown["error"].(map[string]any); eb["code"] != float64(codeMethodNotFound) {
		t.Fatalf("following request reply = %v, want method-not-found", unknown)
	}
	if unknown["id"] != float64(2) {
		t.Fatalf("following request id = %v, want 2", unknown["id"])
	}
}

func TestACPContinuesAfterOversizedFrame(t *testing.T) {
	var in, out bytes.Buffer
	in.WriteString(strings.Repeat("x", maxRPCFrame+1) + "\n")
	in.WriteString(`{"jsonrpc":"2.0","id":3,"method":"nope"}` + "\n")

	if err := RunACP(context.Background(), &in, &out, Config{}); err != nil {
		t.Fatalf("RunACP returned a terminal error for an oversized frame: %v", err)
	}
	frames := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2", len(frames))
	}
	var reply map[string]any
	if err := json.Unmarshal([]byte(frames[1]), &reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if reply["id"] != float64(3) {
		t.Fatalf("reply = %v, want id 3", reply)
	}
}
