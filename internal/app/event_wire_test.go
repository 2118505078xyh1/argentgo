package app

import (
	"context"
	"testing"
	"time"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/security"
)

// TestWireRunEventsPublishesToHubSubscribers 证明无界面传输所依赖的接线契约：
// 通过运行 context 转发的 agent.Event 能到达 hub 订阅者，
// 且桥接器会随运行 context 一起停止。
func TestWireRunEventsPublishesToHubSubscribers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hub := NewEventHub()
	emit, done := WireRunEvents(hub, ctx)
	// 这正是 app_execute.go 的 Emit 回调所使用的 context 交接方式。
	ctx = withEventEmit(ctx, emit)

	ch, stop := hub.Subscribe(0)
	defer stop()

	want := agent.Event{Kind: agent.EventNotice, Text: `approval:request {"v":1,"tool":"bash","digest":"d","id":"c"}`}
	EmitEvent(ctx, want)

	select {
	case got := <-ch:
		if got.Kind != want.Kind || got.Text != want.Text {
			t.Fatalf("subscriber got %+v, want %+v", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event fed through WireRunEvents was not observable via hub.Subscribe")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("bridge goroutine did not stop when the run context was cancelled")
	}
}

// TestEmitEventWithoutBridgeIsNoOp 守护默认安全的契约：没有接入 hub 的调用方
// 保持仅在进程内的行为。
func TestEmitEventWithoutBridgeIsNoOp(t *testing.T) {
	emit, done := WireRunEvents(nil, context.Background())
	select {
	case <-done:
	default:
		t.Fatal("nil-hub bridge done channel must be closed")
	}
	emit(agent.Event{Kind: agent.EventNotice, Text: "ignored"})
	EmitEvent(context.Background(), agent.Event{Kind: agent.EventNotice, Text: "ignored"})
}

type fakeApprover struct {
	granted bool
	err     error
	calls   int
	onAsk   func()
}

func (f *fakeApprover) Ask(_ context.Context, _, _ string) (bool, error) {
	f.calls++
	if f.onAsk != nil {
		f.onAsk()
	}
	return f.granted, f.err
}

// TestPendingApprovalsMirrorRequestsAndResolveOnAnswer 通过伪审批器演练持久化
// 审批路径：询问操作人员期间请求在磁盘上，
// 答案记录后即被移除。
func TestPendingApprovalsMirrorRequestsAndResolveOnAnswer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		granted bool
	}{
		{name: "granted", granted: true},
		{name: "denied", granted: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := security.NewPendingStore(t.TempDir())
			const session = "sess-123"
			const tool = "write"
			args := `{"path":"a.txt","content":"secret"}`

			var during []security.PendingApproval
			inner := &fakeApprover{granted: tc.granted, onAsk: func() {
				var err error
				during, err = store.List(session)
				if err != nil {
					t.Errorf("List during Ask: %v", err)
				}
			}}

			approver := WithPendingApprovals(store, session, inner)
			granted, err := approver.Ask(context.Background(), tool, args)
			if err != nil {
				t.Fatalf("Ask returned error: %v", err)
			}
			if granted != tc.granted {
				t.Fatalf("granted = %v, want %v (decision must be unchanged)", granted, tc.granted)
			}
			if inner.calls != 1 {
				t.Fatalf("inner approver calls = %d, want 1", inner.calls)
			}
			if len(during) != 1 {
				t.Fatalf("pending requests while asking = %d, want 1", len(during))
			}
			got := during[0]
			if got.Tool != tool || got.Session != session || got.ID == "" {
				t.Errorf("pending record = %+v, want tool/session/id set", got)
			}
			if want := security.PendingArgsDigest(args); got.ArgsDigest != want {
				t.Errorf("ArgsDigest = %q, want %q", got.ArgsDigest, want)
			}
			if after, err := store.List(session); err != nil {
				t.Fatalf("List after answer: %v", err)
			} else if len(after) != 0 {
				t.Fatalf("pending requests after answer = %d, want 0", len(after))
			}
		})
	}
}

// TestPendingStoreClearDropsStaleRequests 覆盖运行器在运行开始时执行的清理，
// 使崩溃的运行不会留下幽灵审批。
func TestPendingStoreClearDropsStaleRequests(t *testing.T) {
	store := security.NewPendingStore(t.TempDir())
	const session = "sess-456"
	if err := store.Add(security.PendingApproval{ID: "old", Session: session, Tool: "bash"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if items, err := store.List(session); err != nil || len(items) != 1 {
		t.Fatalf("List before Clear = %v, %v; want one item", items, err)
	}
	if err := store.Clear(session); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if items, err := store.List(session); err != nil || len(items) != 0 {
		t.Fatalf("List after Clear = %v, %v; want empty", items, err)
	}
}

// TestWithPendingApprovalsDisabled 让该装饰器对没有持久化存储或没有内部审批器的
// 调用方保持可选。
func TestWithPendingApprovalsDisabled(t *testing.T) {
	inner := &fakeApprover{granted: true}
	if got := WithPendingApprovals(nil, "s", inner); got != inner {
		t.Errorf("nil store should return the inner approver, got %T", got)
	}
	if got := WithPendingApprovals(security.NewPendingStore(t.TempDir()), "s", nil); got != nil {
		t.Errorf("nil inner approver should disable mirroring, got %T", got)
	}
	if got := WithPendingApprovals(nil, "s", nil); got != nil {
		t.Errorf("nil store and inner should return nil, got %T", got)
	}
}
