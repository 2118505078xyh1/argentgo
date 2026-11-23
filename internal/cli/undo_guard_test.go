package cli

import (
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

// TestCheckUndoRestoreBlocksEscapingManifest 固定 CLI 撤销前置检查：
// 工作区内的清单放行，逃逸清单被策略拒绝并给出可读错误。
func TestCheckUndoRestoreBlocksEscapingManifest(t *testing.T) {
	workspace := t.TempDir()
	stdio, _, _ := newTestIO("", nil)
	app := App{IO: stdio, StorageDir: t.TempDir()}
	tests := []struct {
		name    string
		files   []session.CheckpointFile
		wantErr bool
	}{
		{name: "in-workspace path allowed", files: []session.CheckpointFile{{Path: "a.txt"}}},
		{name: "traversal denied", files: []session.CheckpointFile{{Path: "../outside.txt"}}, wantErr: true},
		{name: "absolute escape denied", files: []session.CheckpointFile{{Path: "C:/outside.txt"}}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkUndoRestore(app, workspace, "session-1", session.Checkpoint{Files: tc.files})
			if (err != nil) != tc.wantErr {
				t.Fatalf("checkUndoRestore error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr && !strings.Contains(err.Error(), "撤销被安全链拦截") {
				t.Fatalf("denial error lacks readable context: %v", err)
			}
		})
	}
}
