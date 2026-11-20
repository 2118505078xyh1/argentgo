package main

import (
	"bufio"
	"io"
	"strings"
	"sync"
	"testing"
)

// TestDeltaHandlerRegistrationDoesNotRaceWithReadLoop keeps onDelta safe under
// -race: readLoop snapshots the callback while another goroutine swaps it for
// the next streaming round. The old code let the UI thread write b.onDelta
// directly while readLoop read it, which the race detector flags.
func TestDeltaHandlerRegistrationDoesNotRaceWithReadLoop(t *testing.T) {
	pr, pw := io.Pipe()
	b := &bridge{
		stdout:  bufio.NewReader(pr),
		pending: make(map[int]chan rpcReply),
	}
	done := make(chan struct{})
	go func() {
		b.readLoop() // returns after the writer closes pw
		close(done)
	}()

	const rounds = 300
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		line := `{"jsonrpc":"2.0","method":"prompt/delta","params":{"text":"d"}}` + "\n"
		for i := 0; i < rounds; i++ {
			if _, err := io.WriteString(pw, line); err != nil {
				return
			}
		}
	}()
	// Swap handlers concurrently with delta delivery, as sendReal does on the
	// UI thread when a new round begins.
	for i := 0; i < rounds; i++ {
		b.setDeltaHandler(func(string) {})
	}
	wg.Wait()
	pw.Close()
	<-done
}

// TestReadLoopUsesLatestDeltaHandler proves that a replacement callback takes
// over immediately: a readLoop snapshot can never deliver a delta to a handler
// that a newer round has already superseded.
func TestReadLoopUsesLatestDeltaHandler(t *testing.T) {
	line := `{"jsonrpc":"2.0","method":"prompt/delta","params":{"text":"hello"}}` + "\n"
	b := &bridge{
		stdout:  bufio.NewReader(strings.NewReader(line)),
		pending: make(map[int]chan rpcReply),
	}
	first, second := 0, 0
	b.setDeltaHandler(func(string) { first++ })
	gen2 := b.setDeltaHandler(func(s string) {
		if s == "hello" {
			second++
		}
	})
	if gen2 != 2 || !b.isCurrentDeltaGen(gen2) || b.isCurrentDeltaGen(1) {
		t.Fatalf("generation bookkeeping off: gen2=%d, current=1? %v, current=2? %v",
			gen2, b.isCurrentDeltaGen(1), b.isCurrentDeltaGen(gen2))
	}

	b.readLoop()

	if first != 0 || second != 1 {
		t.Fatalf("first=%d second=%d; want the latest handler to get the only delta", first, second)
	}
}

// TestSupersededRoundCannotClobberNewReply guards cross-round 串台: when round
// 1 is aborted and round 2 starts before round 1's result returns, round 1's
// late completion must not run finish() (which writes app.reply, by then owned
// by round 2). The generation token makes that stale completion a no-op.
func TestSupersededRoundCannotClobberNewReply(t *testing.T) {
	b := &bridge{pending: make(map[int]chan rpcReply)}
	reply1 := &message{id: 1, kind: msgAssistant, text: "old stream"}
	reply2 := &message{id: 2, kind: msgAssistant}
	app := &chat{engine: b, reply: reply1, streaming: true}
	app.messages = []*message{reply1}

	gen1 := b.setDeltaHandler(func(string) {})
	// Round 2 supersedes round 1 before round 1's result arrives.
	app.reply = reply2
	app.messages = append(app.messages, reply2)
	gen2 := b.setDeltaHandler(func(string) {})

	app.completeRealReply(b, gen1, reply1, "old answer", nil)
	if got := reply2.text; got != "" || reply2.done {
		t.Fatalf("stale completion touched new reply: text=%q done=%v", got, reply2.done)
	}
	if app.reply != reply2 || !app.streaming || app.status != "" {
		t.Fatalf("stale completion changed round-2 state: reply=%p streaming=%v status=%q",
			app.reply, app.streaming, app.status)
	}

	// Round 2's own completion still applies normally.
	app.completeRealReply(b, gen2, reply2, "new answer", nil)
	if reply2.text != "new answer" || !reply2.done {
		t.Fatalf("round-2 completion failed: text=%q done=%v", reply2.text, reply2.done)
	}
	if app.streaming || app.reply != nil || app.status != "已完成" {
		t.Fatalf("round-2 state wrong: streaming=%v reply=%v status=%q",
			app.streaming, app.reply, app.status)
	}
}
