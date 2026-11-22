package security

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// readPendingSyncSource 读取当前包内的源码文件，用于掉电语义的确定性守卫。
// 优先用 runtime.Caller 定位，回退到 go test 的包工作目录。
func readPendingSyncSource(t *testing.T, name string) string {
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

// TestAtomicWriteFileIntegrity 校验 atomicWriteFile 原子写的两个不变量：
// rename 后目标文件内容逐字节一致、权限保持仅所有者可见；同时确认临时文件已清理。
func TestAtomicWriteFileIntegrity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pending.json")
	data := []byte("line1\nline2\n")
	if err := atomicWriteFile(path, data); err != nil {
		t.Fatalf("atomicWriteFile: %v", err)
	}

	// 1) 内容逐字节一致。
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("内容不符: got %q, want %q", got, data)
	}

	// 临时文件必须被清理。
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "pending.json" {
		t.Fatalf("残留临时文件: %v", entries)
	}

	// 2) 权限保持：0600。Windows 无 POSIX 权限位，跳过。
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("mode = %o, want 600", mode)
		}
	}
}

// TestAtomicWriteFileSyncsBeforeRename 是掉电语义的确定性守卫：Sync 直接作用于
// 具体的 *os.File，无法注入，因此在源码层面锁定 atomicWriteFile 必须在 os.Rename
// 之前对临时文件调用 Sync，防止回归为"写后不刷盘就 rename"导致目标文件被截断。
func TestAtomicWriteFileSyncsBeforeRename(t *testing.T) {
	source := readPendingSyncSource(t, "pending.go")
	start := strings.Index(source, "func atomicWriteFile(")
	if start < 0 {
		t.Fatal("找不到 atomicWriteFile 定义")
	}
	body := source[start:]
	syncIdx := strings.Index(body, "tmp.Sync()")
	renameIdx := strings.Index(body, "os.Rename(")
	if syncIdx < 0 {
		t.Fatal("atomicWriteFile 缺少 tmp.Sync()")
	}
	if renameIdx < 0 {
		t.Fatal("atomicWriteFile 缺少 os.Rename")
	}
	if syncIdx > renameIdx {
		t.Fatal("atomicWriteFile 必须在 os.Rename 之前调用 tmp.Sync()")
	}
}
