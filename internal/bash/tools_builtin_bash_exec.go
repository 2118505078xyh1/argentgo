package bash

import (
	"context"
	"fmt"
	"strings"

	"example.com/argentgo/internal/jobs"
)

// runBashViaExecSession 是 bash 工具在统一 exec 会话开启时的执行分支。
// 带 session_id 表示续读/写入已有会话，否则启动一条新会话并立刻回报 id。
// 每次观察都携带结构化的 still_running/cwd/pid/exit_code（PS1 哨兵解析、
// 与 stdout 分离）：软超时仍在运行时 exit_code=-1，模型因此知道进程还活着
// 而不必重跑造成重复副作用。
func runBashViaExecSession(ctx context.Context, root string, registry *jobs.SessionRegistry,
	command, sessionID, stdin string, tty bool, yieldMS int) (string, error) {
	yield := sessionYield(yieldMS)

	if sessionID != "" {
		// C-c：向会话写入中断字节打断当前进程，但保留会话本身——
		// 后续仍可用空 command 续读尾部日志；这与显式 close 移除会话不同。
		if command == "C-c" || isSessionInterrupt(stdin) {
			if _, _, err := registry.WriteStdin(sessionID, []byte("\x03"), interruptYield(yield)); err != nil {
				return "", fmt.Errorf("bash: interrupt exec session: %w", err)
			}
			return fmt.Sprintf("exec session %s interrupted (session retained)", sessionID), nil
		}
		// 空 command（stdin 也为空）不写入任何字节，只收集会话新产生
		// 的尾部日志——即"view additional logs"，会话保持存活。
		out, running, err := registry.WriteStdin(sessionID, []byte(stdin), yield)
		if err != nil {
			return clipBashOutput(ctx, out), fmt.Errorf("bash: %w", err)
		}
		known, _ := lookupSessionMeta(sessionID)
		stdout, obs, parsed := observationFromOutput(out, running, known)
		if parsed {
			rememberSessionMeta(sessionID, sessionMeta{PID: obs.PID, CWD: obs.CWD})
		}
		body := formatBashObservation(ctx, stdout, obs)
		if !running {
			body += "[exec session exited]\n"
		}
		return body, nil
	}

	// 在命令末尾追加 PS1 哨兵探针，使命令退出时打印 cwd/pid/退出码；
	// 展示用原始 command，执行用带探针的版本，二者刻意分离。
	observed := command + "\n" + metaProbeCommand() + "\n"
	path, argv := shellInvocation(observed)
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

	known := sessionMeta{PID: info.PID, CWD: root}
	stdout, obs, _ := observationFromOutput(out, info.Running, known)
	// obs 已合并哨兵值与启动时的 pid/cwd，记住它供后续读取复用。
	rememberSessionMeta(info.ID, sessionMeta{PID: obs.PID, CWD: obs.CWD})

	header := fmt.Sprintf("started exec session %s (pid %d, tty=%t): %s",
		info.ID, info.PID, info.TTY, command)
	if !info.Running {
		header += " [already exited]"
	}
	if strings.TrimSpace(stdout) == "" {
		return header + "\n" + observationLine(obs), nil
	}
	return header + "\n" + formatBashObservation(ctx, stdout, obs), nil
}
