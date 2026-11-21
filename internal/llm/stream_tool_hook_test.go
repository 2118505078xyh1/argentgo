package llm_test

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestStreamToolCollectorCompleteAll 用表驱动覆盖:首片带元数据、多工具交错、
// CJK 参数、非零起始 Index、仅空片段等场景。每例都把 NewStreamToolCollector
// 返回的回调装进 context 再取出调用,验证它确实可直接传给 WithToolDeltaHook;
// 随后断言 CompleteAll 按 Index 升序返回且会清空采集器(二次取出为空)。
func TestStreamToolCollectorCompleteAll(t *testing.T) {
	cases := []struct {
		name   string
		deltas []llm.ToolCallDelta
		want   []llm.ToolCallDelta
	}{
		{
			name: "fragments of a single call assemble",
			deltas: []llm.ToolCallDelta{
				{Index: 0, ID: "call_a", Name: "read", Arguments: `{"f":`},
				{Index: 0, Arguments: `"a.txt"}`},
			},
			want: []llm.ToolCallDelta{
				{Index: 0, ID: "call_a", Name: "read", Arguments: `{"f":"a.txt"}`},
			},
		},
		{
			name: "interleaved multi-tool fragments sorted by index",
			deltas: []llm.ToolCallDelta{
				{Index: 1, ID: "call_b", Name: "write", Arguments: `{"path":`},
				{Index: 0, ID: "call_a", Name: "read", Arguments: `{"f":`},
				{Index: 1, Arguments: `"b.txt","content":`},
				{Index: 2, ID: "call_c", Name: "ls", Arguments: `{}`},
				{Index: 0, Arguments: `"a.txt"}`},
				{Index: 1, Arguments: `"hi"}`},
			},
			want: []llm.ToolCallDelta{
				{Index: 0, ID: "call_a", Name: "read", Arguments: `{"f":"a.txt"}`},
				{Index: 1, ID: "call_b", Name: "write", Arguments: `{"path":"b.txt","content":"hi"}`},
				{Index: 2, ID: "call_c", Name: "ls", Arguments: `{}`},
			},
		},
		{
			name: "cjk arguments accumulate across fragments",
			deltas: []llm.ToolCallDelta{
				{Index: 3, ID: "call_x", Name: "write", Arguments: `{"路径":"中`},
				{Index: 3, Arguments: `文","内容":"你好"}`},
			},
			want: []llm.ToolCallDelta{
				{Index: 3, ID: "call_x", Name: "write", Arguments: `{"路径":"中文","内容":"你好"}`},
			},
		},
		{
			name: "non-zero first index keeps its slot and first metadata",
			deltas: []llm.ToolCallDelta{
				{Index: 7, ID: "call_7", Name: "read", Arguments: "{"},
				{Index: 7, ID: "call_other", Name: "write", Arguments: "}"},
			},
			want: []llm.ToolCallDelta{
				{Index: 7, ID: "call_7", Name: "read", Arguments: "{}"},
			},
		},
		{
			name: "empty fragment only creates a slot",
			deltas: []llm.ToolCallDelta{
				{Index: 5},
			},
			want: []llm.ToolCallDelta{
				{Index: 5},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, hook := llm.NewStreamToolCollector()
			ctx := llm.WithToolDeltaHook(context.Background(), hook)
			emit := llm.HookFromContext(ctx)
			if emit == nil {
				t.Fatal("HookFromContext() = nil, want the returned hook")
			}
			for _, d := range tc.deltas {
				before := d
				emit(d)
				if d != before {
					t.Errorf("hook 修改了入参: %+v -> %+v", before, d)
				}
			}

			got := c.CompleteAll()
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("CompleteAll() = %+v, want %+v", got, tc.want)
			}
			if again := c.CompleteAll(); len(again) != 0 {
				t.Errorf("二次 CompleteAll() = %+v, want 空切片", again)
			}
		})
	}
}

// TestStreamToolCollectorReset 验证 Reset 清空全部槽位,且不破坏原 hook:
// Reset 之后继续收片,仍能正确取出新的一轮调用。
func TestStreamToolCollectorReset(t *testing.T) {
	c, hook := llm.NewStreamToolCollector()
	hook(llm.ToolCallDelta{Index: 0, ID: "old", Name: "read", Arguments: "{"})
	hook(llm.ToolCallDelta{Index: 1, ID: "old2", Name: "write", Arguments: "{}"})
	if got := c.CompleteAll(); len(got) != 2 {
		t.Fatalf("Reset 前 CompleteAll() len = %d, want 2", len(got))
	}

	// 先把数据喂进去再 Reset,确认整批被丢弃。
	hook(llm.ToolCallDelta{Index: 2, ID: "drop", Name: "ls", Arguments: "{}"})
	c.Reset()
	if got := c.CompleteAll(); len(got) != 0 {
		t.Fatalf("Reset 后 CompleteAll() = %+v, want 空切片", got)
	}

	// 原 hook 仍然可用。
	hook(llm.ToolCallDelta{Index: 4, ID: "new", Name: "read", Arguments: `{"k":`})
	hook(llm.ToolCallDelta{Index: 4, Arguments: "1}"})
	want := []llm.ToolCallDelta{{Index: 4, ID: "new", Name: "read", Arguments: `{"k":1}`}}
	if got := c.CompleteAll(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Reset 后重新收片 CompleteAll() = %+v, want %+v", got, want)
	}
}

// TestStreamToolCollectorZeroValue 验证零值采集器可直接调用 CompleteAll/Reset,
// 既不 panic 也不返回残留数据。
func TestStreamToolCollectorZeroValue(t *testing.T) {
	var c llm.StreamToolCollector
	if got := c.CompleteAll(); len(got) != 0 {
		t.Fatalf("零值 CompleteAll() = %+v, want 空切片", got)
	}
	c.Reset()
	if got := c.CompleteAll(); len(got) != 0 {
		t.Fatalf("零值 Reset 后 CompleteAll() = %+v, want 空切片", got)
	}
	c.Reset() // 重复 Reset 仍安全。
}

// TestStreamToolCollectorConcurrentFeed 让多个 goroutine 各自向独立 Index 收片,
// 全部结束后一次性取出,验证并发收片既不丢片也不串槽(配合 -race 检查数据竞争)。
func TestStreamToolCollectorConcurrentFeed(t *testing.T) {
	c, hook := llm.NewStreamToolCollector()
	const workers = 8
	const perWorker = 60

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				hook(llm.ToolCallDelta{Index: index, ID: "call", Name: "tool", Arguments: "x"})
			}
		}(w)
	}
	wg.Wait()

	got := c.CompleteAll()
	if len(got) != workers {
		t.Fatalf("CompleteAll() len = %d, want %d", len(got), workers)
	}
	wantArgs := strings.Repeat("x", perWorker)
	for i := 0; i < workers; i++ {
		want := llm.ToolCallDelta{Index: i, ID: "call", Name: "tool", Arguments: wantArgs}
		if got[i] != want {
			t.Errorf("CompleteAll()[%d] = %+v, want %+v", i, got[i], want)
		}
	}
}

// TestStreamToolCollectorConcurrentDrain 让收片与 CompleteAll 并发进行:
// 一个 drainer 持续取出,喂片结束后再做一次收尾取出。汇总所有取出的参数,
// 每一片都应恰好被取走一次(总长度守恒),且元数据与内容保持完整。
func TestStreamToolCollectorConcurrentDrain(t *testing.T) {
	c, hook := llm.NewStreamToolCollector()
	const workers = 8
	const perWorker = 50

	var feedWG sync.WaitGroup
	for w := 0; w < workers; w++ {
		feedWG.Add(1)
		go func() {
			defer feedWG.Done()
			for i := 0; i < perWorker; i++ {
				hook(llm.ToolCallDelta{Index: 0, ID: "call", Name: "tool", Arguments: "x"})
			}
		}()
	}

	var mu sync.Mutex
	var drained []llm.ToolCallDelta
	stop := make(chan struct{})
	var drainWG sync.WaitGroup
	drainWG.Add(1)
	go func() {
		defer drainWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			batch := c.CompleteAll()
			mu.Lock()
			drained = append(drained, batch...)
			mu.Unlock()
		}
	}()

	feedWG.Wait()
	final := c.CompleteAll()
	close(stop)
	drainWG.Wait()

	mu.Lock()
	drained = append(drained, final...)
	total := 0
	for _, d := range drained {
		if d.ID != "call" || d.Name != "tool" {
			t.Errorf("drained call = %+v, want id/name call/tool", d)
		}
		for _, r := range d.Arguments {
			if r != 'x' {
				t.Errorf("drained arguments = %q, want only 'x'", d.Arguments)
				break
			}
		}
		total += len(d.Arguments)
	}
	mu.Unlock()

	if want := workers * perWorker; total != want {
		t.Fatalf("drained 参数总长度 = %d, want %d", total, want)
	}
}

// TestStreamToolCollectorConcurrentReset 混合并发收片、Reset 与 CompleteAll,
// 结束后仍可正常收片并取出,验证在并发重置下无 panic、无数据竞争且状态可用。
func TestStreamToolCollectorConcurrentReset(t *testing.T) {
	c, hook := llm.NewStreamToolCollector()
	const workers = 8
	const perWorker = 40

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				hook(llm.ToolCallDelta{Index: index % 3, ID: "call", Arguments: "x"})
				if i%7 == 0 {
					c.Reset()
				}
				if i%11 == 0 {
					_ = c.CompleteAll()
				}
			}
		}(w)
	}
	wg.Wait()

	c.Reset() // 清掉残留,再验证采集器仍可用。
	hook(llm.ToolCallDelta{Index: 9, ID: "final", Name: "tool", Arguments: "{}"})
	want := []llm.ToolCallDelta{{Index: 9, ID: "final", Name: "tool", Arguments: "{}"}}
	if got := c.CompleteAll(); !reflect.DeepEqual(got, want) {
		t.Fatalf("并发 Reset 后 CompleteAll() = %+v, want %+v", got, want)
	}
}
