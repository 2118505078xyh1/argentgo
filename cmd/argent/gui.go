package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"

	"example.com/argentgo/internal/app"
	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/session"
)

// isGUIRequest 报告参数是否命名原生 GUI 子命令。
// 只有前导 `gui` 算数。扫描每个参数会劫持普通
// 提示如 `argentgo fix gui bug`，也会在某个标志把 `gui` 作为其值
// 消费时（`argentgo --model gui hello`）启动 GUI。`--gui`
// 标志由 CLI 自身解析，它报告为未迁移。
func isGUIRequest(args []string) bool {
	return len(args) > 0 && args[0] == "gui"
}

func runGUI(stdio cli.IO, args []string) int {
	// main 在前导 `gui` 子命令处分派到这里；只剥离那个
	// 标记。过滤每个 `gui`/`--gui` 参数会吞掉提示词
	// 并静默忽略 GUI 解析器应拒绝的标志。
	if len(args) > 0 && args[0] == "gui" {
		args = args[1:]
	}

	flags := flag.NewFlagSet("argentgo gui", flag.ContinueOnError)
	flags.SetOutput(stdio.Err)
	var model, baseURL, apiKey, provider, protocol, locale, cwd string
	flags.StringVar(&model, "m", "", "model id")
	flags.StringVar(&model, "model", "", "model id")
	flags.StringVar(&baseURL, "base-url", "", "OpenAI-compatible base URL")
	flags.StringVar(&apiKey, "api-key", "", "API key")
	flags.StringVar(&provider, "provider", "", "provider template")
	flags.StringVar(&protocol, "protocol", "", "wire protocol")
	flags.StringVar(&locale, "locale", "", "ui locale")
	flags.StringVar(&cwd, "C", "", "workspace root")
	flags.StringVar(&cwd, "cwd", "", "workspace root")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintln(stdio.Err, "argentgo gui: unexpected argument:", flags.Arg(0))
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	opts := cli.Options{
		Cwd: cwd, Model: model, BaseURL: baseURL, APIKey: apiKey,
		Protocol: protocol, Locale: locale,
	}
	resolved, err := app.Resolve(stdio, opts)
	if err != nil {
		fmt.Fprintln(stdio.Err, "error:", err)
		return 1
	}
	// 会话 store 仍被打开，使原生客户端的 engine 有一个
	// 就绪的持久化对话位置，在它启动之前。
	if _, err := session.Open(filepath.Join(resolved.Storage, "sessions")); err != nil {
		fmt.Fprintln(stdio.Err, "error: open sessions:", err)
		return 1
	}

	if runtime.GOOS != "windows" {
		fmt.Fprintln(stdio.Err, "argentgo gui: the native client is Windows-only")
		return 1
	}
	return launchNativeFrontend(ctx, stdio, resolved)
}

// nativeFrontendEnv 构造传给原生客户端的环境变量。
// 集中到此处便于测试，也确保 --locale 经 Resolve 后不会再被静默丢弃：
// 即使 locale 为空，也显式写出 ARGENT_LOCALE=，让原生侧能区分"未传"与"空值"。
// base 通常为 os.Environ()；传 nil 时仍会得到完整的 ARGENT_* 变量。
func nativeFrontendEnv(base []string, exe, root, locale string) []string {
	return append(base,
		"ARGENT_EXE="+exe,
		"ARGENT_CWD="+root,
		"ARGENT_LOCALE="+locale,
	)
}

// launchNativeFrontend 启动 MyGo 原生客户端（argentgo-native.exe），
// 它通过 --rpc 连接回此 engine。在 Windows 上原生二进制
// 与 argentgo.exe 在同一发布 zip 中。
func launchNativeFrontend(ctx context.Context, stdio cli.IO, resolved app.Resolved) int {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stdio.Err, "error:", err)
		return 1
	}
	native := filepath.Join(filepath.Dir(exe), "argentgo-native.exe")
	if _, err := os.Stat(native); err != nil {
		fmt.Fprintln(stdio.Err, "error: native frontend not found:", native)
		return 1
	}
	cmd := exec.Command(native)
	cmd.Dir = resolved.Root
	cmd.Env = nativeFrontendEnv(os.Environ(), exe, resolved.Root, resolved.Locale)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(stdio.Err, "error:", err)
		return 1
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return 0
	case err := <-done:
		if err != nil {
			fmt.Fprintln(stdio.Err, "error:", err)
			return 1
		}
		return 0
	}
}
