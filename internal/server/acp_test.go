package server

import (
	"context"
	"encoding/json"
	"io"
	"testing"
)

func TestACPSessionFlow(t *testing.T) {
	runner := &fakeRunner{reply: "hello", chunks: []string{"hel", "lo"}}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunACP(context.Background(), inR, outW, Config{Runner: runner, Version: "9.9.9"})
	}()

	enc := json.NewEncoder(inW)
	dec := json.NewDecoder(outR)

	// initialize
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	init := readJSON(t, dec)
	initResult, _ := init["result"].(map[string]any)
	if initResult["protocolVersion"] != float64(1) {
		t.Fatalf("initialize result = %v", initResult)
	}
	info, _ := initResult["agentInfo"].(map[string]any)
	if info["name"] != "argentgo" || info["version"] != "9.9.9" {
		t.Fatalf("agentInfo = %v", info)
	}

	// session/new
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/new"})
	created := readJSON(t, dec)
	createdResult, _ := created["result"].(map[string]any)
	sessionID, _ := createdResult["sessionId"].(string)
	if sessionID == "" {
		t.Fatalf("session/new result = %v", created)
	}

	// session/prompt streams updates, then answers end_turn
	_ = enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "session/prompt",
		"params": map[string]any{
			"sessionId": sessionID,
			"prompt":    []map[string]any{{"type": "text", "text": "hi"}},
		},
	})
	var updates []map[string]any
	var promptResult map[string]any
	for {
		msg := readJSON(t, dec)
		if msg["id"] == float64(3) {
			promptResult = msg
			break
		}
		updates = append(updates, msg)
	}
	if len(updates) != 2 {
		t.Fatalf("updates = %d (%v), want 2", len(updates), updates)
	}
	if updates[0]["method"] != "session/update" {
		t.Fatalf("update method = %v", updates[0]["method"])
	}
	params, _ := updates[0]["params"].(map[string]any)
	if params["sessionId"] != sessionID {
		t.Fatalf("update sessionId = %v", params["sessionId"])
	}
	update, _ := params["update"].(map[string]any)
	if update["sessionUpdate"] != "agent_message_chunk" {
		t.Fatalf("update = %v", update)
	}
	content, _ := update["content"].(map[string]any)
	if content["text"] != "hel" {
		t.Fatalf("update content = %v", content)
	}
	resultBody, _ := promptResult["result"].(map[string]any)
	if resultBody["stopReason"] != "end_turn" {
		t.Fatalf("prompt result = %v", promptResult)
	}

	// session/cancel aborts the runner
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 4, "method": "session/cancel", "params": map[string]any{"sessionId": sessionID}})
	cancel := readJSON(t, dec)
	if _, ok := cancel["result"].(map[string]any); !ok {
		t.Fatalf("cancel response = %v", cancel)
	}
	if !runner.abortedNow() {
		t.Fatal("session/cancel did not abort the runner")
	}

	// unknown method
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 5, "method": "session/teleport"})
	unknown := readJSON(t, dec)
	if errBody, _ := unknown["error"].(map[string]any); errBody["code"] != float64(-32601) {
		t.Fatalf("unknown method response = %v", unknown)
	}

	_ = inW.Close()
	waitFor(t, done)
}

// TestACPPromptAcceptsEarlierSession guards the session registry: a client may
// create several sessions on one connection and then prompt an earlier one. The
// session/new handler records every id in acpState.sessions; prompt must consult
// that registry instead of accepting only the most recently created session.
// Before the fix the second session/new made prompting the first fail with
// -32602 "unknown session" even though it was created on this connection.
func TestACPPromptAcceptsEarlierSession(t *testing.T) {
	runner := &fakeRunner{reply: "ok", chunks: []string{"ok"}}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunACP(context.Background(), inR, outW, Config{Runner: runner})
	}()

	enc := json.NewEncoder(inW)
	dec := json.NewDecoder(outR)

	// Create two sessions; the second becomes s.sessionID (the "current" one).
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "session/new"})
	first := readJSON(t, dec)
	firstBody, _ := first["result"].(map[string]any)
	firstID, _ := firstBody["sessionId"].(string)
	if firstID == "" {
		t.Fatalf("session/new result = %v", first)
	}

	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/new"})
	second := readJSON(t, dec)
	secondBody, _ := second["result"].(map[string]any)
	secondID, _ := secondBody["sessionId"].(string)
	if secondID == "" || secondID == firstID {
		t.Fatalf("second session/new result = %v", second)
	}

	// Prompt the *first* session. It is a known session from this connection,
	// so it must be accepted despite not being the current one.
	_ = enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "session/prompt",
		"params": map[string]any{
			"sessionId": firstID,
			"prompt":    []map[string]any{{"type": "text", "text": "hi"}},
		},
	})
	var promptResult map[string]any
	for {
		msg := readJSON(t, dec)
		if msg["id"] == float64(3) {
			promptResult = msg
			break
		}
		if msg["method"] != "session/update" {
			t.Fatalf("unexpected message = %v", msg)
		}
		params, _ := msg["params"].(map[string]any)
		if params["sessionId"] != firstID {
			t.Fatalf("update sessionId = %v, want first session %q", params["sessionId"], firstID)
		}
	}
	if _, ok := promptResult["error"]; ok {
		t.Fatalf("prompting a previously created session failed: %v", promptResult)
	}
	body, _ := promptResult["result"].(map[string]any)
	if body["stopReason"] != "end_turn" {
		t.Fatalf("prompt result = %v", promptResult)
	}

	_ = inW.Close()
	waitFor(t, done)
}

func TestACPPromptForUnknownSession(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunACP(context.Background(), inR, outW, Config{Runner: &fakeRunner{}})
	}()

	enc := json.NewEncoder(inW)
	dec := json.NewDecoder(outR)
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "session/new"})
	readJSON(t, dec)

	_ = enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "session/prompt",
		"params": map[string]any{"sessionId": "sess_missing", "prompt": "hi"},
	})
	response := readJSON(t, dec)
	if errBody, _ := response["error"].(map[string]any); errBody["code"] != float64(-32602) {
		t.Fatalf("response = %v", response)
	}

	_ = inW.Close()
	waitFor(t, done)
}

func TestACPReturnsWhenContextCancelledWhileWaitingOnStdin(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer inW.Close()
	defer outW.Close()
	defer outR.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunACP(ctx, inR, outW, Config{Runner: &fakeRunner{}})
	}()

	// No data on inR: scanLines is blocked in Read, so only ctx can wake us.
	cancel()
	waitFor(t, done)
}

func TestACPInFlightPromptFinishesOnCancel(t *testing.T) {
	runner := newBlockingRunner()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer inW.Close()
	defer outW.Close()
	defer outR.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunACP(ctx, inR, outW, Config{Runner: runner})
	}()

	enc := json.NewEncoder(inW)
	dec := json.NewDecoder(outR)
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "session/new"})
	readJSON(t, dec) // session/new result
	_ = enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "session/prompt",
		"params": map[string]any{"prompt": []map[string]any{{"type": "text", "text": "hi"}}},
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
