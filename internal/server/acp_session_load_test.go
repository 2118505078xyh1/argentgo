package server

import (
	"context"
	"encoding/json"
	"io"
	"testing"
)

func TestACPSessionLoad(t *testing.T) {
	store := fakeSessions{events: []SessionEvent{
		{ID: "e1", Kind: "user", Data: json.RawMessage(`{"text":"hello"}`)},
		{ID: "e2", Kind: "assistant", Data: json.RawMessage(`{"text":"hi"}`)},
	}}
	runner := &fakeRunner{reply: "ok"}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunACP(context.Background(), inR, outW, Config{Runner: runner, Sessions: store, Version: "1"})
	}()
	enc := json.NewEncoder(inW)
	dec := json.NewDecoder(outR)

	// initialize 应宣告 loadSession=true（配置了 store）。
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	init := readJSON(t, dec)
	caps, _ := init["result"].(map[string]any)["agentCapabilities"].(map[string]any)
	if caps["loadSession"] != true {
		t.Fatalf("initialize agentCapabilities.loadSession = %v, want true", caps["loadSession"])
	}

	// session/load 返回事件，并把会话注册到本连接。
	_ = enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "session/load",
		"params": map[string]any{"sessionId": "saved-1"},
	})
	loaded := readJSON(t, dec)
	result, _ := loaded["result"].(map[string]any)
	if result["sessionId"] != "saved-1" {
		t.Fatalf("session/load result = %v", loaded)
	}
	events, _ := result["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("session/load events = %v, want 2", events)
	}

	// 加载后的会话可被 prompt 使用（不再报 unknown session）。
	_ = enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "session/prompt",
		"params": map[string]any{"sessionId": "saved-1", "prompt": []any{
			map[string]any{"type": "text", "text": "continue"},
		}},
	})
	// 先读流式 update，再读终态。
	seenPromptReply := false
	for i := 0; i < 6; i++ {
		msg := readJSON(t, dec)
		if msg["id"] == float64(3) {
			seenPromptReply = true
			break
		}
	}
	if !seenPromptReply {
		t.Fatal("prompt on loaded session never completed")
	}

	_ = inW.Close()
	_ = outW.Close()
	<-done
}

func TestACPSessionLoadWithoutStore(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunACP(context.Background(), inR, outW, Config{Runner: &fakeRunner{}})
	}()
	enc := json.NewEncoder(inW)
	dec := json.NewDecoder(outR)

	// 无 store 时 initialize 不宣告 loadSession。
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	init := readJSON(t, dec)
	caps, _ := init["result"].(map[string]any)["agentCapabilities"].(map[string]any)
	if caps["loadSession"] != false {
		t.Fatalf("loadSession = %v, want false without store", caps["loadSession"])
	}

	// session/load 优雅失败（not supported），不是 panic / unknown method 混乱。
	_ = enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "session/load",
		"params": map[string]any{"sessionId": "x"},
	})
	resp := readJSON(t, dec)
	if resp["error"] == nil {
		t.Fatalf("session/load without store should error, got %v", resp)
	}
	_ = inW.Close()
	_ = outW.Close()
	<-done
}
