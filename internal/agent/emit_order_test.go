package agent

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

// TestEmitDeliversInAppendOrderUnderConcurrency 检查核心契约：即使有大量并发发出调用和一个会让出的消费者，
// emitFn 观察到的事件顺序也与它们追加到 s.events 的顺序完全一致。
// （接上行）
func TestEmitDeliversInAppendOrderUnderConcurrency(t *testing.T) {
	const n = 64

	var deliveredMu sync.Mutex
	var delivered []string
	s := newRunState(Options{}, "", func(e Event) {
		// 在持有投递轮次时让出：没有票号门控，这会让较晚的追加者超过较早的。
		// （接上行）
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

// TestEmitRecordsWhileConsumerBlocked 检查被阻塞的 emitFn 不能阻止事件被记录：第一个事件仍卡在消费者内部时，第二个发出会追加。
// 截止时间只是防挂起保护，不是计时断言。
// （接上行）
// （接上行）
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

// waitRecordedEvents 等待 len(s.events) 达到 want，读取时使用发出器所用的同一把锁。截止时间只防挂起。
// （接上行）
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
