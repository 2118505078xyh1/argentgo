package main

import (
	"bufio"
	"io"
	"strings"
	"sync"
	"testing"
)

// TestDeltaHandlerRegistrationDoesNotRaceWithReadLoop 在 -race 下保持 onDelta
// 安全：readLoop 快照回调，而另一个 goroutine 为下一轮交换它。
// TestDeltaHandlerRegistrationDoesNotRaceWithReadLoop 在 -race 下保持 onDelta 安全：readLoop 快照回调，而另一个 goroutine 为下一轮流式交换它。旧代码让 UI 线程直接写 b.onDelta 而 readLoop 读它，竞态检测器会标记。与 delta 传递并发交换处理器，就像 sendReal 在新一轮开始时的 UI 线程上所做的。
// 直接写它而 readLoop 读它，竞态检测器会标记。
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
	// 与 delta 传递并发交换处理器，就像 sendReal 在
	// 新一轮开始时的 UI 线程上所做的。
	for i := 0; i < rounds; i++ {
		b.setDeltaHandler(func(string) {})
	}
	wg.Wait()
	pw.Close()
	<-done
}

// TestReadLoopUsesLatestDeltaHandler 证明替换的回调立即
// 接管：readLoop 快照绝不可能把 delta 传递给已被
// 更新轮次取代的处理器。
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
// 轮 1 被中止且轮 2 在轮 1 结果返回前开始，轮 1 的
// 迟到完成绝不能运行 finish()（它写 app.reply，那时已归
func TestSupersededRoundCannotClobberNewReply(t *testing.T) {
	b := &bridge{pending: make(map[int]chan rpcReply)}
	reply1 := &message{id: 1, kind: msgAssistant, text: "old stream"}
	reply2 := &message{id: 2, kind: msgAssistant}
	app := &chat{engine: b, reply: reply1, streaming: true}
	app.messages = []*message{reply1}

	gen1 := b.setDeltaHandler(func(string) {})
	// 轮 2 在轮 1 的结果到达前取代轮 1。
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

	// 轮 2 自己的完成仍正常应用。
	app.completeRealReply(b, gen2, reply2, "new answer", nil)
	if reply2.text != "new answer" || !reply2.done {
		t.Fatalf("round-2 completion failed: text=%q done=%v", reply2.text, reply2.done)
	}
	if app.streaming || app.reply != nil || app.status != "已完成" {
		t.Fatalf("round-2 state wrong: streaming=%v reply=%v status=%q",
			app.streaming, app.reply, app.status)
	}
}
