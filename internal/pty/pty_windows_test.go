//go:build windows

package pty

import (
	"context"
	"sync"
	"testing"
)

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
