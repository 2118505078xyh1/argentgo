package app

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/config"
	"example.com/argentgo/internal/hooks"
)

// countingTool 记录 Run 是否被调用。嵌入 agent.ToolRunner 只为满足 Specs，
// hookedTools.Run 不会调用它。
type countingTool struct {
	agent.ToolRunner
	calls int
}

func (c *countingTool) Run(_ context.Context, _, _ string) (string, error) {
	c.calls++
	return "ok", nil
}

// hookVerdictBridge 构造一个带受信任 hook 文档的 bridge，dispatcher 可注入。
func hookVerdictBridge(t *testing.T, dispatcher HookDispatcher) *hookBridge {
	t.Helper()
	root := t.TempDir()
	storage := t.TempDir()
	path := filepath.Join(root, ".argent", "hooks.json")
	writeFile(t, path, hookDocument)
	trustHookFile(t, path, storage)
	res := Resolved{
		Root: root, Storage: storage, Home: t.TempDir(),
		Cfg: config.Config{Raw: map[string]any{}},
	}
	if dispatcher != nil {
		res.HookDispatcher = dispatcher
	}
	return newHookBridge(res, "session-1", nil)
}

func TestHookVerdictEnabledPure(t *testing.T) {
	tests := []struct {
		name string
		env  func(string) string
		want bool
	}{
		{"nil_env", nil, false},
		{"missing", func(string) string { return "" }, false},
		{"zero", func(string) string { return "0" }, false},
		{"true_word", func(string) string { return "true" }, false},
		{"one", func(string) string { return "1" }, true},
		{"padded_one", func(string) string { return " 1 " }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hookVerdictEnabled(tt.env); got != tt.want {
				t.Fatalf("hookVerdictEnabled = %t, want %t", got, tt.want)
			}
		})
	}
}

// TestHookBridgeVerdictWiring 验证默认零值逐字节不变，opt-in 下默认 dispatcher
// 具体化为 *hooks.Dispatcher 并注入 VerdictEnabled 与内置中间件。
func TestHookBridgeVerdictWiring(t *testing.T) {
	t.Run("default_unchanged", func(t *testing.T) {
		bridge := hookVerdictBridge(t, nil)
		if bridge == nil {
			t.Fatal("trusted document produced no bridge")
		}
		d, ok := bridge.dispatcher.(*hooks.Dispatcher)
		if !ok {
			t.Fatalf("default dispatcher type = %T, want *hooks.Dispatcher", bridge.dispatcher)
		}
		if d.VerdictEnabled || len(d.Middlewares) != 0 {
			t.Fatalf("default must stay zero-valued: %+v", d)
		}
	})

	t.Run("opt_in_wires_middlewares", func(t *testing.T) {
		t.Setenv(hookVerdictEnv, "1")
		bridge := hookVerdictBridge(t, nil)
		if bridge == nil {
			t.Fatal("trusted document produced no bridge")
		}
		d, ok := bridge.dispatcher.(*hooks.Dispatcher)
		if !ok {
			t.Fatalf("dispatcher type = %T, want *hooks.Dispatcher", bridge.dispatcher)
		}
		if !d.VerdictEnabled {
			t.Fatal("opt-in must enable verdict folding")
		}
		if len(d.Middlewares) == 0 {
			t.Fatal("opt-in must inject built-in middlewares")
		}
	})

	t.Run("injected_dispatcher_untouched", func(t *testing.T) {
		t.Setenv(hookVerdictEnv, "1")
		bridge := hookVerdictBridge(t, &recordingDispatcher{})
		if bridge == nil {
			t.Fatal("trusted document produced no bridge")
		}
		if _, ok := bridge.dispatcher.(*hooks.Dispatcher); ok {
			t.Fatal("injected dispatcher must not be replaced by opt-in wiring")
		}
	})
}

// TestHookVerdictMiddlewareFillsContextAndAudits 验证内置中间件补齐
// session/cwd（不覆盖已有值）并写出有界审计记录。
func TestHookVerdictMiddlewareFillsContextAndAudits(t *testing.T) {
	var buf bytes.Buffer
	mws := hookVerdictMiddlewares("session-1", "/work", &buf)
	if len(mws) != 1 {
		t.Fatalf("middleware count = %d, want 1", len(mws))
	}
	args := &hooks.Payload{Event: "PreToolUse", Tool: "bash"}
	result, err := mws[0](context.Background(), hooks.PreToolUse, args, func() (hooks.Result, error) {
		return hooks.Result{Blocked: true}, nil
	})
	if err != nil || !result.Blocked {
		t.Fatalf("next result lost: %+v, %v", result, err)
	}
	if args.SessionID != "session-1" || args.CWD != "/work" {
		t.Fatalf("middleware did not fill context: %+v", args)
	}
	audit := buf.String()
	if !strings.Contains(audit, "event=PreToolUse") || !strings.Contains(audit, "blocked=true") {
		t.Fatalf("audit line lost or malformed: %q", audit)
	}

	// 已有值不被覆盖；warn 为 nil 时审计为空操作。
	keep := &hooks.Payload{SessionID: "keep", CWD: "/keep"}
	mwsNil := hookVerdictMiddlewares("session-1", "/work", nil)
	if _, err := mwsNil[0](context.Background(), hooks.PreToolUse, keep, func() (hooks.Result, error) {
		return hooks.Result{}, nil
	}); err != nil {
		t.Fatalf("nil-warn middleware: %v", err)
	}
	if keep.SessionID != "keep" || keep.CWD != "/keep" {
		t.Fatalf("middleware overwrote existing context: %+v", keep)
	}
}

// TestHookVerdictDenyStdoutPreventsToolExecution 是端到端验收：fake runner 返回
// exit 0 + permissionDecision:deny，真实 *hooks.Dispatcher 折入 Blocked，
// hookedTools 因此不执行内层工具。
func TestHookVerdictDenyStdoutPreventsToolExecution(t *testing.T) {
	dispatcher := &hooks.Dispatcher{
		Runner: func(_ context.Context, command hooks.Command, _ hooks.Payload) hooks.Outcome {
			return hooks.Outcome{
				Event: command.Event, Command: command.Command, ExitCode: 0,
				Stdout: `{"permissionDecision":"deny","permissionDecisionReason":"拒绝执行此工具"}`,
			}
		},
	}
	bridge := hookVerdictBridge(t, dispatcher)
	if bridge == nil {
		t.Fatal("trusted document produced no bridge")
	}
	tool := &countingTool{}
	h := &hookedTools{inner: tool, bridge: bridge}
	_, err := h.Run(context.Background(), "bash", `{}`)
	if err == nil {
		t.Fatal("expected a block error from PreToolUse deny")
	}
	if !strings.Contains(err.Error(), "blocked by a PreToolUse hook") || !strings.Contains(err.Error(), "拒绝执行此工具") {
		t.Fatalf("block error lost its reason: %v", err)
	}
	if tool.calls != 0 {
		t.Fatalf("tool executed %d times, want 0", tool.calls)
	}
}
