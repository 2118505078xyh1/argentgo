package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// TestAppendStderrContextNoReplay 覆盖把捕获的 stderr 纯折叠进
// 启动错误的过程。它从不运行命令，因此丰富化没有副作用。
func TestAppendStderrContextNoReplay(t *testing.T) {
	t.Run("connection error gains stderr", func(t *testing.T) {
		base := fmt.Errorf(`mcp "x": initialize: mcp "x": server closed the connection`)
		got := appendStderrContext(base, "node: bad option --nope\nUsage: node [options]\n")
		if got == base {
			t.Fatal("want a new enriched error, got the original")
		}
		if !errors.Is(got, base) {
			t.Fatalf("original cause lost: %v", got)
		}
		for _, want := range []string{"server stderr", "bad option", "Usage: node"} {
			if !strings.Contains(got.Error(), want) {
				t.Fatalf("enriched error %q missing %q", got.Error(), want)
			}
		}
	})

	t.Run("empty or whitespace stderr leaves error untouched", func(t *testing.T) {
		base := io.EOF
		for _, stderr := range []string{"", "   \n\t  "} {
			got := appendStderrContext(base, stderr)
			if got != base {
				t.Fatalf("stderr %q changed the error: %v", stderr, got)
			}
		}
	})

	t.Run("non connection error leaves error untouched", func(t *testing.T) {
		base := errors.New("boom")
		got := appendStderrContext(base, "diagnostic from a healthy-but-chatty server")
		if got != base || got.Error() != base.Error() {
			t.Fatalf("non-connection error changed: %v", got)
		}
	})

	t.Run("CJK stderr is preserved", func(t *testing.T) {
		base := io.EOF
		got := appendStderrContext(base, "错误：找不到模块 '服务器'\n启动失败\n")
		for _, want := range []string{"错误：找不到模块 '服务器'", "启动失败"} {
			if !strings.Contains(got.Error(), want) {
				t.Fatalf("enriched error %q missing CJK %q", got.Error(), want)
			}
		}
		if strings.Contains(got.Error(), "\uFFFD") {
			t.Fatalf("CJK output corrupted: %q", got.Error())
		}
	})

	t.Run("large stderr is truncated on a rune boundary", func(t *testing.T) {
		got := appendStderrContext(io.EOF, strings.Repeat("中文", 5000))
		if !strings.Contains(got.Error(), "[server stderr truncated]") {
			t.Fatalf("want truncation marker, got %q", got.Error())
		}
		if strings.Contains(got.Error(), "\uFFFD") {
			t.Fatalf("truncation split a UTF-8 rune: %q", got.Error())
		}
	})
}

// TestClientEnrichStderrSources 验证客户端从 stdio 传输的
// 环形缓冲区读取 stderr，且对非 stdio 传输或非连接
// 错误保持空操作。
func TestClientEnrichStderrSources(t *testing.T) {
	newStdioClient := func(stderr string) *client {
		ring := &ringBuffer{}
		if stderr != "" {
			_, _ = ring.Write([]byte(stderr))
		}
		done := make(chan struct{})
		close(done)
		return &client{
			spec: Spec{Name: "x"},
			tr:   &stdioTransport{diag: ring, drainDone: done},
		}
	}

	t.Run("stdio transport supplies stderr", func(t *testing.T) {
		c := newStdioClient("cause: port 3000 already in use\n")
		got := c.enrichStderr(io.EOF)
		if !strings.Contains(got.Error(), "port 3000 already in use") {
			t.Fatalf("stderr not attached: %v", got)
		}
		if !errors.Is(got, io.EOF) {
			t.Fatalf("cause lost: %v", got)
		}
	})

	t.Run("empty ring leaves error untouched", func(t *testing.T) {
		base := io.EOF
		if got := newStdioClient("").enrichStderr(base); got != base {
			t.Fatalf("empty ring changed the error: %v", got)
		}
	})

	t.Run("non stdio transport leaves error untouched", func(t *testing.T) {
		base := io.EOF
		c := &client{spec: Spec{Name: "x"}, tr: &fakeTransport{}}
		if got := c.enrichStderr(base); got != base {
			t.Fatalf("non-stdio transport changed the error: %v", got)
		}
	})

	t.Run("non connection error leaves error untouched", func(t *testing.T) {
		base := errors.New("decode failure")
		if got := newStdioClient("still prints useful things").enrichStderr(base); got != base {
			t.Fatalf("non-connection error changed: %v", got)
		}
	})
}

// TestStderrWithDrainIsBounded 守护诊断等待：一个排空信号永不出现的
// 传输不得把调用方拖过有界窗口。
func TestStderrWithDrainIsBounded(t *testing.T) {
	old := stderrDrainWait
	stderrDrainWait = 20 * time.Millisecond
	defer func() { stderrDrainWait = old }()

	ring := &ringBuffer{}
	_, _ = ring.Write([]byte("captured"))
	tr := &stdioTransport{diag: ring, drainDone: make(chan struct{})} // 永不关闭

	start := time.Now()
	got := tr.stderrWithDrain()
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("stderrWithDrain stalled for %v", elapsed)
	}
	if got != "captured" {
		t.Fatalf("stderrWithDrain = %q, want captured", got)
	}
}

// TestConnectStdioFailureIncludesStderr 是端到端的审计案例：
// 一台打印启动错误后退出的服务器会使 Initialize 失败，并在
// 返回的错误中带上 stderr，因此应用现有的"打印错误"路径就足够了——
// 无需重放命令，也无需修改 internal/app。
func TestConnectStdioFailureIncludesStderr(t *testing.T) {
	c, err := Connect(context.Background(), helperSpec("failfast", 2*time.Second))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = c.Initialize(ctx)
	if err == nil {
		t.Fatal("expected initialize to fail for a server that exits at startup")
	}
	if !strings.Contains(err.Error(), "server stderr") ||
		!strings.Contains(err.Error(), "port 3000 in use") {
		t.Fatalf("startup failure missing captured stderr: %v", err)
	}
}
