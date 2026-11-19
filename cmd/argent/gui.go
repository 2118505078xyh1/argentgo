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

	"github.com/2118505078xyh1/argentgo/internal/app"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

func isGUIRequest(args []string) bool {
	for _, arg := range args {
		if arg == "gui" || arg == "--gui" {
			return true
		}
	}
	return false
}

func runGUI(stdio cli.IO, args []string) int {
	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "gui" || arg == "--gui" {
			continue
		}
		filtered = append(filtered, arg)
	}

	flags := flag.NewFlagSet("argent gui", flag.ContinueOnError)
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
	if err := flags.Parse(filtered); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintln(stdio.Err, "argent gui: unexpected argument:", flags.Arg(0))
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
	// The session store is still opened so the native client's engine has a
	// ready place to persist conversations before it is launched.
	if _, err := session.Open(filepath.Join(resolved.Storage, "sessions")); err != nil {
		fmt.Fprintln(stdio.Err, "error: open sessions:", err)
		return 1
	}

	if runtime.GOOS != "windows" {
		fmt.Fprintln(stdio.Err, "argent gui: the native client is Windows-only")
		return 1
	}
	return launchNativeFrontend(ctx, stdio, resolved)
}

// launchNativeFrontend starts the MyGo native client (argent-native.exe),
// which connects back to this engine over --rpc. On Windows the native binary
// ships beside argent.exe in the same release zip.
func launchNativeFrontend(ctx context.Context, stdio cli.IO, resolved app.Resolved) int {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stdio.Err, "error:", err)
		return 1
	}
	native := filepath.Join(filepath.Dir(exe), "argent-native.exe")
	if _, err := os.Stat(native); err != nil {
		fmt.Fprintln(stdio.Err, "error: native frontend not found:", native)
		return 1
	}
	cmd := exec.Command(native)
	cmd.Dir = resolved.Root
	cmd.Env = append(os.Environ(), "ARGENT_EXE="+exe, "ARGENT_CWD="+resolved.Root)
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
