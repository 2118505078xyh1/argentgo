package app

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/server"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

func newStatusTestSession(t *testing.T) (storage string, sess *session.Session) {
	t.Helper()
	storage = t.TempDir()
	store, err := session.Open(filepath.Join(storage, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sess, err = store.NewSession("status test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sessionStatusRegistry.Delete(sess.ID()) })
	return storage, sess
}

// TestSessionListStatusOptIn 锁定 #28 的可见性：store 的 List 带 status 字段，
// session.list RPC 也把它回传给客户端；未记录的会话仍省略该字段。
func TestSessionListStatusOptIn(t *testing.T) {
	storage, sess := newStatusTestSession(t)
	adapter := Sessions(storage)

	// 工作态：registry 与 store 共享实例，status 立即出现在 List 与 RPC 结果里。
	finish := beginSessionStatus(sess)
	if got := sessionStatusRegistry.Get(sess.ID()); got != agent.StatusWorking {
		t.Fatalf("入口状态 = %q, want working", got)
	}
	infos, err := adapter.List()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, info := range infos {
		if info.ID == sess.ID() {
			found = true
			if info.Status != "working" {
				t.Fatalf("info.Status = %q, want working", info.Status)
			}
		}
	}
	if !found {
		t.Fatalf("List() 未包含会话 %s", sess.ID())
	}
	snapshot := adapter.(*sessionStore).Snapshot()
	if snapshot[sess.ID()] != "working" {
		t.Fatalf("Snapshot = %v", snapshot)
	}

	var out bytes.Buffer
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"session.list"}` + "\n")
	if err := server.RunRPC(context.Background(), in, &out, server.Config{Sessions: adapter}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"status":"working"`) {
		t.Fatalf("session.list 缺少 status 字段: %s", out.String())
	}

	// 成功/失败收尾；错误时 status=error，之后 Delete 回到 idle（字段被省略）。
	finish(nil)
	if got := sessionStatusRegistry.Get(sess.ID()); got != agent.StatusDone {
		t.Fatalf("收尾状态 = %q, want done", got)
	}
	finish(errors.New("boom"))
	if got := sessionStatusRegistry.Get(sess.ID()); got != agent.StatusError {
		t.Fatalf("错误收尾状态 = %q, want error", got)
	}
	clearSessionStatus(sess)
	if got := sessionStatusRegistry.Get(sess.ID()); got != agent.StatusIdle {
		t.Fatalf("退出后状态 = %q, want idle", got)
	}
	infos, err = adapter.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.ID == sess.ID() && info.Status != "" {
			t.Fatalf("退出后 status 未省略: %q", info.Status)
		}
	}

	// nil 会话安全。
	noop := beginSessionStatus(nil)
	noop(nil)
	clearSessionStatus(nil)
}
