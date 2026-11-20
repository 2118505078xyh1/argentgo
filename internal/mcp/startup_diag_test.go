package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestEnrichStartupErrorEOFWithOutput(t *testing.T) {
	base := io.EOF
	run := func(context.Context, string, []string) ([]byte, error) {
		return []byte("node: bad option: --nope\nUsage: node [options]\n"), nil
	}

	got := EnrichStartupError(base, "node", []string{"--nope", "server.js"}, run)
	if got == base {
		t.Fatal("want a new enriched error, got the original EOF")
	}
	if !errors.Is(got, io.EOF) {
		t.Fatalf("enriched error must keep the original cause: %v", got)
	}
	for _, want := range []string{"node", "--nope", "bad option", "Usage: node"} {
		if !strings.Contains(got.Error(), want) {
			t.Fatalf("enriched error %q missing %q", got.Error(), want)
		}
	}
}

func TestEnrichStartupErrorCommandNotFoundOutput(t *testing.T) {
	base := io.EOF
	run := func(context.Context, string, []string) ([]byte, error) {
		return []byte("sh: 1: nonexistent-mcp-server: not found\n"), errors.New("exit status 127")
	}

	got := EnrichStartupError(base, "nonexistent-mcp-server", nil, run)
	if !errors.Is(got, io.EOF) {
		t.Fatalf("enriched error must keep the original cause: %v", got)
	}
	for _, want := range []string{"not found", "exit status 127"} {
		if !strings.Contains(got.Error(), want) {
			t.Fatalf("enriched error %q missing %q", got.Error(), want)
		}
	}
}

func TestEnrichStartupErrorTimeoutRewrite(t *testing.T) {
	old := startupReplayTimeout
	startupReplayTimeout = 40 * time.Millisecond
	defer func() { startupReplayTimeout = old }()

	t.Run("run honours the context", func(t *testing.T) {
		run := func(ctx context.Context, _ string, _ []string) ([]byte, error) {
			<-ctx.Done()
			return []byte("partial output"), ctx.Err()
		}
		got := EnrichStartupError(io.EOF, "slow", nil, run)
		if !strings.Contains(got.Error(), "timed out after 40ms") {
			t.Fatalf("want timeout rewrite, got %q", got.Error())
		}
		if !strings.Contains(got.Error(), "partial output: partial output") {
			t.Fatalf("want partial output kept, got %q", got.Error())
		}
		if !errors.Is(got, io.EOF) {
			t.Fatalf("enriched error must keep the original cause: %v", got)
		}
	})

	t.Run("run ignores the context", func(t *testing.T) {
		run := func(context.Context, string, []string) ([]byte, error) {
			time.Sleep(250 * time.Millisecond)
			return []byte("too late"), nil
		}
		start := time.Now()
		got := EnrichStartupError(io.EOF, "stubborn", nil, run)
		if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
			t.Fatalf("EnrichStartupError did not enforce its own timeout: %v", elapsed)
		}
		if !strings.Contains(got.Error(), "timed out after 40ms") {
			t.Fatalf("want timeout rewrite, got %q", got.Error())
		}
	})
}

func TestEnrichStartupErrorNonEOFUnchanged(t *testing.T) {
	called := false
	run := func(context.Context, string, []string) ([]byte, error) {
		called = true
		return []byte("should not be used"), nil
	}

	cases := []error{
		errors.New("boom"),
		fmt.Errorf("mcp %q: decode initialize result: invalid character", "x"),
	}
	for _, base := range cases {
		got := EnrichStartupError(base, "server", nil, run)
		if got != base || got.Error() != base.Error() {
			t.Fatalf("non-connection error changed: got %q, want %q", got.Error(), base.Error())
		}
	}
	if called {
		t.Fatal("run must not be called for a non-connection error")
	}
	if got := EnrichStartupError(nil, "server", nil, run); got != nil {
		t.Fatalf("nil error must stay nil, got %v", got)
	}
}

func TestEnrichStartupErrorCJKOutput(t *testing.T) {
	run := func(context.Context, string, []string) ([]byte, error) {
		return []byte("错误：找不到模块 '服务器'\n启动失败\n"), nil
	}
	got := EnrichStartupError(io.EOF, "服务器工具", []string{"--启动"}, run)
	for _, want := range []string{"错误：找不到模块 '服务器'", "启动失败", "服务器工具", "--启动"} {
		if !strings.Contains(got.Error(), want) {
			t.Fatalf("enriched error %q missing CJK text %q", got.Error(), want)
		}
	}
	if strings.Contains(got.Error(), "\uFFFD") {
		t.Fatalf("CJK output was corrupted: %q", got.Error())
	}
}

func TestEnrichStartupErrorTruncatesOnRuneBoundary(t *testing.T) {
	run := func(context.Context, string, []string) ([]byte, error) {
		return []byte(strings.Repeat("中文", 5000)), nil
	}
	got := EnrichStartupError(io.EOF, "server", nil, run)
	if !strings.Contains(got.Error(), "[startup replay output truncated]") {
		t.Fatalf("want truncation marker, got %q", got.Error())
	}
	if strings.Contains(got.Error(), "\uFFFD") {
		t.Fatalf("truncation split a UTF-8 rune: %q", got.Error())
	}
}

func TestEnrichStartupErrorNilRun(t *testing.T) {
	base := io.EOF
	if got := EnrichStartupError(base, "server", nil, nil); got != base {
		t.Fatalf("nil run must leave the error unchanged, got %v", got)
	}

	called := false
	run := func(context.Context, string, []string) ([]byte, error) {
		called = true
		return []byte("x"), nil
	}
	if got := EnrichStartupError(base, "   ", nil, run); got != base {
		t.Fatalf("empty command must leave the error unchanged, got %v", got)
	}
	if called {
		t.Fatal("run must not be called for an empty command")
	}
}

func TestEnrichStartupErrorConnectionFailuresTrigger(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"bare EOF", io.EOF},
		{"unexpected EOF", io.ErrUnexpectedEOF},
		{"wrapped EOF", fmt.Errorf("mcp %q: initialize: %w", "x", io.EOF)},
		{"text EOF", errors.New("EOF")},
		{"stdio text EOF", errors.New(`mcp "x": server closed the connection`)},
		{"reset", syscall.ECONNRESET},
		{"refused", syscall.ECONNREFUSED},
	}
	run := func(context.Context, string, []string) ([]byte, error) {
		return []byte("cause: port 3000 already in use"), nil
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EnrichStartupError(tc.err, "server", nil, run)
			if got == tc.err {
				t.Fatalf("%s: want enrichment, got the original error", tc.name)
			}
			if !strings.Contains(got.Error(), "already in use") {
				t.Fatalf("%s: replay output missing: %q", tc.name, got.Error())
			}
			if !errors.Is(got, tc.err) {
				t.Fatalf("%s: original cause lost: %v", tc.name, got)
			}
		})
	}
}

func TestEnrichStartupErrorPassesCommandAndDeadline(t *testing.T) {
	run := func(ctx context.Context, cmd string, args []string) ([]byte, error) {
		if cmd != "node" || len(args) != 2 || args[0] != "server.js" || args[1] != "--port=0" {
			t.Errorf("run got cmd=%q args=%q", cmd, args)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Error("run got a context without a deadline")
		}
		return []byte("listening"), nil
	}
	got := EnrichStartupError(io.EOF, "node", []string{"server.js", "--port=0"}, run)
	if !strings.Contains(got.Error(), `"node server.js --port=0"`) {
		t.Fatalf("command label missing: %q", got.Error())
	}
	if !strings.Contains(got.Error(), "listening") {
		t.Fatalf("replay output missing: %q", got.Error())
	}
}
