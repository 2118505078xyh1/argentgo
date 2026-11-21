package memory

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeArtifact 在 dir 中写一个规范命名的工件；mod 非零时用 os.Chtimes
// 固定其修改时间，便于精确断言最旧/最新时间戳。返回工件路径。
func writeArtifact(t *testing.T, dir, id string, data []byte, mod time.Time) string {
	t.Helper()
	path := filepath.Join(dir, spillFileName(id))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if !mod.IsZero() {
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// TestAnalyzeSpillDir 表驱动验证溢出目录统计：目录不存在、空目录、多工件
// 汇总、单工件、mtime 极值、忽略不规范文件与 CJK 路径。
func TestAnalyzeSpillDir(t *testing.T) {
	const (
		sec1000 = int64(1000)
		sec2000 = int64(2000)
		sec3000 = int64(3000)
		sec2100 = int64(4102444800) // 2100-01-01，作为未来时间极值
	)

	cases := []struct {
		name  string
		dir   func(t *testing.T, base string) string
		setup func(t *testing.T, dir string)
		want  SpillStats
	}{
		{
			name: "目录不存在返回零值不报错",
			dir: func(t *testing.T, base string) string {
				return filepath.Join(base, "not-created", "spill")
			},
			want: SpillStats{},
		},
		{
			name: "空目录返回零值",
			dir: func(t *testing.T, base string) string {
				return filepath.Join(base, "empty", "spill")
			},
			setup: func(t *testing.T, dir string) {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: SpillStats{},
		},
		{
			name: "多工件统计数量字节与最旧最新",
			dir: func(t *testing.T, base string) string {
				return filepath.Join(base, "multi", "spill")
			},
			setup: func(t *testing.T, dir string) {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				writeArtifact(t, dir, "alpha", []byte("abc"), time.Unix(sec2000, 0))
				writeArtifact(t, dir, "beta-1", []byte("12345"), time.Unix(sec1000, 0))
				writeArtifact(t, dir, "gamma_2", []byte("1234567"), time.Unix(sec3000, 0))
			},
			want: SpillStats{Count: 3, TotalBytes: 15, OldestTS: sec1000, NewestTS: sec3000},
		},
		{
			name: "单工件最旧与最新相同",
			dir: func(t *testing.T, base string) string {
				return filepath.Join(base, "single", "spill")
			},
			setup: func(t *testing.T, dir string) {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				writeArtifact(t, dir, "only", []byte("hello"), time.Unix(sec2000, 0))
			},
			want: SpillStats{Count: 1, TotalBytes: 5, OldestTS: sec2000, NewestTS: sec2000},
		},
		{
			name: "mtime 极值",
			dir: func(t *testing.T, base string) string {
				return filepath.Join(base, "extreme", "spill")
			},
			setup: func(t *testing.T, dir string) {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				writeArtifact(t, dir, "epoch", []byte("x"), time.Unix(0, 0))
				writeArtifact(t, dir, "future", []byte("yz"), time.Unix(sec2100, 0))
			},
			want: SpillStats{Count: 2, TotalBytes: 3, OldestTS: 0, NewestTS: sec2100},
		},
		{
			name: "忽略不规范文件与子目录",
			dir: func(t *testing.T, base string) string {
				return filepath.Join(base, "mixed", "spill")
			},
			setup: func(t *testing.T, dir string) {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				writeArtifact(t, dir, "good1", []byte("abcd"), time.Unix(sec1000, 0))
				// 以下文件都不符合 <安全 id>.txt 约定，应被忽略。
				writeArtifact(t, dir, "a.b", []byte("ignored"), time.Unix(sec3000, 0)) // id 含点
				writeArtifact(t, dir, "中文", []byte("ignored"), time.Unix(sec3000, 0))  // id 含非 ASCII
				if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("ignored"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, ".txt"), []byte("ignored"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "bad name.txt"), []byte("ignored"), 0o600); err != nil {
					t.Fatal(err)
				}
				// 名字看起来规范但其实是目录，也必须忽略。
				if err := os.Mkdir(filepath.Join(dir, "folder.txt"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: SpillStats{Count: 1, TotalBytes: 4, OldestTS: sec1000, NewestTS: sec1000},
		},
		{
			name: "CJK 目录路径",
			dir: func(t *testing.T, base string) string {
				return filepath.Join(base, "溢出记录", "附件", "spill")
			},
			setup: func(t *testing.T, dir string) {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				writeArtifact(t, dir, "cjk-a", []byte("你好"), time.Unix(sec1000, 0))
				writeArtifact(t, dir, "cjk-b", []byte("世界你好"), time.Unix(sec2000, 0))
			},
			want: SpillStats{Count: 2, TotalBytes: 18, OldestTS: sec1000, NewestTS: sec2000},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			dir := tc.dir(t, base)
			if tc.setup != nil {
				tc.setup(t, dir)
			}
			got, err := AnalyzeSpillDir(dir)
			if err != nil {
				t.Fatalf("AnalyzeSpillDir(%q) 返回错误: %v", dir, err)
			}
			if got != tc.want {
				t.Fatalf("AnalyzeSpillDir(%q) = %+v, want %+v", dir, got, tc.want)
			}
		})
	}
}

// snap 是单个目录项的只读快照，用于验证 AnalyzeSpillDir 不改动任何文件。
type snap struct {
	size int64
	mod  int64
	data []byte
}

// snapshotDir 读取 dir 下所有文件的大小、修改时间与内容。
func snapshotDir(t *testing.T, dir string) map[string]snap {
	t.Helper()
	out := map[string]snap{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = snap{size: info.Size(), mod: info.ModTime().Unix(), data: data}
	}
	return out
}

// TestAnalyzeSpillDirReadOnly 表驱动验证统计过程完全只读：调用前后目录内
// 每个文件（含被忽略的文件）的大小、mtime 与内容都保持不变。
func TestAnalyzeSpillDirReadOnly(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{
			name: "含规范工件与被忽略文件",
			setup: func(t *testing.T, dir string) {
				writeArtifact(t, dir, "ab", []byte("data"), time.Unix(1111, 0))
				writeArtifact(t, dir, "cd", []byte("more data"), time.Unix(2222, 0))
				if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("ignore"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "空目录",
			setup: func(t *testing.T, dir string) {
				// 目录由测试框架创建，无需额外文件。
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "spill")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			tc.setup(t, dir)
			before := snapshotDir(t, dir)
			if _, err := AnalyzeSpillDir(dir); err != nil {
				t.Fatalf("AnalyzeSpillDir(%q) 返回错误: %v", dir, err)
			}
			after := snapshotDir(t, dir)
			if len(before) != len(after) {
				t.Fatalf("目录项数量变化: before=%d after=%d", len(before), len(after))
			}
			for name, b := range before {
				a, ok := after[name]
				if !ok {
					t.Fatalf("调用后文件 %q 消失", name)
				}
				if a.size != b.size || a.mod != b.mod || !bytes.Equal(a.data, b.data) {
					t.Fatalf("文件 %q 被改动: before=%+v after=%+v", name, b, a)
				}
			}
		})
	}
}
