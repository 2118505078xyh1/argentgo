package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/pluginapi"
)

// execCtxCaptureTool 在 Run 时把注册表注入的 ExecContext 原样捕获，
// 用于验证 SetShellSessions 是否真的流入工具可见的执行上下文。
type execCtxCaptureTool struct {
	captured pluginapi.ExecContext
}

func (c *execCtxCaptureTool) Name() string        { return "exec_ctx_capture" }
func (c *execCtxCaptureTool) Description() string { return "capture exec context" }
func (c *execCtxCaptureTool) Mutating() bool      { return false }
func (c *execCtxCaptureTool) Schema() string      { return `{"type":"object","properties":{}}` }
func (c *execCtxCaptureTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	c.captured, _ = pluginapi.ExecContextFrom(ctx)
	return "ok", nil
}

// fakeShellSessions 满足 shell 侧消费方的小接口断言，只用于类型身份校验。
type fakeShellSessions struct{}

func (fakeShellSessions) Exec(ctx context.Context, workspace, shellKind, command string) (string, int, error) {
	return "", 0, nil
}

func TestSetShellSessionsFlowsIntoExecContext(t *testing.T) {
	r := New(t.TempDir(), true)
	probe := &execCtxCaptureTool{}
	r.tools[probe.Name()] = probe
	r.order = append(r.order, probe.Name())

	// 未注入时 ExecContext 中 ShellSessions 为 nil。
	if _, err := r.Run(context.Background(), probe.Name(), `{}`); err != nil {
		t.Fatal(err)
	}
	if probe.captured.ShellSessions != nil {
		t.Fatalf("未注入时 ShellSessions = %v, want nil", probe.captured.ShellSessions)
	}

	// 注入后，同一次注册表的调用必须能在 ExecContext 中读回同一实例。
	sessions := fakeShellSessions{}
	r.SetShellSessions(sessions)
	if _, err := r.Run(context.Background(), probe.Name(), `{}`); err != nil {
		t.Fatal(err)
	}
	got, ok := probe.captured.ShellSessions.(fakeShellSessions)
	if !ok {
		t.Fatalf("ExecContext.ShellSessions 类型 = %T, want fakeShellSessions", probe.captured.ShellSessions)
	}
	if got != sessions {
		t.Fatalf("ExecContext.ShellSessions = %v, want the injected instance", got)
	}
}
