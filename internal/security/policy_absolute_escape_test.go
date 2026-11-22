package security

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestPolicyRejectsAbsoluteEscape 锁定策略层的绝对路径边界：无论工具有没有内置
// 二次拦截，工作区/--add-dir 之外的绝对路径都必须被拒绝，而工作区内与 --add-dir
// 内的绝对路径仍然放行。
func TestPolicyRejectsAbsoluteEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	addDir := t.TempDir()
	// --add-dir 通过 WriteRoots 把额外目录纳入允许的根。
	policy := NewPolicy(root, PolicyConfig{WriteRoots: []string{addDir}})
	volumeRoot := filepath.VolumeName(root) + string(filepath.Separator)

	cases := []struct {
		name string
		tool string
		path string
		want Decision
	}{
		{"read absolute outside workspace", "read", filepath.Join(outside, "secret.txt"), Deny},
		{"read absolute system path outside workspace", "read",
			filepath.Join(volumeRoot, "Windows", "System32", "drivers", "etc", "hosts"), Deny},
		{"read absolute inside add-dir", "read", filepath.Join(addDir, "allowed.txt"), Allow},
		{"write absolute inside add-dir", "write", filepath.Join(addDir, "allowed.txt"), Ask},
		{"read absolute inside workspace", "read", filepath.Join(root, "src", "main.go"), Allow},
		{"write absolute inside workspace", "write", filepath.Join(root, "src", "main.go"), Ask},
		{"relative traversal escapes", "read", filepath.Join("..", "outside", "secret.txt"), Deny},
		{"CJK path inside workspace", "read", filepath.Join(root, "工作区", "文档.txt"), Allow},
		{"CJK path outside workspace", "read", filepath.Join(outside, "机密", "密钥.txt"), Deny},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := []byte(fmt.Sprintf(`{"path":%q}`, tc.path))
			got := policy.Evaluate(tc.tool, args)
			if got.Decision != tc.want {
				t.Fatalf("%s %q: decision=%v, want %v (reason %q)", tc.tool, tc.path, got.Decision, tc.want, got.Reason)
			}
			if tc.want == Deny && got.Reason == "" {
				t.Fatal("a deny must carry a reason")
			}
		})
	}
}

// TestPolicyAllowsAbsolutePathInDangerFullAccess 说明沙箱模式的区分方式：Policy
// 本身不持有 mode 字段，但 danger-full-access 会把 writeRoots 放宽到整卷根，因此
// 基于 writeRoots 的绝对路径判定在同卷范围内自然放行，无需额外模式分支。
func TestPolicyAllowsAbsolutePathInDangerFullAccess(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if filepath.VolumeName(root) != filepath.VolumeName(outside) {
		t.Skip("root and outside live on different volumes")
	}
	volumeRoot := filepath.VolumeName(root) + string(filepath.Separator)
	full := NewPolicy(root, PolicyConfig{WriteRoots: []string{volumeRoot}})
	args := []byte(fmt.Sprintf(`{"path":%q}`, filepath.Join(outside, "x.txt")))

	// 只读工具访问整卷内的绝对路径：放行。
	if got := full.Check("read", args); got != Allow {
		t.Fatalf("danger-full-access read: got %v, want Allow", got)
	}
	// 修改类工具不受边界拦截，落到常规审批。
	if got := full.Check("write", args); got != Ask {
		t.Fatalf("danger-full-access write: got %v, want Ask", got)
	}
}

// TestAbsolutePathUsesRealRootsToAvoidFalseDeny 验证 realRoots 复核：当 --add-dir
// 或工作区根本身是符号链接时，工具传入该链接的真实目标绝对路径，词法上不在
// writeRoots 内，但真实位置确实位于允许的根目录，不能被误判为逃逸。
func TestAbsolutePathUsesRealRootsToAvoidFalseDeny(t *testing.T) {
	requireSymlinks(t)
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	// root 是指向 real 的链接：writeRoots 收录链接拼写，realRoots 收录真实位置。
	policy := NewPolicy(link, PolicyConfig{})
	args := []byte(fmt.Sprintf(`{"path":%q}`, filepath.Join(real, "a.txt")))
	if got := policy.Check("read", args); got != Allow {
		t.Fatalf("absolute path at the real root: got %v, want Allow", got)
	}
}
