package security

import "testing"

// TestMutatingToolClassification 锁定下沉后的统一判定：内置精确表、带命名空间的
// MCP 修改类工具、写动词启发式与只读工具各归其位。
func TestMutatingToolClassification(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		// 内置精确表。
		{"write", true},
		{"edit", true},
		{"multi_edit", true},
		{"bash", true},
		{"apply_patch", true},
		// 写动词启发式，含命名空间前缀与命名风格变化。
		{"write_file", true},
		{"mcp__server__write_file", true},
		{"delete_file", true},
		{"deploy", true},
		{"git_commit", true},
		{"writeFile", true},
		{"write2file", true},
		// 只读工具与未知名称。
		{"read", false},
		{"read_file", false},
		{"search", false},
		{"mcp__server__search", false},
		{"ls", false},
		{"grep", false},
		{"git_status", false},
		{"web_fetch", false},
		{"todo", false},
		{"widget_gadget", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := MutatingTool(tc.name); got != tc.want {
			t.Errorf("MutatingTool(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
