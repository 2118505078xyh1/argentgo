package server

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"
)

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
