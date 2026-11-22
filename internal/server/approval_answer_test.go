package server

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/security"
)

// approvalAnswerRunner 是 fakeRunner 加上 approval.answer 会发现的
// 两个可选接缝：一个持久的待处理存储和一个共享事件 hub。
type approvalAnswerRunner struct {
	fakeRunner
	pending *security.PendingStore
	hub     *EventHub
}

func (r *approvalAnswerRunner) PendingStore() *security.PendingStore { return r.pending }

func (r *approvalAnswerRunner) EventHub() *EventHub { return r.hub }

// rpcApprovalAnswer 在一个全新的传输上发送一条 approval.answer 请求，
// 并返回它的单条 JSON-RPC 回复。
func rpcApprovalAnswer(t *testing.T, cfg Config, params any) map[string]any {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunRPC(context.Background(), inR, outW, cfg)
	}()

	if err := json.NewEncoder(inW).Encode(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "approval.answer", "params": params,
	}); err != nil {
		t.Fatalf("encode approval.answer: %v", err)
	}
	resp := readJSON(t, json.NewDecoder(outR))
	_ = inW.Close()
	waitFor(t, done)
	return resp
}

func assertRPCError(t *testing.T, resp map[string]any, wantCode int) {
	t.Helper()
	errBody, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("want JSON-RPC error, got %v", resp)
	}
	if code, _ := errBody["code"].(float64); int(code) != wantCode {
		t.Fatalf("error code = %v (message %v), want %d", errBody["code"], errBody["message"], wantCode)
	}
}

func TestRPCApprovalAnswerRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name    string
		granted bool
	}{
		{"allow", true},
		{"deny", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := security.NewPendingStore(t.TempDir())
			if err := store.Add(security.PendingApproval{
				ID: "call_42", Session: "sess-1", Tool: "bash",
				ArgsDigest: "abcd", Created: "2026-01-01T00:00:00Z",
			}); err != nil {
				t.Fatalf("seed pending approval: %v", err)
			}

			hub := NewEventHub()
			sub, stop := hub.Subscribe(0)
			defer stop()
			runner := &approvalAnswerRunner{pending: store, hub: hub}

			resp := rpcApprovalAnswer(t, Config{Runner: runner, Events: hub}, map[string]any{
				"session": "sess-1", "id": "call_42", "granted": tc.granted,
			})
			body, _ := resp["result"].(map[string]any)
			if body["ok"] != true {
				t.Fatalf("approval.answer result = %v, want ok:true", resp)
			}

			// Resolve 必须从持久镜像中移除待处理项。
			items, err := store.List("sess-1")
			if err != nil {
				t.Fatalf("list pending after answer: %v", err)
			}
			if len(items) != 0 {
				t.Fatalf("pending item survived approval.answer: %+v", items)
			}

			// 服务器在共享 hub 上发布 resolved 通知，使
			// 原生桥（纯文本）可以撤下审批卡片。
			select {
			case ev := <-sub:
				kind, tool, id, granted, ok := agent.ParseApprovalEvent(ev)
				if ev.Kind != agent.EventNotice || !ok || kind != "resolved" ||
					tool != "bash" || id != "call_42" || granted != tc.granted {
					t.Fatalf("resolved notice = %+v (kind=%q tool=%q id=%q granted=%v ok=%v)",
						ev, kind, tool, id, granted, ok)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("no approval:resolved notice published")
			}
		})
	}
}

func TestRPCApprovalAnswerRejectsUnknownID(t *testing.T) {
	// 待批镜像里只有 call_42；攻击者猜测 bash-1 不得翻转决定，
	// 也不得发布 approval:resolved 事件（原生端据此撤下审批卡片）。
	store := security.NewPendingStore(t.TempDir())
	if err := store.Add(security.PendingApproval{
		ID: "call_42", Session: "sess-1", Tool: "bash",
		ArgsDigest: "abcd", Created: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed pending approval: %v", err)
	}

	hub := NewEventHub()
	sub, stop := hub.Subscribe(0)
	defer stop()
	runner := &approvalAnswerRunner{pending: store, hub: hub}

	resp := rpcApprovalAnswer(t, Config{Runner: runner, Events: hub}, map[string]any{
		"session": "sess-1", "id": "bash-1", "granted": true,
	})
	assertRPCError(t, resp, codeInvalidParams)

	// 猜测的决定没有进入待批镜像：原待批项仍在。
	items, err := store.List("sess-1")
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(items) != 1 || items[0].ID != "call_42" {
		t.Fatalf("pending mirror changed by unknown id answer: %+v", items)
	}

	// 没有发布任何事件：seq 仍为 0，且订阅通道为空。
	if seq := hub.Seq(); seq != 0 {
		t.Fatalf("event hub seq = %d, want 0 (no event should be published)", seq)
	}
	select {
	case ev := <-sub:
		t.Fatalf("unexpected event published for unknown id: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRPCApprovalAnswerCJKID(t *testing.T) {
	// CJK 的 id/session 必须按精确匹配通过校验、正常 Resolve，
	// 并在 resolved 事件中保留原样的 tool/id 供原生端配对。
	const cjkID = "审批-四十二"
	const cjkSession = "会话-1"
	store := security.NewPendingStore(t.TempDir())
	if err := store.Add(security.PendingApproval{
		ID: cjkID, Session: cjkSession, Tool: "bash",
		ArgsDigest: "digest", Created: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed pending approval: %v", err)
	}

	hub := NewEventHub()
	sub, stop := hub.Subscribe(0)
	defer stop()
	runner := &approvalAnswerRunner{pending: store, hub: hub}

	resp := rpcApprovalAnswer(t, Config{Runner: runner, Events: hub}, map[string]any{
		"session": cjkSession, "id": cjkID, "granted": false,
	})
	body, _ := resp["result"].(map[string]any)
	if body["ok"] != true {
		t.Fatalf("approval.answer result = %v, want ok:true", resp)
	}

	items, err := store.List(cjkSession)
	if err != nil {
		t.Fatalf("list pending after answer: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("pending item survived approval.answer: %+v", items)
	}

	select {
	case ev := <-sub:
		kind, tool, id, granted, ok := agent.ParseApprovalEvent(ev)
		if ev.Kind != agent.EventNotice || !ok || kind != "resolved" ||
			tool != "bash" || id != cjkID || granted != false {
			t.Fatalf("resolved notice = %+v (kind=%q tool=%q id=%q granted=%v ok=%v)",
				ev, kind, tool, id, granted, ok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no approval:resolved notice published")
	}
}

func TestRPCApprovalAnswerWithoutStore(t *testing.T) {
	// 普通的 fakeRunner 不暴露 PendingStore()：该方法必须报告
	// 结构化的错误，而不是 panic。
	resp := rpcApprovalAnswer(t, Config{Runner: &fakeRunner{}}, map[string]any{
		"id": "call_1", "granted": true,
	})
	assertRPCError(t, resp, codeServerError)
}

func TestRPCApprovalAnswerInvalidParams(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params any
	}{
		{"missing id", map[string]any{"granted": true}},
		{"empty id", map[string]any{"id": "  ", "granted": true}},
		{"missing granted", map[string]any{"id": "call_1"}},
		{"wrong params type", "not-an-object"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := rpcApprovalAnswer(t, Config{Runner: &fakeRunner{}}, tc.params)
			assertRPCError(t, resp, codeInvalidParams)
		})
	}
}
