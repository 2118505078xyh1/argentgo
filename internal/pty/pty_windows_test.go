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

// TestAttributeListBufferOutlivesGC guards the Go-side lifetime of the Win32
// proc-thread attribute list. Its address is a bare uintptr, which the GC does
// not treat as a reference: returning only that address (and a closure that
// captures nothing else) lets the backing buffer be collected before
// CreateProcessW or DeleteProcThreadAttributeList dereferences it.
func TestAttributeListBufferOutlivesGC(t *testing.T) {
	attrs, err := newAttributeList(0)
	if err != nil {
		t.Fatalf("newAttributeList: %v", err)
	}
	collected := make(chan struct{})
	runtime.SetFinalizer(&attrs.buffer[0], func(*byte) { close(collected) })

	// Drop the struct but keep the cleanup reachable, as startPlatform does
	// with its deferred call: the buffer must survive until cleanup runs.
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

// TestWaitConcurrentCloseNeverErrors reproduces a ConPTY use-after-close.
// Close used to close the child's process handle while a concurrent Wait was
// still using it, so GetExitCodeProcess failed with "The handle is invalid"
// and Wait reported a spurious error for an otherwise normally exited child.
// The common pattern is a Wait goroutine plus Close from context cancellation.
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

// TestReadReturnsEOFAfterWait guards the ConPTY output-pipe lifetime. The
// console keeps its end of the output pipe open even after the child exits, so
// a reader draining the terminal used to block forever unless someone called
// Close. Wait now releases the HPCON once the child is reaped, which closes the
// pipe and lets Read report EOF after the buffered output is consumed.
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

// TestExitedDoesNotUseAClosedProcessHandle models the state a racing Close can
// leave behind: Close has terminated and reaped the child and closed the
// process handle, but a reader that loaded c.process just before still holds
// its numeric value. Exited/ExitCode must never call WaitForSingleObject or
// GetExitCodeProcess on that stale handle — Wait already duplicates it for the
// same reason. Querying the stale value returned WAIT_FAILED, which made
// Exited report a dead child as still running.
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

// TestExitedAndExitCodeRaceWithClose exercises the process-handle lifetime
// under the real ConPTY lifecycle: a waiter, a closer (context cancellation's
// job) and a status poller racing on the same terminal. It complements the
// deterministic stale-handle test above with the actual concurrency pattern.
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
		// Once Close returned the child has been terminated and reaped.
		if !p.Exited() {
			t.Fatalf("round %d: Exited() = false after Close", i)
		}
		_ = p.Close()
	}
}

// TestResizeKeepsConsoleAliveAcrossPlatformCall guards the HPCON lifetime
// against a concurrent Close. Resize used to read c.console under c.mu, release
// the lock, and only then call ResizePseudoConsole; a racing Close (or Wait
// reaping the child) could null out and close the HPCON in that window, leaving
// Resize calling ResizePseudoConsole on a freed handle. The lock must cover the
// platform call, mirroring the duplicate-handle protection in Wait/Exited.
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

	// A Resize already inside the platform call owns the HPCON; closeConsole
	// must wait on c.mu until it returns rather than closing the handle here.
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
