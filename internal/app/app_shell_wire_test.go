package app

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/tools"
)

const shellWireFakeMarker = "SHELL_WIRE_FAKE_OK"

// shellWireFakeSessions 同时满足 shellSessionExec 与 shellManager 契约，
// 用于验证接线是否把会话安装到每个注册表，而不依赖真实 shell 进程。
type shellWireFakeSessions struct {
	calls  int
	closes int
	closed bool
}

func (f *shellWireFakeSessions) Exec(ctx context.Context, workspace, shellKind, command string) (string, int, error) {
	f.calls++
	return shellWireFakeMarker + "\n", 0, nil
}

func (f *shellWireFakeSessions) Close() error {
	f.closes++
	f.closed = true
	return nil
}

// newShellWireToolset 用给定的注册表构造一个最小 toolset，避免为接线测试
// 走完整的 Resolve / Session 装配。
func newShellWireToolset(regs ...*tools.Registry) *toolset {
	return &toolset{base: &multiRootTools{regs: regs}}
}

// installFakeShellManager 替换会话构造子，使 wire 注入伪实例而非真实进程。
func installFakeShellManager(t *testing.T, fake *shellWireFakeSessions) {
	t.Helper()
	orig := newShellManager
	newShellManager = func() shellManager { return fake }
	t.Cleanup(func() { newShellManager = orig })
}

func TestWireShellSessionsDefaultOffNoSideEffects(t *testing.T) {
	reg := tools.New(t.TempDir(), true)
	set := newShellWireToolset(reg)
	before, _ := json.Marshal(reg.Specs())

	closeFn := wireShellSessions(envFrom(nil), set)
	if closeFn == nil {
		t.Fatal("默认关闭时仍应返回非空 close")
	}
	// 未 opt-in：不创建管理器、不安装会话，close 为可重复调用的 no-op。
	closeFn()
	closeFn()

	after, _ := json.Marshal(reg.Specs())
	if !bytes.Equal(before, after) {
		t.Fatal("默认关闭时注册表 specs 发生变化")
	}

	// 进程 env 即便打开，bash 也走原路径：若误装了一个已被关闭的管理器，
	// 注入的会话会返回 "manager closed" 错误而非正常执行。
	t.Setenv(shellSessionsOptInEnv, "1")
	out, err := reg.Run(context.Background(), "bash", `{"command":"echo hi"}`)
	if err != nil {
		t.Fatalf("默认关闭时 bash 不应报错: %v, out=%q", err, out)
	}
	if strings.Contains(out, "manager closed") || strings.Contains(out, shellWireFakeMarker) {
		t.Fatalf("默认关闭却安装了会话管理器: %q", out)
	}
}

func TestWireShellSessionsOptInInstallsOnAllRegs(t *testing.T) {
	t.Setenv(shellSessionsOptInEnv, "1")
	fake := &shellWireFakeSessions{}
	installFakeShellManager(t, fake)

	r1 := tools.New(t.TempDir(), true)
	r2 := tools.New(t.TempDir(), true)
	set := newShellWireToolset(r1, r2)
	closeFn := wireShellSessions(envFrom(map[string]string{shellSessionsOptInEnv: " 1 "}), set)
	defer closeFn()

	// 两个注册表都必须真正注入了会话：bash 调用会命中伪 Exec。
	for i, reg := range []*tools.Registry{r1, r2} {
		out, err := reg.Run(context.Background(), "bash", `{"command":"echo hi"}`)
		if err != nil {
			t.Fatalf("reg %d: bash: %v", i, err)
		}
		if !strings.Contains(out, shellWireFakeMarker) {
			t.Fatalf("reg %d 未安装持久化会话, out=%q", i, out)
		}
	}
	if fake.calls != 2 {
		t.Fatalf("伪会话 Exec 调用次数 = %d, want 2", fake.calls)
	}
}

func TestCloseIdempotent(t *testing.T) {
	fake := &shellWireFakeSessions{}
	installFakeShellManager(t, fake)

	set := newShellWireToolset(tools.New(t.TempDir(), true))
	closeFn := wireShellSessions(envFrom(map[string]string{shellSessionsOptInEnv: "1"}), set)
	// 退出钩子可安全重复调用；底层 Manager.Close 幂等。
	for i := 0; i < 3; i++ {
		closeFn()
	}
	if !fake.closed {
		t.Fatal("close 未关闭会话管理器")
	}
	// 默认关闭路径的 no-op close 同样可重复调用。
	noop := wireShellSessions(envFrom(nil), set)
	noop()
	noop()
}
