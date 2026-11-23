package app

import (
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TUI 独占屏幕时，任何写到 stderr 的日志行都会画进帧里。
func TestRedirectTUILogsWritesToFileAndRestores(t *testing.T) {
	storage := t.TempDir()
	prev := log.Writer()

	restore := redirectTUILogs(storage, func(string) string { return "" })
	if log.Writer() == prev {
		t.Fatal("log output was not redirected away from the terminal")
	}
	log.Print("tui-test-line-1")
	// agent 的逐轮计时走 slog.Default()，它经标准库 log 输出；两者必须一起改道。
	slog.Info("tui-test-slog-line")
	restore()

	if got := log.Writer(); got != prev {
		t.Fatalf("log writer not restored: %T", got)
	}

	path := filepath.Join(storage, filepath.FromSlash(tuiLogFile))
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("log file missing: %v", err)
	}
	if !strings.Contains(string(body), "tui-test-line-1") {
		t.Fatalf("log file does not contain the redirected line: %q", body)
	}
	if !strings.Contains(string(body), "tui-test-slog-line") {
		t.Fatalf("slog.Default did not follow log.SetOutput: %q", body)
	}
}

// 拿不到落盘路径时宁可丢弃，也不能回落到终端污染帧。
func TestRedirectTUILogsDiscardsWithoutStorage(t *testing.T) {
	restore := redirectTUILogs("", func(string) string { return "" })
	defer restore()

	if log.Writer() != io.Discard {
		t.Fatalf("expected io.Discard, got %T", log.Writer())
	}
	log.Print("must-not-reach-the-terminal")
}

func TestTUILogPathPrefersEnvOverride(t *testing.T) {
	storage := filepath.Join("C:", "storage")
	env := func(key string) string {
		if key == logFileEnv {
			return `D:\custom\tui.log`
		}
		return ""
	}
	if got, want := tuiLogPath(storage, env), `D:\custom\tui.log`; got != want {
		t.Fatalf("env override ignored: got %q, want %q", got, want)
	}
	if got, want := tuiLogPath(storage, nil), filepath.Join(storage, filepath.FromSlash(tuiLogFile)); got != want {
		t.Fatalf("default path: got %q, want %q", got, want)
	}
	if got := tuiLogPath("", nil); got != "" {
		t.Fatalf("empty storage must yield an empty path, got %q", got)
	}
}
