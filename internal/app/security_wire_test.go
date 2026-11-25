package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/security"
	"example.com/argentgo/internal/server"
)

// countingApprover 记录操作人员被询问的次数。
type countingApprover struct {
	calls int
	grant bool
	err   error
}

func (a *countingApprover) Ask(ctx context.Context, _, _ string) (bool, error) {
	a.calls++
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if a.err != nil {
		return false, a.err
	}
	return a.grant, nil
}

// verdictInspector 是供接线测试使用的可控 security.Inspector。
type verdictInspector struct {
	name    string
	verdict security.Verdict
	err     error
}

func (i verdictInspector) Name() string { return i.name }

func (i verdictInspector) Inspect(ctx context.Context, _ string, _ json.RawMessage) (security.Verdict, error) {
	if err := ctx.Err(); err != nil {
		return security.Verdict{}, err
	}
	return i.verdict, i.err
}

func managerWith(inspectors ...security.Inspector) *security.InspectionManager {
	m := &security.InspectionManager{}
	for _, in := range inspectors {
		m.Add(in)
	}
	return m
}

func askChain(root string, approver security.Approver) *security.Chain {
	policy := security.NewPolicy(root, security.PolicyConfig{
		Rules: []security.Rule{{Decision: security.Ask, Tool: "bash"}},
	})
	return security.NewChain(policy, approver, nil, "s1")
}

func TestInspectionGuardDenyIsHardStop(t *testing.T) {
	approver := &countingApprover{grant: true}
	chainApprover := &countingApprover{grant: false}
	guard := &inspectionGuard{
		manager: managerWith(verdictInspector{
			name:    "deny",
			verdict: security.Verdict{Action: security.InspectDeny, Reason: "inspector deny", Confidence: 1, Inspector: "deny"},
		}),
		chain:    askChain(t.TempDir(), chainApprover),
		approver: approver,
	}

	err := guard.Check(context.Background(), "bash", json.RawMessage(`{"command":"echo hi"}`))
	var stop *security.StopError
	if !errors.As(err, &stop) {
		t.Fatalf("err = %v, want *security.StopError", err)
	}
	if stop.Refused {
		t.Fatal("an inspector deny must not be reported as an operator refusal")
	}
	if !strings.Contains(stop.Reason, "inspector deny") {
		t.Fatalf("stop reason = %q, want the inspector reason", stop.Reason)
	}
	if approver.calls != 0 || chainApprover.calls != 0 {
		t.Fatalf("approver calls = %d, chain calls = %d; want 0/0", approver.calls, chainApprover.calls)
	}
}

func TestInspectionGuardRequireApprovalRefusedIsStopError(t *testing.T) {
	approver := &countingApprover{grant: false}
	chainApprover := &countingApprover{grant: true}
	guard := &inspectionGuard{
		manager: managerWith(verdictInspector{
			name:    "approval",
			verdict: security.Verdict{Action: security.InspectRequireApproval, Reason: "needs a human", Confidence: 1, Inspector: "approval"},
		}),
		chain:    askChain(t.TempDir(), chainApprover),
		approver: approver,
	}

	err := guard.Check(context.Background(), "bash", json.RawMessage(`{"command":"echo hi"}`))
	var stop *security.StopError
	if !errors.As(err, &stop) || !stop.Refused {
		t.Fatalf("err = %v, want a refused *security.StopError", err)
	}
	if approver.calls != 1 {
		t.Fatalf("approver called %d times, want 1", approver.calls)
	}
	if chainApprover.calls != 0 {
		t.Fatal("the chain must not run after a refusal")
	}
}

func TestInspectionGuardRequireApprovalAskedOnce(t *testing.T) {
	approver := &countingApprover{grant: true}
	// 链自身的审批器拒绝：若 Chain.Check 再次询问，调用会失败。
	// 一个固定的重放审批器必须阻止这第二次提示。
	chainApprover := &countingApprover{grant: false}
	guard := &inspectionGuard{
		manager: managerWith(verdictInspector{
			name:    "approval",
			verdict: security.Verdict{Action: security.InspectRequireApproval, Reason: "needs a human", Confidence: 1, Inspector: "approval"},
		}),
		chain:    askChain(t.TempDir(), chainApprover),
		approver: approver,
	}

	if err := guard.Check(context.Background(), "bash", json.RawMessage(`{"command":"echo hi"}`)); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if approver.calls != 1 {
		t.Fatalf("approver called %d times, want exactly 1", approver.calls)
	}
	if chainApprover.calls != 0 {
		t.Fatal("chain re-prompted via its own approver")
	}
}

func TestInspectionGuardAllowFallsThroughToChain(t *testing.T) {
	approver := &countingApprover{grant: true}
	chainApprover := &countingApprover{grant: false}
	guard := &inspectionGuard{
		manager: managerWith(verdictInspector{
			name:    "allow",
			verdict: security.Verdict{Action: security.InspectAllow, Confidence: 1, Inspector: "allow"},
		}),
		chain:    askChain(t.TempDir(), chainApprover),
		approver: approver,
	}

	err := guard.Check(context.Background(), "bash", json.RawMessage(`{"command":"echo hi"}`))
	var stop *security.StopError
	if !errors.As(err, &stop) || !stop.Refused {
		t.Fatalf("Allow must still run the chain (want chain refusal), got %v", err)
	}
	if approver.calls != 0 {
		t.Fatalf("an Allow verdict asked the approver %d times", approver.calls)
	}
	if chainApprover.calls != 1 {
		t.Fatalf("chain approver called %d times, want 1", chainApprover.calls)
	}
}

func TestDestructiveCommandInspectorVerdicts(t *testing.T) {
	inspector := destructiveCommandInspector{}
	ctx := context.Background()

	safe, _ := inspector.Inspect(ctx, "bash", json.RawMessage(`{"command":"echo hi"}`))
	if safe.Action != security.InspectAllow {
		t.Fatalf("read-only shell verdict = %v, want Allow", safe.Action)
	}
	bad, _ := inspector.Inspect(ctx, "bash", json.RawMessage(`{"command":"rm -rf /tmp/x"}`))
	if bad.Action != security.InspectRequireApproval {
		t.Fatalf("destructive shell verdict = %v, want RequireApproval", bad.Action)
	}
	// 带有 "command" 字段的非 shell 工具不是 shell 命令。
	nonShell, _ := inspector.Inspect(ctx, "read", json.RawMessage(`{"command":"rm -rf /tmp/x"}`))
	if nonShell.Action != security.InspectAllow {
		t.Fatalf("non-shell verdict = %v, want Allow", nonShell.Action)
	}
}

func TestSecurityGatesDefaultOff(t *testing.T) {
	if inspectionEnabled(nil) || inspectionEnabled(envFrom(map[string]string{"ARGENT_INSPECTION": "0"})) {
		t.Fatal("inspection must be default-off")
	}
	if !inspectionEnabled(envFrom(map[string]string{"ARGENT_INSPECTION": "1"})) {
		t.Fatal("ARGENT_INSPECTION=1 must enable inspection")
	}
	if _, on := ttlApprovalGate(nil); on {
		t.Fatal("TTL approval must be default-off with no env")
	}
	if _, on := ttlApprovalGate(envFrom(map[string]string{"ARGENT_APPROVAL_TTL": "0"})); on {
		t.Fatal("ARGENT_APPROVAL_TTL=0 must leave the gate off")
	}
	if d, on := ttlApprovalGate(envFrom(map[string]string{"ARGENT_APPROVAL_TTL": "15m"})); !on || d != 15*time.Minute {
		t.Fatalf("duration gate = (%v, %v), want (15m, true)", d, on)
	}
	if d, on := ttlApprovalGate(envFrom(map[string]string{"ARGENT_APPROVAL_TTL": "5"})); !on || d != 5*time.Minute {
		t.Fatalf("integer gate = (%v, %v), want (5m, true)", d, on)
	}
}

func TestApproverForTTLReplacesCachedApprover(t *testing.T) {
	res := Resolved{Storage: t.TempDir(), Approval: "ask"}
	on := cli.IO{Env: envFrom(map[string]string{"ARGENT_APPROVAL_TTL": "1h"})}
	off := cli.IO{Env: envFrom(map[string]string{})}

	if _, ok := approverFor(on, res).(*ttlApprover); !ok {
		t.Fatalf("gate on: got %T, want *ttlApprover", approverFor(on, res))
	}
	if _, ok := approverFor(off, res).(*security.CachedApprover); !ok {
		t.Fatalf("gate off: got %T, want *security.CachedApprover (unchanged behaviour)", approverFor(off, res))
	}
}

func TestTTLApproverCachesGrantsButNotRefusals(t *testing.T) {
	ctx := context.Background()
	inner := &countingApprover{grant: true}
	a := &ttlApprover{cache: security.NewTTLCache("", time.Hour), inner: inner}

	for i := 0; i < 2; i++ {
		ok, err := a.Ask(ctx, "write", `{"path":"a.txt"}`)
		if err != nil || !ok {
			t.Fatalf("grant call %d = (%v, %v)", i, ok, err)
		}
	}
	if inner.calls != 1 {
		t.Fatalf("inner called %d times for an identical grant, want 1", inner.calls)
	}
	if ok, _ := a.Ask(ctx, "write", `{"path":"b.txt"}`); !ok {
		t.Fatal("distinct args must be approved independently")
	}
	if inner.calls != 2 {
		t.Fatalf("inner called %d times for distinct args, want 2", inner.calls)
	}

	denier := &countingApprover{grant: false}
	refuser := &ttlApprover{cache: security.NewTTLCache("", time.Hour), inner: denier}
	for i := 0; i < 2; i++ {
		ok, err := refuser.Ask(ctx, "write", `{"path":"c.txt"}`)
		if err != nil || ok {
			t.Fatalf("refusal call %d = (%v, %v)", i, ok, err)
		}
	}
	if denier.calls != 2 {
		t.Fatalf("refusal was cached: inner called %d times, want 2", denier.calls)
	}
}

// pendingRunner 是一个 server.Runner，同时通过 approval.list 发现的可选访问器
// 暴露持久化的待处理存储。
type pendingRunner struct {
	pending *security.PendingStore
}

func (r *pendingRunner) Prompt(context.Context, string, func(string)) (string, error) { return "", nil }
func (r *pendingRunner) Abort()                                                       {}
func (r *pendingRunner) PendingStore() *security.PendingStore                         { return r.pending }

func TestRPCApprovalListReturnsPending(t *testing.T) {
	store := security.NewPendingStore(filepath.Join(t.TempDir(), "pending"))
	if err := store.Add(security.PendingApproval{
		ID:         "tool-1",
		Session:    "s1",
		Tool:       "bash",
		ArgsDigest: security.PendingArgsDigest(`{"command":"rm -rf /tmp/x"}`),
	}); err != nil {
		t.Fatalf("add pending: %v", err)
	}

	in := bytes.NewBufferString(`{"jsonrpc":"2.0","id":7,"method":"approval.list","params":{"session":"s1"}}` + "\n")
	var out bytes.Buffer
	if err := server.RunRPC(context.Background(), in, &out, server.Config{Runner: &pendingRunner{pending: store}}); err != nil {
		t.Fatalf("RunRPC: %v", err)
	}

	var resp struct {
		ID     json.RawMessage `json:"id"`
		Error  json.RawMessage `json:"error"`
		Result struct {
			Pending []security.PendingApproval `json:"pending"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", out.String(), err)
	}
	if len(resp.Error) != 0 {
		t.Fatalf("error response: %s", resp.Error)
	}
	if string(resp.ID) != "7" {
		t.Fatalf("id = %s, want 7", resp.ID)
	}
	if len(resp.Result.Pending) != 1 {
		t.Fatalf("pending = %+v, want one item", resp.Result.Pending)
	}
	got := resp.Result.Pending[0]
	if got.ID != "tool-1" || got.Session != "s1" || got.Tool != "bash" {
		t.Fatalf("pending item = %+v", got)
	}
}

func TestRPCApprovalListWithoutStoreIsServerError(t *testing.T) {
	in := bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"approval.list"}` + "\n")
	var out bytes.Buffer
	if err := server.RunRPC(context.Background(), in, &out, server.Config{Runner: &pendingRunner{}}); err != nil {
		t.Fatalf("RunRPC: %v", err)
	}
	var resp struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", out.String(), err)
	}
	if resp.Error.Code != -32000 {
		t.Fatalf("error code = %d, want -32000", resp.Error.Code)
	}
}
