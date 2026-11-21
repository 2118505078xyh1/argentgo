package security

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestMutatingPathToolsRespectWriteRoots(t *testing.T) {
	root := t.TempDir()
	extra := t.TempDir()
	outside := t.TempDir()
	policy := NewPolicy(root, PolicyConfig{WriteRoots: []string{extra}})

	cases := []struct {
		name string
		tool string
		path string
		want Decision
	}{
		{"write inside workspace", "write", filepath.Join(root, "a.txt"), Ask},
		{"write inside add dir", "write", filepath.Join(extra, "b.txt"), Ask},
		{"edit inside add dir", "edit", filepath.Join(extra, "b.txt"), Ask},
		{"multi_edit inside add dir", "multi_edit", filepath.Join(extra, "b.txt"), Ask},
		// apply_patch 纳入修改类工具后，其文件路径同样受 writeRoots 约束。
		{"apply_patch inside workspace", "apply_patch", filepath.Join(root, "a.txt"), Ask},
		{"apply_patch inside add dir", "apply_patch", filepath.Join(extra, "b.txt"), Ask},
		{"write outside all roots", "write", filepath.Join(outside, "c.txt"), Deny},
		{"edit outside all roots", "edit", filepath.Join(outside, "c.txt"), Deny},
		{"apply_patch outside all roots", "apply_patch", filepath.Join(outside, "c.txt"), Deny},
		{"relative escape", "write", "../escape.txt", Deny},
		{"apply_patch relative escape", "apply_patch", "../escape.txt", Deny},
		{"read outside roots stays readable", "read", filepath.Join(outside, "c.txt"), Allow},
		{"read inside add dir", "read", filepath.Join(extra, "b.txt"), Allow},
	}
	for _, tc := range cases {
		args := fmt.Sprintf(`{"path":%q}`, tc.path)
		if got := policy.Check(tc.tool, []byte(args)); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
