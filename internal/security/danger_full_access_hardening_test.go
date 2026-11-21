package security

import (
	"fmt"
	"path/filepath"
	"testing"
)

// TestDangerFullAccessRemovesWriteBoundaryButKeepsExplicitDenies 补齐三态语义中
// 缺失的一态：workspace-write 把修改类路径限制在写入根目录内，而 danger-full-access
// 把写入根目录放宽到整卷，使边界检查成为空操作；但显式的路径拒绝规则在解除边界后
// 仍然生效。app 层用 unrestrictedWriteRoots(root) 得到整卷根目录，这里直接以同样的
// 根构造 Policy，验证其依赖的边界机制本身。
func TestDangerFullAccessRemovesWriteBoundaryButKeepsExplicitDenies(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if filepath.VolumeName(root) != filepath.VolumeName(outside) {
		t.Skipf("root and outside live on different volumes")
	}
	// 与 unrestrictedWriteRoots 一致：整卷根目录使工作区边界检查成为空操作。
	volumeRoot := filepath.VolumeName(root) + string(filepath.Separator)

	allowed := filepath.Join(outside, "allowed.txt")
	allowedArgs := []byte(fmt.Sprintf(`{"path":%q}`, allowed))

	// workspace-write：工作区根目录之外的绝对路径必须被拒绝。
	workspace := NewPolicy(root, PolicyConfig{WriteRoots: []string{root}})
	if got := workspace.Check("write", allowedArgs); got != Deny {
		t.Fatalf("workspace-write outside path: got %v, want Deny", got)
	}

	// danger-full-access：同一个绝对路径不再因边界被拒绝，落到常规审批（Ask）。
	full := NewPolicy(root, PolicyConfig{WriteRoots: []string{volumeRoot}})
	if got := full.Check("write", allowedArgs); got != Ask {
		t.Fatalf("danger-full-access outside path: got %v, want Ask", got)
	}

	// 但解除边界不等于解除策略：显式的路径拒绝依然生效。
	secret := filepath.Join(outside, "secret.txt")
	secretArgs := []byte(fmt.Sprintf(`{"path":%q}`, secret))
	withDeny := NewPolicy(root, PolicyConfig{
		WriteRoots: []string{volumeRoot},
		DenyPaths:  []string{"secret.txt"},
	})
	if got := withDeny.Check("write", secretArgs); got != Deny {
		t.Fatalf("explicit deny under danger-full-access: got %v, want Deny", got)
	}
}
