package tui

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockedBuffer 让测试能在 Run 仍在绘制时安全地读取写出的字节。
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestRunSetsTerminalTitleOnEnterAndExit(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	out := &lockedBuffer{}
	ready := make(chan *Model, 1)
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opts := Options{
		IO:      &IO{In: reader, Out: out},
		OnReady: func(m *Model) { ready <- m },
	}
	go func() { done <- Run(ctx, opts) }()

	var m *Model
	select {
	case m = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("Run never became ready")
	}
	if got := out.String(); !strings.Contains(got, terminalTitle) {
		t.Fatalf("the startup sequence must set the terminal title: %q", got)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	written := out.String()
	if n := strings.Count(written, terminalTitle); n < 2 {
		t.Fatalf("terminal title sequence count = %d, want >= 2 (enter + exit): %q", n, written)
	}
	if !strings.Contains(terminalTitle, "\x1b]0;argentgo\x07") {
		t.Fatalf("the title sequence must be OSC 0 with BEL, got %q", terminalTitle)
	}
	// OSC 只写给 out 流，不进入 Frame/Buffer。
	if strings.Contains(frameText(m, 100, 24), "\x1b]") {
		t.Fatal("a terminal OSC sequence leaked into the rendered frame")
	}
}
