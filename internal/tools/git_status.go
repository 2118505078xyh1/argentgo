package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ---- git_status ----

const gitStatusTimeout = 20 * time.Second

// gitStatusCommand builds the read-only git invocation. It is a variable so
// tests can substitute a deterministic command.
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
	runCtx, cancel := context.WithTimeout(ctx, gitStatusTimeout)
	defer cancel()

	output, err := gitStatusCommand(runCtx, root).CombinedOutput()
	text := strings.TrimSpace(string(output))
	if runCtx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("git_status: timed out after %s", gitStatusTimeout)
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
