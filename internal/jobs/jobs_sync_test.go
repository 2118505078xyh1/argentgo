package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// readJobsSyncSource 读取当前包内的源码文件，用于掉电语义的确定性守卫。
// 优先用 runtime.Caller 定位，回退到 go test 的包工作目录。
func readJobsSyncSource(t *testing.T, name string) string {
	t.Helper()
	if _, thisFile, _, ok := runtime.Caller(0); ok {
		if data, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), name)); err == nil {
			return string(data)
		}
	}
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("读取源码 %s: %v", name, err)
	}
	return string(data)
}

// TestPersistAtomicWriteIntegrity 校验 persist 原子写的两个不变量：rename 后
// jobs.json 内容正确、权限保持临时文件默认的仅所有者可见；同时确认临时文件已清理。
func TestPersistAtomicWriteIntegrity(t *testing.T) {
	dir := t.TempDir()
	r, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	r.mu.Lock()
	r.jobs["job1"] = &Job{ID: "job1", Command: "echo", Status: StatusExited}
	r.next = 7
	snap, version := r.changedLocked()
	r.mu.Unlock()
	if err := r.persist(snap, version); err != nil {
		t.Fatalf("persist: %v", err)
	}

	// 1) 内容正确。
	data, err := os.ReadFile(r.indexPath())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var got index
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Next != 7 || len(got.Jobs) != 1 || got.Jobs[0].ID != "job1" {
		t.Fatalf("内容不符: %#v", got)
	}

	// 临时文件必须被清理。
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".jobs-") && strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("残留临时文件: %s", entry.Name())
		}
	}

	// 2) 权限保持：CreateTemp 默认 0600。Windows 无 POSIX 权限位，跳过。
	if runtime.GOOS != "windows" {
		info, err := os.Stat(r.indexPath())
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("mode = %o, want 600", mode)
		}
	}
}

// TestPersistSyncsBeforeRename 是掉电语义的确定性守卫：Sync 直接作用于具体的
// *os.File，无法注入，因此在源码层面锁定 persist 必须在 os.Rename 之前对临时
// 文件调用 Sync，防止回归为"写后不刷盘就 rename"导致 jobs.json 被截断。
func TestPersistSyncsBeforeRename(t *testing.T) {
	source := readJobsSyncSource(t, "jobs.go")
	start := strings.Index(source, "func (r *Registry) persist(")
	if start < 0 {
		t.Fatal("找不到 persist 定义")
	}
	body := source[start:]
	syncIdx := strings.Index(body, "tmp.Sync()")
	renameIdx := strings.Index(body, "os.Rename(")
	if syncIdx < 0 {
		t.Fatal("persist 缺少 tmp.Sync()")
	}
	if renameIdx < 0 {
		t.Fatal("persist 缺少 os.Rename")
	}
	if syncIdx > renameIdx {
		t.Fatal("persist 必须在 os.Rename 之前调用 tmp.Sync()")
	}
}
