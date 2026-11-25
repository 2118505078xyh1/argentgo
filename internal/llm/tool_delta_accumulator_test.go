package llm_test

import (
	"reflect"
	"strings"
	"sync"
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestToolDeltaAccumulatorAccumulate 用表驱动覆盖多工具交错分片、参数累加、
// CJK 参数、ID/Name 取首个非空值，以及每片入参都不被修改的约定。
func TestToolDeltaAccumulatorAccumulate(t *testing.T) {
	cases := []struct {
		name   string
		deltas []llm.ToolCallDelta
		want   []llm.ToolCallDelta
	}{
		{
			name: "interleaved fragments across tools",
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
			name: "non-zero first index keeps its slot and id/name stay first",
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
			var acc llm.ToolDeltaAccumulator
			for _, d := range tc.deltas {
				before := d
				acc.OnDelta(d)
				if d != before {
					t.Errorf("OnDelta 修改了入参: %+v -> %+v", before, d)
				}
			}
			got := acc.Snapshot()
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Snapshot() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestToolDeltaAccumulatorSnapshotSortedCopy 验证 Snapshot 按 Index 排序、
// 返回的是与内部状态互不影响的值拷贝，且空累积器返回空切片。
func TestToolDeltaAccumulatorSnapshotSortedCopy(t *testing.T) {
	var acc llm.ToolDeltaAccumulator
	acc.OnDelta(llm.ToolCallDelta{Index: 2, ID: "c", Arguments: "2"})
	acc.OnDelta(llm.ToolCallDelta{Index: 0, ID: "a", Arguments: "0"})
	acc.OnDelta(llm.ToolCallDelta{Index: 1, ID: "b", Arguments: "1"})

	snap := acc.Snapshot()
	wantIndices := []int{0, 1, 2}
	if len(snap) != len(wantIndices) {
		t.Fatalf("Snapshot() len = %d, want %d", len(snap), len(wantIndices))
	}
	for i, want := range wantIndices {
		if snap[i].ID != string(rune('a'+i)) || snap[i].Arguments != string(rune('0'+i)) {
			t.Fatalf("Snapshot()[%d] = %+v, want index %d with id/args %c/%c",
				i, snap[i], want, 'a'+i, '0'+i)
		}
	}

	// 修改快照不应影响累积器内部：随后 Complete 取出的仍是原始值。
	snap[0].ID = "MUTATED"
	snap[0].Arguments = "MUTATED"
	snap = append(snap, llm.ToolCallDelta{Index: 99})
	got, ok := acc.Complete(0)
	if !ok || got.ID != "a" || got.Arguments != "0" {
		t.Fatalf("修改快照后 Complete(0) = %+v, %v, want 原始值 + true", got, ok)
	}

	var empty llm.ToolDeltaAccumulator
	if s := empty.Snapshot(); len(s) != 0 {
		t.Fatalf("空累积器 Snapshot() = %+v, want 空切片", s)
	}
}

// TestToolDeltaAccumulatorComplete 验证 Complete 取出并移除指定槽位，
// 重复取出与取出不存在的槽位都返回 false 且零值。
func TestToolDeltaAccumulatorComplete(t *testing.T) {
	var acc llm.ToolDeltaAccumulator
	acc.OnDelta(llm.ToolCallDelta{Index: 0, ID: "a", Name: "read", Arguments: "{"})
	acc.OnDelta(llm.ToolCallDelta{Index: 0, Arguments: "}"})
	acc.OnDelta(llm.ToolCallDelta{Index: 1, ID: "b", Name: "write", Arguments: "{}"})

	want := llm.ToolCallDelta{Index: 0, ID: "a", Name: "read", Arguments: "{}"}
	if got, ok := acc.Complete(0); !ok || got != want {
		t.Fatalf("Complete(0) = %+v, %v, want %+v, true", got, ok, want)
	}
	if s := acc.Snapshot(); len(s) != 1 || s[0].Index != 1 {
		t.Fatalf("取出后 Snapshot() = %+v, want 仅剩 index 1", s)
	}
	if got, ok := acc.Complete(0); ok || got != (llm.ToolCallDelta{}) {
		t.Fatalf("重复 Complete(0) = %+v, %v, want 零值, false", got, ok)
	}
	if got, ok := acc.Complete(42); ok || got != (llm.ToolCallDelta{}) {
		t.Fatalf("Complete(42) = %+v, %v, want 零值, false", got, ok)
	}
}

// TestToolDeltaAccumulatorConcurrent 在多个 goroutine 交错执行 OnDelta 与
// Snapshot，验证锁保护下既不丢片也不产生数据竞争（配合 -race 运行）。
func TestToolDeltaAccumulatorConcurrent(t *testing.T) {
	var acc llm.ToolDeltaAccumulator
	const workers = 8
	const perWorker = 60
	const slots = 3

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				// 读写交错，模拟 hook 消费者一边收片一边读快照。
				_ = acc.Snapshot()
				acc.OnDelta(llm.ToolCallDelta{
					Index:     i % slots,
					ID:        "call",
					Name:      "tool",
					Arguments: "x",
				})
			}
		}()
	}
	wg.Wait()

	snap := acc.Snapshot()
	if len(snap) != slots {
		t.Fatalf("Snapshot() len = %d, want %d", len(snap), slots)
	}
	wantArgs := strings.Repeat("x", workers*perWorker/slots)
	for i := 0; i < slots; i++ {
		want := llm.ToolCallDelta{Index: i, ID: "call", Name: "tool", Arguments: wantArgs}
		if snap[i] != want {
			t.Errorf("Snapshot()[%d] = %+v, want %+v", i, snap[i], want)
		}
	}
}
