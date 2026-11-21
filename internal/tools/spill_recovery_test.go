package tools

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// writeRecoveryArtifact 把一个具有规范溢出名称的文件放入 dir，
// 并返回其路径。固定 id 在这里没问题：这些测试只做读取。
func writeRecoveryArtifact(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "tool-output-0123456789abcdef.log")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSpillRecoveryReadLineRanges(t *testing.T) {
	dir := t.TempDir()
	path := writeRecoveryArtifact(t, dir, "l1\nl2\nl3\nl4\nl5\n")
	r := NewSpillRecovery(dir)

	for _, tc := range []struct {
		name       string
		start, end int
		want       string
	}{
		{"middle range is inclusive", 2, 4, "l2\nl3\nl4"},
		{"single line", 3, 3, "l3"},
		{"first line", 1, 1, "l1"},
		{"start clamped up to one", 0, 2, "l1\nl2"},
		{"negative start clamped", -5, 1, "l1"},
		{"end clamped to total", 4, 999, "l4\nl5"},
		{"to EOF keeps trailing newline", 2, 0, "l2\nl3\nl4\nl5\n"},
		{"negative end reads to EOF", 1, -1, "l1\nl2\nl3\nl4\nl5\n"},
		{"start past total is empty", 6, 10, ""},
		{"reversed range is empty", 4, 2, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Read(path, tc.start, tc.end)
			if err != nil {
				t.Fatalf("Read(%d, %d): %v", tc.start, tc.end, err)
			}
			if got != tc.want {
				t.Fatalf("content = %q, want %q", got, tc.want)
			}
			if !r.IsSpillPath(path) {
				t.Fatal("legitimate artifact reported as not a spill path")
			}
		})
	}
}

func TestSpillRecoveryRejectsUnsafePaths(t *testing.T) {
	dir := t.TempDir()
	artifact := writeRecoveryArtifact(t, dir, "ok\n")
	r := NewSpillRecovery(dir)

	// 带遍历段的、形似产出的路径必须被拒绝，即使它解析回
	// 溢出目录中的真实文件。直接构造字符串：filepath.Join
	// 会把 ".." 清理掉，那样（正确地）就变成合法路径了。
	sep := string(filepath.Separator)
	traversal := dir + sep + ".." + sep + filepath.Base(dir) + sep + "tool-output-0123456789abcdef.log"

	// 溢出目录内的普通非产出文件名同样必须被拒绝。
	notes := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notes, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 规范产出名称、但指向目录外敏感文件的符号链接。
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "tool-output-fedcba9876543210.log")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	missing := filepath.Join(dir, "tool-output-0000000000000000.log")

	for _, tc := range []struct {
		name    string
		path    string
		wantErr error
	}{
		{"traversal segment", traversal, errSpillInvalidPath},
		{"non artifact filename", notes, errSpillNotArtifact},
		{"directory as artifact", dir, errSpillNotArtifact},
		{"symlink artifact", link, errSpillInvalidPath},
		{"missing artifact", missing, nil}, // os.Stat 失败 -> 包装错误
		{"empty path", "", errSpillInvalidPath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Read(tc.path, 1, 1)
			if err == nil {
				t.Fatalf("Read(%q) unexpectedly succeeded with %q", tc.path, got)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want errors.Is(..., %v)", err, tc.wantErr)
			}
			if r.IsSpillPath(tc.path) {
				t.Fatalf("IsSpillPath(%q) = true, want false", tc.path)
			}
		})
	}

	// 健全性检查：同一目录中合法的产出仍可正常读取。
	if got, err := r.Read(artifact, 1, 1); err != nil || got != "ok" {
		t.Fatalf("legitimate artifact = (%q, %v), want (\"ok\", nil)", got, err)
	}
}

func TestSpillRecoveryCJKContent(t *testing.T) {
	dir := t.TempDir()
	content := "中文😀第一行\nsecond 世界🌍line\n再见👋third\n末行 End 行\n"
	path := writeRecoveryArtifact(t, dir, content)
	r := NewSpillRecovery(dir)

	got, err := r.Read(path, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := "second 世界🌍line\n再见👋third"
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	if !utf8.ValidString(got) || strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("range contains broken runes: %q", got)
	}

	// 读到 EOF 的读取也必须保持 rune 安全并保留尾随换行。
	full, err := r.Read(path, 1, 0)
	if err != nil || full != content {
		t.Fatalf("full read = (%q, %v), want (%q, nil)", full, err, content)
	}
}

func TestSpillRecoveryBindsToDir(t *testing.T) {
	// 两个目录各放一个同名合法产出；绑定到其中一个的恢复器
	// 不能读取另一个目录里的产出，即使文件名完全规范。
	inside := t.TempDir()
	outside := t.TempDir()
	insidePath := writeRecoveryArtifact(t, inside, "inside\n")
	outsidePath := writeRecoveryArtifact(t, outside, "outside\n")

	r := NewSpillRecovery(inside)
	if got, err := r.Read(insidePath, 1, 1); err != nil || got != "inside" {
		t.Fatalf("inside artifact = (%q, %v), want (\"inside\", nil)", got, err)
	}
	if _, err := r.Read(outsidePath, 1, 1); err == nil {
		t.Fatal("artifact outside bound dir was readable")
	}
	if r.IsSpillPath(outsidePath) {
		t.Fatal("artifact outside bound dir reported as spill path")
	}
}
