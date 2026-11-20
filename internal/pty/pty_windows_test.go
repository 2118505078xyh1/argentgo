//go:build windows

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
