package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"example.com/argentgo/internal/llm"
)

// warmFakeChatter 记录 Chat/ChatRaw 调用，验证保活只在窗口内发一次重放。
type warmFakeChatter struct {
	chats    int
	rawCalls int
	rawBody  []byte
}

func (f *warmFakeChatter) Chat(context.Context, string, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
	f.chats++
	return llm.Message{}, nil
}

func (f *warmFakeChatter) ChatRaw(_ context.Context, _ string, body []byte, _ func(string)) (llm.Message, error) {
	f.rawCalls++
	f.rawBody = append([]byte(nil), body...)
	return llm.Message{}, nil
}

func TestWarmRequestBodyCapsGenerationOnly(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":4096}`)
	got := warmRequestBody(body)
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(got, &obj); err != nil {
		t.Fatal(err)
	}
	if string(obj["max_tokens"]) != "1" {
		t.Fatalf("max_tokens = %s, want 1", obj["max_tokens"])
	}
	if string(obj["messages"]) != `[{"role":"user","content":"hi"}]` {
		t.Fatalf("prompt changed: %s", obj["messages"])
	}

	unchanged := []byte(`{"messages":[]}`)
	if got := warmRequestBody(unchanged); string(got) != string(unchanged) {
		t.Fatalf("request without token cap changed: %s", got)
	}
}

func TestWarmReplayStoreCopiesBody(t *testing.T) {
	var store warmReplayStore
	body := []byte(`{"messages":[]}`)
	if err := store.record("chat", "m", body); err != nil {
		t.Fatal(err)
	}
	body[0] = 'X'
	_, model, got := store.snapshot()
	if model != "m" || string(got) != `{"messages":[]}` {
		t.Fatalf("snapshot = %q/%s", model, got)
	}
	got[0] = 'Y'
	_, _, again := store.snapshot()
	if string(again) != `{"messages":[]}` {
		t.Fatalf("snapshot leaked mutable bytes: %s", again)
	}
}

func TestWarmRecordChatterRefreshesClock(t *testing.T) {
	warmer := &llm.CacheWarmer{Enabled: true, TTL: time.Hour}
	fake := &warmFakeChatter{}
	wrapped := &warmRecordChatter{inner: fake, warmer: warmer}
	if _, err := wrapped.Chat(context.Background(), "m", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if fake.chats != 1 || !warmer.ShouldWarm(time.Now().Add(59*time.Minute)) {
		t.Fatalf("real request did not refresh the warmer: chats=%d", fake.chats)
	}
	// 重放本身不得刷新时钟。
	if _, err := wrapped.ChatRaw(context.Background(), "m", []byte(`{}`), nil); err != nil {
		t.Fatal(err)
	}
	if fake.rawCalls != 1 {
		t.Fatalf("raw replay calls = %d", fake.rawCalls)
	}
}

func TestMaybeWarmOnlyInsideWindow(t *testing.T) {
	warmer := &llm.CacheWarmer{Enabled: true, TTL: time.Hour}
	now := time.Now()
	warmer.RecordRequest(now.Add(-55 * time.Minute))
	warmer.RecordIdle(now.Add(-10 * time.Minute))

	fake := &warmFakeChatter{}
	var store warmReplayStore
	_ = store.record("chat", "m", []byte(`{"max_tokens":4096,"messages":[{"role":"user","content":"hi"}]}`))
	if !maybeWarm(context.Background(), fake, warmer, &store) {
		t.Fatal("expected a warm replay inside the window")
	}
	if fake.rawCalls != 1 || !strings.Contains(string(fake.rawBody), `"max_tokens":1`) {
		t.Fatalf("replay = %d / %s", fake.rawCalls, fake.rawBody)
	}

	// 同一 warmer 在窗口外不再重放。
	warmer.RecordRequest(now)
	if maybeWarm(context.Background(), fake, warmer, &store) {
		t.Fatal("unexpected replay outside the window")
	}
	if fake.rawCalls != 1 {
		t.Fatalf("extra replay: %d", fake.rawCalls)
	}
}

func TestWarmSinkSurvivesRequestSnapshot(t *testing.T) {
	ctx := withWarmSink(context.Background(), func(string, string, []byte) error { return nil })
	// opt-in 关闭时原样返回 ctx，内存采集器必须仍在。
	if warmSinkFrom(withRequestSnapshot(ctx, nil, nil)) == nil {
		t.Fatal("warm sink lost when request snapshot is disabled")
	}
	// 禁用 warmer 时 runner 不持有任何保活状态。
	warmer, store := newRunnerWarmer(func(string) string { return "0" })
	if warmer != nil || store != nil {
		t.Fatal("disabled warmer must not allocate state")
	}
}
