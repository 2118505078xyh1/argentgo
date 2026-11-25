package server

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"example.com/argentgo/internal/agent"
)

// TestEventHubSubscribeLargeCursorNeverPanics 守护客户端提供的回放
// 游标。after 是 uint64，可能高达 MaxUint64；在转换为切片索引之前，
// 必须先在 uint64 域中钳制。先转换（旧行为）会在 after >= 2^63 时
// 回绕为负数，使回放循环用负偏移索引 history，
// 导致调用方 panic。
func TestEventHubSubscribeLargeCursorNeverPanics(t *testing.T) {
	tests := []struct {
		name    string
		after   uint64
		publish int
	}{
		{"empty history at 2^63", uint64(1) << 63, 0},
		{"empty history at max uint64", math.MaxUint64, 0},
		{"short history at 2^63", uint64(1) << 63, 3},
		{"short history at max int + 1", uint64(math.MaxInt64) + 1, 3},
		{"short history at max uint64", math.MaxUint64, 3},
		{"full history at max uint64", math.MaxUint64, eventHistoryCap},
		{"cursor just beyond seq", 4, 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hub := NewEventHub()
			for i := 0; i < tc.publish; i++ {
				hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "old"})
			}

			// 游标越过了所有已发布事件：回放必须为空。
			// 这里发生 panic 就是 bug（测试框架会以
			// 堆栈跟踪的形式报告失败）。
			ch, stop := hub.Subscribe(tc.after)
			defer stop()
			select {
			case ev := <-ch:
				t.Fatalf("unexpected replayed event for after=%d: %+v", tc.after, ev)
			case <-time.After(20 * time.Millisecond):
			}

			// 订阅仍然有效：之后的发布必须能到达。
			hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "live"})
			select {
			case ev := <-ch:
				if ev.Text != "live" {
					t.Fatalf("live event = %q, want live", ev.Text)
				}
			case <-time.After(time.Second):
				t.Fatal("live event not delivered after large-cursor subscribe")
			}
		})
	}
}

// TestEventHubSubscribeCursorReplaysInRange 确保钳制巨大游标
// 不会让正常的范围内回放退化。
func TestEventHubSubscribeCursorReplaysInRange(t *testing.T) {
	hub := NewEventHub()
	for i := 1; i <= 3; i++ {
		hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "e"})
	}
	for _, after := range []uint64{0, 1, 2, 3} {
		ch, stop := hub.Subscribe(after)
		want := 3 - int(after)
		got := 0
		for i := 0; i < want; i++ {
			select {
			case <-ch:
				got++
			case <-time.After(time.Second):
				stop()
				t.Fatalf("after=%d: timed out waiting for replay %d/%d", after, got, want)
			}
		}
		select {
		case <-ch:
			stop()
			t.Fatalf("after=%d: replayed more than %d events", after, want)
		default:
		}
		stop()
	}
}

// TestEventHandlerHugeAfterCursor 通过 SSE 前端（?after=...）演练同样的溢出。
// 预取消的 context 使处理器在 connected 帧之后立即返回，
// 因此不涉及网络或阻塞。
func TestEventHandlerHugeAfterCursor(t *testing.T) {
	tests := []string{
		"9223372036854775807",  // MaxInt64
		"9223372036854775808",  // 2^63, wraps int on 64-bit
		"18446744073709551615", // MaxUint64
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			hub := NewEventHub()
			for i := 0; i < 3; i++ {
				hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "old"})
			}
			handler := newEventHandlerInterval(hub, time.Hour)
			ctx, cancel := context.WithCancel(context.Background())
			cancel() // 在 connected 帧之后立即退出流循环

			req := httptest.NewRequest(http.MethodGet, "/v1/event?after="+raw, nil).WithContext(ctx)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req) // 直接调用：panic 会使该子测试失败

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, "event: server.connected") {
				t.Fatalf("missing connected frame: %q", body)
			}
			if strings.Contains(body, "\"Text\":\"old\"") {
				t.Fatalf("cursor past seq replayed old events: %q", body)
			}
		})
	}
}
