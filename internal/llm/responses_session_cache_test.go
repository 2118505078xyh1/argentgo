package llm_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// responsesWire 用给定的线上选项跑一次 Responses Chat，返回原始请求体字节
// 与解码后的对象，供字节级与字段级断言共用。客户端以外的配置保持固定，
// 使不同用例之间唯一的变量就是 Options。
func responsesWire(t *testing.T, opts llm.Options, messages []llm.Message) ([]byte, map[string]any) {
	t.Helper()
	var raw []byte
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		raw = got
		if err := json.Unmarshal(got, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		writeSSE(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":8,\"output_tokens\":2}}}\n\n")
	}))
	defer server.Close()

	client := llm.NewResponses(server.URL, "secret")
	client.Options = opts
	if _, err := client.Chat(context.Background(), "m", messages, twoToolSpecs(), nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	return raw, body
}

// TestResponsesSessionIDYieldsStableCacheKeyAcrossTurns 验证同一 SessionID
// 下，即使稳定前缀（系统提示）发生变化，prompt_cache_key 仍保持一致——
// 这正是跨 turn/fork 复用缓存的前提；deriveCacheKey 路径则会随前缀漂移。
func TestResponsesSessionIDYieldsStableCacheKeyAcrossTurns(t *testing.T) {
	opts := llm.Options{ForcePromptCache: true, SessionID: "sess-123"}
	_, firstBody := responsesWire(t, opts, stableMessages())
	first, ok := firstBody["prompt_cache_key"].(string)
	if !ok {
		t.Fatalf("prompt_cache_key omitted: %#v", firstBody["prompt_cache_key"])
	}
	if first != "session:sess-123" {
		t.Fatalf("prompt_cache_key = %q, want %q", first, "session:sess-123")
	}

	// 模拟下一轮：系统提示不同、对话尾部多了一轮（分叉/续接的典型形态）。
	changed := stableMessages()
	changed[0].Content = "a different system prompt"
	changed = append(changed, llm.Message{Role: "user", Content: "a new turn"})
	_, secondBody := responsesWire(t, opts, changed)
	second, ok := secondBody["prompt_cache_key"].(string)
	if !ok {
		t.Fatalf("prompt_cache_key omitted on second turn: %#v", secondBody["prompt_cache_key"])
	}
	if second != first {
		t.Fatalf("cache key drifted across turns: %q != %q", second, first)
	}
}

// TestResponsesCJKSessionIDNotCutMidRune 验证超长中文会话 ID 派生出的键
// 在 128 rune 上限处被截断，但不会把某个多字节字符切成两半。
func TestResponsesCJKSessionIDNotCutMidRune(t *testing.T) {
	// 足够长，使 "session:"+id 超过 128 rune 上限，强制触发截断。
	id := strings.Repeat("会", 200)
	_, body := responsesWire(t, llm.Options{ForcePromptCache: true, SessionID: id}, stableMessages())
	key, ok := body["prompt_cache_key"].(string)
	if !ok {
		t.Fatalf("prompt_cache_key omitted: %#v", body["prompt_cache_key"])
	}
	if !strings.HasPrefix(key, "session:") {
		t.Fatalf("key = %q, want session: prefix", key)
	}
	if !utf8.ValidString(key) || strings.ContainsRune(key, utf8.RuneError) {
		t.Fatalf("key is not valid UTF-8 (cut mid-rune): %q", key)
	}
	// 与 options.go 的 sessionCacheKeyMaxLen 一致。
	if n := utf8.RuneCountInString(key); n != 128 {
		t.Fatalf("key has %d runes, want 128", n)
	}
}

// TestResponsesEmptySessionIDKeepsDerivedKeyBytes 验证无会话 ID 时线协议字节
// 与接线前完全一致：零值与显式空串产生相同字节，且回退到 deriveCacheKey
// 的 sha256: 前缀，而不是 session: 前缀。
func TestResponsesEmptySessionIDKeepsDerivedKeyBytes(t *testing.T) {
	zeroRaw, zeroBody := responsesWire(t, llm.Options{ForcePromptCache: true}, stableMessages())
	emptyRaw, _ := responsesWire(t, llm.Options{ForcePromptCache: true, SessionID: ""}, stableMessages())
	if !bytes.Equal(zeroRaw, emptyRaw) {
		t.Fatalf("empty SessionID changed wire bytes:\n zero  = %s\n empty = %s", zeroRaw, emptyRaw)
	}
	key, _ := zeroBody["prompt_cache_key"].(string)
	if !strings.HasPrefix(key, "sha256:") {
		t.Fatalf("no-ID key = %q, want deriveCacheKey (sha256:) fallback", key)
	}
	// 同时确认同一稳定前缀仍然产生确定性键。
	againRaw, _ := responsesWire(t, llm.Options{ForcePromptCache: true}, stableMessages())
	if !bytes.Equal(zeroRaw, againRaw) {
		t.Fatal("deriveCacheKey fallback is not deterministic across requests")
	}
}

// TestResponsesOverrideWinsOverSessionID 验证显式覆盖仍然是最高优先级：
// 同时设置 SessionID 与 PromptCacheKey 时，采用覆盖值。
func TestResponsesOverrideWinsOverSessionID(t *testing.T) {
	opts := llm.Options{
		ForcePromptCache: true,
		SessionID:        "sess-123",
		PromptCacheKey:   "explicit-key",
	}
	_, body := responsesWire(t, opts, stableMessages())
	if got := body["prompt_cache_key"]; got != "explicit-key" {
		t.Fatalf("prompt_cache_key = %v, want explicit override", got)
	}
}
