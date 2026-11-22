package cli

import (
	"context"
	"strings"
	"testing"
)

// TestSessionsSnapshotsNoSession 验证子命令对不存在的会话给出明确错误而非 panic。
func TestSessionsSnapshotsNoSession(t *testing.T) {
	stdio, _, errOut := newTestIO("", nil)
	code := runSessions(App{IO: stdio}, context.Background(), []string{"snapshots", "no-such-session"})
	if code == 0 {
		t.Fatalf("exit = 0, stderr = %q, want non-zero", errOut.String())
	}
	if !strings.Contains(errOut.String(), "open session") {
		t.Fatalf("stderr = %q, want an open-session error", errOut.String())
	}
}
