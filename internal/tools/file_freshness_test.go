package tools

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeFreshnessFile 写入测试文件；失败即终止当前测试。
func writeFreshnessFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeFreshnessFile(%q): %v", path, err)
	}
}

// freshnessModTime 返回文件当前的 modtime。
func freshnessModTime(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat(%q): %v", path, err)
	}
	return info.ModTime()
}

// externalChange 模拟「外部改动」：改写内容并把 modtime 推到明显不同的时刻，
// 避免文件系统时间粒度导致 size/modtime 恰巧不变而漏检。
func externalChange(t *testing.T, path, content string) {
	t.Helper()
	writeFreshnessFile(t, path, content)
	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes(%q): %v", path, err)
	}
}

func TestFileFreshnessNotStaleAfterRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	writeFreshnessFile(t, path, "hello")

	var f FileFreshness // 零值可用
	f.MarkRead(path)
	if f.IsStale(path) {
		t.Fatal("读取后文件不应为 stale")
	}

	// 不同拼写（含 "." 与 ".."）经规范化后应命中同一条记录。
	odd := filepath.Join(dir, "sub", "..", ".", "a.txt")
	if f.IsStale(odd) {
		t.Fatal("规范化路径应命中同一记录，读取后不应为 stale")
	}
	f.MarkRead(odd)
	if f.IsStale(path) {
		t.Fatal("用另一种拼写 MarkRead 后，原路径也不应为 stale")
	}
}

func TestFileFreshnessStaleAfterExternalChange(t *testing.T) {
	t.Run("size changed", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "size.txt")
		writeFreshnessFile(t, path, "short")
		mt := freshnessModTime(t, path)

		f := NewFileFreshness()
		f.MarkRead(path)
		// 只改大小，保持 modtime 不变。
		writeFreshnessFile(t, path, "a much longer content")
		if err := os.Chtimes(path, mt, mt); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
		if !f.IsStale(path) {
			t.Fatal("size 变化应判为 stale")
		}
	})

	t.Run("modtime changed", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "mtime.txt")
		writeFreshnessFile(t, path, "same length")

		f := NewFileFreshness()
		f.MarkRead(path)
		// 只改 modtime，保持内容长度不变。
		future := time.Now().Add(2 * time.Hour)
		if err := os.Chtimes(path, future, future); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
		if !f.IsStale(path) {
			t.Fatal("modtime 变化应判为 stale")
		}
	})
}

func TestFileFreshnessNotStaleAfterWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edit.txt")
	writeFreshnessFile(t, path, "v0")

	f := NewFileFreshness()
	f.MarkRead(path)
	externalChange(t, path, "external v1")
	if !f.IsStale(path) {
		t.Fatal("外部改动后应为 stale")
	}

	// 模拟 agent 自己写完后的登记：新的磁盘状态成为快照，stale 被清除。
	writeFreshnessFile(t, path, "our v2")
	f.MarkWrite(path)
	if f.IsStale(path) {
		t.Fatal("自己的写入完成后不应再报 stale")
	}
}

func TestFileFreshnessStaleWarning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "warn.txt")
	writeFreshnessFile(t, path, "v0")

	f := NewFileFreshness()
	if got := f.StaleWarning(path); got != "" {
		t.Fatalf("未跟踪文件的警告应为空，得到 %q", got)
	}

	f.MarkRead(path)
	if got := f.StaleWarning(path); got != "" {
		t.Fatalf("新鲜文件的警告应为空，得到 %q", got)
	}

	externalChange(t, path, "v1")
	got := f.StaleWarning(path)
	if !strings.Contains(got, "文件已被外部修改，建议改前重读") {
		t.Fatalf("警告文本应包含固定提示短语，得到 %q", got)
	}
	if !strings.Contains(got, path) {
		t.Fatalf("警告文本应包含文件路径 %q，得到 %q", path, got)
	}

	f.MarkWrite(path)
	if got := f.StaleWarning(path); got != "" {
		t.Fatalf("写入后警告应为空，得到 %q", got)
	}
}

func TestFileFreshnessCJKPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "文档-测试.txt")
	writeFreshnessFile(t, path, "初始内容")

	f := NewFileFreshness()
	f.MarkRead(path)
	if f.IsStale(path) {
		t.Fatal("CJK 路径读取后不应为 stale")
	}

	externalChange(t, path, "外部改动后的内容")
	if !f.IsStale(path) {
		t.Fatal("CJK 路径外部改动后应为 stale")
	}
	got := f.StaleWarning(path)
	if !strings.Contains(got, "文件已被外部修改，建议改前重读") || !strings.Contains(got, path) {
		t.Fatalf("CJK 路径警告不正确: %q", got)
	}

	f.MarkWrite(path)
	if f.IsStale(path) {
		t.Fatal("CJK 路径写入后不应为 stale")
	}
}

func TestFileFreshnessMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gone.txt")

	f := NewFileFreshness()
	// 未跟踪的缺失文件：没有基线，不报 stale。
	if f.IsStale(path) {
		t.Fatal("未跟踪文件不应为 stale")
	}

	writeFreshnessFile(t, path, "v0")
	f.MarkRead(path)
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !f.IsStale(path) {
		t.Fatal("文件被外部删除应判为 stale")
	}
}

func TestFileFreshnessDoesNotMutateInput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input.txt")
	writeFreshnessFile(t, path, "v0")

	original := path
	f := NewFileFreshness()
	f.MarkRead(path)
	if path != original {
		t.Fatalf("MarkRead 修改了入参: %q -> %q", original, path)
	}
	externalChange(t, path, "v1")
	if !f.IsStale(path) || path != original {
		t.Fatalf("IsStale 修改了入参或判定错误: path=%q", path)
	}
	_ = f.StaleWarning(path)
	if path != original {
		t.Fatalf("StaleWarning 修改了入参: %q -> %q", original, path)
	}
}

func TestFileFreshnessConcurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "concurrent.txt")
	writeFreshnessFile(t, path, "v0")

	var f FileFreshness
	f.MarkRead(path)

	const workers, rounds = 8, 50
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < rounds; j++ {
				switch (i + j) % 4 {
				case 0:
					f.MarkRead(path)
				case 1:
					f.MarkWrite(path)
				case 2:
					_ = f.IsStale(path)
				case 3:
					_ = f.StaleWarning(path)
				}
			}
		}(i)
	}
	wg.Wait()

	// 最后我们自己的写入应清除 stale。
	writeFreshnessFile(t, path, "final")
	f.MarkWrite(path)
	if f.IsStale(path) {
		t.Fatal("并发读写后，最后一次自己的写入应使文件不 stale")
	}
}
