package bash

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// writeStdinTool 向统一 exec 会话写入 stdin 并收集输出。
// 它与 bash 工具共享进程内的会话注册表，因此 session_id 可以跨两次工具调用存活。
type writeStdinTool struct{}

func (writeStdinTool) Name() string { return "write_stdin" }

func (writeStdinTool) Description() string {
	return "向统一 exec 会话写入 stdin 并在 yield 窗口内收集输出；opt-in ARGENT_EXEC_SESSION=1 时生效。"
}

func (writeStdinTool) Mutating() bool { return true }

func (writeStdinTool) Schema() string {
	return `{"type":"object","properties":{"session_id":{"type":"string"},"input":{"type":"string"},"yield_time_ms":{"type":"integer"},"close":{"type":"boolean"}},"required":["session_id"]}`
}

func (writeStdinTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		SessionID   string `json:"session_id"`
		Input       string `json:"input"`
		YieldTimeMS int    `json:"yield_time_ms"`
		Close       bool   `json:"close"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("write_stdin: bad arguments: %w", err)
	}
	if strings.TrimSpace(args.SessionID) == "" {
		return "", fmt.Errorf("write_stdin: session_id must not be empty")
	}
	if !unifiedExecEnabled() {
		return "", fmt.Errorf("write_stdin: unified exec sessions are disabled (set ARGENT_EXEC_SESSION=1)")
	}

	registry := unifiedExecRegistry()
	if args.Close || isSessionInterrupt(args.Input) {
		if err := registry.Close(args.SessionID); err != nil {
			return "", fmt.Errorf("write_stdin: %w", err)
		}
		return fmt.Sprintf("exec session %s closed", args.SessionID), nil
	}

	out, running, err := registry.WriteStdin(args.SessionID, []byte(args.Input), sessionYield(args.YieldTimeMS))
	if err != nil {
		return clipBashOutput(ctx, out), fmt.Errorf("write_stdin: %w", err)
	}
	text := clipBashOutput(ctx, out)
	if !running {
		text += "\n[exec session exited]"
	}
	if strings.TrimSpace(text) == "" {
		return bashNoOutput(out, text), nil
	}
	return text, nil
}
