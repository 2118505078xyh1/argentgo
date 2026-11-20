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

// approvalAnswerRunner is fakeRunner plus the two optional seams approval.answer
// discovers: a durable pending store and a shared event hub.
type approvalAnswerRunner struct {
	fakeRunner
	pending *security.PendingStore
	hub     *EventHub
}

func (r *approvalAnswerRunner) PendingStore() *security.PendingStore { return r.pending }

func (r *approvalAnswerRunner) EventHub() *EventHub { return r.hub }

// rpcApprovalAnswer sends one approval.answer request on a fresh transport and
// returns its single JSON-RPC reply.
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

			// Resolve must remove the pending item from the durable mirror.
			items, err := store.List("sess-1")
			if err != nil {
				t.Fatalf("list pending after answer: %v", err)
			}
			if len(items) != 0 {
				t.Fatalf("pending item survived approval.answer: %+v", items)
			}

			// The server publishes the resolved notice on the shared hub so the
			// native bridge (text-only) can retire the approval card.
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

func TestRPCApprovalAnswerWithoutStore(t *testing.T) {
	// A plain fakeRunner exposes no PendingStore(): the method must report a
	// structured error instead of panicking.
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
