package hooks

import (
	"sync"
	"sync/atomic"
	"testing"
)

// dedupeOp 描述对 EventDeduplicator 的一次操作及其预期结果。
// wantFirst 仅对 "first" 操作有意义；wantLen 是操作后的预期记录数。
type dedupeOp struct {
	name      string
	op        string // first / release / reset
	key       string
	wantFirst bool
	wantLen   int
}

// runDedupeOps 逐条执行操作序列并在每步校验 First 返回值与 Len。
func runDedupeOps(t *testing.T, d *EventDeduplicator, ops []dedupeOp) {
	t.Helper()
	for _, step := range ops {
		t.Run(step.name, func(t *testing.T) {
			switch step.op {
			case "first":
				if got := d.First(step.key); got != step.wantFirst {
					t.Fatalf("First(%q) = %v, want %v", step.key, got, step.wantFirst)
				}
			case "release":
				d.Release(step.key)
			case "reset":
				d.Reset()
			default:
				t.Fatalf("unknown op %q", step.op)
			}
			if got := d.Len(); got != step.wantLen {
				t.Fatalf("after %s(%q) Len() = %d, want %d", step.op, step.key, got, step.wantLen)
			}
		})
	}
}

func TestEventDeduplicatorFirstAndRepeat(t *testing.T) {
	// 首次见返回 true 并记录；重复见返回 false 且不增加计数。
	var d EventDeduplicator
	runDedupeOps(t, &d, []dedupeOp{
		{name: "first is true for a new key", op: "first", key: "call-1", wantFirst: true, wantLen: 1},
		{name: "repeat of same key is false", op: "first", key: "call-1", wantFirst: false, wantLen: 1},
		{name: "repeat again stays false", op: "first", key: "call-1", wantFirst: false, wantLen: 1},
		{name: "a different key is first", op: "first", key: "call-2", wantFirst: true, wantLen: 2},
		{name: "empty key is a valid distinct key", op: "first", key: "", wantFirst: true, wantLen: 3},
		{name: "empty key repeats", op: "first", key: "", wantFirst: false, wantLen: 3},
	})
}

func TestEventDeduplicatorReleaseAllowsFirstAgain(t *testing.T) {
	cases := []struct {
		name string
		ops  []dedupeOp
	}{
		{
			name: "release then first succeeds",
			ops: []dedupeOp{
				{op: "first", key: "call-1", wantFirst: true, wantLen: 1},
				{op: "first", key: "call-1", wantFirst: false, wantLen: 1},
				{op: "release", key: "call-1", wantLen: 0},
				{op: "first", key: "call-1", wantFirst: true, wantLen: 1},
				{op: "first", key: "call-1", wantFirst: false, wantLen: 1},
			},
		},
		{
			name: "release only affects the released key",
			ops: []dedupeOp{
				{op: "first", key: "a", wantFirst: true, wantLen: 1},
				{op: "first", key: "b", wantFirst: true, wantLen: 2},
				{op: "release", key: "a", wantLen: 1},
				{op: "first", key: "a", wantFirst: true, wantLen: 2},
				{op: "first", key: "b", wantFirst: false, wantLen: 2},
			},
		},
		{
			name: "releasing a missing key is a no-op",
			ops: []dedupeOp{
				{op: "release", key: "never-seen", wantLen: 0},
				{op: "release", key: "never-seen", wantLen: 0},
				{op: "first", key: "never-seen", wantFirst: true, wantLen: 1},
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var d EventDeduplicator
			runDedupeOps(t, &d, test.ops)
		})
	}
}

func TestEventDeduplicatorLen(t *testing.T) {
	// Len 反映当前未释放的不同 key 数量，重复 First 不影响它。
	var d EventDeduplicator
	if got := d.Len(); got != 0 {
		t.Fatalf("zero value Len() = %d, want 0", got)
	}
	runDedupeOps(t, &d, []dedupeOp{
		{op: "first", key: "k1", wantFirst: true, wantLen: 1},
		{op: "first", key: "k1", wantFirst: false, wantLen: 1},
		{op: "first", key: "k2", wantFirst: true, wantLen: 2},
		{op: "first", key: "k3", wantFirst: true, wantLen: 3},
		{op: "release", key: "k2", wantLen: 2},
		{op: "release", key: "k1", wantLen: 1},
		{op: "release", key: "k3", wantLen: 0},
	})
}

func TestEventDeduplicatorReset(t *testing.T) {
	cases := []struct {
		name string
		ops  []dedupeOp
	}{
		{
			name: "reset clears all keys",
			ops: []dedupeOp{
				{op: "first", key: "a", wantFirst: true, wantLen: 1},
				{op: "first", key: "b", wantFirst: true, wantLen: 2},
				{op: "reset", wantLen: 0},
				{op: "first", key: "a", wantFirst: true, wantLen: 1},
				{op: "first", key: "b", wantFirst: true, wantLen: 2},
			},
		},
		{
			name: "reset on an empty deduplicator is a no-op",
			ops: []dedupeOp{
				{op: "reset", wantLen: 0},
				{op: "first", key: "a", wantFirst: true, wantLen: 1},
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var d EventDeduplicator
			runDedupeOps(t, &d, test.ops)
		})
	}
}

func TestEventDeduplicatorCJKKeys(t *testing.T) {
	// key 是任意字符串，不解析内容；CJK 文本按字节精确区分。
	cases := []struct {
		name string
		ops  []dedupeOp
	}{
		{
			name: "CJK key deduplicates by exact bytes",
			ops: []dedupeOp{
				{op: "first", key: "子代理-调用-1", wantFirst: true, wantLen: 1},
				{op: "first", key: "子代理-调用-1", wantFirst: false, wantLen: 1},
				{op: "first", key: "子代理-调用-2", wantFirst: true, wantLen: 2},
			},
		},
		{
			name: "visually similar CJK keys stay distinct",
			ops: []dedupeOp{
				{op: "first", key: "停止", wantFirst: true, wantLen: 1},
				{op: "first", key: "停止 ", wantFirst: true, wantLen: 2},
				{op: "first", key: "停止", wantFirst: false, wantLen: 2},
				{op: "release", key: "停止", wantLen: 1},
				{op: "first", key: "停止", wantFirst: true, wantLen: 2},
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var d EventDeduplicator
			runDedupeOps(t, &d, test.ops)
		})
	}
}

func TestEventDeduplicatorConcurrentFirstUnique(t *testing.T) {
	// 对同一个 key 并发调用 First：必然恰好一个调用方得到 true。
	const workers = 128
	var d EventDeduplicator
	var gotTrue atomic.Int64

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	done.Add(workers)

	for i := 0; i < workers; i++ {
		go func() {
			defer done.Done()
			start.Wait()
			if d.First("concurrent-call-id") {
				gotTrue.Add(1)
			}
		}()
	}
	start.Done()
	done.Wait()

	if got := gotTrue.Load(); got != 1 {
		t.Fatalf("concurrent First winners = %d, want 1", got)
	}
	if got := d.Len(); got != 1 {
		t.Fatalf("Len() after concurrent First = %d, want 1", got)
	}

	// 并发下重复仍为 false。
	if d.First("concurrent-call-id") {
		t.Fatal("repeat First after concurrency must be false")
	}
}

func TestEventDeduplicatorConcurrentDistinctKeys(t *testing.T) {
	// 不同 key 并发 First 各自成功，Len 与 key 数量一致。
	const keys = 256
	var d EventDeduplicator
	var gotTrue atomic.Int64

	var wg sync.WaitGroup
	wg.Add(keys)
	for i := 0; i < keys; i++ {
		go func(n int) {
			defer wg.Done()
			if d.First("call-" + string(rune(n))) {
				gotTrue.Add(1)
			}
		}(i)
	}
	wg.Wait()

	if got := gotTrue.Load(); got != keys {
		t.Fatalf("concurrent First winners = %d, want %d", got, keys)
	}
	if got := d.Len(); got != keys {
		t.Fatalf("Len() = %d, want %d", got, keys)
	}
}

func TestEventDeduplicatorConcurrentReleaseAndReset(t *testing.T) {
	// 混合 First/Release/Reset 的并发压力测试：不崩溃、不产生数据竞争，
	// 且结束后计数在合理范围内。race 检测器会捕获任何未加锁的访问。
	var d EventDeduplicator
	var wg sync.WaitGroup
	const goroutines = 32
	const iterations = 200

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(n int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				key := "k" + string(rune(i%16))
				switch (n + i) % 4 {
				case 0:
					d.First(key)
				case 1:
					d.Release(key)
				case 2:
					d.Len()
				case 3:
					if i%50 == 0 {
						d.Reset()
					}
				}
			}
		}(g)
	}
	wg.Wait()

	if got := d.Len(); got < 0 || got > 16 {
		t.Fatalf("Len() after churn = %d, want within [0,16]", got)
	}
}
