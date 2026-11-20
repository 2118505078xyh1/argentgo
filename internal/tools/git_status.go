package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// ---- git_status ----

// gitStatusCommand 构建只读的 git 调用。它是一个变量，
// 以便测试可以替换为一个确定性的命令。
var gitStatusCommand = func(ctx context.Context, dir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", "status", "--short", "--branch")
	cmd.Dir = dir
	return cmd
}

type gitStatusTool struct{}

func (gitStatusTool) Name() string { return "git_status" }
func (gitStatusTool) Description() string {
	return "Show the working tree status (git status --short --branch). Read-only."
}
func (gitStatusTool) Mutating() bool { return false }
func (gitStatusTool) Schema() string { return `{"type":"object","properties":{}}` }

func (gitStatusTool) Run(ctx context.Context, root string, _ json.RawMessage) (string, error) {
	timeout := clampTimeout("git_status", 0, maxTimeout(ctx))
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	output, err := gitStatusCommand(runCtx, root).CombinedOutput()
	text := strings.TrimSpace(string(output))
	if runCtx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("git_status: timed out after %s", timeout)
	}
	if err != nil {
		if text == "" {
			return "", fmt.Errorf("git_status: %w", err)
		}
		return clip(ctx, text), fmt.Errorf("git_status: %w", err)
	}
	if text == "" {
		return "(clean)", nil
	}
	return clip(ctx, text), nil
}
