package automation

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestClaimIsStaleFreshUnstampedMarker 捕获 O_EXCL 创建锁文件与所有者写入其标记之间的窗口。标记为空
// 创建锁文件与所有者写入其标记之间的窗口。标记为空
// 但崭新；把它当作被放弃的认领会让第二个调用方窃取
// 但崭新；把它当作被放弃的认领会让第二个调用方窃取一个已在运行的任务。
func TestClaimIsStaleFreshUnstampedMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.lock")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if claimIsStale(path) {
		t.Fatal("a just-created, still-empty claim must not be considered stale")
	}
}

// TestClaimIsStaleUnusableMarker 验证文档记载的契约：不能被当作普通文件读取的标记必须可回收，而非
// 不能被当作普通文件读取的标记必须可回收，而非
// 永远卡住任务。在锁路径放一个目录是在所有平台上使标记不可用的
// 永远卡住任务。在锁路径放一个目录是在所有平台上使标记不可用的可移植方式。
func TestClaimIsStaleUnusableMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocked.lock")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if !claimIsStale(path) {
		t.Fatal("an unusable claim marker must be considered stale")
	}
}

func TestClaimIsStaleByModificationTime(t *testing.T) {
	dir := t.TempDir()
	fresh := filepath.Join(dir, "fresh.lock")
	if err := os.WriteFile(fresh, []byte(fmt.Sprintf("%d\n", time.Now().UnixNano())), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if claimIsStale(fresh) {
		t.Fatal("a fresh claim with a valid stamp must not be stale")
	}

	old := filepath.Join(dir, "old.lock")
	if err := os.WriteFile(old, []byte(fmt.Sprintf("%d\n", time.Now().UnixNano())), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	stamp := time.Now().Add(-2 * staleClaimAfter)
	if err := os.Chtimes(old, stamp, stamp); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	if !claimIsStale(old) {
		t.Fatal("an abandoned claim past the grace period must be stale")
	}
}

// TestClaimRejectsFreshUnstampedLock 是该竞态的端到端版本：当一个调用方拥有刚创建的锁（其标记尚未写入）时，第二个调用方
// 当一个调用方拥有刚创建的锁（其标记尚未写入）时，第二个调用方
// 必须得到 errBusy，而非窃取认领并运行两次同一任务。
func TestClaimRejectsFreshUnstampedLock(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	lock := filepath.Join(store.claimDir(), claimFileName("nightly")+".lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// 模拟第一个调用方的 O_EXCL 成功之后紧接着的瞬间。
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := store.claim("nightly"); !errors.Is(err, errBusy) {
		t.Fatalf("claim over a fresh unstamped lock = %v, want errBusy", err)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("the original claim marker was stolen: %v", err)
	}
}

// TestClaimReclaimsAbandonedLock 是正向对应情形：真正旧的认领必须仍可恢复，使崩溃的进程不会卡住任务。
// 认领必须仍可恢复，使崩溃的进程不会卡住任务。
func TestClaimReclaimsAbandonedLock(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	lock := filepath.Join(store.claimDir(), claimFileName("nightly")+".lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(lock, []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	stamp := time.Now().Add(-2 * staleClaimAfter)
	if err := os.Chtimes(lock, stamp, stamp); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	seq, err := store.claim("nightly")
	if err != nil {
		t.Fatalf("claim over an abandoned lock: %v", err)
	}
	if seq == 0 {
		t.Fatal("claim returned a zero sequence number")
	}
	defer store.release("nightly")
	if info, err := os.Stat(lock); err != nil || info.Mode()&os.ModeType != 0 {
		t.Fatalf("reclaimed claim is not a regular file: info=%v err=%v", info, err)
	}
}
