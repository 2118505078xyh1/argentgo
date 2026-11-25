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

	"example.com/argentgo/internal/security"
	"example.com/argentgo/internal/server"
)

// gatedApprover 是一个本地审批器，它会在被调用时发出通知，
// 然后阻塞直到测试释放一个 y/n 答案（或 ctx 被取消）。
// 它既模拟真实的 TUI 弹窗，也通过永不释放来模拟无人通过 stdin 回答的
// 无界面运行。
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

// waitForPendingID 轮询持久化镜像，直到 Ask 持久化了它的请求；
// 它返回 approval.answer 必须携带的关联 id。
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
			inner := newGatedApprover() // 永不释放：stdin 处没有人类
			approver := WithPendingApprovals(store, session, inner)

			resCh := make(chan askOutcome, 1)
			go func() {
				granted, err := approver.Ask(context.Background(), "bash", `{"command":"ls"}`)
				resCh <- askOutcome{granted: granted, err: err}
			}()

			id := waitForPendingID(t, store, session)
			// 模拟 approval.answer：RPC 处理器解析该决定。
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
			// 重放该决定（重连竞态）必须是安全的空操作：
			// 没有致命错误，磁盘上也没有重复的决定。
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

			// 没有外部的 approval.answer：必须由本地审批器做决定。
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

// approvalAnswerRunner 是 approval.answer 所需的最小 server.Runner 接缝：
// 一个持久化的待处理存储（EventHub 可选；selectHub 会创建）。
type approvalAnswerRunner struct {
	pending *security.PendingStore
}

func (r *approvalAnswerRunner) Prompt(context.Context, string, func(string)) (string, error) {
	return "", nil
}
func (r *approvalAnswerRunner) Abort()                               {}
func (r *approvalAnswerRunner) PendingStore() *security.PendingStore { return r.pending }

// TestPendingApproverApprovalAnswerEndToEnd 通过 server.RunRPC 驱动一次真实的
// approval.answer RPC，证明完整链路：
// approval.answer -> PendingStore.Resolve -> waiter -> pendingApprover.Ask。
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
			inner := newGatedApprover() // 无界面：stdin 从不回答
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
