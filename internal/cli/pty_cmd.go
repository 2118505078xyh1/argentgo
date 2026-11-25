package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"example.com/argentgo/internal/pty"
)

// ptyOptInEnv 是启用 pty 子命令的环境变量。默认未设置时本文件不注册任何
// 命令，CLI 的 usage/help/completion 与字节输出保持完全不变。
const ptyOptInEnv = "ARGENT_PTY_INTERACTIVE"

// defaultPTYMaxBytes 是一次 pty 会话写入 stdout 的默认字节上限，防止
// 失控的子进程把终端和模型上下文淹没。
const defaultPTYMaxBytes = 1 << 20

// init 在且仅在 opt-in 打开时把 pty 子命令接入分发表。环境变量在进程启动时
// 判定一次；未设置时连子命令都不存在，因此默认路径零副作用。
func init() {
	if !ptyInteractiveEnabled(os.Getenv) {
		return
	}
	subcommands["pty"] = runPTY
	subcommandOrder = append(subcommandOrder, "pty")
}

// ptyInteractiveEnabled 判定 opt-in 是否打开。只认 "1"/"true"（忽略大小写与
// 首尾空白），其余值一律视为关闭，避免误开。
func ptyInteractiveEnabled(lookup func(string) string) bool {
	if lookup == nil {
		return false
	}
	v := strings.TrimSpace(lookup(ptyOptInEnv))
	return v == "1" || strings.EqualFold(v, "true")
}

// ptyUsage 打印 pty 子命令的用法。
func ptyUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo pty [-C <dir>] [--cols N] [--rows N] [--max-bytes N] -- <command> [args...]

Run a command attached to a pseudo-terminal (ConPTY on Windows, /dev/ptmx on
unix), then stream its output to stdout with terminal-query sequences removed.
Requires ARGENT_PTY_INTERACTIVE=1. Input from stdin is forwarded to the child;
SIGINT (or context cancellation) terminates the child and closes the terminal.
`)
}

// runPTY 实现 opt-in 的 `argentgo pty` 子命令。
func runPTY(app App, ctx context.Context, args []string) int {
	if !ptyInteractiveEnabled(app.env) {
		fmt.Fprintln(app.IO.Err, "argentgo: pty: 设置 ARGENT_PTY_INTERACTIVE=1 以启用交互式终端")
		return 2
	}
	cwd, rest, err := splitRoot(args)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argentgo:", err)
		return 2
	}
	for _, arg := range rest {
		if arg == "-h" || arg == "--help" {
			ptyUsage(app.IO.Out)
			return 0
		}
		if !strings.HasPrefix(arg, "-") {
			break
		}
	}
	size, limit, cmdArgs, err := parsePTYArgs(rest)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argentgo:", err)
		ptyUsage(app.IO.Err)
		return 2
	}
	if len(cmdArgs) == 0 {
		fmt.Fprintln(app.IO.Err, "argentgo: pty: 缺少要执行的命令")
		ptyUsage(app.IO.Err)
		return 2
	}
	dir, err := workspaceRoot(app, cwd)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}

	cmd := pty.Command{Path: cmdArgs[0], Args: cmdArgs[1:], Dir: dir}
	result, err := pty.Run(ctx, cmd, size, app.IO.In, app.IO.Out, limit)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argentgo:", err)
		return 1
	}
	if result.ExitCode < 0 {
		return 1
	}
	if result.ExitCode > 125 {
		return 125
	}
	return result.ExitCode
}

// parsePTYArgs 解析 pty 子命令的选项是可选的：--cols/--rows/--max-bytes，
// 并以 -- 显式分隔命令；返回终端大小、输出上限与要执行的 argv。
func parsePTYArgs(args []string) (pty.Size, int, []string, error) {
	size := pty.DefaultSize
	limit := defaultPTYMaxBytes
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return size, limit, args[i+1:], nil
		case arg == "--cols" || arg == "--rows" || arg == "--max-bytes":
			if i+1 >= len(args) {
				return size, limit, nil, &usageError{msg: arg + " needs a value"}
			}
			i++
			value, err := strconv.Atoi(args[i])
			if err != nil || value <= 0 {
				return size, limit, nil, &usageError{msg: arg + " needs a positive integer"}
			}
			switch arg {
			case "--cols":
				size.Cols = clampUint16(value)
			case "--rows":
				size.Rows = clampUint16(value)
			case "--max-bytes":
				limit = value
			}
		case strings.HasPrefix(arg, "-"):
			return size, limit, nil, &usageError{msg: "unknown option " + arg}
		default:
			return size, limit, args[i:], nil
		}
	}
	return size, limit, nil, nil
}

// clampUint16 把正整数钳到 uint16 范围内，避免超大列数/行数溢出。
func clampUint16(value int) uint16 {
	if value > int(^uint16(0)) {
		return ^uint16(0)
	}
	return uint16(value)
}
