package agent

import (
	"context"
	"testing"

	"example.com/argentgo/internal/llm"
)

// replayChatter 同时实现 Chatter 与 RawChatter，记录各路径调用次数与收到的 body。
type replayChatter struct {
	chatCalls int
	rawCalls  int
	rawBodies [][]byte
	reply     func() llm.Message
}

func (r *replayChatter) Chat(_ context.Context, _ string, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
	r.chatCalls++
	return r.reply(), nil
}

func (r *replayChatter) ChatRaw(_ context.Context, _ string, body []byte, _ func(string)) (llm.Message, error) {
	r.rawCalls++
	r.rawBodies = append(r.rawBodies, append([]byte(nil), body...))
	return r.reply(), nil
}

func textReplay(s string) func() llm.Message {
	return func() llm.Message { return llm.Message{Role: "assistant", Content: s} }
}

func TestByteReplayFirstRequestOnly(t *testing.T) {
	fake := &replayChatter{reply: textReplay("ok")}
	s := newRunState(Options{
		Model:          "m",
		System:         "s",
		Chatter:        fake,
		ReplayBody:     []byte(`{"raw":"snapshot"}`),
		ReplayProtocol: llm.ProtocolChatCompletions,
	}, "hi", nil)

	_, _ = s.chat(context.Background(), nil)
	if fake.rawCalls != 1 || fake.chatCalls != 0 {
		t.Fatalf("first call: raw=%d chat=%d, want raw=1 chat=0", fake.rawCalls, fake.chatCalls)
	}
	if len(fake.rawBodies) != 1 || string(fake.rawBodies[0]) != `{"raw":"snapshot"}` {
		t.Fatalf("raw body mismatch: %q", fake.rawBodies)
	}

	// 第二轮应走普通 Chat，且不再用 raw。
	_, _ = s.chat(context.Background(), nil)
	if fake.rawCalls != 1 || fake.chatCalls != 1 {
		t.Fatalf("second call: raw=%d chat=%d, want raw=1 chat=1", fake.rawCalls, fake.chatCalls)
	}
}

// plainChatter 只实现 Chatter（不实现 RawChatter）。
type plainChatter struct{ calls int }

func (p *plainChatter) Chat(_ context.Context, _ string, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
	p.calls++
	return llm.Message{Role: "assistant", Content: "ok"}, nil
}

func TestByteReplayFallsBackWhenRawUnsupported(t *testing.T) {
	plain := &plainChatter{}
	s := newRunState(Options{
		Model:      "m",
		System:     "s",
		Chatter:    plain,
		ReplayBody: []byte(`{"raw":"snapshot"}`),
	}, "hi", nil)

	_, _ = s.chat(context.Background(), nil)
	if plain.calls != 1 {
		t.Fatalf("plain chatter calls = %d, want 1", plain.calls)
	}
	if !s.replayUsed || len(s.replayBody) != 0 {
		t.Fatal("replay state should be cleared even on fallback")
	}
}
