package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/security"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// countingReader 代替 os.Stdin。它记录每一次 Read，使测试能
// 证明无界面审批器从不触碰 RPC 服务拥有的流，并
// 保持未读字节完整，使被窃取的 RPC 帧也能被看到。
type countingReader struct {
	mu    sync.Mutex
	data  []byte
	reads atomic.Int32
}

func newCountingReader(body string) *countingReader {
	return &countingReader{data: []byte(body)}
}

func (r *countingReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads.Add(1)
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func (r *countingReader) readCount() int32 { return r.reads.Load() }

func (r *countingReader) remaining() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.data)
}

// captureApproverExecute 是运行器的 Execute 接缝：它记录运行器为该次运行安装的
// 审批器，并在不调用真实模型的情况下返回。
func captureApproverExecute(ch chan<- security.Approver) func(context.Context, cli.IO, Resolved, *session.Session, string, func(string)) (agent.Result, error) {
	return func(_ context.Context, _ cli.IO, res Resolved, _ *session.Session, _ string, _ func(string)) (agent.Result, error) {
		ch <- res.Approve
		return agent.Result{}, nil
	}
}

// approvalTestRunner 用给定的传输标志构建运行器，驱动一次
// Prompt，并返回运行器、其待处理镜像以及一个通道，
// 该通道携带为该次运行解析出的审批器。
func approvalTestRunner(t *testing.T, opts cli.Options, in io.Reader) (*runner, *security.PendingStore, <-chan security.Approver) {
	t.Helper()
	env, _ := testEnv(t)
	opts.Cwd = t.TempDir()
	res, err := Resolve(cli.IO{Env: env}, opts)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	captured := make(chan security.Approver, 1)
	pending := security.NewPendingStore(t.TempDir())
	r := &runner{
		stdio:   cli.IO{In: in, Env: env},
		res:     res,
		store:   testStore(t, res),
		pending: pending,
		execute: captureApproverExecute(captured),
	}
	if _, err := r.Prompt(context.Background(), "approve test", nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	return r, pending, captured
}

// TestHeadlessApproverNeverReadsStdinAndHonoursExternalAnswer 是针对共享 stdin
// 隐患的回归测试：--rpc/--serve/--acp 解析出的审批器
// 绝不能从 stdin 读取一个字节，因为 server.scanLines 正在从同一流中读取
// JSON-RPC 帧。操作人员的决定通过 Resolve 到达。
func TestHeadlessApproverNeverReadsStdinAndHonoursExternalAnswer(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts cli.Options
	}{
		{name: "rpc", opts: cli.Options{RPC: true, Approval: cli.ApprovalAsk}},
		{name: "serve", opts: cli.Options{Serve: true, Approval: cli.ApprovalAsk}},
		{name: "acp", opts: cli.Options{ACP: true, Approval: cli.ApprovalAsk}},
		{name: "rpc auto", opts: cli.Options{RPC: true, Approval: cli.ApprovalAuto}},
	} {
		for _, answer := range []struct {
			name    string
			granted bool
		}{
			{name: "allow", granted: true},
			{name: "deny", granted: false},
		} {
			t.Run(tc.name+"/"+answer.name, func(t *testing.T) {
				// stdin 中有一行看似合理的本地 "y"。PromptApprover 会消费它；
				// 无界面审批器必须保持其原样不动。
				in := newCountingReader("y\n")
				r, pending, captured := approvalTestRunner(t, tc.opts, in)
				sessionID := r.SessionID()
				approver := <-captured
				if approver == nil {
					t.Fatal("runner installed a nil approver")
				}
				if _, ok := approver.(*pendingApprover); !ok {
					t.Fatalf("headless approver type = %T, want *pendingApprover", approver)
				}

				resCh := make(chan askOutcome, 1)
				go func() {
					granted, err := approver.Ask(context.Background(), "bash", `{"command":"ls"}`)
					resCh <- askOutcome{granted: granted, err: err}
				}()

				id := waitForPendingID(t, pending, sessionID)
				// 尚无外部答案：在审批待处理期间，stdin 必须完全空闲，
				// 缓冲的那一行保持完整。
				if got := in.readCount(); got != 0 {
					t.Fatalf("stdin reads while pending = %d, want 0", got)
				}
				if got := in.remaining(); got != "y\n" {
					t.Fatalf("stdin after pending = %q, want %q (an RPC frame would be stolen)", got, "y\n")
				}

				if err := pending.Resolve(sessionID, id, answer.granted); err != nil {
					t.Fatalf("Resolve: %v", err)
				}
				select {
				case got := <-resCh:
					if got.err != nil || got.granted != answer.granted {
						t.Fatalf("Ask = %v, %v; want %v, nil", got.granted, got.err, answer.granted)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("headless Ask did not unblock on the external answer")
				}
				if got := in.readCount(); got != 0 {
					t.Fatalf("stdin reads after answer = %d, want 0", got)
				}
				if got := in.remaining(); got != "y\n" {
					t.Fatalf("stdin after answer = %q, want %q", got, "y\n")
				}
				if items, err := pending.List(sessionID); err != nil || len(items) != 0 {
					t.Fatalf("pending after answer = %v, %v; want empty", items, err)
				}
			})
		}
	}
}

// TestHeadlessApproverCancellationUnblocksWithoutReadingStdin 覆盖无人回答的情形：
// 运行 context 必须释放 Ask，且 stdin
// 仍不得被触碰。
func TestHeadlessApproverCancellationUnblocksWithoutReadingStdin(t *testing.T) {
	in := newCountingReader("y\n")
	r, pending, captured := approvalTestRunner(t, cli.Options{RPC: true}, in)
	sessionID := r.SessionID()
	approver := <-captured

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resCh := make(chan askOutcome, 1)
	go func() {
		granted, err := approver.Ask(ctx, "bash", `{"command":"sleep 1"}`)
		resCh <- askOutcome{granted: granted, err: err}
	}()
	waitForPendingID(t, pending, sessionID)
	cancel()
	select {
	case got := <-resCh:
		if got.err == nil || got.granted {
			t.Fatalf("cancelled Ask = %v, %v; want false, non-nil error", got.granted, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("headless Ask did not unblock on ctx cancellation")
	}
	if got := in.readCount(); got != 0 {
		t.Fatalf("stdin reads after cancellation = %d, want 0", got)
	}
}

// TestCLIDirectApproverStillReadsLocalYn proves the non-headless path is byte-
// 直接的 CLI 运行仍在操作人员的 tty 上询问 y/n。
func TestCLIDirectApproverStillReadsLocalYn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		input   string
		granted bool
	}{
		{name: "yes", input: "y\n", granted: true},
		{name: "lowercase word", input: "yes\n", granted: true},
		{name: "no", input: "n\n", granted: false},
		{name: "empty line", input: "\n", granted: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := newCountingReader(tc.input)
			r, pending, captured := approvalTestRunner(t, cli.Options{}, in)
			approver := <-captured
			granted, err := approver.Ask(context.Background(), "bash", `{"command":"pwd"}`)
			if err != nil || granted != tc.granted {
				t.Fatalf("Ask = %v, %v; want %v, nil", granted, err, tc.granted)
			}
			if got := in.readCount(); got == 0 {
				t.Fatal("direct CLI approver never read stdin")
			}
			if items, err := pending.List(r.SessionID()); err != nil || len(items) != 0 {
				t.Fatalf("pending after local answer = %v, %v; want empty", items, err)
			}
		})
	}
}

// TestHeadlessApproverNilStoreIsUnavailable 锁定空对象行为：
// 没有待处理镜像时根本没有答案通道。这是不可用的审批通道，
// 而非操作人员的拒绝——对于可能恢复的通道，模型绝不能
// 收到 "最终、不可重试" 的提示。
func TestHeadlessApproverNilStoreIsUnavailable(t *testing.T) {
	approver := WithHeadlessPendingApprovals(nil, "s")
	if _, ok := approver.(security.UnavailableApprover); !ok {
		t.Fatalf("nil-store headless approver = %T, want security.UnavailableApprover", approver)
	}
	granted, err := approver.Ask(context.Background(), "bash", `{}`)
	if granted {
		t.Fatal("an unavailable approval channel must not grant")
	}
	if !errors.Is(err, security.ErrApprovalUnavailable) {
		t.Fatalf("Ask error = %v, want ErrApprovalUnavailable", err)
	}
	if outcome := security.ClassifyApproval(granted, err); outcome != security.ApprovalUnavailable {
		t.Fatalf("ClassifyApproval = %q, want %q", outcome, security.ApprovalUnavailable)
	}
	if errors.Is(err, security.ErrRefused) || errors.Is(err, security.ErrDenied) {
		t.Fatalf("unavailable channel must not be a refusal or a policy deny: %v", err)
	}
	if strings.Contains(err.Error(), "This block is intentional and final") {
		t.Fatalf("unavailable channel must not carry finality: %q", err.Error())
	}
}

// TestNilStoreHeadlessChainAuditsUnavailableNotRefused 把 nil 存储的无界面审批器
// 接入真实链。审计必须记录 "unavailable"，且
// 返回的工具错误不得带有拒绝/终结性的措辞，这与显式配置的
// NeverApprover 不同。
func TestNilStoreHeadlessChainAuditsUnavailableNotRefused(t *testing.T) {
	root := t.TempDir()
	audit := security.NewAuditLog(root)
	chain := security.NewChain(security.DefaultPolicy(root), WithHeadlessPendingApprovals(nil, "s"), audit, "s")
	err := chain.Check(context.Background(), "write", json.RawMessage(`{"path":"a.txt"}`))
	if err == nil {
		t.Fatal("an unavailable approval channel must fail closed")
	}
	if !errors.Is(err, security.ErrApprovalUnavailable) {
		t.Fatalf("Check error = %v, want ErrApprovalUnavailable", err)
	}
	if errors.Is(err, security.ErrRefused) || errors.Is(err, security.ErrDenied) {
		t.Fatalf("unavailable channel must not be a refusal or a policy deny: %v", err)
	}
	var stop *security.StopError
	if errors.As(err, &stop) {
		t.Fatalf("unavailable channel must not be a StopError: %#v", err)
	}
	if strings.Contains(err.Error(), "This block is intentional and final") {
		t.Fatalf("unavailable channel must not carry finality: %q", err.Error())
	}
	entries, readErr := security.ReadAudit(audit.Path())
	if readErr != nil || len(entries) != 1 {
		t.Fatalf("audit: %v entries=%d", readErr, len(entries))
	}
	if entries[0].Decision != "unavailable" {
		t.Fatalf("audit entry = %+v, want decision unavailable", entries[0])
	}
	if strings.Contains(entries[0].Reason, "refused") {
		t.Fatalf("audit reason must not claim a refusal: %q", entries[0].Reason)
	}
}

// TestExplicitApproversKeepTheirVerdict 证明修复仅限于 nil 通道：
// 显式配置的拒绝仍是最终的 StopError，并审计为
// "refused"，显式的批准仍被允许。通过
// WithPendingApprovals 的镜像对这两种决定都必须保持透明。
func TestExplicitApproversKeepTheirVerdict(t *testing.T) {
	for _, tc := range []struct {
		name      string
		approver  security.Approver
		wantErr   bool
		wantAudit string
	}{
		{name: "explicit never", approver: security.NeverApprover{}, wantErr: true, wantAudit: "refused"},
		{name: "explicit grant", approver: &countingApprover{grant: true}, wantErr: false, wantAudit: "allow"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			audit := security.NewAuditLog(root)
			approver := WithPendingApprovals(security.NewPendingStore(root), "s", tc.approver)
			chain := security.NewChain(security.DefaultPolicy(root), approver, audit, "s")
			err := chain.Check(context.Background(), "write", json.RawMessage(`{"path":"a.txt"}`))
			if tc.wantErr {
				if err == nil {
					t.Fatal("an explicit refusal must fail the call")
				}
				if !errors.Is(err, security.ErrRefused) || errors.Is(err, security.ErrDenied) {
					t.Fatalf("got %v, want ErrRefused only", err)
				}
				var stop *security.StopError
				if !errors.As(err, &stop) || !stop.Refused {
					t.Fatalf("got %v, want a refusing StopError", err)
				}
				if !strings.Contains(err.Error(), "This block is intentional and final") {
					t.Fatalf("a real refusal must carry finality: %q", err.Error())
				}
				if errors.Is(err, security.ErrApprovalUnavailable) {
					t.Fatalf("a real refusal must not be an unavailable channel: %v", err)
				}
			} else if err != nil {
				t.Fatalf("an explicit grant Check = %v, want nil", err)
			}
			entries, readErr := security.ReadAudit(audit.Path())
			if readErr != nil || len(entries) != 1 {
				t.Fatalf("audit: %v entries=%d", readErr, len(entries))
			}
			if entries[0].Decision != tc.wantAudit {
				t.Fatalf("audit entry = %+v, want decision %q", entries[0], tc.wantAudit)
			}
		})
	}
}
