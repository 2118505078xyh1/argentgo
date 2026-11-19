package agent

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

// TestEmitDeliversInAppendOrderUnderConcurrency checks the core contract: even
// with many concurrent emit calls and a consumer that yields, emitFn observes
// events in exactly the order they were appended to s.events.
func TestEmitDeliversInAppendOrderUnderConcurrency(t *testing.T) {
	const n = 64

	var deliveredMu sync.Mutex
	var delivered []string
	s := newRunState(Options{}, "", func(e Event) {
		// Yield while holding the delivery turn: without the ticket gate this
		// makes later appenders overtake earlier ones.
		runtime.Gosched()
		deliveredMu.Lock()
		delivered = append(delivered, e.Text)
		deliveredMu.Unlock()
	})

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.emit(Event{Kind: EventNotice, Text: fmt.Sprintf("event-%03d", i)})
		}(i)
	}
	wg.Wait()

	s.eventsMu.Lock()
	appended := make([]string, len(s.events))
	for i, e := range s.events {
		appended[i] = e.Text
	}
	s.eventsMu.Unlock()

	if len(delivered) != len(appended) {
		t.Fatalf("emitFn saw %d event(s), recorded %d", len(delivered), len(appended))
	}
	for i := range appended {
		if delivered[i] != appended[i] {
			t.Fatalf("event %d: emitFn saw %q, appended %q", i, delivered[i], appended[i])
		}
	}
}

// TestEmitRecordsWhileConsumerBlocked checks that a blocked emitFn cannot stop
// events from being recorded: a second emit appends while the first event is
// still stuck inside the consumer. The deadline is only a hang guard, not a
// timing assertion.
func TestEmitRecordsWhileConsumerBlocked(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	s := newRunState(Options{}, "", func(Event) {
		once.Do(func() { close(entered) })
		<-release
	})

	go s.emit(Event{Kind: EventNotice, Text: "first"})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("emitFn was never called")
	}

	secondDone := make(chan struct{})
	go func() {
		s.emit(Event{Kind: EventNotice, Text: "second"})
		close(secondDone)
	}()

	waitRecordedEvents(t, s, 2)

	close(release)
	select {
	case <-secondDone:
	case <-time.After(5 * time.Second):
		t.Fatal("emit did not finish after the consumer was released")
	}
}

// waitRecordedEvents waits until len(s.events) reaches want, reading under the
// same lock the emitter uses. The deadline only guards against a hang.
func waitRecordedEvents(t *testing.T, s *runState, want int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		s.eventsMu.Lock()
		n := len(s.events)
		s.eventsMu.Unlock()
		if n >= want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("only %d event(s) recorded, want %d", n, want)
		default:
			runtime.Gosched()
		}
	}
}
