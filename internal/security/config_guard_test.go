package security

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// humanAlwaysApprover 模拟真人在审批界面选择"始终允许"：它给出肯定回答，
// 且声明自己来自真人交互，因此敏感调用的批准会被链接受（随后由持久化层
// 决定"是否记下来"）。
type humanAlwaysApprover struct{}

func (humanAlwaysApprover) Ask(context.Context, string, string) (bool, error) { return true, nil }
func (humanAlwaysApprover) InteractiveReply() bool                            { return true }

// TestConfigFileGuardCannotBeAlways 验证 #21：配置/说明文件的编辑调用被守卫命中，
// 且"始终允许"只记一次性——既不写入持久规则文件，也不被下一次运行的审批器兑现。
func TestConfigFileGuardCannotBeAlways(t *testing.T) {
	tests := []struct {
		name string
		tool string
		args string
		want bool
	}{
		{"agents md", "write", `{"path":"AGENTS.md"}`, true},
		{"claude md nested", "edit", `{"path":"src/CLAUDE.md"}`, true},
		{"config json", "write", `{"path":"config.json"}`, true},
		{"config jsonc", "write", `{"path":".argentgo/config.jsonc"}`, true},
		{"argent dir", "write", `{"path":".argent/state.json"}`, true},
		{"case insensitive", "write", `{"path":"src/agents.md"}`, true},
		{"normal file", "write", `{"path":"src/main.go"}`, false},
		{"read is not guarded", "read", `{"path":"AGENTS.md"}`, false},
		{"no path", "write", `{"content":"x"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := configFileGuard(tt.tool, json.RawMessage(tt.args))
			if got != tt.want {
				t.Fatalf("configFileGuard(%s, %s) = %v, want %v", tt.tool, tt.args, got, tt.want)
			}
		})
	}

	ctx := context.Background()
	const agentsArgs = `{"path":"AGENTS.md"}`

	// 命中配置守卫的调用：链标记 single-use，持久化审批器不落盘 always。
	dir := t.TempDir()
	first := NewFileBackedScopedApproverHierarchical(humanAlwaysApprover{}, dir, nil)
	if err := NewChain(DefaultPolicy(dir), first, nil, "s1").Check(ctx, "write", json.RawMessage(agentsArgs)); err != nil {
		t.Fatalf("first config-file approval should succeed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, persistentApprovalsFile))
	if err == nil && bytes.Contains(data, []byte(`"kind":"always"`)) {
		t.Fatalf("config-file always must not be persisted, file: %s", data)
	}

	// 新的审批器（内层拒绝）面对同一路径仍需询问并拒绝，证明 always 没有被记住。
	second := NewFileBackedScopedApproverHierarchical(NeverApprover{}, dir, nil)
	if err := NewChain(DefaultPolicy(dir), second, nil, "s2").Check(ctx, "write", json.RawMessage(agentsArgs)); err == nil {
		t.Fatal("config-file scope must not survive as an always grant")
	}

	// 对照组：普通文件写入的 always 仍然跨重启生效。
	normalDir := t.TempDir()
	normal := NewFileBackedScopedApproverHierarchical(humanAlwaysApprover{}, normalDir, nil)
	if err := NewChain(DefaultPolicy(normalDir), normal, nil, "s3").Check(ctx, "write", json.RawMessage(`{"path":"src/main.go"}`)); err != nil {
		t.Fatalf("normal approval should succeed: %v", err)
	}
	normalData, err := os.ReadFile(filepath.Join(normalDir, persistentApprovalsFile))
	if err != nil || !bytes.Contains(normalData, []byte(`"kind":"always"`)) {
		t.Fatalf("normal always should be persisted, file: %s, err: %v", normalData, err)
	}
}
