package bash

import (
	"context"
	"fmt"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/jobs"
)

// runBashViaExecSession 是 bash 工具在统一 exec 会话开启时的执行分支。
// 带 session_id 表示向已有会话写入 stdin，否则启动一条新会话并立刻回报 id。
// 输出仍走既有的清洗/裁剪管线（含 opt-in overflow spill），因此与
// 普通 bash 路径的可观察输出格式保持一致。
func runBashViaExecSession(ctx context.Context, root string, registry *jobs.SessionRegistry,
	command, sessionID, stdin string, tty bool, yieldMS int) (string, error) {
	yield := sessionYield(yieldMS)

	if sessionID != "" {
		// Ctrl-C 不通过 stdin 透传：直接终止目标会话，确保只影响这一条会话。
		if isSessionInterrupt(stdin) {
			if err := registry.Close(sessionID); err != nil {
				return "", fmt.Errorf("bash: interrupt exec session: %w", err)
			}
			return fmt.Sprintf("exec session %s interrupted", sessionID), nil
		}
		out, running, err := registry.WriteStdin(sessionID, []byte(stdin), yield)
		if err != nil {
			return clipBashOutput(ctx, out), fmt.Errorf("bash: %w", err)
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

	path, argv := shellInvocation(command)
	info, out, err := registry.Start(jobs.SessionStart{
		Command: command,
		Dir:     root,
		TTY:     tty,
		Yield:   yield,
		Stdin:   []byte(stdin),
		Runner:  execRunner{path: path, args: argv, dir: root, tty: tty},
	})
	if err != nil {
		return "", fmt.Errorf("bash: %w", err)
	}

	header := fmt.Sprintf("started exec session %s (pid %d, tty=%t): %s",
		info.ID, info.PID, info.TTY, command)
	if !info.Running {
		header += " [already exited]"
	}
	text := clipBashOutput(ctx, out)
	if strings.TrimSpace(text) == "" {
		return header, nil
	}
	return header + "\n" + text, nil
}
