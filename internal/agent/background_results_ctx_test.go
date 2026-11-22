package agent

import (
	"context"
	"testing"
)

// TestBackgroundResultsContextRoundTrip 守护 ctx 载体：nil 安全、注入后可读回同一指针。
func TestBackgroundResultsContextRoundTrip(t *testing.T) {
	if got := BackgroundResultsFrom(nil); got != nil {
		t.Fatalf("nil ctx 应返回 nil，得到 %v", got)
	}
	if got := BackgroundResultsFrom(context.Background()); got != nil {
		t.Fatalf("未注入应返回 nil，得到 %v", got)
	}
	bg := NewBackgroundResults(0, 2)
	ctx := WithBackgroundResults(context.Background(), bg)
	if got := BackgroundResultsFrom(ctx); got != bg {
		t.Fatalf("注入后读回的队列不一致: got %p want %p", got, bg)
	}
	// nil 队列是空操作，不应改变 ctx 中的既有值。
	ctx2 := WithBackgroundResults(ctx, nil)
	if got := BackgroundResultsFrom(ctx2); got != bg {
		t.Fatalf("nil 注入应保留原队列: got %p want %p", got, bg)
	}
}
