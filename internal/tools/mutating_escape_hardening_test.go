package tools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// newEscapeDirLink 在 link 处创建一个指向 outside 的目录链接。POSIX 上用符号链接；
// Windows 上 os.Symlink 常需提权，因此回退到无需提权的 junction。
func newEscapeDirLink(t *testing.T, outside, link string) {
	t.Helper()
	if err := os.Symlink(outside, link); err == nil {
		return
	} else if filepath.Separator != '\\' {
		t.Skipf("symlinks unavailable: %v", err)
	}
	out, err := exec.Command("cmd", "/C", "mklink", "/J", link, outside).CombinedOutput()
	if err != nil {
		t.Skipf("directory junctions unavailable: %v (%s)", err, strings.TrimSpace(string(out)))
	}
}

// applyPatchUpdateArgs 构造一条会改写目标文件的 apply_patch 调用参数。
func applyPatchUpdateArgs(target string) string {
	patch := "*** Begin Patch\n*** Update File: " + filepath.ToSlash(target) +
		"\n@@\n-SENTINEL\n+PWNED\n*** End Patch\n"
	b, err := json.Marshal(map[string]string{"input": patch})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// TestMutatingToolsRejectPathEscapes 通过公开的 Registry.Run 入口参数化验证每个
// 修改类文件系统工具对三种逃逸向量的拒绝：`../` 穿越、绝对路径、工作区内链接指向
// 外部。除了断言被拒绝，还断言工作区外的目标文件逐字节保持原样 —— 真正的安全
// 属性是“越界写入没有发生”，而不是仅仅“返回了某个错误”。
//
// apply_patch 的 `../` 与绝对路径逃逸已由 TestApplyPatchRejectsRelativeEscape /
// TestApplyPatchRejectsAbsolutePath 覆盖，这里只补它缺失的符号链接向量。
// bash/powershell 不接受路径参数，其边界由命令安全检查负责，不在本表范围内。
func TestMutatingToolsRejectPathEscapes(t *testing.T) {
	const sentinel = "SENTINEL\n"

	type vector struct {
		name   string
		target func(t *testing.T, root, outside string) string
	}
	vectors := []vector{
		{
			name: "traversal",
			target: func(t *testing.T, root, outside string) string {
				t.Helper()
				rel, err := filepath.Rel(root, filepath.Join(outside, "pwned.txt"))
				if err != nil {
					t.Fatal(err)
				}
				return rel
			},
		},
		{
			name: "absolute",
			target: func(_ *testing.T, _ string, outside string) string {
				return filepath.Join(outside, "pwned.txt")
			},
		},
		{
			name: "symlink",
			target: func(t *testing.T, root, outside string) string {
				t.Helper()
				link := filepath.Join(root, "escape_link")
				newEscapeDirLink(t, outside, link)
				return filepath.Join(link, "pwned.txt")
			},
		},
	}

	type mutator struct {
		name string
		args func(target string) string
		// 仅对缺失向量的工具才逐项枚举；apply_patch 只需补 symlink。
		vectors map[string]bool
	}
	mutators := []mutator{
		{
			name: "write",
			args: func(target string) string {
				return fmt.Sprintf(`{"path":%q,"content":%q}`, target, "PWNED\n")
			},
		},
		{
			name: "edit",
			args: func(target string) string {
				return fmt.Sprintf(`{"path":%q,"old":%q,"new":%q}`, target, "SENTINEL", "PWNED")
			},
		},
		{
			name: "multi_edit",
			args: func(target string) string {
				return fmt.Sprintf(`{"path":%q,"edits":[{"old":%q,"new":%q}]}`, target, "SENTINEL", "PWNED")
			},
		},
		{
			name:    "apply_patch",
			args:    applyPatchUpdateArgs,
			vectors: map[string]bool{"symlink": true},
		},
	}

	for _, m := range mutators {
		for _, v := range vectors {
			if m.vectors != nil && !m.vectors[v.name] {
				continue
			}
			t.Run(m.name+"/"+v.name, func(t *testing.T) {
				root := t.TempDir()
				outside := t.TempDir()
				outsideFile := filepath.Join(outside, "pwned.txt")
				if err := os.WriteFile(outsideFile, []byte(sentinel), 0o644); err != nil {
					t.Fatal(err)
				}
				target := v.target(t, root, outside)

				registry := tools.New(root, true)
				if m.name == "apply_patch" {
					tools.RegisterApplyPatch(registry)
				}
				_, err := registry.Run(context.Background(), m.name, m.args(target))
				if err == nil {
					t.Fatalf("%s via %s: expected a boundary rejection", m.name, v.name)
				}
				if !strings.Contains(err.Error(), "outside the workspace") {
					t.Fatalf("%s via %s: err = %q, want a boundary diagnostic", m.name, v.name, err)
				}

				// 越界文件必须保持原样：既没被截断也没被改写。
				data, readErr := os.ReadFile(outsideFile)
				if readErr != nil || string(data) != sentinel {
					t.Fatalf("%s via %s: outside file changed: %q err=%v", m.name, v.name, data, readErr)
				}
			})
		}
	}
}
