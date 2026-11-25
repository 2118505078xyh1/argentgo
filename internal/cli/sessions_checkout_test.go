package cli

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"example.com/argentgo/internal/session"
)

// TestSessionsCheckoutCommand 验证 sessions checkout 子命令能就地切换活动记录，
// 并对参数缺失与非法记录返回合适的退出码。
func TestSessionsCheckoutCommand(t *testing.T) {
	// 本测试验证 checkout 命令本身的活动叶切换；分支摘要会额外追加一条摘要记录
	// 使活动叶偏移，属另一个特性的职责，这里关闭以隔离被测行为。
	t.Setenv("ARGENT_BRANCH_SUMMARY", "0")
	storage := t.TempDir()
	store, err := session.Open(filepath.Join(storage, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("checkout")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(session.KindUser, "first"); err != nil {
		t.Fatal(err)
	}
	middle, err := sess.AppendRecord(session.KindAssistant, "middle")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(session.KindUser, "last"); err != nil {
		t.Fatal(err)
	}

	stdio, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: storage}, []string{
		"sessions", "checkout", sess.ID(), middle.ID,
	}); code != 0 {
		t.Fatalf("checkout exit = %d, err %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "checked out") ||
		!strings.Contains(out.String(), sess.ID()) ||
		!strings.Contains(out.String(), middle.ID) {
		t.Fatalf("checkout output = %q", out.String())
	}

	// 缺少 <entry> 参数应当返回用法退出码。
	stdio, out, errOut = newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: storage}, []string{
		"sessions", "checkout", sess.ID(),
	}); code != 2 {
		t.Fatalf("missing entry exit = %d, err %q", code, errOut.String())
	}

	// 不存在的记录应当返回错误退出码，而不是 panic。
	stdio, out, errOut = newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: storage}, []string{
		"sessions", "checkout", sess.ID(), "missing",
	}); code != 1 {
		t.Fatalf("invalid entry exit = %d, err %q", code, errOut.String())
	}
}

// TestParseAtFlag 验证 --at 已被注册为可解析的 flag：解析不报错，
// 且剩余的位置参数（prompt）保持完整。
func TestParseAtFlag(t *testing.T) {
	opts, prompt, err := parseArgs([]string{
		"--session", "abc", "--at", "n000003", "hello world",
	}, func(string) string { return "" }, io.Discard)
	if err != nil {
		t.Fatalf("parse --at: %v", err)
	}
	if opts.Session != "abc" {
		t.Fatalf("session = %q, want abc", opts.Session)
	}
	// --at 的值必须真正存入 Options，供 app 恢复流程使用，而不是只注册了 flag 表面。
	if opts.At != "n000003" {
		t.Fatalf("at = %q, want n000003", opts.At)
	}
	if len(prompt) != 1 || prompt[0] != "hello world" {
		t.Fatalf("prompt = %q", prompt)
	}

	// flagSurface 已经由 parity 测试覆盖，这里再确认 --at 确实出现在其中。
	found := false
	for _, name := range flagSurface() {
		if name == "at" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("flagSurface does not contain --at")
	}
}

// TestCheckoutGuardEnabled 验证 ARGENT_CHECKPOINT_GUARD 仅认 "1"（忽略首尾空白），
// 未设置或显式关闭时都返回 false，保持 opt-in 默认关闭。
func TestCheckoutGuardEnabled(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"unset", "", false},
		{"explicit-off", "0", false},
		{"one", "1", true},
		{"padded-one", "  1\t", true},
		{"loose-truthy", "yes", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdio, _, _ := newTestIO("", map[string]string{checkpointGuardEnv: tc.raw})
			if got := checkoutGuardEnabled(App{IO: stdio}); got != tc.want {
				t.Fatalf("checkoutGuardEnabled(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestSessionsCheckoutGuardOptIn 验证 opt-in 开关对 checkout 命令的影响：
// 默认关闭与显式关闭都走原路径；开启时合法目标仍通过且输出不变。
// 拒绝分支（逃逸/非 restored）由 session 包的守卫测试覆盖，因为 CLI 的
// 合法会话目标必然位于会话根内，无法在 CLI 层构造逃逸输入。
func TestSessionsCheckoutGuardOptIn(t *testing.T) {
	// 隔离 checkout 守卫本身；分支摘要的额外记录由 session 包测试覆盖。
	t.Setenv("ARGENT_BRANCH_SUMMARY", "0")
	storage := t.TempDir()
	store, err := session.Open(filepath.Join(storage, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("checkout-guard")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(session.KindUser, "first"); err != nil {
		t.Fatal(err)
	}
	middle, err := sess.AppendRecord(session.KindAssistant, "middle")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(session.KindUser, "last"); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		raw     string
		wantOut bool
	}{
		{"default-off", "", true},
		{"explicit-off", "0", true},
		{"opt-in-on", "1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdio, out, errOut := newTestIO("", map[string]string{checkpointGuardEnv: tc.raw})
			code := RunWith(App{IO: stdio, StorageDir: storage}, []string{
				"sessions", "checkout", sess.ID(), middle.ID,
			})
			if code != 0 {
				t.Fatalf("checkout exit = %d, err %q", code, errOut.String())
			}
			if tc.wantOut && (!strings.Contains(out.String(), "checked out") ||
				!strings.Contains(out.String(), sess.ID()) ||
				!strings.Contains(out.String(), middle.ID)) {
				t.Fatalf("checkout output = %q", out.String())
			}
		})
	}
}
