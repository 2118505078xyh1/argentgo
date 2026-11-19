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
	if info["name"] != "argent-go" || info["version"] != "9.9.9" {
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
