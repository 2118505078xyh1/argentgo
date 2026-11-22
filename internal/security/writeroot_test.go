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
		// 绝对路径逃逸对所有工具生效，包括只读工具：过去这里期望 Allow，
		// 但只读 MCP/第三方工具因此能读到工作区外文件，现已收紧为 Deny。
		{"read outside roots is denied", "read", filepath.Join(outside, "c.txt"), Deny},
		{"read inside add dir", "read", filepath.Join(extra, "b.txt"), Allow},
	}
	for _, tc := range cases {
		args := fmt.Sprintf(`{"path":%q}`, tc.path)
		if got := policy.Check(tc.tool, []byte(args)); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
