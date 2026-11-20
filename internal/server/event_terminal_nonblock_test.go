package server

import (
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
)

// TestEventHubTerminalPublishNeverBlocksOnConcurrentDrain 守护必达终态路径的
// 非阻塞投递约定。
//
// 订阅者的通道被填满，消费者并发地排空它。
// 当 Publish 看到通道已满时，它必须释放预留的
// 终态槽位且绝不阻塞。若使用普通的阻塞 `<-ch>`，在
// 该窗口内清空通道的消费者会让 Publish 卡住，
// 同时持有 h.mu——使其他发布者和订阅者全部死锁。
// 该测试在这种竞争下发布多个终态事件，若有任何 Publish
// 未能及时返回则失败。
func TestEventHubTerminalPublishNeverBlocksOnConcurrentDrain(t *testing.T) {
	const (
		rounds    = 40
		terminals = 400
	)
	for round := 0; round < rounds; round++ {
		hub := NewEventHub()
		ch, stop := hub.Subscribe(0)

		// 填到容量上限，使每次终态发布都走"满"分支。
		for i := 0; i < cap(ch); i++ {
			hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "fill"})
		}

		// 消费者持续排空，可在任意时刻清空通道，
		// 包括 Publish 的满通道检查与
		// 最旧事件移除之间。
		drained := make(chan struct{})
		go func() {
			defer close(drained)
			for range ch {
			}
		}()

		published := make(chan struct{})
		go func() {
			defer close(published)
			for i := 0; i < terminals; i++ {
				hub.Publish(agent.Event{Kind: agent.EventStop, Reason: "done"})
			}
		}()

		select {
		case <-published:
		case <-time.After(3 * time.Second):
			stop()
			t.Fatalf("round %d: terminal Publish blocked — hub deadlocked", round)
		}

		stop() // unsubscribes; the drain loop ends once the channel is closed
		select {
		case <-drained:
		case <-time.After(3 * time.Second):
			t.Fatalf("round %d: consumer did not finish draining", round)
		}
	}
}

// TestEventHubTerminalStillDeliveredWhenFull 演练该修复必须保持的约定：
// 终态事件能送达普通配额已满的订阅者，
// 使用预留槽位而不是淘汰它。
func TestEventHubTerminalStillDeliveredWhenFull(t *testing.T) {
	hub := NewEventHub()
	ch, stop := hub.Subscribe(0)
	defer stop()

	for i := 0; i < eventSubCap; i++ {
		hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "pending"})
	}
	hub.Publish(agent.Event{Kind: agent.EventStop, Reason: agent.StopComplete})

	if got := hub.Evicted(); got != 0 {
		t.Fatalf("evicted = %d, want 0: a terminal must not evict", got)
	}
	terminals := 0
	for i := 0; i < eventSubCap+1; i++ {
		select {
		case ev := <-ch:
			if ev.Kind == agent.EventStop {
				terminals++
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out draining event %d", i)
		}
	}
	if terminals != 1 {
		t.Fatalf("terminals = %d, want 1", terminals)
	}
}
