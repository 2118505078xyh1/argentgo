package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// 本文件是 R17 P1 的"死原语接线"回归测试：把计划模式与 accept-edits 的行为
// 作为一条独立的守卫链验证一遍。它只依赖生产 API（EnablePlanMode /
// PlanModeEnabled / ParseApprovalMode / WrapForMode），不触碰任何生产代码。
// 为避免与其它代理写的 plan_mode_test.go / accept_edits_test.go 中的辅助符号
// 撞名，这里的辅助类型与函数统一使用 pmg 前缀。

// pmgRequest 记录一次委托给底层审批器的调用，用于参数透传断言。
type pmgRequest struct {
	name string
	args string
}

// pmgApprover 是可并发安全使用的审批器替身。
type pmgApprover struct {
	mu      sync.Mutex
	answer  bool
	err     error
	calls   int
	records []pmgRequest
}

func (a *pmgApprover) Ask(_ context.Context, name, args string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	a.records = append(a.records, pmgRequest{name: name, args: args})
	return a.answer, a.err
}

func (a *pmgApprover) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

func (a *pmgApprover) recorded() []pmgRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]pmgRequest, len(a.records))
	copy(out, a.records)
	return out
}

// pmgOutcome 是一次链检查的可比较快照：错误字节、审批次数与审计决策序列。
type pmgOutcome struct {
	errText   string
	approvals int
	decisions []string
}

// pmgRunChain 构造一条默认策略链并跑一次 bash 检查，返回可比较的快照。
func pmgRunChain(t *testing.T, command string, setup func(*Chain)) pmgOutcome {
	t.Helper()
	dir := t.TempDir()
	approver := &pmgApprover{answer: true}
	audit := NewAuditLog(dir)
	chain := NewChain(DefaultPolicy(dir), approver, audit, "s1")
	if setup != nil {
		setup(chain)
	}
	raw, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	checkErr := chain.Check(context.Background(), "bash", raw)
	out := pmgOutcome{approvals: approver.count()}
	if checkErr != nil {
		out.errText = checkErr.Error()
	}
	entries, readErr := ReadAudit(audit.Path())
	if readErr != nil {
		t.Fatalf("read audit: %v", readErr)
	}
	for _, entry := range entries {
		out.decisions = append(out.decisions, entry.Decision)
	}
	return out
}

// pmgAssertSameOutcome 逐字段比较两份快照，确保"字节/决策不变"。
func pmgAssertSameOutcome(t *testing.T, baseline, got pmgOutcome) {
	t.Helper()
	if baseline.errText != got.errText ||
		baseline.approvals != got.approvals ||
		!reflect.DeepEqual(baseline.decisions, got.decisions) {
		t.Fatalf("behavior changed:\nbaseline=%+v\ngot=%+v", baseline, got)
	}
}

// TestPMGPlanModeDefaultMatchesBaseline 验证默认未启用时，显式关闭（新 API 或
// 旧字段）与从未触碰计划模式 API 的基线路径逐字节一致。nil 接收者同样安全。
func TestPMGPlanModeDefaultMatchesBaseline(t *testing.T) {
	t.Setenv("ARGENT_GUARDRAILS", "enforce")

	var nilChain *Chain
	nilChain.EnablePlanMode(true) // nil 接收者必须是无操作
	if PlanModeEnabled(nil) {
		t.Fatal("PlanModeEnabled(nil) must be false")
	}
	if PlanModeEnabled(NewChain(DefaultPolicy(t.TempDir()), nil, nil, "")) {
		t.Fatal("a fresh chain must report plan mode disabled")
	}

	for _, command := range []string{"rm -rf x", "git status", "Remove-Item -Force x", "go test ./..."} {
		t.Run(command, func(t *testing.T) {
			baseline := pmgRunChain(t, command, nil)
			pmgAssertSameOutcome(t, baseline, pmgRunChain(t, command, func(c *Chain) {
				c.EnablePlanMode(false)
			}))
			pmgAssertSameOutcome(t, baseline, pmgRunChain(t, command, func(c *Chain) {
				c.PlanCommandGuard = false
			}))
		})
	}
}

// TestPMGPlanModeBlocksMutatingWithReadableHint 验证启用后修改类命令在策略/审批/
// 审计之前被拒，返回可恢复的 ErrRecoverablePlanBlock，并携带稳定的可读提示。
func TestPMGPlanModeBlocksMutatingWithReadableHint(t *testing.T) {
	t.Setenv("ARGENT_GUARDRAILS", "enforce")
	dir := t.TempDir()
	approver := &pmgApprover{answer: true}
	audit := NewAuditLog(dir)
	chain := NewChain(DefaultPolicy(dir), approver, audit, "s1")
	chain.EnablePlanMode(true)
	if !PlanModeEnabled(chain) {
		t.Fatal("PlanModeEnabled must report true after EnablePlanMode(true)")
	}

	err := chain.Check(context.Background(), "bash", json.RawMessage(`{"command":"rm -rf x"}`))
	if err == nil {
		t.Fatal("a mutating command must be blocked in plan mode")
	}
	if !errors.Is(err, ErrRecoverablePlanBlock) {
		t.Fatalf("got %v, want ErrRecoverablePlanBlock", err)
	}
	if errors.Is(err, ErrDenied) || errors.Is(err, ErrRefused) {
		t.Fatalf("a recoverable block must not also be a hard denial: %v", err)
	}
	if !strings.Contains(err.Error(), "blocked in plan mode") {
		t.Fatalf("error %v lacks the stable plan-mode hint", err)
	}
	reason, ok := ReasonFromError(err)
	if !ok {
		t.Fatal("ReasonFromError must classify a plan block")
	}
	if reason.Kind != RefusalKindToolError || !reason.Recoverable {
		t.Fatalf("reason = %+v, want recoverable tool-error", reason)
	}
	if hint := reason.ModelHint(); !strings.Contains(hint, "重试") {
		t.Fatalf("ModelHint = %q, want retry guidance", hint)
	}
	if approver.count() != 0 {
		t.Fatalf("guard must run before approval, approver.calls = %d", approver.count())
	}
	entries, readErr := ReadAudit(audit.Path())
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("guard must run before audit, audit: %v entries=%d", readErr, len(entries))
	}
}

// TestPMGPlanModeReadOnlyPasses 验证计划模式不拦截只读命令：仍走正常审批与审计。
func TestPMGPlanModeReadOnlyPasses(t *testing.T) {
	t.Setenv("ARGENT_GUARDRAILS", "enforce")
	for _, command := range []string{"git status", "go test ./...", "Get-Content note.txt"} {
		t.Run(command, func(t *testing.T) {
			out := pmgRunChain(t, command, func(c *Chain) { c.EnablePlanMode(true) })
			if out.errText != "" {
				t.Fatalf("read-only command must pass in plan mode, error: %s", out.errText)
			}
			if out.approvals != 1 {
				t.Fatalf("approver.calls = %d, want 1", out.approvals)
			}
			if !reflect.DeepEqual(out.decisions, []string{"allow"}) {
				t.Fatalf("want one allow decision, got %v", out.decisions)
			}
		})
	}
}

// TestPMGPlanModeToggleRestores 验证关闭后恢复原路径、可再次开启；且旧字段与新
// API 按"或"关系共同决定是否启用守卫。
func TestPMGPlanModeToggleRestores(t *testing.T) {
	t.Setenv("ARGENT_GUARDRAILS", "enforce")
	dir := t.TempDir()
	chain := NewChain(DefaultPolicy(dir), &pmgApprover{answer: true}, NewAuditLog(dir), "s1")
	check := func() error {
		return chain.Check(context.Background(), "bash", json.RawMessage(`{"command":"rm -rf x"}`))
	}
	mustBlock := func(err error, phase string) {
		t.Helper()
		if err == nil || !errors.Is(err, ErrRecoverablePlanBlock) {
			t.Fatalf("%s: got %v, want plan block", phase, err)
		}
	}

	chain.EnablePlanMode(true)
	mustBlock(check(), "enabled")
	chain.EnablePlanMode(false)
	if err := check(); err != nil {
		t.Fatalf("disabled: mutating command must follow the normal path: %v", err)
	}
	chain.EnablePlanMode(true)
	mustBlock(check(), "re-enabled")

	// 旧字段单独为真时依然拦截；两者都关时恢复。
	chain.EnablePlanMode(false)
	chain.PlanCommandGuard = true
	mustBlock(check(), "legacy field")
	chain.PlanCommandGuard = false
	if err := check(); err != nil {
		t.Fatalf("both off: mutating command must follow the normal path: %v", err)
	}
}

// TestPMGPlanModeConcurrencySafe 让并发切换与并发检查/诊断同时发生；两阶段各以
// 全同写者收尾，给出确定断言。在 -race 下若标志非原子会报数据竞争。
func TestPMGPlanModeConcurrencySafe(t *testing.T) {
	t.Setenv("ARGENT_GUARDRAILS", "enforce")
	dir := t.TempDir()
	// nil 审批器/审计日志，避免把无关可变状态带进并发路径。
	chain := NewChain(DefaultPolicy(dir), nil, nil, "s1")
	args := json.RawMessage(`{"command":"rm -rf x"}`)

	run := func(enabled bool) {
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 50; j++ {
					chain.EnablePlanMode(enabled)
				}
			}()
		}
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 50; j++ {
					_ = PlanModeEnabled(chain)
					_ = chain.Check(context.Background(), "bash", args)
				}
			}()
		}
		wg.Wait()
	}

	run(true)
	if !PlanModeEnabled(chain) {
		t.Fatal("all writers enabled plan mode, want enabled")
	}
	run(false)
	if PlanModeEnabled(chain) {
		t.Fatal("all writers disabled plan mode, want disabled")
	}
}

// TestPMGParseApprovalModeAllValues 覆盖所有配置值：规范值、大小写/别名归一化，
// 以及未知或空值回落到 ModeAsk（保持历史默认行为）。
func TestPMGParseApprovalModeAllValues(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want ApprovalMode
	}{
		{"ask", ModeAsk},
		{"never", ModeNever},
		{"auto", ModeAuto},
		{"yolo", ModeYolo},
		{"accept-edits", ModeAcceptEdits},
		{"ACCEPT-EDITS", ModeAcceptEdits},
		{"  accept-edits  ", ModeAcceptEdits},
		{"accept_edits", ModeAcceptEdits},
		{"Accept Edits", ModeAcceptEdits},
		{"acceptEdits", ModeAcceptEdits},
		{"", ModeAsk},
		{"   ", ModeAsk},
		{"bogus", ModeAsk},
		{"accept_all", ModeAsk},
	} {
		if got := ParseApprovalMode(tc.in); got != tc.want {
			t.Errorf("ParseApprovalMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestPMGWrapForModeOtherModesPassThrough 验证除 ModeAcceptEdits 外都原样返回
// base，逐字节保持既有行为，且不触发任何审批。
func TestPMGWrapForModeOtherModesPassThrough(t *testing.T) {
	base := &pmgApprover{answer: true}
	for _, mode := range []ApprovalMode{ModeAsk, ModeNever, ModeAuto, ModeYolo, ApprovalMode("unknown")} {
		if got := WrapForMode(base, mode); got != base {
			t.Errorf("mode %q: WrapForMode returned a different approver", mode)
		}
	}
	if got := base.count(); got != 0 {
		t.Fatalf("pass-through must not call the base, got %d calls", got)
	}
}

// TestPMGWrapForModeAcceptEditsAsksOncePerTool 验证同类（同工具名）批准只问一次：
// 作用域按工具名而非参数划分；另一个工具仍需重新询问。
func TestPMGWrapForModeAcceptEditsAsksOncePerTool(t *testing.T) {
	base := &pmgApprover{answer: true}
	wrapped := WrapForMode(base, ModeAcceptEdits)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		args := fmt.Sprintf(`{"command":"ls %d"}`, i)
		if ok, err := wrapped.Ask(ctx, "bash", args); err != nil || !ok {
			t.Fatalf("call %d: ok=%v err=%v, want an approve", i, ok, err)
		}
	}
	if got := base.count(); got != 1 {
		t.Fatalf("same tool asked the operator %d times, want once", got)
	}
	if records := base.recorded(); len(records) != 1 || records[0].name != "bash" {
		t.Fatalf("base records = %+v, want one bash call", records)
	}
	if ok, err := wrapped.Ask(ctx, "write", `{}`); err != nil || !ok {
		t.Fatalf("write: ok=%v err=%v", ok, err)
	}
	if got := base.count(); got != 2 {
		t.Fatalf("a different tool must prompt again, total %d calls", got)
	}
}

// TestPMGWrapForModeAcceptEditsCJKName 验证 CJK 工具名与参数被原样透传，且同样
// 只问一次。
func TestPMGWrapForModeAcceptEditsCJKName(t *testing.T) {
	base := &pmgApprover{answer: true}
	wrapped := WrapForMode(base, ModeAcceptEdits)
	const name = "编辑文件"
	const args = `{"路径":"目录/世界.txt","内容":"你好"}`
	for i := 0; i < 2; i++ {
		if ok, err := wrapped.Ask(context.Background(), name, args); err != nil || !ok {
			t.Fatalf("call %d: ok=%v err=%v", i, ok, err)
		}
	}
	records := base.recorded()
	if len(records) != 1 {
		t.Fatalf("base recorded %d calls, want 1", len(records))
	}
	if records[0].name != name || records[0].args != args {
		t.Fatalf("base got name=%q args=%q, want name=%q args=%q", records[0].name, records[0].args, name, args)
	}
}

// TestPMGWrapForModeAcceptEditsRefusalAndErrorNotCached 验证拒绝与错误都不会被
// 缓存，操作员仍可改变主意、通道恢复后仍可再问。
func TestPMGWrapForModeAcceptEditsRefusalAndErrorNotCached(t *testing.T) {
	refusal := &pmgApprover{answer: false}
	refused := WrapForMode(refusal, ModeAcceptEdits)
	for i := 0; i < 3; i++ {
		if ok, err := refused.Ask(context.Background(), "bash", "{}"); err != nil || ok {
			t.Fatalf("refusal call %d: ok=%v err=%v", i, ok, err)
		}
	}
	if got := refusal.count(); got != 3 {
		t.Fatalf("a refusal must not be cached: base asked %d times, want 3", got)
	}

	sentinel := errors.New("approval channel down")
	failing := &pmgApprover{err: sentinel}
	broken := WrapForMode(failing, ModeAcceptEdits)
	for i := 0; i < 2; i++ {
		ok, err := broken.Ask(context.Background(), "bash", "{}")
		if ok || !errors.Is(err, sentinel) {
			t.Fatalf("error call %d: ok=%v err=%v, want the base error", i, ok, err)
		}
	}
	if got := failing.count(); got != 2 {
		t.Fatalf("an error must not be cached: base asked %d times, want 2", got)
	}
}

// TestPMGWrapForModeAcceptEditsConcurrentCJKNames 让多个不同的 CJK 工具名并发
// 首次询问、再并发命中缓存；验证包装器并发安全且每个名字只问一次。
func TestPMGWrapForModeAcceptEditsConcurrentCJKNames(t *testing.T) {
	base := &pmgApprover{answer: true}
	wrapped := WrapForMode(base, ModeAcceptEdits)
	ctx := context.Background()

	const workers = 32
	nameOf := func(i int) string { return fmt.Sprintf("工具-%d", i) }
	errs := make(chan string, workers*2)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := nameOf(i)
			for j := 0; j < 4; j++ {
				if ok, err := wrapped.Ask(ctx, name, `{"path":"中文.txt"}`); err != nil || !ok {
					errs <- fmt.Sprintf("%s call %d: ok=%v err=%v", name, j, ok, err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
	if got := base.count(); got != workers {
		t.Fatalf("distinct CJK names asked the base %d times, want %d (once per name)", got, workers)
	}
	records := base.recorded()
	seen := make(map[string]bool, workers)
	for _, record := range records {
		if seen[record.name] {
			t.Fatalf("tool %q was asked more than once", record.name)
		}
		seen[record.name] = true
	}
}
