//go:build windows

package pty

import (
	"context"
	"errors"
	"io"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestAttributeListBufferOutlivesGC 守护 Win32 proc-thread 属性列表的 Go 侧
// 不把它当作引用：只返回该地址（以及一个不捕获
// 其他内容的闭包）会让底层缓冲区在
// 解引用它之前被回收。
func TestAttributeListBufferOutlivesGC(t *testing.T) {
	attrs, err := newAttributeList(0)
	if err != nil {
		t.Fatalf("newAttributeList: %v", err)
	}
	collected := make(chan struct{})
	runtime.SetFinalizer(&attrs.buffer[0], func(*byte) { close(collected) })

	// 丢弃结构体但保持清理可达，正如 startPlatform 用其
	// 延迟调用所做的：缓冲区必须存活到清理运行。
	del := attrs.delete
	attrs = nil
	for i := 0; i < 5; i++ {
		runtime.GC()
	}
	select {
	case <-collected:
		t.Fatal("attribute-list buffer was collected while the list was still in use")
	default:
	}
	del()
}

// TestWaitConcurrentCloseNeverErrors 复现 ConPTY 关闭后使用。
// Close 过去在并发 Wait 仍在使用子进程进程句柄时关闭它，
// 使 GetExitCodeProcess 以 "句柄无效" 失败，
// Wait 对本来正常退出的子进程报告虚假错误。
// TestWaitConcurrentCloseNeverErrors 复现 ConPTY 关闭后使用。Close 过去在并发 Wait 仍在使用子进程进程句柄时关闭它，使 GetExitCodeProcess 以 "句柄无效" 失败，Wait 对本来正常退出的子进程报告虚假错误。常见模式是 Wait goroutine 加上上下文取消触发的 Close。
func TestWaitConcurrentCloseNeverErrors(t *testing.T) {
	const rounds = 200
	for i := 0; i < rounds; i++ {
		p, err := Start(context.Background(), exitCommand("0"), Size{Cols: 80, Rows: 24})
		if err != nil {
			t.Fatalf("start (round %d): %v", i, err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		var waitErr error
		go func() {
			defer wg.Done()
			_, waitErr = p.Wait()
		}()
		go func() {
			defer wg.Done()
			_ = p.Close()
		}()
		wg.Wait()
		if waitErr != nil {
			t.Fatalf("round %d: Wait racing with Close returned %v", i, waitErr)
		}
	}
}

// TestReadReturnsEOFAfterWait 守护 ConPTY 输出管道生命周期。
// 控制台即使在子进程退出后也保持其输出管道端打开，
// 因此排空终端的读者过去会永远阻塞除非有人调用 Close。
// TestReadReturnsEOFAfterWait 守护 ConPTY 输出管道生命周期。控制台即使在子进程退出后也保持其输出管道端打开，因此排空终端的读者过去会永远阻塞除非有人调用 Close。Wait 现在在子进程被回收后释放 HPCON，这会关闭管道并使 Read 在消费完缓冲输出后报告 EOF。
// 这会在消费完缓冲输出后让 Read 报告 EOF。
func TestReadReturnsEOFAfterWait(t *testing.T) {
	p, err := Start(context.Background(),
		Command{Path: "cmd.exe", Args: []string{"/C", "echo pty-eof-ready"}},
		Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = p.Close() }()

	var mu sync.Mutex
	var output strings.Builder
	readErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := p.Read(buf)
			if n > 0 {
				mu.Lock()
				output.Write(buf[:n])
				mu.Unlock()
			}
			if err != nil {
				readErr <- err
				return
			}
		}
	}()

	if code, err := p.Wait(); err != nil || code != 0 {
		t.Fatalf("wait: code = %d, err = %v", code, err)
	}
	select {
	case err := <-readErr:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("read after Wait = %T %v, want io.EOF", err, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Read is still blocked after the child exited and Wait returned")
	}

	mu.Lock()
	got := output.String()
	mu.Unlock()
	if !strings.Contains(got, "pty-eof-ready") {
		t.Fatalf("output = %q, want the child's buffered line", got)
	}
}

// TestExitedDoesNotUseAClosedProcessHandle 建模竞态 Close 可能留下的
// 状态：Close 已终止并回收子进程并关闭进程句柄，但刚在之前加载 c.process 的读者仍持有
// 其数值。Exited/ExitCode 绝不能对那个陈旧句柄调用 WaitForSingleObject 或
// 句柄 —— Wait 已出于同样原因复制它。
// 查询陈旧值返回 WAIT_FAILED，这使
// Exited 把已死的子进程报告为仍在运行。
func TestExitedDoesNotUseAClosedProcessHandle(t *testing.T) {
	self, err := syscall.GetCurrentProcess()
	if err != nil {
		t.Skipf("GetCurrentProcess: %v", err)
	}
	var handle syscall.Handle
	if err := syscall.DuplicateHandle(self, self, self, &handle, 0, false, syscall.DUPLICATE_SAME_ACCESS); err != nil {
		t.Skipf("DuplicateHandle: %v", err)
	}
	if err := syscall.CloseHandle(handle); err != nil {
		t.Fatalf("CloseHandle: %v", err)
	}

	t.Run("handle closed while terminal still reports open", func(t *testing.T) {
		c := &conPty{process: handle, exitCode: -1}
		if !c.Exited() {
			t.Fatal("Exited() = false after the process handle was closed")
		}
		if got := c.ExitCode(); got != -1 {
			t.Fatalf("ExitCode() = %d, want -1", got)
		}
	})

	t.Run("closed flag set but stale handle still held", func(t *testing.T) {
		c := &conPty{process: handle, closed: true, exitCode: -1}
		if !c.Exited() {
			t.Fatal("Exited() = false for a terminal that is being closed")
		}
	})
}

// TestExitedAndExitCodeRaceWithClose 在真实 ConPTY 生命周期下演练进程句柄
// 生命周期：一个等待者、一个关闭者（上下文取消的任务）
// 和一个状态轮询者在同一个终端上竞争。它与
// 上面的确定性陈旧句柄测试互补，提供真实的并发模式。
func TestExitedAndExitCodeRaceWithClose(t *testing.T) {
	const rounds = 100
	for i := 0; i < rounds; i++ {
		p, err := Start(context.Background(), shellCommand("echo racing"), Size{Cols: 80, Rows: 24})
		if err != nil {
			t.Fatalf("start (round %d): %v", i, err)
		}
		var wg sync.WaitGroup
		wg.Add(3)
		go func() { defer wg.Done(); _, _ = p.Wait() }()
		go func() { defer wg.Done(); _ = p.Close() }()
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = p.Exited()
				_ = p.ExitCode()
			}
		}()
		wg.Wait()
		// TestExitedAndExitCodeRaceWithClose 在真实 ConPTY 生命周期下演练进程句柄生命周期：一个等待者、一个关闭者（上下文取消的任务）和一个状态轮询者在同一个终端上竞争。它与上面的确定性陈旧句柄测试互补，提供真实的并发模式。一旦 Close 返回，子进程已被终止和回收。
		if !p.Exited() {
			t.Fatalf("round %d: Exited() = false after Close", i)
		}
		_ = p.Close()
	}
}

// TestResizeKeepsConsoleAliveAcrossPlatformCall 守护 HPCON 生命周期
// 释放锁，然后才调用 ResizePseudoConsole；竞争的 Close（或 Wait
// 回收子进程）可能在那个窗口把 HPCON 置空并关闭，使 Resize
// 对已释放句柄调用 ResizePseudoConsole。锁必须覆盖该
// 平台调用，镜像 Wait/Exited 中的复制句柄保护。
func TestResizeKeepsConsoleAliveAcrossPlatformCall(t *testing.T) {
	oldResize, oldClose := callResizePseudoConsole, callClosePseudoConsole
	defer func() { callResizePseudoConsole, callClosePseudoConsole = oldResize, oldClose }()

	resizeEntered := make(chan struct{})
	releaseResize := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseResize) }) }
	defer release()

	callResizePseudoConsole = func(syscall.Handle, uintptr) error {
		close(resizeEntered)
		<-releaseResize
		return nil
	}
	consoleClosed := make(chan struct{})
	callClosePseudoConsole = func(syscall.Handle) { close(consoleClosed) }

	c := &conPty{console: 1, exitCode: -1}
	resizeDone := make(chan error, 1)
	go func() { resizeDone <- c.Resize(Size{Cols: 80, Rows: 24}) }()

	select {
	case <-resizeEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("Resize did not reach the platform call")
	}

	go c.closeConsole()

	// 已在平台调用中的 Resize 拥有 HPCON；closeConsole
	// 必须等待 c.mu 直到它返回，而非在这里关闭句柄。
	select {
	case <-consoleClosed:
		t.Fatal("ClosePseudoConsole ran while Resize still held the HPCON")
	case <-time.After(100 * time.Millisecond):
	}

	release()
	if err := <-resizeDone; err != nil {
		t.Fatalf("Resize: %v", err)
	}
	select {
	case <-consoleClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("ClosePseudoConsole did not run after Resize returned")
	}
	c.mu.Lock()
	closed := c.console == 0
	c.mu.Unlock()
	if !closed {
		t.Fatal("console was not released after closeConsole")
	}
}
