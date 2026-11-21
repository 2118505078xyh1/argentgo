package session

import (
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeUsageFile 在 dir 下写入指定相对路径的文件，内容为 n 个 'x'，返回其完整路径。
func writeUsageFile(t *testing.T, dir, rel string, n int) string {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(path, make([]byte, n), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return path
}

// zeroDiskUsage 返回空目录下的期望快照：三个键恒为 0。
func zeroDiskUsage() DiskUsage {
	return DiskUsage{ByKind: map[string]int64{
		diskKindSession: 0,
		diskKindArchive: 0,
		diskKindOther:   0,
	}}
}

func TestMeasureDiskUsageClassifiesFiles(t *testing.T) {
	dir := t.TempDir()

	// 覆盖三种分类、复合扩展名与大写后缀。
	writeUsageFile(t, dir, "chat.jsonl", 10)
	writeUsageFile(t, dir, "bundle.zip", 20)
	writeUsageFile(t, dir, "dump.tar.gz", 30)
	writeUsageFile(t, dir, "notes.txt", 7)
	writeUsageFile(t, dir, "UPPER.JSONL", 5)
	writeUsageFile(t, dir, "README", 3)

	got, err := MeasureDiskUsage(dir)
	if err != nil {
		t.Fatalf("MeasureDiskUsage: %v", err)
	}
	want := DiskUsage{
		Files: 6,
		Bytes: 10 + 20 + 30 + 7 + 5 + 3,
		ByKind: map[string]int64{
			diskKindSession: 10 + 5,
			diskKindArchive: 20 + 30,
			diskKindOther:   7 + 3,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("usage mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestMeasureDiskUsageRecursesSubdirs(t *testing.T) {
	dir := t.TempDir()

	writeUsageFile(t, dir, "root.jsonl", 4)
	writeUsageFile(t, dir, "a/b/deep.jsonl", 6)
	writeUsageFile(t, dir, "a/b/c/old.tar.gz", 8)
	writeUsageFile(t, dir, "x", 2)

	got, err := MeasureDiskUsage(dir)
	if err != nil {
		t.Fatalf("MeasureDiskUsage: %v", err)
	}
	want := DiskUsage{
		Files: 4,
		Bytes: 4 + 6 + 8 + 2,
		ByKind: map[string]int64{
			diskKindSession: 4 + 6,
			diskKindArchive: 8,
			diskKindOther:   2,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("usage mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestMeasureDiskUsageSkipsSymlinks(t *testing.T) {
	dir := t.TempDir()

	writeUsageFile(t, dir, "real.jsonl", 11)
	writeUsageFile(t, dir, "inside/real.zip", 13)

	// 文件符号链接、目录符号链接、断开的符号链接都应被跳过，不跟随也不计入。
	linkTargets := []struct {
		name   string
		target string
	}{
		{"link.jsonl", filepath.Join(dir, "real.jsonl")},
		{"linkdir", filepath.Join(dir, "inside")},
		{"broken.jsonl", filepath.Join(dir, "missing.jsonl")},
	}
	for _, lt := range linkTargets {
		if err := os.Symlink(lt.target, filepath.Join(dir, lt.name)); err != nil {
			t.Skipf("skip: cannot create symlink (需要开发者模式/权限): %v", err)
		}
	}

	got, err := MeasureDiskUsage(dir)
	if err != nil {
		t.Fatalf("MeasureDiskUsage: %v", err)
	}
	want := DiskUsage{
		Files: 2,
		Bytes: 11 + 13,
		ByKind: map[string]int64{
			diskKindSession: 11,
			diskKindArchive: 13,
			diskKindOther:   0,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("symlinks should be skipped:\n got: %#v\nwant: %#v", got, want)
	}
}

// usageFakeDirEntry 是 fs.DirEntry 的最小实现，用于在无符号链接创建权限的平台上
// 验证 symlink 判定分支。
type usageFakeDirEntry struct {
	name  string
	mode  fs.FileMode
	isDir bool
}

func (e usageFakeDirEntry) Name() string               { return e.name }
func (e usageFakeDirEntry) IsDir() bool                { return e.isDir }
func (e usageFakeDirEntry) Type() fs.FileMode          { return e.mode }
func (e usageFakeDirEntry) Info() (fs.FileInfo, error) { return nil, errors.New("unused") }

func TestDiskUsageIsSymlink(t *testing.T) {
	tests := []struct {
		name  string
		entry usageFakeDirEntry
		want  bool
	}{
		{"符号链接", usageFakeDirEntry{name: "l", mode: os.ModeSymlink}, true},
		{"带权限位的符号链接", usageFakeDirEntry{name: "l", mode: os.ModeSymlink | 0o777}, true},
		{"普通文件", usageFakeDirEntry{name: "f", mode: 0}, false},
		{"目录", usageFakeDirEntry{name: "d", mode: os.ModeDir, isDir: true}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := diskUsageIsSymlink(tc.entry); got != tc.want {
				t.Fatalf("diskUsageIsSymlink(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestMeasureDiskUsageEmptyDir(t *testing.T) {
	dir := t.TempDir()
	// 只建一个空子目录，仍应视为零占用。
	if err := os.Mkdir(filepath.Join(dir, "empty-child"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := MeasureDiskUsage(dir)
	if err != nil {
		t.Fatalf("MeasureDiskUsage: %v", err)
	}
	if want := zeroDiskUsage(); !reflect.DeepEqual(got, want) {
		t.Fatalf("empty dir usage = %#v, want %#v", got, want)
	}
}

func TestMeasureDiskUsageCJKNames(t *testing.T) {
	dir := t.TempDir()

	writeUsageFile(t, dir, "会话记录.jsonl", 9)
	writeUsageFile(t, dir, "归档/历史.tar.gz", 15)
	writeUsageFile(t, dir, "中文.txt", 4)

	got, err := MeasureDiskUsage(dir)
	if err != nil {
		t.Fatalf("MeasureDiskUsage: %v", err)
	}
	want := DiskUsage{
		Files: 3,
		Bytes: 9 + 15 + 4,
		ByKind: map[string]int64{
			diskKindSession: 9,
			diskKindArchive: 15,
			diskKindOther:   4,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CJK names mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestClampAddInt64(t *testing.T) {
	tests := []struct {
		name string
		a, b int64
		want int64
	}{
		{"普通相加", 10, 5, 15},
		{"零值", 0, 0, 0},
		{"负加数不溢出", 7, -3, 4},
		{"恰好到上限", math.MaxInt64 - 1, 1, math.MaxInt64},
		{"超过上限钳制", math.MaxInt64, 1, math.MaxInt64},
		{"大数相加钳制", math.MaxInt64/2 + 1, math.MaxInt64/2 + 1, math.MaxInt64},
		{"上限加负数不钳制", math.MaxInt64, -1, math.MaxInt64 - 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampAddInt64(tc.a, tc.b); got != tc.want {
				t.Fatalf("clampAddInt64(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestMeasureDiskUsageDoesNotModifyFiles(t *testing.T) {
	dir := t.TempDir()

	paths := []string{
		writeUsageFile(t, dir, "a.jsonl", 12),
		writeUsageFile(t, dir, "b.zip", 8),
		writeUsageFile(t, dir, "sub/c.txt", 5),
	}

	// 快照：路径集合、大小与修改时间；测量前后必须完全一致。
	before := usageSnapshot(t, dir, paths)
	if _, err := MeasureDiskUsage(dir); err != nil {
		t.Fatalf("MeasureDiskUsage: %v", err)
	}
	after := usageSnapshot(t, dir, paths)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("MeasureDiskUsage modified files:\n before: %#v\n after: %#v", before, after)
	}
}

// usageSnapshot 记录目录中的文件清单及其大小、修改时间，用于验证纯读性质。
func usageSnapshot(t *testing.T, dir string, paths []string) []usageSnap {
	t.Helper()
	snap := make([]usageSnap, 0, len(paths))
	for _, p := range paths {
		info, err := os.Lstat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		snap = append(snap, usageSnap{
			name: p,
			size: info.Size(),
			mod:  info.ModTime().UnixNano(),
		})
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 { // a.jsonl, b.zip 与子目录 sub。
		t.Fatalf("unexpected entry count at root: %d", len(entries))
	}
	return snap
}

// usageSnap 是 os.FileInfo 的可比较投影，避免直接比对接口内部实现。
type usageSnap struct {
	name string
	size int64
	mod  int64
}

func TestMeasureDiskUsageRejectsInvalidRoot(t *testing.T) {
	dir := t.TempDir()
	file := writeUsageFile(t, dir, "a.jsonl", 1)

	if _, err := MeasureDiskUsage(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing dir: expected error")
	}
	if _, err := MeasureDiskUsage(file); err == nil {
		t.Error("regular file root: expected error")
	}

	link := filepath.Join(dir, "dirlink")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("skip: cannot create symlink (需要开发者模式/权限): %v", err)
	}
	if _, err := MeasureDiskUsage(link); err == nil {
		t.Error("symlink root: expected error")
	}
}
