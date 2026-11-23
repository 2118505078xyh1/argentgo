package security

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// autoGrantMachine 模拟无头/YOLO 客户端的自动批准：回答"是"，且显式声明
// 不是真人交互。它对敏感调用的批准必须被链拒绝。
type autoGrantMachine struct{}

func (autoGrantMachine) Ask(context.Context, string, string) (bool, error) { return true, nil }
func (autoGrantMachine) InteractiveReply() bool                            { return false }

// TestSensitiveApprovalIgnoresAutoReply 验证 #23：敏感审批只认真人交互回答。
// 机器自动批准被忽略并降级为拒绝；真人批准仍被接受；非敏感调用不受影响。
func TestSensitiveApprovalIgnoresAutoReply(t *testing.T) {
	tests := []struct {
		name     string
		approver Approver
		profile  CallProfile
		tool     string
		args     string
		wantErr  bool
	}{
		{"machine auto reply ignored", autoGrantMachine{}, CallProfile{Sensitive: true}, "write", `{"path":"a.txt"}`, true},
		{"human reply accepted", humanAlwaysApprover{}, CallProfile{Sensitive: true}, "write", `{"path":"a.txt"}`, false},
		{"non sensitive auto reply allowed", autoGrantMachine{}, CallProfile{}, "write", `{"path":"a.txt"}`, false},
		{"config file guard marks sensitive", autoGrantMachine{}, CallProfile{}, "write", `{"path":"AGENTS.md"}`, true},
		{"human config file edit accepted", humanAlwaysApprover{}, CallProfile{}, "write", `{"path":"AGENTS.md"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			audit := NewAuditLog(dir)
			chain := NewChain(DefaultPolicy(dir), tt.approver, audit, "s1")
			ctx := WithCallProfile(context.Background(), tt.profile)
			err := chain.Check(ctx, tt.tool, json.RawMessage(tt.args))
			if (err != nil) != tt.wantErr {
				t.Fatalf("Check() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				var stop *StopError
				if !errors.As(err, &stop) {
					t.Fatalf("sensitive rejection must be a StopError, got %T: %v", err, err)
				}
				entries, readErr := ReadAudit(audit.Path())
				if readErr != nil {
					t.Fatalf("read audit: %v", readErr)
				}
				warned := false
				for _, entry := range entries {
					if entry.Decision == "warning" {
						warned = true
					}
				}
				if !warned {
					t.Fatalf("sensitive rejection must audit a warning, entries: %+v", entries)
				}
			}
		})
	}
}
