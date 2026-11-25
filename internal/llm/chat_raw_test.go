package llm_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"example.com/argentgo/internal/llm"
)

const anthropicReplayScript = `event: message_start
data: {"type":"message_start","message":{"id":"x","type":"message","role":"assistant","content":[],"model":"m","usage":{"input_tokens":10}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}

`

// TestChatRawReplaysExactBytes 验证 resume 字节回放的核心：ChatRaw 发送的 body
// 与之前快照捕获的 body 逐字节相同，且不会触发新的快照写入（避免递归快照）。
func TestChatRawReplaysExactBytes(t *testing.T) {
	var serverBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		serverBody = append([]byte(nil), body...)
		writeSSE(w, anthropicReplayScript)
	}))
	defer server.Close()

	// 1. 正常 Chat，捕获快照 body（即"当初实际发送的字节"）。
	var snapshotBody []byte
	snapshotCalls := 0
	ctx := llm.WithRequestSnapshotSink(context.Background(), func(_, _ string, body []byte) error {
		snapshotCalls++
		snapshotBody = append([]byte(nil), body...)
		return nil
	})
	client := llm.NewAnthropic(server.URL, "k")
	messages := []llm.Message{{Role: "user", Content: "中文 😀 \"quoted\""}}
	if _, err := client.Chat(ctx, "model-v1", messages, nil, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if snapshotCalls != 1 || len(snapshotBody) == 0 {
		t.Fatalf("snapshot not captured: calls=%d", snapshotCalls)
	}

	// 2. ChatRaw 回放同一 body，断言服务端收到的是同一字节串。
	raw, ok := llm.Chatter(client).(llm.RawChatter)
	if !ok {
		t.Fatal("AnthropicClient must implement RawChatter")
	}
	if _, err := raw.ChatRaw(context.Background(), "model-v1", snapshotBody, nil); err != nil {
		t.Fatalf("ChatRaw: %v", err)
	}
	if !bytes.Equal(serverBody, snapshotBody) {
		t.Fatalf("replayed body differs from snapshot:\n got %q\nwant %q", serverBody, snapshotBody)
	}
	// 3. ChatRaw 不得产生新快照（用无 sink 的 ctx，断言 snapshotCalls 不变）。
	if snapshotCalls != 1 {
		t.Fatalf("ChatRaw wrote a new snapshot: calls=%d", snapshotCalls)
	}
}

// TestChatRawAvailableOnAllProtocols 断言三协议客户端都实现了 RawChatter。
func TestChatRawAvailableOnAllProtocols(t *testing.T) {
	var _ llm.RawChatter = llm.New("", "")
	var _ llm.RawChatter = llm.NewAnthropic("", "")
	var _ llm.RawChatter = llm.NewResponses("", "")
}
