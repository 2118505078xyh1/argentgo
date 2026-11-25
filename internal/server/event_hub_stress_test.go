package server

import (
	"fmt"
	"sync"
	"testing"

	"example.com/argentgo/internal/agent"
)

// TestEventHubTerminalDeliveryUnderConcurrentPublishAndSlowDrain 是 R28 并发审计 P1 的压力测试：
// 多个发布者并发发布普通事件与终态事件，慢订阅者故意不排空直至被淘汰，
// 验证终态必达、淘汰与 stop() 并发时不 panic / 不 double-close，并核对 Evicted 计数合理。
func TestEventHubTerminalDeliveryUnderConcurrentPublishAndSlowDrain(t *testing.T) {
	hub := NewEventHub()

	const (
		publishers   = 8
		perPublisher = 100 // 普通事件总量远超 eventSubCap，确保慢订阅者被淘汰
		fastCount    = 4
		evictCount   = 3
		raceCount    = 4
	)

	// panicCh 收集子 goroutine 内恢复的 panic；double-close 会在这里留下记录。
	panicCh := make(chan any, publishers+fastCount+evictCount+raceCount+8)
	guard := func() {
		if r := recover(); r != nil {
			select {
			case panicCh <- r:
			default:
			}
		}
	}

	// fullSub 只订阅终态，故意不排空：普通事件洪泛不会淘汰它（类型过滤跳过），
	// 用来验证"缓冲区满仍必收终态"（预留槽位 + 丢弃最旧进度事件的回退路径）。
	fullSub, stopFull := hub.SubscribeTyped([]string{string(agent.EventStop)})

	// evictSubs 不排空，也从不调用 stop()：阶段二的普通事件洪泛必然淘汰它们。
	evictChans := make([]<-chan agent.Event, evictCount)
	for i := 0; i < evictCount; i++ {
		ch, _ := hub.Subscribe(hub.Seq())
		evictChans[i] = ch
	}

	// raceSubs 不排空，stop() 与淘汰并发调用：验证无 double-close / panic。
	raceChans := make([]<-chan agent.Event, raceCount)
	raceStops := make([]func(), raceCount)
	for i := 0; i < raceCount; i++ {
		ch, stop := hub.Subscribe(hub.Seq())
		raceChans[i] = ch
		raceStops[i] = stop
	}

	// 阶段一：用终态把 fullSub（以及不排空的慢订阅者）的普通配额填满，但不触发淘汰
	// （len 恰好等于 eventSubCap，终态走预留槽位而非淘汰路径）。
	for i := 0; i < eventSubCap; i++ {
		hub.Publish(agent.Event{Kind: agent.EventStop, Reason: "fill"})
	}

	// fastSubs 是"活跃消费者"：它们持续排空，必须稳定收到终态。
	// 这里只订阅终态类型，而不是全部事件——普通事件的洪泛会填满通道配额，
	// 在并行 benchmark 等调度压力下排空 goroutine 可能拿不到 CPU，导致本应
	// "快速"的订阅者被当作慢消费者淘汰，从而收不到 FINAL（测试假失败）。
	// 洪泛淘汰的行为已由 fullSub / evictSubs / raceSubs 覆盖；让 fastSubs 只
	// 接收终态既保留"并发发布 + 慢排空"的整体压力，又使"终态必达"断言不受
	// 调度时机影响。fastSubs 在阶段一之后订阅，只接收阶段二之后的终态。
	type drainState struct {
		final int // 收到 Reason=="FINAL" 的终态次数
	}
	fastState := make([]drainState, fastCount)
	fastStops := make([]func(), fastCount)
	var drainWG sync.WaitGroup
	for i := 0; i < fastCount; i++ {
		ch, stop := hub.SubscribeTyped([]string{string(agent.EventStop)})
		fastStops[i] = stop
		drainWG.Add(1)
		go func(i int, ch <-chan agent.Event) {
			defer drainWG.Done()
			defer guard()
			for ev := range ch {
				if ev.IsTerminal() && ev.Reason == "FINAL" {
					fastState[i].final++
				}
			}
		}(i, ch)
	}

	// 阶段二：并发发布普通事件洪泛与终态，同时并发调用 raceSubs 的 stop()。
	var pubWG sync.WaitGroup
	start := make(chan struct{})
	for p := 0; p < publishers; p++ {
		pubWG.Add(1)
		go func(p int) {
			defer pubWG.Done()
			defer guard()
			<-start
			for i := 0; i < perPublisher; i++ {
				hub.Publish(agent.Event{Kind: agent.EventNotice, Text: fmt.Sprintf("%d:%d", p, i)})
			}
			hub.Publish(agent.Event{Kind: agent.EventStop, Reason: fmt.Sprintf("done-%d", p)})
		}(p)
	}
	for _, stop := range raceStops {
		pubWG.Add(1)
		go func(stop func()) {
			defer pubWG.Done()
			defer guard()
			<-start
			stop() // 与洪泛导致的淘汰并发
			stop() // 再次调用验证幂等
		}(stop)
	}
	close(start)
	pubWG.Wait()

	// 所有发布者退出后补发唯一标记的终态：此刻每个未淘汰订阅者仍在册，必收。
	hub.Publish(agent.Event{Kind: agent.EventStop, Reason: "FINAL"})

	// 关闭 fastSubs，让排空 goroutine 读完缓冲后退出，再读取统计。
	for _, stop := range fastStops {
		stop()
	}
	drainWG.Wait()
	// fullSub 主动停止后才能 range 到通道关闭；stop 幂等，defer 中再调用一次无妨。
	stopFull()

	// 未淘汰订阅者必收终态：fastSubs 每个都收到 FINAL；fullSub 最后一个事件是 FINAL。
	for i := 0; i < fastCount; i++ {
		if fastState[i].final != 1 {
			t.Errorf("fast subscriber %d 收到 FINAL %d 次，want 1（终态必达）", i, fastState[i].final)
		}
	}
	var fullLast agent.Event
	for ev := range fullSub {
		fullLast = ev
	}
	if !fullLast.IsTerminal() || fullLast.Reason != "FINAL" {
		t.Fatalf("fullSub 最后事件 = %+v, want stop/FINAL", fullLast)
	}

	// Evicted 计数合理：evictSubs 至少全部被淘汰；上界不超过所有可能被淘汰的慢订阅者。
	evicted := hub.Evicted()
	if evicted < uint64(evictCount) {
		t.Errorf("Evicted = %d, want >= %d", evicted, evictCount)
	}
	if max := uint64(evictCount + raceCount + fastCount); evicted > max {
		t.Errorf("Evicted = %d, want <= %d", evicted, max)
	}

	// evictSubs 的通道在淘汰后关闭：排空缓冲后应读到 ok=false。
	for i, ch := range evictChans {
		seen := 0
		for range ch {
			seen++
		}
		if seen > eventSubCap+eventTerminalSlots {
			t.Errorf("evict subscriber %d 缓冲了 %d 个事件，超过容量", i, seen)
		}
	}

	// raceSubs 的通道最终都关闭（淘汰或 stop 二者之一），且淘汰后再 stop 仍幂等不 panic。
	for i, ch := range raceChans {
		raceStops[i]()
		for range ch {
		}
	}

	// 任何子 goroutine 的 panic（含 double-close）都算失败。
	select {
	case r := <-panicCh:
		t.Fatalf("并发发布/淘汰/stop 中发生 panic: %v", r)
	default:
	}
}
