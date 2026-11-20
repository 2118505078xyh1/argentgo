package proc

import (
	"sync"
	"testing"
)

// TestCaptureSurvivesConcurrentFlush guards the invariant that Run relies on
// when awaitOutcome gives up waiting for cmd.Wait (a killed child can leave a
// descendant holding a stdout/stderr pipe open, so exec's copy goroutines are
// still running). Flush and String must not race with the copy goroutine's
// writes. Before the fix this test is reported as a data race under -race.
func TestCaptureSurvivesConcurrentFlush(t *testing.T) {
	cb := &cappedBuffer{max: 64}
	cw := newUTF8CarryWriter(cb)

	const iterations = 500
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			// One byte at a time forces the carry writer to mutate carry on
			// every call, maximising the overlap with Flush on the other side.
			for _, b := range []byte("中文测试") {
				_, _ = cw.Write([]byte{b})
			}
		}
	}()
	for i := 0; i < iterations; i++ {
		cw.Flush()
		_ = cb.String()
	}
	wg.Wait()
	cw.Flush()
	_ = cb.String()
}
