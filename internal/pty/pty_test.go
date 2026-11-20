package pty

import (
	"context"
	"errors"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// shellCommand 构建一个打印一行然后滞留的子进程，使
// 终端有内容可读且在测试检查时仍打开。
func shellCommand(line string) Command {
	if runtime.GOOS == "windows" {
		return Command{
			Path: "cmd.exe",
			Args: []string{"/C", line + " & ping -n 6 127.0.0.1 >NUL"},
		}
	}
	return Command{
		Path: "/bin/sh",
		Args: []string{"-c", line + "; sleep 5"},
	}
}

func exitCommand(code string) Command {
	if runtime.GOOS == "windows" {
		return Command{Path: "cmd.exe", Args: []string{"/C", "exit " + code}}
	}
	return Command{Path: "/bin/sh", Args: []string{"-c", "exit " + code}}
}

// collector 在后台读取终端并保留一切。
type collector struct {
	mu   sync.Mutex
	text strings.Builder
}

func (c *collector) start(p *Pty) {
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := p.Read(buf)
			if n > 0 {
				c.mu.Lock()
				c.text.Write(buf[:n])
				c.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
}

func (c *collector) all() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.text.String()
}

func (c *collector) waitFor(needle string, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if got := c.all(); strings.Contains(got, needle) {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	return c.all()
}

func TestSupportedPlatforms(t *testing.T) {
	if Supported() {
		return
	}
	p, err := Start(context.Background(), shellCommand("echo hi"), DefaultSize)
	if p != nil {
		_ = p.Close()
		t.Fatal("an unsupported platform must not return a terminal")
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestStartReadsChildOutput(t *testing.T) {
	if !Supported() {
		t.Skip("pseudoterminals are not supported here")
	}
	p, err := Start(context.Background(), shellCommand("echo pty-ready"), Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		_ = p.Close()
		_, _ = p.Wait()
	}()

	if p.PID() <= 0 {
		t.Fatalf("pid = %d", p.PID())
	}
	if got := p.Size(); got != (Size{Cols: 80, Rows: 24}) {
		t.Fatalf("size = %+v", got)
	}

	output := &collector{}
	output.start(p)
	got := output.waitFor("pty-ready", 20*time.Second)
	if !strings.Contains(got, "pty-ready") {
		t.Fatalf("terminal output = %q", got)
	}
}

func TestResizeReachesTheChild(t *testing.T) {
	if !Supported() {
		t.Skip("pseudoterminals are not supported here")
	}
	p, err := Start(context.Background(), shellCommand("echo sized"), Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		_ = p.Close()
		_, _ = p.Wait()
	}()

	if err := p.Resize(Size{Cols: 100, Rows: 30}); err != nil {
		t.Fatalf("resize: %v", err)
	}
	if got := p.Size(); got != (Size{Cols: 100, Rows: 30}) {
		t.Fatalf("size = %+v", got)
	}
	if err := p.Resize(Size{}); err == nil {
		t.Fatal("a 0x0 size must be refused")
	}
}

func TestWriteReachesTheChild(t *testing.T) {
	if !Supported() {
		t.Skip("pseudoterminals are not supported here")
	}
	// shellCommand 构建一个打印一行然后滞留的子进程，使终端有内容可读且在测试检查时仍打开。collector 在后台读取终端并保留一切。交互式 shell 会回显我们键入的内容，因此返回的标记证明终端的写端已接好。
	// 证明终端的写端已接好。
	cmd := Command{Path: "cmd.exe"}
	if runtime.GOOS != "windows" {
		cmd = Command{Path: "/bin/sh"}
	}
	p, err := Start(context.Background(), cmd, Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		_ = p.Close()
		_, _ = p.Wait()
	}()

	output := &collector{}
	output.start(p)
	if _, err := p.Write([]byte("echo typed-through-pty\r")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := output.waitFor("typed-through-pty", 20*time.Second); !strings.Contains(got, "typed-through-pty") {
		t.Fatalf("terminal output = %q", got)
	}
}

func TestWaitReportsTheExitCode(t *testing.T) {
	if !Supported() {
		t.Skip("pseudoterminals are not supported here")
	}
	p, err := Start(context.Background(), exitCommand("7"), Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = p.Close() }()

	code, err := p.Wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if code != 7 {
		t.Fatalf("exit code = %d, want 7", code)
	}
	if !p.Exited() {
		t.Fatal("the child must be reported as exited")
	}
	if got := p.ExitCode(); got != 7 {
		t.Fatalf("ExitCode = %d, want 7", got)
	}
}

func TestCloseIsIdempotentAndUnblocksReads(t *testing.T) {
	if !Supported() {
		t.Skip("pseudoterminals are not supported here")
	}
	p, err := Start(context.Background(), shellCommand("echo closing"), Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := p.Wait(); err != nil {
		t.Fatalf("wait after close: %v", err)
	}
}

func TestContextCancellationClosesTheTerminal(t *testing.T) {
	if !Supported() {
		t.Skip("pseudoterminals are not supported here")
	}
	ctx, cancel := context.WithCancel(context.Background())
	p, err := Start(ctx, shellCommand("echo cancelled"), Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	cancel()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if p.Exited() {
			_ = p.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = p.Close()
	t.Fatal("cancelling ctx must terminate the child")
}

// recordingTerminal 是一个记住最后应用到它的大小的终端桩。
// 它使调整大小并发测试能在每个平台上运行而无需
// recordingTerminal 是一个记住最后应用大小的终端桩。它使调整大小并发测试能在每个平台上运行而无需真实 ConPTY 或 /dev/ptmx。
type recordingTerminal struct {
	mu       sync.Mutex
	lastSize Size
}

func (r *recordingTerminal) Read([]byte) (int, error) { return 0, io.EOF }
func (r *recordingTerminal) Write(b []byte) (int, error) {
	return len(b), nil
}
func (r *recordingTerminal) Close() error { return nil }
func (r *recordingTerminal) Resize(s Size) error {
	r.mu.Lock()
	r.lastSize = s
	r.mu.Unlock()
	return nil
}
func (r *recordingTerminal) Wait() (int, error) { return 0, nil }
func (r *recordingTerminal) Exited() bool       { return true }
func (r *recordingTerminal) ExitCode() int      { return 0 }
func (r *recordingTerminal) PID() int           { return 1 }
func (r *recordingTerminal) applied() Size {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastSize
}

// TestConcurrentResizeAndSize 守护 size 字段免受数据竞争
// TestConcurrentResizeAndSize 守护 size 字段免受 UI 调整大小事件与渲染当前大小的读者之间的数据竞争。它还检查报告的大小始终与最后应用到终端的大小一致，这能捕捉两个 Resize 重叠时的丢失更新。大小必须是请求的大小之一（另一个写入者可能赢得了竞态），绝不可以是两者的撕裂混合。
// 它检查报告的大小始终与最后应用到
// 终端的大小一致，这能捕捉两个 Resize 重叠时的丢失更新。
func TestConcurrentResizeAndSize(t *testing.T) {
	term := &recordingTerminal{lastSize: DefaultSize}
	p := &Pty{term: term, size: DefaultSize, closed: make(chan struct{})}

	const (
		writers = 8
		loops   = 500
	)
	var wg sync.WaitGroup
	for id := 0; id < writers; id++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			want := Size{Cols: uint16(80 + id), Rows: uint16(24 + id)}
			for i := 0; i < loops; i++ {
				if err := p.Resize(want); err != nil {
					t.Errorf("resize: %v", err)
					return
				}
				// 大小必须是请求的大小之一（另一个写入者
				// 可能赢得了竞态），绝不可以是两者的撕裂混合。
				got := p.Size()
				if got.Cols < 80 || got.Cols >= 80+writers ||
					got.Rows < 24 || got.Rows >= 24+writers {
					t.Errorf("Size() = %+v during concurrent resize", got)
					return
				}
			}
		}(id)
	}
	wg.Wait()

	if got, applied := p.Size(), term.applied(); got != applied {
		t.Fatalf("reported size %+v disagrees with applied size %+v", got, applied)
	}
}

func TestStartRejectsAnEmptyPath(t *testing.T) {
	if !Supported() {
		t.Skip("pseudoterminals are not supported here")
	}
	if _, err := Start(context.Background(), Command{}, DefaultSize); err == nil {
		t.Fatal("an empty path must be an error")
	}
}

func TestZeroSizeFallsBackToDefault(t *testing.T) {
	if !Supported() {
		t.Skip("pseudoterminals are not supported here")
	}
	p, err := Start(context.Background(), exitCommand("0"), Size{})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = p.Close() }()
	if got := p.Size(); got != DefaultSize {
		t.Fatalf("size = %+v, want %+v", got, DefaultSize)
	}
	if _, err := p.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
}
