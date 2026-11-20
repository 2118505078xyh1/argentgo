package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/security"
	"github.com/2118505078xyh1/argentgo/internal/server"
)

// gatedApprover is a local approver that announces when it has been engaged
// and then blocks until the test releases a y/n answer (or ctx is cancelled).
// It models both a real TUI popup and, by never being released, a headless run
// where no human answers at stdin.
type gatedApprover struct {
	called chan struct{}
	local  chan bool
}

func (g *gatedApprover) Ask(ctx context.Context, _, _ string) (bool, error) {
	select {
	case g.called <- struct{}{}:
	default:
	}
	select {
	case granted := <-g.local:
		return granted, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func newGatedApprover() *gatedApprover {
	return &gatedApprover{called: make(chan struct{}, 1), local: make(chan bool, 1)}
}

type askOutcome struct {
	granted bool
	err     error
}

// waitForPendingID polls the durable mirror until Ask has persisted its
// request; it returns the correlation id an approval.answer must carry.
func waitForPendingID(t *testing.T, store *security.PendingStore, session string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		items, err := store.List(session)
		if err != nil {
			t.Fatalf("List pending: %v", err)
		}
		if len(items) == 1 {
			return items[0].ID
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("pending approval never appeared on disk")
	return ""
}

func TestPendingApproverExternalAnswerUnblocksAsk(t *testing.T) {
	for _, tc := range []struct {
		name    string
		granted bool
	}{
		{name: "allow", granted: true},
		{name: "deny", granted: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			store := security.NewPendingStore(base)
			const session = "sess-rpc"
			inner := newGatedApprover() // never released: no human at stdin
			approver := WithPendingApprovals(store, session, inner)

			resCh := make(chan askOutcome, 1)
			go func() {
				granted, err := approver.Ask(context.Background(), "bash", `{"command":"ls"}`)
				resCh <- askOutcome{granted: granted, err: err}
			}()

			id := waitForPendingID(t, store, session)
			// Simulate approval.answer: the RPC handler resolves the decision.
			if err := store.Resolve(session, id, tc.granted); err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			select {
			case got := <-resCh:
				if got.err != nil {
					t.Fatalf("Ask returned error: %v", got.err)
				}
				if got.granted != tc.granted {
					t.Fatalf("Ask granted = %v, want %v", got.granted, tc.granted)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Ask did not unblock after approval.answer")
			}

			if items, err := store.List(session); err != nil || len(items) != 0 {
				t.Fatalf("pending after answer = %v, %v; want empty", items, err)
			}
			// Replaying the decision (a reconnect race) must be a safe no-op:
			// no fatal error and no duplicate decision on disk.
			if err := store.Resolve(session, id, !tc.granted); err != nil {
				t.Fatalf("replayed Resolve: %v", err)
			}
			logs, err := filepath.Glob(filepath.Join(base, "*.decisions.jsonl"))
			if err != nil || len(logs) != 1 {
				t.Fatalf("decision logs = %v, %v; want one", logs, err)
			}
			data, err := os.ReadFile(logs[0])
			if err != nil {
				t.Fatalf("ReadFile decision log: %v", err)
			}
			if lines := strings.Count(strings.TrimSpace(string(data)), "\n") + 1; lines != 1 {
				t.Fatalf("decision log has %d lines, want 1 (Resolve must be idempotent)", lines)
			}
		})
	}
}

func TestPendingApproverLocalAnswerStillWorks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		granted bool
	}{
		{name: "allow", granted: true},
		{name: "deny", granted: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := security.NewPendingStore(t.TempDir())
			const session = "sess-cli"
			inner := newGatedApprover()
			approver := WithPendingApprovals(store, session, inner)

			resCh := make(chan askOutcome, 1)
			go func() {
				granted, err := approver.Ask(context.Background(), "bash", `{"command":"pwd"}`)
				resCh <- askOutcome{granted: granted, err: err}
			}()

			// No external approval.answer: the local approver must decide.
			select {
			case <-inner.called:
			case <-time.After(2 * time.Second):
				t.Fatal("local approver was never engaged")
			}
			inner.local <- tc.granted
			select {
			case got := <-resCh:
				if got.err != nil || got.granted != tc.granted {
					t.Fatalf("Ask = %v, %v; want %v, nil", got.granted, got.err, tc.granted)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Ask did not return the local answer")
			}
			if items, err := store.List(session); err != nil || len(items) != 0 {
				t.Fatalf("pending after local answer = %v, %v; want empty", items, err)
			}
		})
	}
}

func TestPendingApproverCancelUnblocksAsk(t *testing.T) {
	store := security.NewPendingStore(t.TempDir())
	const session = "sess-cancel"
	inner := newGatedApprover()
	approver := WithPendingApprovals(store, session, inner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resCh := make(chan askOutcome, 1)
	go func() {
		granted, err := approver.Ask(ctx, "bash", `{"command":"sleep 1"}`)
		resCh <- askOutcome{granted: granted, err: err}
	}()
	waitForPendingID(t, store, session)
	cancel()
	select {
	case got := <-resCh:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("Ask error = %v, want context.Canceled", got.err)
		}
		if got.granted {
			t.Fatal("cancelled Ask granted the request")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ask did not unblock on ctx cancellation")
	}
}

// approvalAnswerRunner is the minimal server.Runner seam approval.answer needs:
// a durable pending store (EventHub is optional; selectHub makes one).
type approvalAnswerRunner struct {
	pending *security.PendingStore
}

func (r *approvalAnswerRunner) Prompt(context.Context, string, func(string)) (string, error) {
	return "", nil
}
func (r *approvalAnswerRunner) Abort()                               {}
func (r *approvalAnswerRunner) PendingStore() *security.PendingStore { return r.pending }

// TestPendingApproverApprovalAnswerEndToEnd drives a real approval.answer RPC
// through server.RunRPC, proving the full chain:
// approval.answer -> PendingStore.Resolve -> waiter -> pendingApprover.Ask.
func TestPendingApproverApprovalAnswerEndToEnd(t *testing.T) {
	for _, tc := range []struct {
		name    string
		granted bool
	}{
		{name: "allow", granted: true},
		{name: "deny", granted: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := security.NewPendingStore(t.TempDir())
			const session = "sess-rpc"
			inner := newGatedApprover() // headless: stdin never answers
			approver := WithPendingApprovals(store, session, inner)

			resCh := make(chan askOutcome, 1)
			go func() {
				granted, err := approver.Ask(context.Background(), "bash", `{"command":"ls"}`)
				resCh <- askOutcome{granted: granted, err: err}
			}()
			id := waitForPendingID(t, store, session)

			inR, inW := io.Pipe()
			outR, outW := io.Pipe()
			rpcDone := make(chan error, 1)
			go func() {
				rpcDone <- server.RunRPC(context.Background(), inR, outW, server.Config{
					Runner: &approvalAnswerRunner{pending: store},
				})
			}()

			req := map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"method":  "approval.answer",
				"params": map[string]any{
					"session": session, "id": id, "granted": tc.granted,
				},
			}
			if err := json.NewEncoder(inW).Encode(req); err != nil {
				t.Fatalf("encode approval.answer: %v", err)
			}
			var resp struct {
				Result struct {
					OK bool `json:"ok"`
				} `json:"result"`
				Error map[string]any `json:"error"`
			}
			if err := json.NewDecoder(outR).Decode(&resp); err != nil {
				t.Fatalf("decode approval.answer response: %v", err)
			}
			if resp.Error != nil {
				t.Fatalf("approval.answer error: %v", resp.Error)
			}
			if !resp.Result.OK {
				t.Fatalf("approval.answer result = %+v, want ok:true", resp.Result)
			}

			select {
			case got := <-resCh:
				if got.err != nil || got.granted != tc.granted {
					t.Fatalf("Ask = %v, %v; want %v, nil", got.granted, got.err, tc.granted)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Ask did not unblock after the approval.answer RPC")
			}

			_ = inW.Close()
			select {
			case err := <-rpcDone:
				if err != nil {
					t.Fatalf("RunRPC: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("RunRPC did not exit after input closed")
			}
		})
	}
}
