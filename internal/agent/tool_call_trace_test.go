package agent

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// clockFrom 返回一个按序取用给定时刻的时钟，用于单线程测试中的确定性计时。
// 时刻用尽后重复最后一个，避免越界。该闭包本身非并发安全，仅用于顺序测试。
func clockFrom(times ...time.Time) func() time.Time {
	i := 0
	return func() time.Time {
		if i >= len(times) {
			return times[len(times)-1]
		}
		at := times[i]
		i++
		return at
	}
}

// TestToolCallTraceChainOrder 验证调用链顺序：Begin 决定 Seq，即便 End 乱序到达，
// Snapshot 仍按调用发起顺序排列，且每跨度的耗时按各自的起止时刻正确计算。
func TestToolCallTraceChainOrder(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	var tr ToolCallTrace
	tr.now = clockFrom(
		base,                          // Begin a
		base.Add(10*time.Millisecond), // Begin b
		base.Add(20*time.Millisecond), // Begin c
		base.Add(25*time.Millisecond), // End c -> 5ms
		base.Add(40*time.Millisecond), // End a -> 40ms
		base.Add(45*time.Millisecond), // End b -> 35ms
	)

	a := tr.Begin("a")
	b := tr.Begin("b")
	c := tr.Begin("c")
	if a != 0 || b != 1 || c != 2 {
		t.Fatalf("spanID = %d/%d/%d, want 0/1/2", a, b, c)
	}

	// 故意乱序结算：c、a、b。
	tr.End(c, true, nil)
	tr.End(a, true, nil)
	tr.End(b, true, nil)

	got := tr.Snapshot()
	if len(got) != 3 {
		t.Fatalf("Snapshot 长度 = %d, want 3", len(got))
	}
	want := []TraceSpan{
		{Seq: 0, Tool: "a", DurationMs: 40, Ok: true},
		{Seq: 1, Tool: "b", DurationMs: 35, Ok: true},
		{Seq: 2, Tool: "c", DurationMs: 5, Ok: true},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("span[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestToolCallTraceSuccessFailure 验证成功/失败被正确记录，错误文本落库，
// 且失败次数按 Ok=false 统计。
func TestToolCallTraceSuccessFailure(t *testing.T) {
	base := time.Unix(1_700_000_100, 0)
	var tr ToolCallTrace
	tr.now = clockFrom(
		base, base.Add(10*time.Millisecond), // ok: 10ms
		base, base.Add(20*time.Millisecond), // fail: 20ms
	)

	idOK := tr.Begin("read")
	tr.End(idOK, true, nil)
	idFail := tr.Begin("write")
	tr.End(idFail, false, errors.New("boom"))

	got := tr.Snapshot()
	if len(got) != 2 {
		t.Fatalf("Snapshot 长度 = %d, want 2", len(got))
	}
	if got[0].Tool != "read" || !got[0].Ok || got[0].Error != "" || got[0].DurationMs != 10 {
		t.Fatalf("成功跨度异常: %+v", got[0])
	}
	if got[1].Tool != "write" || got[1].Ok || got[1].Error != "boom" || got[1].DurationMs != 20 {
		t.Fatalf("失败跨度异常: %+v", got[1])
	}
	if want := "2次调用/1次失败/总耗时30ms"; tr.Summary() != want {
		t.Fatalf("Summary = %q, want %q", tr.Summary(), want)
	}
}

// TestToolCallTraceSummary 覆盖零调用、全成功、混合与总耗时累计的摘要格式。
func TestToolCallTraceSummary(t *testing.T) {
	base := time.Unix(1_700_000_200, 0)
	cases := []struct {
		name string
		run  func(*ToolCallTrace)
		want string
	}{
		{"empty", func(*ToolCallTrace) {}, "0次调用/0次失败/总耗时0ms"},
		{"all-ok", func(tr *ToolCallTrace) {
			id := tr.Begin("x")
			tr.End(id, true, nil)
		}, "1次调用/0次失败/总耗时0ms"},
		{"mixed", func(tr *ToolCallTrace) {
			id1 := tr.Begin("x")
			tr.End(id1, true, nil)
			id2 := tr.Begin("y")
			tr.End(id2, false, errors.New("e"))
			id3 := tr.Begin("z")
			tr.End(id3, false, errors.New("e"))
		}, "3次调用/2次失败/总耗时0ms"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var tr ToolCallTrace
			tr.now = clockFrom(base)
			tc.run(&tr)
			if got := tr.Summary(); got != tc.want {
				t.Fatalf("Summary = %q, want %q", got, tc.want)
			}
		})
	}

	// 总耗时为各跨度之和。
	var tr ToolCallTrace
	tr.now = clockFrom(
		base, base.Add(7*time.Millisecond),
		base, base.Add(5*time.Millisecond),
	)
	id1 := tr.Begin("a")
	tr.End(id1, true, nil)
	id2 := tr.Begin("b")
	tr.End(id2, true, nil)
	if got, want := tr.Summary(), "2次调用/0次失败/总耗时12ms"; got != want {
		t.Fatalf("Summary = %q, want %q", got, want)
	}
}

// TestToolCallTraceConcurrent 在并发 Begin/End 下验证无数据竞争、计数自洽、
// Seq 唯一且连续、耗时非负。
func TestToolCallTraceConcurrent(t *testing.T) {
	var tr ToolCallTrace
	const n = 50
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			id := tr.Begin("concurrent")
			tr.End(id, i%2 == 0, nil)
		}(i)
	}
	close(start)
	wg.Wait()

	got := tr.Snapshot()
	if len(got) != n {
		t.Fatalf("Snapshot 长度 = %d, want %d", len(got), n)
	}
	seen := make(map[int]bool, n)
	for _, s := range got {
		if seen[s.Seq] {
			t.Fatalf("Seq 重复: %d", s.Seq)
		}
		seen[s.Seq] = true
		if s.Seq < 0 || s.Seq >= n {
			t.Fatalf("Seq 越界: %d", s.Seq)
		}
		if s.DurationMs < 0 {
			t.Fatalf("DurationMs 为负: %d", s.DurationMs)
		}
	}
	if want := "50次调用/25次失败/总耗时"; !strings.HasPrefix(tr.Summary(), want) {
		t.Fatalf("并发 Summary 前缀异常: %q, want 前缀 %q", tr.Summary(), want)
	}
}

// TestToolCallTraceClampsNegativeDuration 验证系统时钟回拨导致的负耗时被钳制为 0。
func TestToolCallTraceClampsNegativeDuration(t *testing.T) {
	base := time.Unix(1_700_000_300, 0)
	var tr ToolCallTrace
	tr.now = clockFrom(base.Add(100*time.Millisecond), base) // End 早于 Begin 90ms+
	id := tr.Begin("clock-skew")
	tr.End(id, true, nil)

	got := tr.Snapshot()
	if len(got) != 1 || got[0].DurationMs != 0 {
		t.Fatalf("负耗时未钳制: %+v", got)
	}
}

// TestToolCallTraceCJKToolNamesAndErrors 验证中文工具名与中文错误文本被原样保留，
// 不影响统计与摘要。
func TestToolCallTraceCJKToolNamesAndErrors(t *testing.T) {
	base := time.Unix(1_700_000_400, 0)
	var tr ToolCallTrace
	tr.now = clockFrom(
		base, base.Add(10*time.Millisecond),
		base, base.Add(15*time.Millisecond),
	)
	id1 := tr.Begin("读取文件")
	tr.End(id1, true, nil)
	id2 := tr.Begin("写入文件")
	tr.End(id2, false, errors.New("磁盘已满"))

	got := tr.Snapshot()
	if got[0].Tool != "读取文件" || got[1].Tool != "写入文件" {
		t.Fatalf("中文工具名异常: %+v", got)
	}
	if got[1].Error != "磁盘已满" {
		t.Fatalf("中文错误文本异常: %q", got[1].Error)
	}
	if want := "2次调用/1次失败/总耗时25ms"; tr.Summary() != want {
		t.Fatalf("Summary = %q, want %q", tr.Summary(), want)
	}
}

// TestToolCallTraceZeroValue 验证零值各方法可用：空链、空摘要、未知 End 为 no-op，
// 且惰性初始化后仍可正常记录。
func TestToolCallTraceZeroValue(t *testing.T) {
	var tr ToolCallTrace
	if got := tr.Snapshot(); len(got) != 0 {
		t.Fatalf("零值 Snapshot = %+v, want 空", got)
	}
	if got, want := tr.Summary(), "0次调用/0次失败/总耗时0ms"; got != want {
		t.Fatalf("零值 Summary = %q, want %q", got, want)
	}
	tr.End(999, true, nil) // 未知 id 不应污染统计
	if got := tr.Snapshot(); len(got) != 0 {
		t.Fatalf("未知 End 后 Snapshot = %+v, want 空", got)
	}

	id := tr.Begin("seed")
	tr.End(id, true, nil)
	snap := tr.Snapshot()
	if len(snap) != 1 || snap[0].Tool != "seed" || snap[0].DurationMs < 0 {
		t.Fatalf("零值记录后快照异常: %+v", snap)
	}
}

// TestToolCallTraceSnapshotIsCopy 验证 Snapshot 返回的是副本：修改返回切片不影响
// 内部状态，重复调用仍得到一致结果。
func TestToolCallTraceSnapshotIsCopy(t *testing.T) {
	base := time.Unix(1_700_000_500, 0)
	var tr ToolCallTrace
	tr.now = clockFrom(
		base, base.Add(10*time.Millisecond),
		base, base.Add(20*time.Millisecond),
	)
	id1 := tr.Begin("a")
	tr.End(id1, true, nil)
	id2 := tr.Begin("b")
	tr.End(id2, true, nil)

	snap := tr.Snapshot()
	snap[0].Tool = "hacked"
	snap[0].Seq = -1
	snap[0].DurationMs = -999
	snap[1].Tool = "also-hacked"

	again := tr.Snapshot()
	if len(again) != 2 {
		t.Fatalf("Snapshot 长度 = %d, want 2", len(again))
	}
	if again[0].Tool != "a" || again[0].Seq != 0 || again[0].DurationMs != 10 ||
		again[1].Tool != "b" {
		t.Fatalf("修改副本影响了内部状态: %+v", again)
	}
}

// TestToolCallTraceEndIdempotentAndInputSafe 验证重复 End 为 no-op，且不会修改入参：
// Begin 传入的工具名与 Err 的错误文本在结算后保持原样。
func TestToolCallTraceEndIdempotentAndInputSafe(t *testing.T) {
	base := time.Unix(1_700_000_600, 0)
	var tr ToolCallTrace
	tr.now = clockFrom(base, base.Add(5*time.Millisecond))

	tool := "original"
	errBoom := errors.New("original-err")
	id := tr.Begin(tool)
	tr.End(id, false, errBoom)
	tr.End(id, true, nil) // 重复结算应被忽略

	if tool != "original" || errBoom.Error() != "original-err" {
		t.Fatalf("入参被修改: tool=%q err=%q", tool, errBoom.Error())
	}
	got := tr.Snapshot()
	if len(got) != 1 {
		t.Fatalf("重复 End 产生了额外跨度: %+v", got)
	}
	if got[0].Ok || got[0].Error != "original-err" {
		t.Fatalf("重复 End 覆盖了首次结果: %+v", got[0])
	}
}
