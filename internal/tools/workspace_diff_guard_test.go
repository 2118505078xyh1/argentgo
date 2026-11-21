package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// 本文件是 R17 P3 shell 间接改动发现相关的守卫测试：只依赖
// SnapshotWorkspace / DiscoverWorkspaceChanges 两个公开 API，按真实签名编写。
// 与同目录其它测试使用不同的 helper 与测试名，避免符号冲突。

// wdGuardWrite 在 root 下按工作区相对路径（正斜杠）写一个文件，必要时创建父目录。
func wdGuardWrite(t *testing.T, root, rel, content string) string {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", abs, err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", abs, err)
	}
	return abs
}

// wdGuardSnapshot 调用 SnapshotWorkspace 并在出错时直接失败测试。
func wdGuardSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snap, err := SnapshotWorkspace(root)
	if err != nil {
		t.Fatalf("SnapshotWorkspace(%q): %v", root, err)
	}
	return snap
}

// TestWorkspaceDiffGuardSnapshotStableHash 验证快照以相对路径为键、摘要稳定：
// 同内容不同路径摘要相同、不同内容摘要不同、重复调用结果一致。
func TestWorkspaceDiffGuardSnapshotStableHash(t *testing.T) {
	root := t.TempDir()
	wdGuardWrite(t, root, "a.txt", "hello")
	wdGuardWrite(t, root, "b.txt", "hello")
	wdGuardWrite(t, root, "dir/c.txt", "world")

	snap1 := wdGuardSnapshot(t, root)
	snap2 := wdGuardSnapshot(t, root)
	if !reflect.DeepEqual(snap1, snap2) {
		t.Fatalf("快照不稳定：\n第一次 %v\n第二次 %v", snap1, snap2)
	}
	if len(snap1) != 3 {
		t.Fatalf("快照条目数 = %d, want 3: %v", len(snap1), snap1)
	}
	for _, rel := range []string{"a.txt", "b.txt", "dir/c.txt"} {
		if snap1[rel] == "" {
			t.Errorf("快照缺少相对路径 %q: %v", rel, snap1)
		}
	}
	if snap1["a.txt"] != snap1["b.txt"] {
		t.Errorf("同内容应有同摘要：a=%q b=%q", snap1["a.txt"], snap1["b.txt"])
	}
	if snap1["a.txt"] == snap1["dir/c.txt"] {
		t.Errorf("不同内容应有不同摘要：a=%q c=%q", snap1["a.txt"], snap1["dir/c.txt"])
	}
	// 摘要为固定长度的小写十六进制串。
	for rel, hash := range snap1 {
		if len(hash) != workspaceDiffHashBytes*2 {
			t.Errorf("%q 摘要长度 = %d, want %d", rel, len(hash), workspaceDiffHashBytes*2)
		}
		for _, r := range hash {
			if !strings.ContainsRune("0123456789abcdef", r) {
				t.Errorf("%q 摘要含非十六进制字符 %q: %q", rel, r, hash)
				break
			}
		}
	}
}

// TestWorkspaceDiffGuardSnapshotSkipsNoiseDirs 验证噪目录整棵跳过，且不影响正常文件入表。
func TestWorkspaceDiffGuardSnapshotSkipsNoiseDirs(t *testing.T) {
	root := t.TempDir()
	wdGuardWrite(t, root, "keep.txt", "keep")
	noise := []string{
		".git/config", "node_modules/pkg/index.js", "target/out.o", "dist/app.js",
		".argent/state.json", "vendor/lib.go", ".idea/workspace.xml", ".vscode/settings.json",
		"build/app.exe", "out/x", "__pycache__/x.pyc", ".venv/x", "venv/x",
		".cache/x", ".next/x", "coverage/x",
	}
	for _, rel := range noise {
		wdGuardWrite(t, root, rel, "noise")
	}

	snap := wdGuardSnapshot(t, root)
	if len(snap) != 1 || snap["keep.txt"] == "" {
		t.Fatalf("只应保留 keep.txt，实际快照: %v", snap)
	}
	for rel := range snap {
		for _, seg := range strings.Split(rel, "/") {
			if workspaceDiffIgnoredDirs[seg] {
				t.Errorf("噪目录条目 %q 不应出现在快照中", rel)
			}
		}
	}
}

// TestWorkspaceDiffGuardSnapshotSkipsNonRegularFiles 验证符号链接不被记录，
// 普通文件正常入表。若环境不支持创建符号链接则跳过该子断言。
func TestWorkspaceDiffGuardSnapshotSkipsNonRegularFiles(t *testing.T) {
	root := t.TempDir()
	wdGuardWrite(t, root, "regular.txt", "regular")

	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(filepath.Join(root, "regular.txt"), link); err != nil {
		t.Logf("当前环境无法创建符号链接，跳过该子断言: %v", err)
	} else {
		snap := wdGuardSnapshot(t, root)
		if len(snap) != 1 || snap["regular.txt"] == "" {
			t.Fatalf("符号链接不应入表，实际快照: %v", snap)
		}
		if _, ok := snap["link.txt"]; ok {
			t.Error("符号链接 link.txt 不应出现在快照中")
		}
	}
}

// TestWorkspaceDiffGuardSnapshotBoundedPrefix 验证单文件读取有上限：
// 仅前 workspaceDiffFileReadLimit 字节参与摘要，超出部分的差异不影响结果；
// 含 NUL / 高位字节的二进制文件同样得到稳定摘要而不 panic。
func TestWorkspaceDiffGuardSnapshotBoundedPrefix(t *testing.T) {
	root := t.TempDir()
	prefix := strings.Repeat("x", workspaceDiffFileReadLimit)
	wdGuardWrite(t, root, "prefix-a.bin", prefix+"tail-A")
	wdGuardWrite(t, root, "prefix-b.bin", prefix+"tail-B")
	wdGuardWrite(t, root, "binary.bin", string([]byte{0, 1, 2, 0xff, 0xfe, 0, '\n', 0}))

	snap := wdGuardSnapshot(t, root)
	if snap["prefix-a.bin"] == "" || snap["prefix-b.bin"] == "" {
		t.Fatalf("大文件应有摘要: %v", snap)
	}
	if snap["prefix-a.bin"] != snap["prefix-b.bin"] {
		t.Errorf("超出读取上限的尾部差异不应影响摘要：a=%q b=%q",
			snap["prefix-a.bin"], snap["prefix-b.bin"])
	}
	if snap["binary.bin"] == "" {
		t.Fatalf("二进制文件应有摘要: %v", snap)
	}
	if again := wdGuardSnapshot(t, root); again["binary.bin"] != snap["binary.bin"] {
		t.Errorf("二进制文件摘要不稳定：%q vs %q", snap["binary.bin"], again["binary.bin"])
	}
}

// TestWorkspaceDiffGuardSnapshotCJKPaths 验证 CJK 路径按 UTF-8 原样保留为正斜杠相对路径。
func TestWorkspaceDiffGuardSnapshotCJKPaths(t *testing.T) {
	root := t.TempDir()
	wdGuardWrite(t, root, "文档/说明.txt", "你好")
	wdGuardWrite(t, root, "模型/claude-中文.txt", "内容")
	wdGuardWrite(t, root, "mixed/普通-名字.md", "# 标题")

	snap := wdGuardSnapshot(t, root)
	if len(snap) != 3 {
		t.Fatalf("快照条目数 = %d, want 3: %v", len(snap), snap)
	}
	for _, rel := range []string{"文档/说明.txt", "模型/claude-中文.txt", "mixed/普通-名字.md"} {
		if snap[rel] == "" {
			t.Errorf("快照缺少 CJK 相对路径 %q: %v", rel, snap)
		}
	}
	for rel := range snap {
		if strings.Contains(rel, `\`) {
			t.Errorf("相对路径应使用正斜杠: %q", rel)
		}
	}
}

// TestWorkspaceDiffGuardSnapshotErrors 验证 root 不存在或不是目录时返回错误。
func TestWorkspaceDiffGuardSnapshotErrors(t *testing.T) {
	root := t.TempDir()
	if _, err := SnapshotWorkspace(filepath.Join(root, "missing")); err == nil {
		t.Error("root 不存在时应返回错误")
	}
	file := wdGuardWrite(t, root, "notadir.txt", "x")
	if _, err := SnapshotWorkspace(file); err == nil {
		t.Error("root 是普通文件时应返回错误")
	}
}

// TestWorkspaceDiffGuardDiscoverAddedModifiedDeleted 验证新增、修改、删除都被计入，
// 且无变化时返回 nil。
func TestWorkspaceDiffGuardDiscoverAddedModifiedDeleted(t *testing.T) {
	root := t.TempDir()
	wdGuardWrite(t, root, "a.txt", "a")
	wdGuardWrite(t, root, "b.txt", "before")
	wdGuardWrite(t, root, "c.txt", "c")
	before := wdGuardSnapshot(t, root)

	if got := DiscoverWorkspaceChanges(root, before); got != nil {
		t.Errorf("无变化时应返回 nil, got %v", got)
	}

	wdGuardWrite(t, root, "b.txt", "after") // 修改
	if err := os.Remove(filepath.Join(root, "c.txt")); err != nil {
		t.Fatalf("remove c.txt: %v", err)
	}
	wdGuardWrite(t, root, "d.txt", "d") // 新增

	got := DiscoverWorkspaceChanges(root, before)
	want := []string{"b.txt", "c.txt", "d.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DiscoverWorkspaceChanges = %v, want %v", got, want)
	}
}

// TestWorkspaceDiffGuardDiscoverStableSortedWithCJK 验证返回列表按字典序稳定排序，
// CJK 路径按 UTF-8 字节序参与排序且不被截断。
func TestWorkspaceDiffGuardDiscoverStableSortedWithCJK(t *testing.T) {
	root := t.TempDir()
	names := []string{
		"z.txt", "中文/乙.txt", "a.txt", "中文/甲.txt",
		"B.txt", "文档/说明.txt", "á.txt", "mix/混-Z.txt",
	}
	for _, name := range names {
		wdGuardWrite(t, root, name, name+"-content")
	}

	before := map[string]string{} // 空 before：全部文件视为新增。
	got := DiscoverWorkspaceChanges(root, before)
	if len(got) != len(names) {
		t.Fatalf("变更数 = %d, want %d: %v", len(got), len(names), got)
	}
	wantSorted := append([]string(nil), got...)
	sort.Strings(wantSorted)
	if !reflect.DeepEqual(got, wantSorted) {
		t.Errorf("返回列表未按字典序排序：\ngot  %v\nwant %v", got, wantSorted)
	}
	for i := 0; i < 5; i++ {
		if again := DiscoverWorkspaceChanges(root, before); !reflect.DeepEqual(again, got) {
			t.Fatalf("第 %d 次调用结果不稳定：%v vs %v", i, again, got)
		}
	}
	for _, name := range []string{"中文/甲.txt", "中文/乙.txt", "文档/说明.txt", "mix/混-Z.txt"} {
		found := false
		for _, p := range got {
			if p == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("CJK 路径 %q 未出现在变更列表中: %v", name, got)
		}
	}
}

// TestWorkspaceDiffGuardDiscoverDoesNotMutateBefore 验证 before 快照绝不被修改。
func TestWorkspaceDiffGuardDiscoverDoesNotMutateBefore(t *testing.T) {
	root := t.TempDir()
	wdGuardWrite(t, root, "a.txt", "a")
	wdGuardWrite(t, root, "b.txt", "b")
	before := wdGuardSnapshot(t, root)

	copyBefore := make(map[string]string, len(before))
	for k, v := range before {
		copyBefore[k] = v
	}

	wdGuardWrite(t, root, "b.txt", "changed")
	wdGuardWrite(t, root, "c.txt", "new")
	_ = DiscoverWorkspaceChanges(root, before)

	if !reflect.DeepEqual(before, copyBefore) {
		t.Errorf("before 被修改：\n现在 %v\n原始 %v", before, copyBefore)
	}
}

// TestWorkspaceDiffGuardDiscoverIgnoresNoiseChanges 验证噪目录中的文件变动不被报告。
func TestWorkspaceDiffGuardDiscoverIgnoresNoiseChanges(t *testing.T) {
	root := t.TempDir()
	wdGuardWrite(t, root, "keep.txt", "k")
	before := wdGuardSnapshot(t, root)

	wdGuardWrite(t, root, ".git/config", "changed")
	wdGuardWrite(t, root, "node_modules/pkg/index.js", "new")
	wdGuardWrite(t, root, "keep.txt", "changed")

	got := DiscoverWorkspaceChanges(root, before)
	want := []string{"keep.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DiscoverWorkspaceChanges = %v, want %v", got, want)
	}
}

// TestWorkspaceDiffGuardDiscoverBounded 验证返回路径数有上限。
func TestWorkspaceDiffGuardDiscoverBounded(t *testing.T) {
	root := t.TempDir()
	count := workspaceDiffMaxChanges + 37
	for i := 0; i < count; i++ {
		wdGuardWrite(t, root, filepath.Join("many", fmt.Sprintf("f%04d.txt", i)), "x")
	}
	got := DiscoverWorkspaceChanges(root, map[string]string{})
	if len(got) != workspaceDiffMaxChanges {
		t.Errorf("变更数 = %d, want 上限 %d", len(got), workspaceDiffMaxChanges)
	}
}

// TestWorkspaceDiffGuardDiscoverInvalidRootReportsDeletions 验证 root 失效时
// 不 panic，before 中的路径按删除报告且不泄漏越界路径。
func TestWorkspaceDiffGuardDiscoverInvalidRootReportsDeletions(t *testing.T) {
	before := map[string]string{"a.txt": "x", "中文/b.txt": "y"}
	want := []string{"a.txt", "中文/b.txt"}

	missing := filepath.Join(t.TempDir(), "missing")
	if got := DiscoverWorkspaceChanges(missing, before); !reflect.DeepEqual(got, want) {
		t.Errorf("root 不存在时 = %v, want %v", got, want)
	}

	tmp := t.TempDir()
	file := wdGuardWrite(t, tmp, "notadir.txt", "x")
	if got := DiscoverWorkspaceChanges(file, before); !reflect.DeepEqual(got, want) {
		t.Errorf("root 是普通文件时 = %v, want %v", got, want)
	}
}
