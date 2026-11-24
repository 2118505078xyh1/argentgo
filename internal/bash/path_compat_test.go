package bash

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestNormalizeMSYSPath 锁定 /x/... → X:/... 的映射边界：
// 只认单字母盘符且紧跟斜杠或结尾；多字母、已带盘符、相对路径、POSIX 一律不动。
func TestNormalizeMSYSPath(t *testing.T) {
	cases := []struct {
		in   string
		want string // Windows 上的期望；非 Windows 期望恒为 in（原样）
	}{
		{"/d/code/mpi", "D:/code/mpi"},
		{"/d", "D:/"},
		{"/D/code", "D:/code"},
		{"/z/tmp/file", "Z:/tmp/file"},
		{"/dir/foo", "/dir/foo"}, // 多字母目录名，不是盘符
		{"C:/code", "C:/code"},   // 已是盘符绝对路径
		{"relative/path", "relative/path"},
		{"/", "/"},
		{"", ""},
	}
	for _, tc := range cases {
		want := tc.in
		if runtime.GOOS == "windows" {
			want = tc.want
		}
		if got := normalizeMSYSPath(tc.in); got != want {
			t.Errorf("normalizeMSYSPath(%q) = %q, want %q (GOOS=%s)", tc.in, got, want, runtime.GOOS)
		}
	}
}

// TestBuiltinCdMSYSPath 端到端复现测评里的失败场景：在工作区根目录里
// cd /d/...（Git Bash 合法写法）应当成功且 pwd 落在工作区，而不是报
// "cd: not a directory"。非 Windows 跳过。
func TestBuiltinCdMSYSPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("MSYS 盘符路径兼容仅适用于 Windows")
	}
	dir := t.TempDir()
	vol := filepath.VolumeName(dir)
	if len(vol) != 2 || vol[1] != ':' {
		t.Skipf("无法从 %q 提取盘符，跳过", dir)
	}
	drive := strings.ToLower(vol[:1])
	msys := "/" + drive + filepath.ToSlash(dir[len(vol):])
	got, err := Run("cd "+msys+" && pwd", testOptions(dir))
	if err != nil {
		t.Fatalf("cd %s: %v", msys, err)
	}
	want := dir + "\n"
	if got.Output != want {
		t.Fatalf("cd %s && pwd => %q, want %q (exit=%d)", msys, got.Output, want, got.ExitStatus)
	}
}
