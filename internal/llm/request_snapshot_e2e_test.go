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

// TestRequestSnapshotMatchesWireBytes 验证 killi 决策 21 的核心保证：
// 快照收集器拿到的字节与提供方实际收到的请求体逐字节相同（含 CJK、JSON 转义、
// system 提升、工具定义），而不是重建后的近似表示。
func TestRequestSnapshotMatchesWireBytes(t *testing.T) {
	script := `event: message_start
data: {"type":"message_start","message":{"id":"x","type":"message","role":"assistant","content":[],"model":"m","usage":{"input_tokens":10}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}

`
	var serverBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		serverBody = append([]byte(nil), body...)
		writeSSE(w, script)
	}))
	defer server.Close()

	var snapshotBody []byte
	var snapshotProtocol, snapshotModel string
	ctx := llm.WithRequestSnapshotSink(context.Background(), func(protocol, model string, body []byte) error {
		snapshotProtocol, snapshotModel = protocol, model
		snapshotBody = append([]byte(nil), body...)
		return nil
	})

	messages := []llm.Message{{Role: "user", Content: "中文 \"quoted\" 😀"}}
	tools := []llm.ToolSpec{{Type: "function"}}
	if _, err := llm.NewAnthropic(server.URL, "k").Chat(ctx, "model-v1", messages, tools, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if snapshotProtocol != llm.ProtocolAnthropicMessages || snapshotModel != "model-v1" {
		t.Fatalf("snapshot protocol/model = %q/%q", snapshotProtocol, snapshotModel)
	}
	if len(snapshotBody) == 0 {
		t.Fatal("snapshot sink was never called")
	}
	if !bytes.Equal(snapshotBody, serverBody) {
		t.Fatalf("snapshot bytes differ from wire bytes:\nsnapshot: %s\nserver:   %s", snapshotBody, serverBody)
	}
}

// TestRequestSnapshotAbsentDoesNotChangeWire 验证默认（未挂收集器）时请求字节不变。
func TestRequestSnapshotAbsentDoesNotChangeWire(t *testing.T) {
	script := `event: message_start
data: {"type":"message_start","message":{"id":"x","type":"message","role":"assistant","content":[],"model":"m","usage":{"input_tokens":5}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}

`
	var serverBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		serverBody = append([]byte(nil), body...)
		writeSSE(w, script)
	}))
	defer server.Close()

	if _, err := llm.NewAnthropic(server.URL, "k").Chat(context.Background(), "m", nil, nil, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(serverBody) == 0 {
		t.Fatal("server received no request body")
	}
}
