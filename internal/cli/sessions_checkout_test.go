package cli

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

// TestSessionsCheckoutCommand 验证 sessions checkout 子命令能就地切换活动记录，
// 并对参数缺失与非法记录返回合适的退出码。
func TestSessionsCheckoutCommand(t *testing.T) {
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
