package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

// 本文件是 R19 接入 checkpoint 所需的 WorkspaceFilePaths 测试。
// 使用独立的 helper 名，避免与同包其它测试冲突。

// wfpWrite 在 root 下按工作区相对路径（正斜杠）写一个普通文件。
func wfpWrite(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", abs, err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", abs, err)
	}
}

// 枚举的键集合必须与 SnapshotWorkspace 完全一致（同一套遍历规则）。
func TestWorkspaceFilePathsMatchesSnapshotKeys(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{
		"a.txt", "dir/b.txt", "dir/sub/c.go", "top.md",
		"中文/说明.txt", "node_modules/pkg/index.js", ".git/config",
	} {
		wfpWrite(t, root, rel, "content-"+rel)
	}

	snap := wdSnapshot(t, root)
	paths := WorkspaceFilePaths(root)

	want := make([]string, 0, len(snap))
	for rel := range snap {
		want = append(want, rel)
	}
	sort.Strings(want)
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("WorkspaceFilePaths = %#v, want snapshot keys %#v", paths, want)
	}
}

// 噪目录整棵跳过，且其内的文件不出现在枚举结果中。
func TestWorkspaceFilePathsSkipsNoiseDirs(t *testing.T) {
	root := t.TempDir()
	wfpWrite(t, root, "keep.txt", "keep")
	for _, rel := range []string{
		".git/config", "node_modules/pkg/index.js", "dist/bundle.js",
		"vendor/lib.go", "target/out.bin", "__pycache__/x.pyc",
		"build/a", "out/b", ".venv/c", "venv/d", ".cache/e", ".next/f",
		"coverage/g", ".idea/h", ".vscode/i", ".argent/state.json",
	} {
		wfpWrite(t, root, rel, "noise")
	}

	got := WorkspaceFilePaths(root)
	want := []string{"keep.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WorkspaceFilePaths = %#v, want %#v", got, want)
	}
	for _, p := range got {
		for _, seg := range strings.Split(p, "/") {
			if workspaceDiffIgnoredDirs[seg] {
				t.Errorf("噪目录条目 %q 不应出现在枚举结果中", p)
			}
		}
	}
}

// 符号链接（含指向外部的目录链接）不参与枚举；不支持创建链接时跳过。
func TestWorkspaceFilePathsIgnoresSymlinks(t *testing.T) {
	root := t.TempDir()
	wfpWrite(t, root, "real.txt", "real")

	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(filepath.Join(root, "real.txt"), link); err != nil {
		t.Skipf("当前环境无法创建符号链接，跳过: %v", err)
	}
	outside := t.TempDir()
	wfpWrite(t, outside, "secret.txt", "secret")
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Skipf("当前环境无法创建目录符号链接，跳过: %v", err)
	}

	got := WorkspaceFilePaths(root)
	want := []string{"real.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WorkspaceFilePaths = %#v, want %#v", got, want)
	}
}

// 路径以正斜杠分隔且按字典序稳定排列，与创建顺序无关。
func TestWorkspaceFilePathsSlashAndSorted(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"z/z.txt", "m.txt", "a/inner.txt", "b.txt", "a/aa.txt"} {
		wfpWrite(t, root, rel, "x")
	}

	got := WorkspaceFilePaths(root)
	want := []string{"a/aa.txt", "a/inner.txt", "b.txt", "m.txt", "z/z.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WorkspaceFilePaths = %#v, want %#v", got, want)
	}
	for _, p := range got {
		if strings.Contains(p, `\`) {
			t.Errorf("路径应使用正斜杠分隔: %q", p)
		}
	}
}

// 超过上限时截断到 limit 项并保持排序。用可注入的小上限验证截断逻辑本身，
// 与默认上限的具体数值无关；真实创建上万文件在 Windows 上耗时十几秒。
func TestWorkspaceFilePathsBounded(t *testing.T) {
	root := t.TempDir()
	const limit = 20
	count := limit + 20
	for i := 0; i < count; i++ {
		wfpWrite(t, root, fmt.Sprintf("f%05d.txt", i), "x")
	}

	got := workspaceFilePaths(root, limit)
	if len(got) != limit {
		t.Fatalf("枚举数 = %d, want %d", len(got), limit)
	}
	if !sort.StringsAreSorted(got) {
		t.Fatal("截断结果未按字典序排序")
	}
	if got[0] != "f00000.txt" || got[len(got)-1] != fmt.Sprintf("f%05d.txt", limit-1) {
		t.Fatalf("截断端点异常: %q ... %q", got[0], got[len(got)-1])
	}
}

// 空工作区、缺失 root 与普通文件 root 均返回 nil。
func TestWorkspaceFilePathsEmptyAndInvalidRoot(t *testing.T) {
	root := t.TempDir()
	if got := WorkspaceFilePaths(root); got != nil {
		t.Fatalf("空工作区枚举 = %v, want nil", got)
	}
	if got := WorkspaceFilePaths(filepath.Join(root, "missing")); got != nil {
		t.Fatalf("缺失 root 枚举 = %v, want nil", got)
	}
	file := filepath.Join(root, "file.txt")
	wfpWrite(t, root, "file.txt", "x")
	if got := WorkspaceFilePaths(file); got != nil {
		t.Fatalf("普通文件 root 枚举 = %v, want nil", got)
	}
}

// CJK 路径按 UTF-8 原样保留，正斜杠分隔且排序稳定。
func TestWorkspaceFilePathsCJKPaths(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"文档/说明.txt", "日本語-文件.txt", "新增/目录/文件.txt"} {
		wfpWrite(t, root, rel, "内容")
	}

	got := WorkspaceFilePaths(root)
	want := []string{"日本語-文件.txt", "新增/目录/文件.txt", "文档/说明.txt"}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WorkspaceFilePaths = %#v, want %#v", got, want)
	}
	for _, p := range got {
		if !utf8.ValidString(p) {
			t.Errorf("路径 %q 不是合法 UTF-8", p)
		}
		if strings.Contains(p, `\`) {
			t.Errorf("路径应使用正斜杠分隔: %q", p)
		}
	}
}

// 纯函数语义：重复调用结果一致，且每次调用独立重新扫描文件系统。
func TestWorkspaceFilePathsPureRepeatable(t *testing.T) {
	root := t.TempDir()
	wfpWrite(t, root, "a.txt", "a")

	first := WorkspaceFilePaths(root)
	if again := WorkspaceFilePaths(root); !reflect.DeepEqual(first, again) {
		t.Fatalf("重复调用结果不一致: %v vs %v", first, again)
	}

	// 文件变动后再次调用应反映变动；旧返回的切片不被改动。
	wfpWrite(t, root, "b.txt", "b")
	if again := WorkspaceFilePaths(root); !reflect.DeepEqual(again, []string{"a.txt", "b.txt"}) {
		t.Fatalf("未反映文件系统变动: %v", again)
	}
	if !reflect.DeepEqual(first, []string{"a.txt"}) {
		t.Fatalf("旧返回值被修改: %v", first)
	}
}
