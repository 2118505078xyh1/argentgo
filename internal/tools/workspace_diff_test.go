package tools

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"unicode/utf8"
)

func wdWrite(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func wdRemove(t *testing.T, root, rel string) {
	t.Helper()
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
		t.Fatal(err)
	}
}

func wdSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot, err := SnapshotWorkspace(root)
	if err != nil {
		t.Fatalf("SnapshotWorkspace: %v", err)
	}
	return snapshot
}

// 覆盖新增、修改、删除三类变化，同时验证摘要格式、稳定排序、不改入参
// 以及与新快照对比时结果为空。
func TestDiscoverWorkspaceChangesAddedModifiedDeleted(t *testing.T) {
	root := t.TempDir()
	wdWrite(t, root, "keep.txt", "keep")
	wdWrite(t, root, "change.txt", "before")
	wdWrite(t, root, "delete.txt", "gone")

	before := wdSnapshot(t, root)
	if len(before) != 3 {
		t.Fatalf("snapshot has %d entries, want 3: %v", len(before), before)
	}
	for path, digest := range before {
		if len(digest) != workspaceDiffHashBytes*2 {
			t.Fatalf("%s digest = %q, want %d hex chars", path, digest, workspaceDiffHashBytes*2)
		}
		if _, err := hex.DecodeString(digest); err != nil {
			t.Fatalf("%s digest %q is not hex: %v", path, digest, err)
		}
	}
	beforeCopy := make(map[string]string, len(before))
	for path, digest := range before {
		beforeCopy[path] = digest
	}

	wdWrite(t, root, "change.txt", "after")
	wdRemove(t, root, "delete.txt")
	wdWrite(t, root, "added.txt", "new")

	got := DiscoverWorkspaceChanges(root, before)
	want := []string{"added.txt", "change.txt", "delete.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changes = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(before, beforeCopy) {
		t.Fatalf("before snapshot was mutated: %v", before)
	}
	if again := DiscoverWorkspaceChanges(root, wdSnapshot(t, root)); len(again) != 0 {
		t.Fatalf("comparing against a fresh snapshot = %v, want no changes", again)
	}
}

// 噪目录内的文件既不进入快照，其变化也不进入差异列表。
func TestWorkspaceDiffSkipsNoiseDirs(t *testing.T) {
	root := t.TempDir()
	wdWrite(t, root, "src/app.go", "package main")
	for _, path := range []string{
		".git/config", "node_modules/pkg/index.js", "dist/bundle.js",
		"vendor/lib.go", "target/out.bin", "__pycache__/x.pyc",
	} {
		wdWrite(t, root, path, "original")
	}

	snapshot := wdSnapshot(t, root)
	want := map[string]string{"src/app.go": snapshot["src/app.go"]}
	if !reflect.DeepEqual(snapshot, want) {
		t.Fatalf("snapshot = %v, want only %v", snapshot, want)
	}

	for _, path := range []string{".git/config", "node_modules/pkg/index.js", "dist/bundle.js"} {
		wdWrite(t, root, path, "changed")
	}
	if got := DiscoverWorkspaceChanges(root, snapshot); len(got) != 0 {
		t.Fatalf("noise dir changes were reported: %v", got)
	}
}

// CJK 路径按原始 UTF-8 记录，排序稳定且不会切在字符中间。
func TestWorkspaceDiffCJKPaths(t *testing.T) {
	root := t.TempDir()
	wdWrite(t, root, "中文/说明.txt", "原文")
	wdWrite(t, root, "日本語-文件.txt", "before")
	before := wdSnapshot(t, root)

	wdWrite(t, root, "中文/说明.txt", "改后")
	wdRemove(t, root, "日本語-文件.txt")
	wdWrite(t, root, "新增/目录/文件.txt", "新")

	got := DiscoverWorkspaceChanges(root, before)
	want := []string{"中文/说明.txt", "新增/目录/文件.txt", "日本語-文件.txt"}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changes = %#v, want %#v", got, want)
	}
	for _, path := range got {
		if !utf8.ValidString(path) {
			t.Fatalf("path %q is not valid UTF-8", path)
		}
	}
}

// 创建顺序与字典序相反时，输出仍按字典序稳定排列。
func TestWorkspaceDiffStableSortedOrder(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"z/z.txt", "m.txt", "a/inner.txt", "b.txt"} {
		wdWrite(t, root, path, "before")
	}
	before := wdSnapshot(t, root)
	for _, path := range []string{"z/z.txt", "m.txt", "a/inner.txt", "b.txt"} {
		wdWrite(t, root, path, "after")
	}
	got := DiscoverWorkspaceChanges(root, before)
	want := []string{"a/inner.txt", "b.txt", "m.txt", "z/z.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changes = %#v, want %#v", got, want)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Fatalf("not sorted: %v", got)
		}
	}
}

// 删除路径的数量超过上限时，返回列表被截断到上限并保持排序。
func TestWorkspaceDiffBounded(t *testing.T) {
	root := t.TempDir()
	before := make(map[string]string, workspaceDiffMaxChanges+5)
	for i := 0; i < workspaceDiffMaxChanges+5; i++ {
		before[fmt.Sprintf("gone/%04d.txt", i)] = "hash"
	}
	got := DiscoverWorkspaceChanges(root, before)
	if len(got) != workspaceDiffMaxChanges {
		t.Fatalf("changes has %d entries, want %d", len(got), workspaceDiffMaxChanges)
	}
	if !sort.StringsAreSorted(got) {
		t.Fatal("bounded result is not sorted")
	}
	if got[0] != "gone/0000.txt" || got[len(got)-1] != fmt.Sprintf("gone/%04d.txt", workspaceDiffMaxChanges-1) {
		t.Fatalf("unexpected bounded endpoints: %q ... %q", got[0], got[len(got)-1])
	}
}

// 空工作区产出空快照与空差异；root 缺失或不是目录时快照返回错误。
func TestSnapshotWorkspaceEmptyAndInvalidRoot(t *testing.T) {
	root := t.TempDir()
	snapshot := wdSnapshot(t, root)
	if len(snapshot) != 0 {
		t.Fatalf("empty workspace snapshot = %v", snapshot)
	}
	if got := DiscoverWorkspaceChanges(root, snapshot); len(got) != 0 {
		t.Fatalf("empty workspace changes = %v", got)
	}
	if _, err := SnapshotWorkspace(filepath.Join(root, "missing")); err == nil {
		t.Fatal("snapshot accepted a missing root")
	}
	file := filepath.Join(root, "file.txt")
	wdWrite(t, root, "file.txt", "x")
	if _, err := SnapshotWorkspace(file); err == nil {
		t.Fatal("snapshot accepted a non-directory root")
	}
}

// 符号链接（含指向 root 外部的链接）既不进入快照，也不会把目标内容泄漏进差异。
func TestWorkspaceDiffIgnoresSymlinks(t *testing.T) {
	root := t.TempDir()
	wdWrite(t, root, "real.txt", "before")
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(filepath.Join(root, "real.txt"), link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	outside := t.TempDir()
	wdWrite(t, outside, "secret.txt", "secret")
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Skipf("directory symlink creation unavailable: %v", err)
	}

	before := wdSnapshot(t, root)
	if _, ok := before["link.txt"]; ok {
		t.Fatal("symlink to a file was snapshotted")
	}
	if _, ok := before["outside"]; ok {
		t.Fatal("symlink to a directory was snapshotted")
	}
	if _, ok := before["outside/secret.txt"]; ok {
		t.Fatal("symlink target leaked into the snapshot")
	}

	wdWrite(t, root, "real.txt", "after")
	got := DiscoverWorkspaceChanges(root, before)
	want := []string{"real.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changes = %#v, want %#v", got, want)
	}
}

// 大文件只读取前缀参与哈希：超出上限的尾部改动不可见，前部改动可见。
func TestWorkspaceDiffLargeFilePrefixBound(t *testing.T) {
	root := t.TempDir()
	data := bytes.Repeat([]byte("a"), workspaceDiffFileReadLimit+128)
	path := filepath.Join(root, "large.bin")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	before := wdSnapshot(t, root)
	if len(before) != 1 {
		t.Fatalf("snapshot = %v, want one large file", before)
	}

	data[len(data)-1] = 'b'
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := DiscoverWorkspaceChanges(root, before); len(got) != 0 {
		t.Fatalf("tail beyond read limit changed the digest: %v", got)
	}

	data[0] = 'b'
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := DiscoverWorkspaceChanges(root, before); !reflect.DeepEqual(got, []string{"large.bin"}) {
		t.Fatalf("prefix change not detected: %v", got)
	}
}
