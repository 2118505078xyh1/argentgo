package tools

import (
	"encoding/json"
	"testing"
)

func TestWriteToolsSupersedeMeta(t *testing.T) {
	cases := []struct {
		name string
		tool Tool
		args string
		want string
	}{
		{"write", writeTool{}, `{"path":"pkg/a.go","content":"x"}`, "read|pkg/a.go"},
		{"edit", editTool{}, `{"path":"pkg/a.go","old":"x","new":"y"}`, "read|pkg/a.go"},
		{"multi_edit first", multiEditTool{}, `{"edits":[{"path":"pkg/a.go"},{"path":"pkg/b.go"}]}`, "read|pkg/a.go"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mt, ok := tc.tool.(interface {
				Meta(json.RawMessage, string, error) ToolMeta
			})
			if !ok {
				t.Fatalf("%s does not implement metaTool", tc.name)
			}
			got := mt.Meta(json.RawMessage(tc.args), "", nil)
			if got.SupersedeKey != tc.want {
				t.Fatalf("SupersedeKey = %q, want %q", got.SupersedeKey, tc.want)
			}
		})
	}
}

func TestWriteToolsSupersedeMetaNoArgOrError(t *testing.T) {
	if got := (writeTool{}).Meta(json.RawMessage(`{}`), "", nil); got != (ToolMeta{}) {
		t.Fatalf("empty path should give zero meta: %+v", got)
	}
	if got := (editTool{}).Meta(json.RawMessage(`{"path":"a.go"}`), "", errSentinel{}); got != (ToolMeta{}) {
		t.Fatalf("error should give zero meta: %+v", got)
	}
	if got := (multiEditTool{}).Meta(json.RawMessage(`{"edits":[]}`), "", nil); got != (ToolMeta{}) {
		t.Fatalf("empty edits should give zero meta: %+v", got)
	}
}
