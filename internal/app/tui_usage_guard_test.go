package app

import (
	"sync"
	"testing"
)

// TestConfigureTUIUsageGateValues 锁定 ARGENT_TUI_USAGE 的「默认开、=0 关」语义：
// env 为 nil 或值修剪后不等于 "0" 时开启；仅修剪后精确等于 "0" 时关闭。
func TestConfigureTUIUsageGateValues(t *testing.T) {
	prev := tuiUsageVisible.Load()
	t.Cleanup(func() { tuiUsageVisible.Store(prev) })

	for _, tc := range []struct {
		name string
		env  func(string) string
		want bool
	}{
		{"nil env", nil, true},
		{"unset", func(string) string { return "" }, true},
		{"exact 0", func(string) string { return "0" }, false},
		{"padded 0", func(string) string { return " 0\t" }, false},
		{"exact 1", func(string) string { return "1" }, true},
		{"other value", func(string) string { return "maybe" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configureTUIUsage(tc.env)
			if got := tuiUsageVisible.Load(); got != tc.want {
				t.Fatalf("configureTUIUsage(%s): visible = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestConfigureTUIUsageConcurrentSafeAndRaceFree 是 R28 并发审计在 TUI 用量门控上的守卫。
//
// tuiUsageVisible 是进程级 atomic.Bool：configureTUIUsage 会反复 Store，渲染/接线路径
// 则反复 Load。这里启动 4 个 writer 反复切换开关（不同的 w 制造相反的写入值，扩大
// 竞争窗口），同时启动 4 个 reader 反复 Load，共 200 轮。
//
// 底层是 atomic.Bool，因此在 -race 下应当无数据竞争；若有人把它退回普通 bool，
// 这里会立刻捕获。读到的值是什么都接受，关键是并发读写本身安全。
//
// 刻意不调用 t.Parallel()：这是进程级全局状态，必须与同包其它测试串行。
func TestConfigureTUIUsageConcurrentSafeAndRaceFree(t *testing.T) {
	// 恢复默认：开启用量显示，避免污染依赖默认开启行为的其它测试。
	t.Cleanup(func() { tuiUsageVisible.Store(true) })

	const (
		writers = 4
		readers = 4
		rounds  = 200
	)
	start := make(chan struct{})
	var wg sync.WaitGroup

	// writer：按轮次与 goroutine 序号交替注入 "1" / "0"，覆盖
	// configureTUIUsage 的 true 与 false 两条分支。
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			for i := 0; i < rounds; i++ {
				on := (i+w)%2 == 0
				configureTUIUsage(func(string) string {
					if on {
						return "1"
					}
					return "0"
				})
			}
		}(w)
	}

	// reader：反复原子读；读到的值是什么都接受，关键是 -race 下不能出现数据竞争。
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < rounds; i++ {
				_ = tuiUsageVisible.Load()
			}
		}()
	}

	close(start)
	wg.Wait()
}
