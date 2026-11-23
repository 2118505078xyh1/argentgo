package security

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapabilityForTool(t *testing.T) {
	tests := []struct {
		tool string
		want Capability
	}{
		{"read", CapabilityRead},
		{"ls", CapabilityRead},
		{"write", CapabilityWrite},
		{"edit", CapabilityWrite},
		{"apply_patch", CapabilityWrite},
		{"bash", CapabilityExec},
		{"powershell", CapabilityExec},
	}
	for _, tt := range tests {
		if got := CapabilityForTool(tt.tool); got != tt.want {
			t.Errorf("CapabilityForTool(%q) = %q, want %q", tt.tool, got, tt.want)
		}
	}
}

func TestScopeForPathFormat(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	fh, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	_ = fh.Close()

	scope := ScopeForPath("read", file)
	if !strings.HasPrefix(scope, "read\x00") {
		t.Fatalf("scope must be capability\\0path, got %q", scope)
	}
	if strings.Count(scope, "\x00") != 1 {
		t.Fatalf("scope must have exactly one NUL, got %q", scope)
	}
	dirScope := ScopeForPath("read", dir)
	if !strings.HasSuffix(dirScope, string(filepath.Separator)) {
		t.Fatalf("directory scope must end with separator, got %q", dirScope)
	}
	if ScopeForPath("read", dir) == ScopeForPath("write", dir) {
		t.Fatal("read and write must use different keys")
	}
}

func TestScopeForPathAncestorGrant(t *testing.T) {
	dir := t.TempDir()
	approver := NewFileBackedScopedApprover(&onceApprover{}, t.TempDir(), func(_, args string) string {
		var payload struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal([]byte(args), &payload)
		return ScopeForPath("write", payload.Path)
	})
	ctx := context.Background()
	// 授予目录本身（一次真人批准）。
	if ok, err := approver.Ask(ctx, "write", fmt.Sprintf(`{"path":%q}`, dir)); !ok || err != nil {
		t.Fatalf("grant directory: ok=%v err=%v", ok, err)
	}
	// 尚未创建的子路径应沿用祖先目录的授予。
	child := filepath.Join(dir, "never-created", "child.txt")
	if ok, err := approver.Ask(ctx, "write", fmt.Sprintf(`{"path":%q}`, child)); !ok || err != nil {
		t.Fatalf("child path must inherit ancestor grant: ok=%v err=%v", ok, err)
	}
}

func TestScopeForPathCapabilityIsolation(t *testing.T) {
	file := filepath.Join(t.TempDir(), "secret.txt")
	approver := NewFileBackedScopedApprover(&onceApprover{}, t.TempDir(), func(_, args string) string {
		var payload struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal([]byte(args), &payload)
		return ScopeForPath("read", payload.Path)
	})
	ctx := context.Background()
	args := fmt.Sprintf(`{"path":%q}`, file)
	if ok, err := approver.Ask(ctx, "read", args); !ok || err != nil {
		t.Fatalf("read grant: ok=%v err=%v", ok, err)
	}
	// scopeFromArgs 为 write 生成的是 write 键，不能命中 read 授予。
	writeScope := scopeFromArgs("write", args)
	if !strings.HasPrefix(writeScope, "write\x00") {
		t.Fatalf("write scope must use write capability, got %q", writeScope)
	}
	approver2 := NewFileBackedScopedApprover(NeverApprover{}, t.TempDir(), func(_, args string) string {
		return scopeFromArgs("write", args)
	})
	// 独立的 write 审批器里没有任何授予，outer 的 read 授予不会生效。
	if ok, err := approver2.Ask(ctx, "write", args); ok || err != nil {
		t.Fatalf("read grant must not authorize write: ok=%v err=%v", ok, err)
	}
	_ = approver
}

func TestScopeForPathSymlinkSharesGrant(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unavailable on this host: %v", err)
	}
	approver := NewFileBackedScopedApprover(&onceApprover{}, t.TempDir(), func(_, args string) string {
		var payload struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal([]byte(args), &payload)
		return ScopeForPath("write", payload.Path)
	})
	ctx := context.Background()
	if ok, err := approver.Ask(ctx, "write", fmt.Sprintf(`{"path":%q}`, real)); !ok || err != nil {
		t.Fatalf("grant real path: ok=%v err=%v", ok, err)
	}
	// 通过符号链接访问同一目录应命中同一授予，不再询问 inner。
	if ok, err := approver.Ask(ctx, "write", fmt.Sprintf(`{"path":%q}`, link)); !ok || err != nil {
		t.Fatalf("symlink must share the grant: ok=%v err=%v", ok, err)
	}
}
