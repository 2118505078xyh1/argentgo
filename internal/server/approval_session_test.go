package server

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/security"
)

// sessionRunner 是 fakeRunner 加上审批方法会发现的两个可选接缝：
// 一个持久的待处理存储，以及 runner 正在追加的会话。
// 原生桥只发送带 {id, granted} 的 approval.answer（无 session），
// 因此服务器必须回退到 SessionID() 才能定位正确的存储键。
type sessionRunner struct {
	fakeRunner
	pending *security.PendingStore
	session string
}

func (r *sessionRunner) PendingStore() *security.PendingStore { return r.pending }
func (r *sessionRunner) SessionID() string                    { return r.session }

// TestRPCApprovalAnswerWithoutSessionUsesRunnerSession 守护真实的原生
// 调用形态：approval.answer {"id":..., "granted":...} 不带 session。
// 待处理项和被阻塞审批者的等待者都以 runner 的
// 当前会话为键，因此在空会话下解析会是静默的
// 空操作：RPC 仍回答 ok，但等待的 Ask 永不解除阻塞。
func TestRPCApprovalAnswerWithoutSessionUsesRunnerSession(t *testing.T) {
	for _, tc := range []struct {
		name    string
		granted bool
	}{
		{"allow", true},
		{"deny", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const session = "sess-current"
			store := security.NewPendingStore(t.TempDir())
			if err := store.Add(security.PendingApproval{
				ID: "call_7", Session: session, Tool: "bash",
				ArgsDigest: "abcd", Created: "2026-01-01T00:00:00Z",
			}); err != nil {
				t.Fatalf("seed pending approval: %v", err)
			}
			answer, cancel := store.RegisterWaiter(session, "call_7")
			defer cancel()

			hub := NewEventHub()
			sub, stop := hub.Subscribe(0)
			defer stop()
			runner := &sessionRunner{pending: store, session: session}

			resp := rpcApprovalAnswer(t, Config{Runner: runner, Events: hub}, map[string]any{
				"id": "call_7", "granted": tc.granted,
			})
			body, _ := resp["result"].(map[string]any)
			if body["ok"] != true {
				t.Fatalf("approval.answer result = %v, want ok:true", resp)
			}

			// 阻塞在 Ask 中的进程内审批者必须收到该决定。
			select {
			case got := <-answer:
				if got != tc.granted {
					t.Fatalf("waiter got %v, want %v", got, tc.granted)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("approval.answer did not wake the pending approver")
			}

			// 持久镜像必须在 runner 的会话下解析。
			items, err := store.List(session)
			if err != nil {
				t.Fatalf("list pending: %v", err)
			}
			if len(items) != 0 {
				t.Fatalf("pending item survived approval.answer: %+v", items)
			}

			// resolved 通知必须携带真实的工具名，它来自
			// 在同一会话中查找该 id。
			select {
			case ev := <-sub:
				kind, tool, id, granted, ok := agent.ParseApprovalEvent(ev)
				if !ok || kind != "resolved" || tool != "bash" || id != "call_7" || granted != tc.granted {
					t.Fatalf("resolved notice = %+v (kind=%q tool=%q id=%q granted=%v ok=%v)",
						ev, kind, tool, id, granted, ok)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("no approval:resolved notice published")
			}
		})
	}
}

// TestRPCApprovalListWithoutSessionUsesRunnerSession 覆盖同级方法：
// 省略 session 的重连客户端必须看到当前会话的
// 待处理项，而不是空会话的。显式 session 仍然优先。
func TestRPCApprovalListWithoutSessionUsesRunnerSession(t *testing.T) {
	const session = "sess-current"
	store := security.NewPendingStore(t.TempDir())
	if err := store.Add(security.PendingApproval{
		ID: "call_9", Session: session, Tool: "write",
		ArgsDigest: "abcd", Created: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed pending approval: %v", err)
	}
	runner := &sessionRunner{pending: store, session: session}

	for _, tc := range []struct {
		name   string
		params any
		wantID string
	}{
		{"no session", nil, "call_9"},
		{"explicit session wins", map[string]any{"session": "other"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inR, inW := io.Pipe()
			outR, outW := io.Pipe()
			done := make(chan error, 1)
			go func() {
				done <- RunRPC(context.Background(), inR, outW, Config{Runner: runner})
			}()

			req := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "approval.list"}
			if tc.params != nil {
				req["params"] = tc.params
			}
			if err := json.NewEncoder(inW).Encode(req); err != nil {
				t.Fatalf("encode approval.list: %v", err)
			}
			resp := readJSON(t, json.NewDecoder(outR))
			body, _ := resp["result"].(map[string]any)
			pending, _ := body["pending"].([]any)
			if tc.wantID == "" {
				if len(pending) != 0 {
					t.Fatalf("pending = %v, want empty", pending)
				}
			} else {
				if len(pending) != 1 {
					t.Fatalf("pending = %v, want one item", pending)
				}
				item, _ := pending[0].(map[string]any)
				if item["id"] != tc.wantID {
					t.Fatalf("pending item = %v, want id %q", item, tc.wantID)
				}
			}
			_ = inW.Close()
			waitFor(t, done)
		})
	}
}
