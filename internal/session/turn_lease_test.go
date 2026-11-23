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

// TestTurnLeaseLocalTTLBoundsStaleMarker 锁定崩溃 + PID 复用场景的安全修复：
// 本机标记即使 PID 仍判活（模拟 PID 被复用），mtime 超过 TTL 也必须被当陈旧
// 清理。旧实现的本机 TTL=24h 会让这种场景拒绝新 turn 长达一天。
func TestTurnLeaseLocalTTLBoundsStaleMarker(t *testing.T) {
	t.Setenv(turnLeaseEnv, "")
	dir := t.TempDir()
	localHost, _ := os.Hostname()
	// 用当前 PID 写标记（PID 判活必为 true），mtime 设为超过新 TTL。
	// 这样陈旧判定只能靠 TTL 生效——正是旧实现漏掉的路径。
	marker := writeTurnLeaseMarker(t, dir, "sess-local-ttl", os.Getpid(), localHost, localTurnLeaseTTL+time.Minute)

	oldTime, _ := os.Stat(marker)
	lease, err := AcquireTurnLease(dir, "sess-local-ttl")
	if err != nil {
		t.Fatalf("stale local marker must be reclaimed even with live PID: %v", err)
	}
	defer lease.Release()
	// 同 PID 下新租约会复用同名路径，因此不能断言文件消失；改为断言标记被
	// 重建/刷新（mtime 变新）——证明旧陈旧标记已被回收而非阻挡本次获取。
	newTime, err := os.Stat(marker)
	if err != nil {
		t.Fatalf("replacement marker missing: %v", err)
	}
	if !newTime.ModTime().After(oldTime.ModTime()) {
		t.Fatalf("marker not refreshed: old=%v new=%v", oldTime.ModTime(), newTime.ModTime())
	}
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
