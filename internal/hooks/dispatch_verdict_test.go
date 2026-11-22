package hooks

import (
	"context"
	"testing"
	"time"
)

// verdictTestConfig 构造只含一条 PreToolUse 命令的配置。fake runner 不启动进程。
func verdictTestConfig(event Event, command string) *Config {
	return &Config{Commands: []Command{{Event: event, Command: command, Timeout: time.Second}}}
}

// verdictTestDispatcher 返回一个使用 fake runner 的 Dispatcher：runner 原样返回
// 传入的 Outcome，使测试能精确控制退出码、stdout 与 Blocked 字段。
func verdictTestDispatcher(o Outcome) *Dispatcher {
	return &Dispatcher{
		Runner: func(_ context.Context, command Command, _ Payload) Outcome {
			o.Event = command.Event
			o.Command = command.Command
			return o
		},
	}
}

// TestDispatchVerdictDenyFromStdoutBlocks 验证 Claude Code 兼容的 exit 0 +
// permissionDecision:deny 会被折入 Result.Blocked（此前被静默丢弃）。
func TestDispatchVerdictDenyFromStdoutBlocks(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
	}{
		{"top_level", `{"permissionDecision":"deny"}`},
		{"nested_claude_style", `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny"}}`},
		{"cjk_reason", `{"permissionDecision":"deny","permissionDecisionReason":"拒绝：包含敏感词「密码」与 🪙 标记"}`},
		{"log_prefix_then_json", "2026/01/01 hook starting\n{\"permissionDecision\":\"deny\"}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := verdictTestDispatcher(Outcome{ExitCode: 0, Stdout: tt.stdout})
			result, err := d.Dispatch(context.Background(), verdictTestConfig(PreToolUse, "deny-hook"),
				Payload{Event: "PreToolUse", Tool: "bash"})
			if err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			if !result.Blocked {
				t.Fatalf("permissionDecision:deny must block: %+v", result)
			}
		})
	}
}

// TestDispatchVerdictAllowBaseline 验证 allow 裁决默认不改变拦截状态，且即使
// 开启 VerdictEnabled，也不会解除 exit 2 这一传统拦截信号。
func TestDispatchVerdictAllowBaseline(t *testing.T) {
	t.Run("allow_alone_never_blocks", func(t *testing.T) {
		d := verdictTestDispatcher(Outcome{ExitCode: 0, Stdout: `{"permissionDecision":"allow"}`})
		d.VerdictEnabled = true
		result, err := d.Dispatch(context.Background(), verdictTestConfig(PreToolUse, "allow-hook"),
			Payload{Event: "PreToolUse", Tool: "bash"})
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if result.Blocked {
			t.Fatalf("allow must not block: %+v", result)
		}
	})

	t.Run("allow_cannot_lift_exit2_block", func(t *testing.T) {
		// 两个 outcome：exit 2 已拦截，另一条声明 allow。deny 绝对优先。
		d := &Dispatcher{
			VerdictEnabled: true,
			Runner: func(_ context.Context, command Command, _ Payload) Outcome {
				switch command.Command {
				case "exit2":
					return Outcome{Event: PreToolUse, Command: "exit2", ExitCode: 2, Blocked: true}
				default:
					return Outcome{Event: PreToolUse, Command: "allow", ExitCode: 0, Stdout: `{"permissionDecision":"allow"}`}
				}
			},
		}
		config := &Config{Commands: []Command{
			{Event: PreToolUse, Command: "exit2", Timeout: time.Second},
			{Event: PreToolUse, Command: "allow", Timeout: time.Second},
		}}
		result, err := d.Dispatch(context.Background(), config, Payload{Event: "PreToolUse", Tool: "bash"})
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if !result.Blocked {
			t.Fatalf("allow must not lift an exit 2 block: %+v", result)
		}
	})

	t.Run("allow_lifts_non_exit2_block_only_when_enabled", func(t *testing.T) {
		// fake runner 直接给出一个非 exit 2 的 Blocked（例如上层中间件设置的拦截），
		// 同时用 stdout allow 表达解除意图。仅 VerdictEnabled 开启时清除。
		out := Outcome{ExitCode: 0, Blocked: true, Stdout: `{"permissionDecision":"allow"}`}
		config := verdictTestConfig(PreToolUse, "mixed")

		off := verdictTestDispatcher(out)
		got, err := off.Dispatch(context.Background(), config, Payload{Event: "PreToolUse", Tool: "bash"})
		if err != nil || !got.Blocked {
			t.Fatalf("default must keep non-exit2 block: result=%+v err=%v", got, err)
		}

		on := verdictTestDispatcher(out)
		on.VerdictEnabled = true
		got, err = on.Dispatch(context.Background(), config, Payload{Event: "PreToolUse", Tool: "bash"})
		if err != nil || got.Blocked {
			t.Fatalf("opt-in must lift non-exit2 block: result=%+v err=%v", got, err)
		}
	})
}

// TestDispatchVerdictOtherEventsUnchanged 验证 permissionDecision 只在
// PreToolUse 生效，其它事件保持 Summarize 的原样（无拦截）。
func TestDispatchVerdictOtherEventsUnchanged(t *testing.T) {
	d := verdictTestDispatcher(Outcome{ExitCode: 0, Stdout: `{"permissionDecision":"deny"}`})
	d.VerdictEnabled = true
	result, err := d.Dispatch(context.Background(), verdictTestConfig(PostToolUse, "post-hook"),
		Payload{Event: "PostToolUse", Tool: "bash"})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result.Blocked {
		t.Fatalf("non-PreToolUse event must stay unblocked: %+v", result)
	}
}

// TestDispatchVerdictPreservesOnErrorAllow 验证 onError=allow 的 exit 2 仍是
// "仅警告"，新增的裁决折叠不会把它重新解释为拦截。
func TestDispatchVerdictPreservesOnErrorAllow(t *testing.T) {
	d := verdictTestDispatcher(Outcome{ExitCode: 2, Stderr: "hook exploded"})
	d.VerdictEnabled = true
	config := &Config{Commands: []Command{{Event: PreToolUse, Command: "soft", OnError: "allow", Timeout: time.Second}}}
	result, err := d.Dispatch(context.Background(), config, Payload{Event: "PreToolUse", Tool: "bash"})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result.Blocked {
		t.Fatalf("onError=allow exit 2 must not block: %+v", result)
	}
}

// TestDispatchDocumentAppliesVerdict 验证 DispatchDocument 走同一条 Dispatch
// 路径，结构化 deny 同样生效。
func TestDispatchDocumentAppliesVerdict(t *testing.T) {
	config := verdictTestConfig(PreToolUse, "deny-hook")
	d := verdictTestDispatcher(Outcome{ExitCode: 0, Stdout: `{"permissionDecision":"deny"}`})
	result, err := d.DispatchDocument(context.Background(),
		&Document{Config: config}, Payload{Event: "PreToolUse", Tool: "bash"})
	if err != nil {
		t.Fatalf("dispatch document: %v", err)
	}
	if !result.Blocked {
		t.Fatalf("DispatchDocument must apply verdict: %+v", result)
	}
}
