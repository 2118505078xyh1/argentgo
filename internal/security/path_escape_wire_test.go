package security

import (
	"encoding/json"
	"testing"
)

// TestPolicyRejectsEncodedPathEscape 守护接线：词法逃逸检测已接入 Policy 的
// 路径解析，使 %2e%2e%2f / 双重编码 / 过宽 UTF-8 / NUL 形式的 .. 在策略层
// 被拒绝，而不是等下游解码后才逃逸。
func TestPolicyRejectsEncodedPathEscape(t *testing.T) {
	root := t.TempDir()
	p := DefaultPolicy(root)
	cases := []string{
		"%2e%2e%2fetc/passwd",
		"%252e%252e%252fsecret",
		"%C0%AE%C0%AE%2Fkey",
		"a%00/../b",
	}
	for _, path := range cases {
		args, _ := json.Marshal(map[string]any{"path": path})
		got := p.Evaluate("read", args)
		if got.Decision != Deny {
			t.Errorf("path %q: decision = %v, want Deny (%s)", path, got.Decision, got.Reason)
		}
	}
}

// TestPolicyAllowsNormalPaths 确认接线不误拒合法路径（含含点分号的文件名）。
func TestPolicyAllowsNormalPaths(t *testing.T) {
	root := t.TempDir()
	p := DefaultPolicy(root)
	for _, path := range []string{"normal.txt", "dir/report.txt", "file.v1.2.go", "a-b_c.md"} {
		args, _ := json.Marshal(map[string]any{"path": path})
		got := p.Evaluate("read", args)
		if got.Decision == Deny {
			t.Errorf("normal path %q was denied: %s", path, got.Reason)
		}
	}
}

// TestInWriteRootsRejectsEncodedEscape 守护 sandbox 侧的同口径拒绝。
func TestInWriteRootsRejectsEncodedEscape(t *testing.T) {
	root := t.TempDir()
	if err := InWriteRoots("%2e%2e%2fetc/passwd", []string{root}); err == nil {
		t.Fatal("encoded escape path must be rejected by InWriteRoots")
	}
	if err := InWriteRoots("normal.txt", []string{root}); err != nil {
		t.Fatalf("normal path rejected: %v", err)
	}
}
