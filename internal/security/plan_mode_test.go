package security

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// chainOutcome 是一次链检查的可比较快照，用来验证"默认关闭时行为逐字节不变"。
type chainOutcome struct {
	errText   string
	approvals int
	decisions []string
}

func runPlanModeChain(t *testing.T, command string, setup func(*Chain)) chainOutcome {
	t.Helper()
	dir := t.TempDir()
	approver := &countingApprover{allow: true}
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
	out := chainOutcome{approvals: approver.calls}
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

// TestPlanModeDefaultMatchesLegacyPath 验证默认关闭时链的决策与错误文本和
// 从未触碰计划模式 API 的遗留路径完全一致（显式 EnablePlanMode(false) 也是
// 无操作）。nil 接收者同样安全。
func TestPlanModeDefaultMatchesLegacyPath(t *testing.T) {
	t.Setenv("ARGENT_GUARDRAILS", "enforce")

	var nilChain *Chain
	nilChain.EnablePlanMode(true) // nil 接收者必须是无操作
	if PlanModeEnabled(nil) {
		t.Fatal("PlanModeEnabled(nil) must be false")
	}
	if PlanModeEnabled(NewChain(DefaultPolicy(t.TempDir()), nil, nil, "")) {
		t.Fatal("a fresh chain must report plan mode disabled")
	}

	cases := []struct {
		name    string
		command string
	}{
		{"mutating command", "rm -rf x"},
		{"read-only command", "git status"},
		{"go test", "go test ./..."},
		{"powershell mutating", "Remove-Item -Force x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			legacy := runPlanModeChain(t, tc.command, nil)
			explicitOff := runPlanModeChain(t, tc.command, func(c *Chain) {
				c.EnablePlanMode(false)
			})
			if legacy.errText != explicitOff.errText ||
				legacy.approvals != explicitOff.approvals ||
				!reflect.DeepEqual(legacy.decisions, explicitOff.decisions) {
				t.Fatalf("explicit disable changed behavior:\nlegacy=%+v\nexplicit=%+v", legacy, explicitOff)
			}
		})
	}
}

// TestEnablePlanModeBlocksMutatingCommand 验证启用后修改类命令在审批/审计之前
// 被拒，返回可恢复的 ErrRecoverablePlanBlock，且拒绝字节与旧字段完全一致。
func TestEnablePlanModeBlocksMutatingCommand(t *testing.T) {
	t.Setenv("ARGENT_GUARDRAILS", "enforce")
	dir := t.TempDir()
	approver := &countingApprover{allow: true}
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
	var stop *StopError
	if errors.As(err, &stop) {
		t.Fatalf("a recoverable block must not be a StopError: %#v", err)
	}
	// 拒绝文案必须与旧 PlanCommandGuard=true 分支逐字节一致。
	const want = `security: mutating shell command blocked in plan mode: "rm"`
	if err.Error() != want {
		t.Fatalf("error text = %q, want %q", err.Error(), want)
	}
	if approver.calls != 0 {
		t.Fatalf("guard must run before approval, approver.calls = %d", approver.calls)
	}
	entries, readErr := ReadAudit(audit.Path())
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("guard must run before audit, audit: %v entries=%d", readErr, len(entries))
	}
}

// TestEnablePlanModeAllowsReadOnly 验证计划模式不拦截只读命令。
func TestEnablePlanModeAllowsReadOnly(t *testing.T) {
	t.Setenv("ARGENT_GUARDRAILS", "enforce")
	for _, command := range []string{"git status", "go test ./...", "Get-Content x"} {
		t.Run(command, func(t *testing.T) {
			out := runPlanModeChain(t, command, func(c *Chain) {
				c.EnablePlanMode(true)
			})
			if out.errText != "" {
				t.Fatalf("read-only command must pass in plan mode, error: %s", out.errText)
			}
			if out.approvals != 1 {
				t.Fatalf("approver.calls = %d, want 1", out.approvals)
			}
			if len(out.decisions) != 1 || out.decisions[0] != "allow" {
				t.Fatalf("want one allow decision, got %v", out.decisions)
			}
		})
	}
}

// TestPlanModeHintStable 验证拦截错误到模型可读提示的映射稳定：错误文本包含
// "blocked in plan mode"，ReasonFromError 将其归为可恢复的工具错误，ModelHint
// 给出固定的自我修正指引。启用方式（新 API 或旧字段）不影响这些字节。
func TestPlanModeHintStable(t *testing.T) {
	t.Setenv("ARGENT_GUARDRAILS", "enforce")
	for _, tc := range []struct {
		name  string
		setup func(*Chain)
	}{
		{"new API", func(c *Chain) { c.EnablePlanMode(true) }},
		{"legacy field", func(c *Chain) { c.PlanCommandGuard = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			chain := NewChain(DefaultPolicy(dir), &countingApprover{allow: true}, NewAuditLog(dir), "s1")
			tc.setup(chain)
			err := chain.Check(context.Background(), "bash", json.RawMessage(`{"command":"rm -rf x"}`))
			if err == nil || !strings.Contains(err.Error(), "blocked in plan mode") {
				t.Fatalf("error %v does not carry the stable plan-mode hint", err)
			}
			reason, ok := ReasonFromError(err)
			if !ok {
				t.Fatal("ReasonFromError must classify a plan block")
			}
			if reason.Kind != RefusalKindToolError || !reason.Recoverable {
				t.Fatalf("reason = %+v, want recoverable tool-error", reason)
			}
			const wantHint = "该工具调用未获通过，请阅读反馈后调整。可以调整请求后重试。"
			if got := reason.ModelHint(); got != wantHint {
				t.Fatalf("ModelHint = %q, want %q", got, wantHint)
			}
		})
	}
}

// TestPlanModeToggleRestores 验证关闭后恢复正常路径，可再次开启。
func TestPlanModeToggleRestores(t *testing.T) {
	t.Setenv("ARGENT_GUARDRAILS", "enforce")
	dir := t.TempDir()
	chain := NewChain(DefaultPolicy(dir), &countingApprover{allow: true}, NewAuditLog(dir), "s1")
	check := func() error {
		return chain.Check(context.Background(), "bash", json.RawMessage(`{"command":"rm -rf x"}`))
	}
	chain.EnablePlanMode(true)
	if err := check(); err == nil || !errors.Is(err, ErrRecoverablePlanBlock) {
		t.Fatalf("enabled: got %v, want plan block", err)
	}
	chain.EnablePlanMode(false)
	if err := check(); err != nil {
		t.Fatalf("disabled: mutating command must follow the normal path: %v", err)
	}
	chain.EnablePlanMode(true)
	if err := check(); err == nil || !errors.Is(err, ErrRecoverablePlanBlock) {
		t.Fatalf("re-enabled: got %v, want plan block", err)
	}
}

// TestPlanModeConcurrencySafe 让并发切换与并发检查/诊断同时发生。在 -race 下，
// 若启用标志不是原子的，这里会报数据竞争。两阶段各以全同写者收尾，给出确定断言。
func TestPlanModeConcurrencySafe(t *testing.T) {
	t.Setenv("ARGENT_GUARDRAILS", "enforce")
	dir := t.TempDir()
	// 用 nil 审批器/审计日志，避免把无关的可变状态带进并发路径。
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
