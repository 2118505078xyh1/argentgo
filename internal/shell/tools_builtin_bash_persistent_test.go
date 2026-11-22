package shell

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/pluginapi"
)

// persistentFakeSessions 满足消费方的小接口断言，记录 Exec 收到的参数，
// 用于验证 opt-in 分支是否正确转发 workspace / shellKind / command。
type persistentFakeSessions struct {
	called    int
	workspace string
	shellKind string
	command   string
	stdout    string
	code      int
	err       error
}

func (f *persistentFakeSessions) Exec(ctx context.Context, workspace, shellKind, command string) (string, int, error) {
	f.called++
	f.workspace, f.shellKind, f.command = workspace, shellKind, command
	return f.stdout, f.code, f.err
}

func ctxWithShellSessions(sessions any) context.Context {
	return pluginapi.WithExecContext(context.Background(), pluginapi.ExecContext{ShellSessions: sessions})
}

func TestBashUsesPersistentSessionWhenEnabled(t *testing.T) {
	t.Setenv("ARGENT_PERSISTENT_SHELL", "1")
	fake := &persistentFakeSessions{stdout: "persistent-ok\n", code: 0}
	out, err := bashTool{}.Run(ctxWithShellSessions(fake), "/work", json.RawMessage(`{"command":"echo hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "persistent-ok") {
		t.Fatalf("输出未来自持久化会话: %q", out)
	}
	if fake.called != 1 {
		t.Fatalf("Exec 调用次数 = %d, want 1", fake.called)
	}
	if fake.workspace != "/work" {
		t.Fatalf("workspace = %q, want /work", fake.workspace)
	}
	if fake.command != "echo hi" {
		t.Fatalf("command = %q, want %q", fake.command, "echo hi")
	}
	// shellKind 必须是持久化内核支持的种类之一。
	if fake.shellKind != "bash" && fake.shellKind != "powershell" {
		t.Fatalf("shellKind = %q, want bash 或 powershell", fake.shellKind)
	}
}

func TestBashFallsBackWhenDisabled(t *testing.T) {
	t.Setenv("ARGENT_PERSISTENT_SHELL", "0")
	fake := &persistentFakeSessions{stdout: "should-not-appear\n"}
	// 即便注册表注入了会话管理器，开关关闭时也绝不调用它。
	_, _ = bashTool{}.Run(ctxWithShellSessions(fake), t.TempDir(), json.RawMessage(`{"command":"echo hi"}`))
	if fake.called != 0 {
		t.Fatalf("开关关闭时不应使用持久化会话, called = %d", fake.called)
	}
}

func TestPersistentShellKindSelection(t *testing.T) {
	bashFound := func(string) (string, error) { return "/usr/bin/bash", nil }
	noBash := func(string) (string, error) { return "", context.Canceled }
	if got := persistentShellKind("linux", noBash); got != "bash" {
		t.Fatalf("posix shellKind = %q, want bash", got)
	}
	if got := persistentShellKind("windows", bashFound); got != "bash" {
		t.Fatalf("windows with bash shellKind = %q, want bash", got)
	}
	if got := persistentShellKind("windows", noBash); got != "powershell" {
		t.Fatalf("windows without bash shellKind = %q, want powershell", got)
	}
}
