package bash

import (
	"sync"
	"testing"
)

// TestOutputAccountingSetterConcurrentSafeAndRaceFree 是 R37 在 shell 侧补的
// opt-in 并发守卫。
//
// SetOutputAccounting 底层是 atomic.Bool，由装配层按 env 在运行期切换；读侧
// OutputIntegrityNote 在每次 bash 静默输出时 Load 同一个字段。这里按统一的
// 4 writer × 4 reader × 200 轮压力守护：写端反复切换 true/false（不同 goroutine
// 写相反值以扩大竞争窗口），读端反复生成对账标注。-race 下不得出现数据竞争；
// 若有人把底层换回普通 bool，这里会立刻捕获。
//
// 刻意不调用 t.Parallel()：这是进程级全局状态，必须与同包其它测试串行。
func TestOutputAccountingSetterConcurrentSafeAndRaceFree(t *testing.T) {
	// 恢复默认：关闭输出对账，避免污染依赖默认关闭行为的其它测试。
	t.Cleanup(func() { SetOutputAccounting(false) })

	const (
		writers = 4
		readers = 4
		rounds  = 200
	)
	start := make(chan struct{})
	var wg sync.WaitGroup

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			for i := 0; i < rounds; i++ {
				SetOutputAccounting((i+w)%2 == 0)
			}
		}(w)
	}
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < rounds; i++ {
				// OutputIntegrityNote 直接 Load 全局开关：关闭时恒为空串，
				// 开启时返回一条有界标注；两种结果都接受，关键是并发读写安全。
				_ = OutputIntegrityNote("raw", "", 3)
			}
		}()
	}

	close(start)
	wg.Wait()

	SetOutputAccounting(false)
	if got := OutputIntegrityNote("raw", "", 3); got != "" {
		t.Fatalf("复位后仍生成对账标注：%q", got)
	}
}
