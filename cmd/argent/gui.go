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

// isGUIRequest reports whether the arguments name the native GUI subcommand.
// Only a leading `gui` counts. Scanning every argument would hijack ordinary
// prompts such as `argent fix gui bug`, and would also launch the GUI when a
// flag consumed `gui` as its value (`argent --model gui hello`). The --gui
// flag is parsed by the CLI itself, which reports it as not migrated.
func isGUIRequest(args []string) bool {
	return len(args) > 0 && args[0] == "gui"
}

func runGUI(stdio cli.IO, args []string) int {
	// main dispatches here on the leading `gui` subcommand; strip only that
	// token. Filtering every `gui`/`--gui` argument would swallow prompt words
	// and silently ignore flags the GUI parser should reject.
	if len(args) > 0 && args[0] == "gui" {
		args = args[1:]
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
	if err := flags.Parse(args); err != nil {
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
