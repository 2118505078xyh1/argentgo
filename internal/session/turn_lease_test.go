package session

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func writeTurnLeaseMarker(t *testing.T, dir, sessionID string, pid int, host string, age time.Duration) string {
	t.Helper()
	path := filepath.Join(dir, sessionID+"."+strconv.Itoa(pid))
	if err := os.WriteFile(path, []byte(host), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-age)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestTurnLeaseForeignDetection 验证 TTL + PID 双判定：异机超时标记被清理，
// 活标记拒绝新 turn；释放后标记消失；=0 时退化为无操作租约。
func TestTurnLeaseForeignDetection(t *testing.T) {
	t.Setenv(turnLeaseEnv, "")
	dir := t.TempDir()
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}

	// 异机但 PID 存活、mtime 已超 60s：按异机 TTL 判定为陈旧并清理。
	// 用父进程 pid 避免与本次获取租约时创建的 <sessionID>.<self pid> 标记同路径。
	foreignPID := os.Getppid()
	if foreignPID <= 0 {
		foreignPID = 2147483647
	}
	stalePath := writeTurnLeaseMarker(t, dir, "s1", foreignPID, "other-host", 2*time.Minute)
	lease, err := AcquireTurnLease(dir, "s1")
	if err != nil {
		t.Fatalf("AcquireTurnLease after stale marker: %v", err)
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatalf("stale foreign marker was not removed: %v", err)
	}
	ownPath := filepath.Join(dir, "s1."+strconv.Itoa(os.Getpid()))
	if _, err := os.Stat(ownPath); err != nil {
		t.Fatalf("own lease marker missing: %v", err)
	}
	lease.Release()
	if _, err := os.Stat(ownPath); !os.IsNotExist(err) {
		t.Fatalf("lease marker was not released: %v", err)
	}

	// 活标记（本机、刚刷新）必须拒绝新租约。
	heldPath := writeTurnLeaseMarker(t, dir, "s2", os.Getpid(), host, time.Second)
	if _, err := AcquireTurnLease(dir, "s2"); !errors.Is(err, ErrTurnLeaseHeld) {
		t.Fatalf("AcquireTurnLease with live marker = %v, want ErrTurnLeaseHeld", err)
	}
	if _, err := os.Stat(heldPath); err != nil {
		t.Fatalf("live marker was wrongly removed: %v", err)
	}

	// 紧急关闭：不创建/扫描任何 marker。
	t.Setenv(turnLeaseEnv, "0")
	noop, err := AcquireTurnLease(dir, "s3")
	if err != nil || noop == nil || !noop.noop {
		t.Fatalf("disabled AcquireTurnLease = %+v, %v", noop, err)
	}
}
