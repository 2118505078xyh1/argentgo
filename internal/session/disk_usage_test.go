package session

import (
	"errors"
	"fmt"
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

// TestMeasureDiskUsageOrderIndependentWithoutSort 验证去掉路径排序后，同一组文件在不同
// 遍历次序下仍得到完全相同的快照。filepath.WalkDir 按文件名升序遍历，故用文件名前缀
// 控制顺序：asc 中小文件先累加，desc 中大文件先累加，但二者的 (kind,size) 多重集合一致。
func TestMeasureDiskUsageOrderIndependentWithoutSort(t *testing.T) {
	type fileSpec struct {
		name string
		size int
	}
	asc := []fileSpec{
		{"a.jsonl", 100},
		{"b.jsonl", 200},
		{"c.jsonl", 300},
		{"d.zip", 400},
		{"e.tar.gz", 500},
		{"f.txt", 600},
		{"g", 700},
	}
	desc := []fileSpec{
		{"a.txt", 700},
		{"b", 600},
		{"c.tar.gz", 500},
		{"d.zip", 400},
		{"e.jsonl", 300},
		{"f.jsonl", 200},
		{"g.jsonl", 100},
	}

	dirAsc, dirDesc := t.TempDir(), t.TempDir()
	for _, f := range asc {
		writeUsageFile(t, dirAsc, f.name, f.size)
	}
	for _, f := range desc {
		writeUsageFile(t, dirDesc, f.name, f.size)
	}

	// 前置校验：两目录首个被访问条目的分类不同，确认遍历次序确实存在差异。
	firstKind := func(dir string) string {
		t.Helper()
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		return diskUsageKind(entries[0].Name())
	}
	if ka, kb := firstKind(dirAsc), firstKind(dirDesc); ka == kb {
		t.Fatalf("预期两个目录遍历次序不同，但首个条目 kind 均为 %s", ka)
	}

	gotAsc, err := MeasureDiskUsage(dirAsc)
	if err != nil {
		t.Fatalf("MeasureDiskUsage(asc): %v", err)
	}
	gotDesc, err := MeasureDiskUsage(dirDesc)
	if err != nil {
		t.Fatalf("MeasureDiskUsage(desc): %v", err)
	}
	if !reflect.DeepEqual(gotAsc, gotDesc) {
		t.Fatalf("不同遍历次序结果不一致:\n asc: %#v\ndesc: %#v", gotAsc, gotDesc)
	}
	want := DiskUsage{
		Files: 7,
		Bytes: 2800,
		ByKind: map[string]int64{
			diskKindSession: 100 + 200 + 300,
			diskKindArchive: 400 + 500,
			diskKindOther:   600 + 700,
		},
	}
	if !reflect.DeepEqual(gotAsc, want) {
		t.Fatalf("usage mismatch:\n got: %#v\nwant: %#v", gotAsc, want)
	}
}

// TestDiskUsageAccumulateAllocIndependentOfFileCount 断言累加路径（去排序、去切片后的
// 热路径）的 B/op 不随文件数线性增长。旧实现按文件数分配 regularFile 切片并排序
// （实测约 76~102B/文件，随切片扩容上升）；饱和加法可交换后直接累加，该路径零分配。
func TestDiskUsageAccumulateAllocIndependentOfFileCount(t *testing.T) {
	allocPerOp := func(n int) int64 {
		paths := make([]string, n)
		for i := range paths {
			paths[i] = fmt.Sprintf("file%06d.jsonl", i)
		}
		r := testing.Benchmark(func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				u := DiskUsage{ByKind: map[string]int64{
					diskKindSession: 0,
					diskKindArchive: 0,
					diskKindOther:   0,
				}}
				for _, p := range paths {
					u.addRegular(p, 1)
				}
			}
		})
		return r.AllocedBytesPerOp()
	}
	small := allocPerOp(100)
	large := allocPerOp(5000)
	t.Logf("累加路径 B/op: n=100 -> %d, n=5000 -> %d", small, large)
	// 线性增长时 large 至少是 small 的数十倍；此处仅允许 map 固定开销的微小波动。
	if large > small+64 {
		t.Fatalf("累加路径 B/op 随文件数增长（疑似引入 per-file 分配）: small=%d large=%d", small, large)
	}
}

// BenchmarkMeasureDiskUsageNoSort 报告完整遍历的 B/op 供人工对比；注意 os.ReadDir
// 本身会按目录条目数线性分配，故完整调用的 B/op 线性增长属预期，去掉的切片开销已由
// TestDiskUsageAccumulateAllocIndependentOfFileCount 单独锁定。
func BenchmarkMeasureDiskUsageNoSort(b *testing.B) {
	dir := b.TempDir()
	for i := 0; i < 1000; i++ {
		p := filepath.Join(dir, fmt.Sprintf("f%06d.jsonl", i))
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := MeasureDiskUsage(dir); err != nil {
			b.Fatal(err)
		}
	}
}
