package app

import (
	"testing"

	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/llm"
)

// TestResolveWiresSessionIDIntoResponsesChatter 验证续接（--session）时的
// 会话 ID 被接入线上选项，使 Responses 客户端能派生跨 turn/fork 稳定的
// prompt_cache_key。
func TestResolveWiresSessionIDIntoResponsesChatter(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	res, err := Resolve(cli.IO{Env: isolatedEnv(home, nil)}, cli.Options{
		Cwd:      root,
		Protocol: "responses",
		Session:  "sess-123",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	client, ok := res.Chatter.(*llm.ResponsesClient)
	if !ok {
		t.Fatalf("chatter = %T, want *llm.ResponsesClient", res.Chatter)
	}
	if got := client.Options.SessionID; got != "sess-123" {
		t.Fatalf("Options.SessionID = %q, want %q", got, "sess-123")
	}
}

// TestResolveLeavesSessionIDEmptyForNewSession 验证新会话（无 --session）时
// 不接入会话 ID，保证 Responses 请求字节与接线前完全一致（opt-in）。
func TestResolveLeavesSessionIDEmptyForNewSession(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	res, err := Resolve(cli.IO{Env: isolatedEnv(home, nil)}, cli.Options{
		Cwd:      root,
		Protocol: "responses",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	client, ok := res.Chatter.(*llm.ResponsesClient)
	if !ok {
		t.Fatalf("chatter = %T, want *llm.ResponsesClient", res.Chatter)
	}
	if got := client.Options.SessionID; got != "" {
		t.Fatalf("Options.SessionID = %q, want empty", got)
	}
}
