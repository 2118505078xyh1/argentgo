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

	// session/prompt 流式发送更新，然后回复 end_turn
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

	// session/cancel 中止 runner
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 4, "method": "session/cancel", "params": map[string]any{"sessionId": sessionID}})
	cancel := readJSON(t, dec)
	if _, ok := cancel["result"].(map[string]any); !ok {
		t.Fatalf("cancel response = %v", cancel)
	}
	if !runner.abortedNow() {
		t.Fatal("session/cancel did not abort the runner")
	}

	// 未知方法
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 5, "method": "session/teleport"})
	unknown := readJSON(t, dec)
	if errBody, _ := unknown["error"].(map[string]any); errBody["code"] != float64(-32601) {
		t.Fatalf("unknown method response = %v", unknown)
	}

	_ = inW.Close()
	waitFor(t, done)
}

// TestACPPromptAcceptsEarlierSession 守护会话注册表：客户端可以
// 在一个连接上创建多个会话，然后提示较早的一个。
// session/new 处理器把每个 id 记录到 acpState.sessions；prompt 必须查询
// 该注册表，而不是只接受最近创建的会话。
// 修复之前，第二次 session/new 会使对第一个的提示失败，报
// -32602 "unknown session"，即使它是在该连接上创建的。
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

	// 创建两个会话；第二个成为 s.sessionID（"当前"的那个）。
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

	// 提示*第一个*会话。它是该连接上已知的会话，
	// 因此即使不是当前会话也必须被接受。
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

	// inR 上没有数据：scanLines 阻塞在 Read 中，因此只有 ctx 能唤醒我们。
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
	<-runner.started // 提示现在正在进行中

	cancel()
	resp := readJSON(t, dec)
	body, _ := resp["result"].(map[string]any)
	if body["stopReason"] != "cancelled" {
		t.Fatalf("response = %v, want stopReason cancelled", resp)
	}
	waitFor(t, done)
}
