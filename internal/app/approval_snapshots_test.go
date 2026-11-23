package app

import (
	"reflect"
	"testing"
)

// TestApprovalSnapshots 固定写类工具参数到 before/after 快照的映射：
// 字段别名、末尾附加文本与畸形输入都不能改变判定。
func TestApprovalSnapshots(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: src/a.go\n@@\n-old\n+new\n*** End Patch"
	tests := []struct {
		name   string
		tool   string
		args   string
		before map[string]string
		after  map[string]string
	}{
		{
			name: "edit uses old and new",
			tool: "edit", args: `{"path":"a.go","old":"one\n","new":"two\n"}`,
			before: map[string]string{"a.go": "one\n"}, after: map[string]string{"a.go": "two\n"},
		},
		{
			name: "edit ignores appended approval preview text",
			tool: "edit", args: `{"path":"a.go","old":"one","new":"two"} (+1/-1 lines)`,
			before: map[string]string{"a.go": "one"}, after: map[string]string{"a.go": "two"},
		},
		{
			name: "multi_edit concatenates aliases",
			tool: "multi_edit", args: `{"path":"a.go","edits":[{"old_text":"a","new_text":"A"},{"old":"b","new":"B"}]}`,
			before: map[string]string{"a.go": "ab"}, after: map[string]string{"a.go": "AB"},
		},
		{
			name: "write treats content as after snapshot",
			tool: "write", args: `{"path":"new.go","content":"x\n"}`,
			before: map[string]string{"new.go": ""}, after: map[string]string{"new.go": "x\n"},
		},
		{
			name: "apply_patch groups hunks by file",
			tool: "apply_patch", args: `{"input":"` + escapeForJSONString(patch) + `"}`,
			before: map[string]string{"src/a.go": "old"}, after: map[string]string{"src/a.go": "new"},
		},
		{name: "read tool has no snapshots", tool: "read", args: `{"path":"a.go"}`},
		{name: "malformed JSON has no snapshots", tool: "edit", args: `{"path":`},
		{name: "edit without text has no snapshots", tool: "edit", args: `{"path":"a.go"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before, after := approvalSnapshots(tc.tool, tc.args)
			if !reflect.DeepEqual(before, tc.before) || !reflect.DeepEqual(after, tc.after) {
				t.Fatalf("approvalSnapshots(%q) = before %v, after %v; want before %v, after %v",
					tc.tool, before, after, tc.before, tc.after)
			}
		})
	}
}

func escapeForJSONString(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch r {
		case '\\':
			out = append(out, '\\', '\\')
		case '"':
			out = append(out, '\\', '"')
		case '\n':
			out = append(out, '\\', 'n')
		default:
			out = append(out, r)
		}
	}
	return string(out)
}
