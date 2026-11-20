package proc

import (
	"sync"
	"testing"
)

// TestCaptureSurvivesConcurrentFlush 守护 Run 在 awaitOutcome 放弃等待
// cmd.Wait 时所依赖的不变量（被终止的子进程可能留下一个持有 stdout/stderr
// 管道的后代，因此 exec 的拷贝 goroutine 仍在运行）。
// Flush 和 String 不得与拷贝 goroutine 的写入竞争。
// 修复之前，该测试在 -race 下会被报告为数据竞争。
func TestCaptureSurvivesConcurrentFlush(t *testing.T) {
	cb := &cappedBuffer{max: 64}
	cw := newUTF8CarryWriter(cb)

	const iterations = 500
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			// 一次一个字节，迫使进位写入器在每次调用都修改 carry，
			// 最大化与另一侧 Flush 的重叠。
			for _, b := range []byte("中文测试") {
				_, _ = cw.Write([]byte{b})
			}
		}
	}()
	for i := 0; i < iterations; i++ {
		cw.Flush()
		_ = cb.String()
	}
	wg.Wait()
	cw.Flush()
	_ = cb.String()
}
