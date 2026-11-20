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

// sessionRunner is fakeRunner plus the two optional seams the approval methods
// discover: a durable pending store and the session the runner is appending to.
// The native bridge sends approval.answer with only {id, granted} (no session),
// so the server must fall back to SessionID() to address the right store key.
type sessionRunner struct {
	fakeRunner
	pending *security.PendingStore
	session string
}

func (r *sessionRunner) PendingStore() *security.PendingStore { return r.pending }
func (r *sessionRunner) SessionID() string                    { return r.session }

// TestRPCApprovalAnswerWithoutSessionUsesRunnerSession guards the real native
// call shape: approval.answer {"id":..., "granted":...} carries no session. The
// pending item and the blocked approver's waiter are keyed by the runner's
// current session, so resolving under the empty session would be a silent
// no-op: the RPC still answers ok, but the waiting Ask never unblocks.
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

			// The in-process approver blocked in Ask must receive the decision.
			select {
			case got := <-answer:
				if got != tc.granted {
					t.Fatalf("waiter got %v, want %v", got, tc.granted)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("approval.answer did not wake the pending approver")
			}

			// The durable mirror must be resolved under the runner's session.
			items, err := store.List(session)
			if err != nil {
				t.Fatalf("list pending: %v", err)
			}
			if len(items) != 0 {
				t.Fatalf("pending item survived approval.answer: %+v", items)
			}

			// The resolved notice must carry the real tool name, which comes
			// from looking the id up in the same session.
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

// TestRPCApprovalListWithoutSessionUsesRunnerSession covers the sibling method:
// a reconnecting client that omits session must see the current session's
// pending items, not the empty session's. An explicit session still wins.
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
