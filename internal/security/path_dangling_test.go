package security

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestDanglingSymlinkCannotEscapeWriteRoots 覆盖目标尚不存在的链接。os.Open 会把它
// 报告为"缺失"，因此词法尾部重挂的路径过去会保留链接自身的名字，让写入顺着它
// 落到工作区之外。即使目标尚不存在，也必须解析出真实目标。
func TestDanglingSymlinkCannotEscapeWriteRoots(t *testing.T) {
	requireSymlinks(t)
	cases := []struct {
		name       string
		linkTarget func(root, outside string) string
		wantInside bool
	}{
		{
			name: "dangling final link to absolute outside path",
			linkTarget: func(_, outside string) string {
				return filepath.Join(outside, "pwned.txt")
			},
		},
		{
			name: "dangling final link to relative outside path",
			linkTarget: func(root, _ string) string {
				return filepath.Join("..", "outside", "pwned.txt")
			},
		},
		{
			name: "dangling link inside the root is still writable",
			linkTarget: func(root, _ string) string {
				return filepath.Join(root, "inside.txt")
			},
			wantInside: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "root")
			outside := filepath.Join(base, "outside")
			for _, dir := range []string{root, outside} {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			link := filepath.Join(root, "outlink")
			symlinkTarget := tc.linkTarget(root, outside)
			if err := os.Symlink(symlinkTarget, link); err != nil {
				t.Fatal(err)
			}

			// 链接会解析到其目标，因此期望的真实路径就是链接目标本身
			// （相对目标的情况会转成绝对路径）。
			// 根内的情况不得与外部逃逸路径比较。
			want := symlinkTarget
			if !filepath.IsAbs(want) {
				want = filepath.Join(root, want)
			}
			assertDanglingLinkBoundary(t, root, link, want, tc.wantInside)
		})
	}
}

// TestDanglingSymlinkInIntermediateComponentCannotEscape 覆盖尚未创建的路径尾部
// 中间的悬空链接。即使最终路径的任何祖先都不存在于磁盘上，边界也必须失败即拒绝。
func TestDanglingSymlinkInIntermediateComponentCannotEscape(t *testing.T) {
	requireSymlinks(t)
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{root, outside} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(root, "linkdir")
	if err := os.Symlink(filepath.Join(outside, "missing-dir"), link); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "missing-dir", "file.txt")
	path := filepath.Join(link, "file.txt")

	resolved, err := ResolveRealPath(path)
	if err != nil {
		t.Fatalf("ResolveRealPath: %v", err)
	}
	// 在 Windows 上 t.TempDir() 可能产生 8.3 短路径，而 ResolveRealPath 返回长形式；
	// 通过同一个辅助函数解析期望路径，使两边写法一致。
	want, err := ResolveRealPath(target)
	if err != nil {
		t.Fatalf("ResolveRealPath(target): %v", err)
	}
	if !samePath(resolved, want) {
		t.Fatalf("ResolveRealPath = %q, want %q", resolved, want)
	}
	if err := InWriteRoots(filepath.Join("linkdir", "file.txt"), []string{root}); err == nil {
		t.Fatal("a write through a dangling intermediate symlink must be denied")
	}
}

// TestDanglingJunctionCannotEscapeWriteRoots 是 Windows 对应版本：junction 不需要
// 符号链接权限，因此它在 os.Symlink 不可用的地方运行，并证明重解析点路径也被覆盖。
func TestDanglingJunctionCannotEscapeWriteRoots(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("junctions are Windows-only")
	}
	cases := []struct {
		name   string
		inside bool
	}{
		{name: "dangling junction to an outside file"},
		{name: "dangling junction inside the root stays writable", inside: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "root")
			outside := filepath.Join(base, "outside")
			for _, dir := range []string{root, outside} {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			targetFile := filepath.Join(outside, "pwned.txt")
			if tc.inside {
				targetFile = filepath.Join(root, "inside.txt")
			}
			link := filepath.Join(root, "outlink")
			out, err := exec.Command("cmd", "/C", "mklink", "/J", link, targetFile).CombinedOutput()
			if err != nil {
				t.Skipf("junction creation unavailable: %v (%s)", err, strings.TrimSpace(string(out)))
			}

			assertDanglingLinkBoundary(t, root, link, targetFile, tc.inside)
		})
	}
}

// assertDanglingLinkBoundary 检查共用契约：ResolveRealPath 能看到链接的真实目标，
// 且写入边界只接受确实位于 root 内的目标。
func assertDanglingLinkBoundary(t *testing.T, root, link, target string, wantInside bool) {
	t.Helper()
	resolved, err := ResolveRealPath(link)
	if err != nil {
		t.Fatalf("ResolveRealPath(%q): %v", link, err)
	}
	// 要与解析后的目标比较，而不是原始词法路径：在 Windows 上临时目录可能包含 8.3
	// 短名组件，而 ResolveRealPath 返回规范的长形式，EqualFold 无法弥合两者。
	wantResolved, err := ResolveRealPath(target)
	if err != nil {
		t.Fatalf("ResolveRealPath(target %q): %v", target, err)
	}
	if !samePath(resolved, wantResolved) {
		t.Fatalf("ResolveRealPath = %q, want %q", resolved, wantResolved)
	}

	err = InWriteRoots(filepath.Base(link), []string{root})
	if wantInside && err != nil {
		t.Fatalf("in-root dangling link must stay writable: %v", err)
	}
	if !wantInside && err == nil {
		t.Fatal("a dangling link pointing outside the roots must be denied")
	}

	decision := DefaultPolicy(root).Check("write", []byte(fmt.Sprintf(`{"path":%q}`, link)))
	want := Deny
	if wantInside {
		want = Ask
	}
	if decision != want {
		t.Fatalf("policy write decision = %v, want %v", decision, want)
	}
}

func samePath(a, b string) bool {
	if a == b {
		return true
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}
