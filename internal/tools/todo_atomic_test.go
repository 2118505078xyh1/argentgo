package tools

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// leftoverTodoTemps 返回目录里遗留的临时文件名。任何成功的写入或失败后的
// 清理都不应留下这些文件。
func leftoverTodoTemps(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func TestAtomicWriteTodoBasic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "todos.json")
	data := []byte("{\"todos\":[]}\n")
	if err := atomicWriteTodo(path, data, 0o644); err != nil {
		t.Fatalf("atomicWriteTodo: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("content = %q, want %q", got, data)
	}
	if left := leftoverTodoTemps(t, dir); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func TestAtomicWriteTodoOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "todos.json")
	if err := atomicWriteTodo(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := atomicWriteTodo(path, []byte("new"), 0o644); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "new" {
		t.Fatalf("content = %q, want %q", got, "new")
	}
	if left := leftoverTodoTemps(t, dir); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func TestAtomicWriteTodoCJK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "todos.json")
	items := []todoItem{
		{ID: "1", Text: "设计原子写入方案", Status: "in_progress"},
		{ID: "2", Text: "验证中文内容持久化", Status: "completed"},
	}
	data, err := marshalTodos(items)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := atomicWriteTodo(path, data, 0o644); err != nil {
		t.Fatalf("write CJK: %v", err)
	}
	// 用真实的读取路径验证往返，而不仅仅是逐字节比较。
	loaded, err := loadTodos(path)
	if err != nil {
		t.Fatalf("load CJK: %v", err)
	}
	if len(loaded) != len(items) || loaded[0].Text != items[0].Text || loaded[1].Text != items[1].Text {
		t.Fatalf("loaded = %+v, want %+v", loaded, items)
	}
	if left := leftoverTodoTemps(t, dir); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func TestAtomicWriteTodoRemovesTempOnFailure(t *testing.T) {
	dir := t.TempDir()
	// 把一个目录当作目标路径，重命名必然失败；放入文件确保 Windows 上
	// 保守的“移除目标再重命名”降级路径也无法误删该目录。
	target := filepath.Join(dir, "not-a-file")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "keep"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteTodo(target, []byte("content"), 0o644); err == nil {
		t.Fatal("write to directory path unexpectedly succeeded")
	}
	if left := leftoverTodoTemps(t, dir); len(left) != 0 {
		t.Fatalf("temp files not cleaned up after failure: %v", left)
	}
}

func TestAtomicWriteTodoPermission(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX 权限位在 Windows 上语义不同")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "todos.json")
	if err := atomicWriteTodo(path, []byte("first"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("perm = %o, want 600", got)
	}
	// 覆盖写也应以调用方给定的权限替换，而非沿用旧文件的权限。
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteTodo(path, []byte("second"), 0o600); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("perm after overwrite = %o, want 600", got)
	}
	if left := leftoverTodoTemps(t, dir); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func TestAtomicWriteTodoConcurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "todos.json")
	const (
		writers = 32
		size    = 4096
	)
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(b byte) {
			defer wg.Done()
			payload := bytes.Repeat([]byte{b}, size)
			if err := atomicWriteTodo(path, payload, 0o644); err != nil {
				errs <- err
			}
		}(byte('a' + i))
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent write: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	// 非原子写入会看到多个写入者内容交错；原子替换保证最终内容来自同一次写入。
	if len(got) != size {
		t.Fatalf("content length = %d, want %d", len(got), size)
	}
	first := got[0]
	if first < 'a' || first >= byte('a'+writers) {
		t.Fatalf("unexpected content byte %q", first)
	}
	for _, b := range got {
		if b != first {
			t.Fatalf("content interleaved: got byte %q, want %q", b, first)
		}
	}
	if left := leftoverTodoTemps(t, dir); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func TestTodoRunUsesAtomicWrite(t *testing.T) {
	root := t.TempDir()
	if _, err := writeTodo(t, root, "接线验证"); err != nil {
		t.Fatalf("writeTodo: %v", err)
	}
	stateDir := filepath.Join(root, ".argent")
	path := filepath.Join(stateDir, todoFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatalf("state file should end with a newline, got %q", data)
	}
	items, err := loadTodos(path)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if len(items) != 1 || items[0].Text != "接线验证" {
		t.Fatalf("items = %+v, want one item with 接线验证", items)
	}
	if left := leftoverTodoTemps(t, stateDir); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}
