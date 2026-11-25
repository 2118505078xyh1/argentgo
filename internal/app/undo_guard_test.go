package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"example.com/argentgo/internal/config"
	"example.com/argentgo/internal/security"
	"example.com/argentgo/internal/session"
)

// TestCheckUndoRestoreGoesThroughChain 固定撤销前置检查：清单路径先过
// 策略 → 审批 → 审计，逃逸路径被拒绝且审计落盘，合法路径放行。
func TestCheckUndoRestoreGoesThroughChain(t *testing.T) {
	root := t.TempDir()
	res := Resolved{
		Root:    root,
		Storage: t.TempDir(),
		Sandbox: security.SandboxWorkspaceWrite,
		Cfg:     config.Config{},
	}
	tests := []struct {
		name    string
		files   []session.CheckpointFile
		wantErr bool
	}{
		{name: "in-workspace path allowed", files: []session.CheckpointFile{{Path: "src/a.txt"}}},
		{name: "path traversal denied", files: []session.CheckpointFile{{Path: "../outside.txt"}}, wantErr: true},
		{name: "empty manifest allowed", files: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chain := undoChainFor(res, "session-1")
			cp := session.Checkpoint{Files: tc.files}
			err := checkUndoRestore(context.Background(), chain, cp)
			if (err != nil) != tc.wantErr {
				t.Fatalf("checkUndoRestore error = %v, wantErr %v", err, tc.wantErr)
			}
			entries, readErr := security.ReadAudit(chainAuditPath(t, res))
			if readErr != nil {
				t.Fatalf("read audit: %v", readErr)
			}
			var found bool
			for _, entry := range entries {
				if entry.Tool == security.RestoreToolName {
					found = true
				}
			}
			if !found {
				t.Fatal("undo action was not recorded in the audit log")
			}
			if tc.wantErr && !strings.Contains(err.Error(), "undo blocked by security chain") {
				t.Fatalf("denial error lacks readable context: %v", err)
			}
		})
	}
}

func chainAuditPath(t *testing.T, res Resolved) string {
	t.Helper()
	return security.NewAuditLog(filepath.Join(res.Storage, "audit")).Path()
}
