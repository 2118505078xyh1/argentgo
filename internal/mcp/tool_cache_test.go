package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestListToolsToolCacheTTL 用表驱动覆盖 TTL 缓存的核心场景：默认关闭时
// 逐字节走原路径、未命中穿透、命中复用、TTL 过期重新发现、以及每次成功
// 发现重置有效期。每个用例注入固定时钟，避免依赖真实时间造成不稳定。
func TestListToolsToolCacheTTL(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		ttl       time.Duration
		callsAt   []time.Time // 每次 ListTools 使用的“当前时间”
		wantNames []string    // 每次期望返回的工具名（据此判断是否复用）
		wantCalls int         // 期望的 tools/list 传输调用次数
	}{
		{
			name:      "默认关闭：每次调用都穿透到传输",
			ttl:       0,
			callsAt:   []time.Time{base, base, base.Add(time.Hour)},
			wantNames: []string{"tool-1", "tool-2", "tool-3"},
			wantCalls: 3,
		},
		{
			name:      "未命中穿透后命中复用",
			ttl:       10 * time.Second,
			callsAt:   []time.Time{base, base.Add(time.Second), base.Add(9 * time.Second)},
			wantNames: []string{"tool-1", "tool-1", "tool-1"},
			wantCalls: 1,
		},
		{
			name:      "TTL 过期后重新发现",
			ttl:       10 * time.Second,
			callsAt:   []time.Time{base, base.Add(10 * time.Second), base.Add(20 * time.Second)},
			wantNames: []string{"tool-1", "tool-2", "tool-3"},
			wantCalls: 3,
		},
		{
			name:      "每次成功发现重置 TTL 有效期",
			ttl:       10 * time.Second,
			callsAt:   []time.Time{base, base.Add(9 * time.Second), base.Add(11 * time.Second), base.Add(15 * time.Second)},
			wantNames: []string{"tool-1", "tool-1", "tool-2", "tool-2"},
			wantCalls: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int64
			ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
				"tools/list": func(json.RawMessage) (any, error) {
					n := calls.Add(1)
					return map[string]any{"tools": []any{
						map[string]any{
							"name":        fmt.Sprintf("tool-%d", n),
							"inputSchema": map[string]any{"type": "object"},
						},
					}}, nil
				},
			}}
			c := &client{
				spec:    Spec{Name: "srv", ToolCacheTTL: tt.ttl},
				tr:      ft,
				timeout: time.Second,
			}
			idx := 0
			c.toolCache.now = func() time.Time { return tt.callsAt[idx] }

			for i, at := range tt.callsAt {
				idx = i
				tools, err := c.ListTools(context.Background())
				if err != nil {
					t.Fatalf("第 %d 次（%v）ListTools: %v", i, at, err)
				}
				if len(tools) != 1 {
					t.Fatalf("第 %d 次工具数 = %d, want 1", i, len(tools))
				}
				if tools[0].Name != tt.wantNames[i] {
					t.Errorf("第 %d 次工具名 = %q, want %q", i, tools[0].Name, tt.wantNames[i])
				}
			}
			if got := int(calls.Load()); got != tt.wantCalls {
				t.Errorf("tools/list 调用次数 = %d, want %d", got, tt.wantCalls)
			}
		})
	}
}

// TestListToolsToolCacheConcurrentSafety 验证并发安全：开启缓存后，N 个并发
// 调用经 single-flight 只触发一次 tools/list，且全部得到一致结果。在 -race
// 下同时验证缓存内部状态无数据竞争。
func TestListToolsToolCacheConcurrentSafety(t *testing.T) {
	const goroutines = 64
	var calls atomic.Int64
	release := make(chan struct{})
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"tools/list": func(json.RawMessage) (any, error) {
			calls.Add(1)
			<-release // 阻塞，制造一个足够长的发现窗口让其他调用者排队
			return map[string]any{"tools": []any{
				map[string]any{"name": "shared", "inputSchema": map[string]any{"type": "object"}},
			}}, nil
		},
	}}
	c := &client{
		spec:    Spec{Name: "srv", ToolCacheTTL: time.Minute},
		tr:      ft,
		timeout: 5 * time.Second,
	}

	var wg sync.WaitGroup
	results := make(chan []Tool, goroutines)
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tools, err := c.ListTools(context.Background())
			if err != nil {
				errs <- err
				return
			}
			results <- tools
		}()
	}
	close(release) // 放行唯一刷新者的发现请求
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("并发 ListTools: %v", err)
	}
	gotResults := 0
	for tools := range results {
		gotResults++
		if len(tools) != 1 || tools[0].Name != "shared" {
			t.Errorf("并发结果不一致: %+v", tools)
		}
	}
	if gotResults != goroutines {
		t.Errorf("成功结果数 = %d, want %d", gotResults, goroutines)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("并发下 tools/list 调用次数 = %d, want 1 (single-flight)", got)
	}
}

// TestListToolsToolCacheFailureNotCached 验证发现失败不写入缓存，下一次调用
// 会重新发现；同时保证失败路径会唤醒等待者，不留下永久阻塞的刷新标记。
func TestListToolsToolCacheFailureNotCached(t *testing.T) {
	var calls atomic.Int64
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"tools/list": func(json.RawMessage) (any, error) {
			if calls.Add(1) == 1 {
				return nil, errors.New("boom")
			}
			return map[string]any{"tools": []any{
				map[string]any{"name": "recovered", "inputSchema": map[string]any{"type": "object"}},
			}}, nil
		},
	}}
	c := &client{
		spec:    Spec{Name: "srv", ToolCacheTTL: time.Minute},
		tr:      ft,
		timeout: time.Second,
	}

	if _, err := c.ListTools(context.Background()); err == nil {
		t.Fatal("首次发现应失败")
	}
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("重新发现: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "recovered" {
		t.Fatalf("重新发现结果 = %+v, want [recovered]", tools)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("tools/list 调用次数 = %d, want 2（失败不缓存）", got)
	}
}

// TestToolCacheReturnsDefensiveCopy 验证命中缓存返回的是副本：调用方修改
// 返回值不会污染后续命中，且 set 也不会持有调用方切片的别名。
func TestToolCacheReturnsDefensiveCopy(t *testing.T) {
	var tc toolCache
	tc.now = func() time.Time { return time.Unix(0, 0) }
	src := []Tool{{Name: "orig"}}
	tc.set(src, time.Minute)

	got1, ok := tc.get()
	if !ok || len(got1) != 1 || got1[0].Name != "orig" {
		t.Fatalf("首次 get = %+v, ok=%v", got1, ok)
	}
	got1[0].Name = "mutated"
	src[0].Name = "mutated-src"

	got2, ok := tc.get()
	if !ok || len(got2) != 1 || got2[0].Name != "orig" {
		t.Fatalf("缓存被污染：get = %+v, ok=%v", got2, ok)
	}
}
