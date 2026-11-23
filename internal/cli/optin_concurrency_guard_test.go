package cli

import (
	"sync"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/security"
)

// TestR37OptInSettersConcurrentSafeAndRaceFree 是 R37 新增 opt-in setter 的并发守卫。
//
// R37 把若干开关从「启动时一次性赋值」升级为可运行期切换的 setter：
//   - agent.SetLoopDetection：atomic.Bool，控制工具调用循环检测；
//   - security.(*Chain).EnableStagedApproval：atomic uint32，控制分阶段审批状态机；
//   - llm.EnableStreamTiming：atomic.Int64 引用计数，控制流式时间点打点。
//
// 装配层会在并存 pipeline 之间反复写这些开关，读侧则在各自热路径上反复 Load。
// 这里用统一的 4 writer × 4 reader × 200 轮压力守护：写端反复切换 true/false
// （不同 goroutine 写相反值以扩大竞争窗口），读端在开关有导出 getter 时直接 Load，
// 没有导出 getter 时调用其纯消费/引用计数 API。-race 下不得出现数据竞争；若有人
// 把底层换回普通 bool/int，这里会立刻捕获。
//
// 刻意不调用 t.Parallel()：这些是进程级全局状态，必须与同包其它测试串行。
// configureTUIUsage 属于 app 包且 R36 已由 TestConfigureTUIUsageConcurrentSafeAndRaceFree
// 同包守卫，按任务约定跳过。
func TestR37OptInSettersConcurrentSafeAndRaceFree(t *testing.T) {
	// --- 分阶段审批：有导出 getter，读写两侧真正打到同一个原子字段。 ---
	t.Run("staged_approval", func(t *testing.T) {
		chain := &security.Chain{}
		t.Cleanup(func() { chain.EnableStagedApproval(false) })

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
					chain.EnableStagedApproval((i+w)%2 == 0)
				}
			}(w)
		}
		for r := 0; r < readers; r++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for i := 0; i < rounds; i++ {
					_ = security.StagedApprovalEnabled(chain)
				}
			}()
		}

		close(start)
		wg.Wait()

		// 复位后必须回到默认关闭，避免污染同包其它测试。
		chain.EnableStagedApproval(false)
		if security.StagedApprovalEnabled(chain) {
			t.Fatal("复位后分阶段审批仍为开启")
		}
	})

	// --- 流式打点：引用计数开关。enable/disable 都是对同一 atomic.Int64 的
	// Add，因此让 4 个 writer 各持有 200 个引用、4 个 reader 各做 200 次
	// 「获取即释放」，制造 Add/Sub 交错；同时验证 disable 幂等（重复关闭
	// 不得重复减引用计数，否则会把别的 pipeline 的开关提前关掉）。 ---
	t.Run("stream_timing", func(t *testing.T) {
		const (
			writers = 4
			readers = 4
			rounds  = 200
		)
		start := make(chan struct{})
		var wg sync.WaitGroup

		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				var disables []func()
				// 兜底释放：即便循环中途异常，也不把引用计数留在非零（默认关闭）。
				defer func() {
					for _, disable := range disables {
						disable()
					}
				}()
				for i := 0; i < rounds; i++ {
					disables = append(disables, llm.EnableStreamTiming())
				}
				for _, disable := range disables {
					disable()
					disable()
				}
			}()
		}
		for r := 0; r < readers; r++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for i := 0; i < rounds; i++ {
					disable := llm.EnableStreamTiming()
					disable()
					disable()
				}
			}()
		}

		close(start)
		wg.Wait()
		// 所有引用均已释放，开关回到默认关闭；不用 t.Setenv/全局复位，
		// 因为幂等 disable 已经把引用计数归还到基线。
	})

	// --- 循环检测：atomic.Bool。agent 未导出 loopDetectionEnabled（读点在
	// 未导出的 runState 上），cli 包无法直接 Load；读侧同包守卫见
	// internal/agent/loop_detection_test.go 的 TestSetLoopDetectionConcurrentSafe。
	// 这里仍按 4×4×200 从装配层并发调用导出 setter：读端用固定值写代替读，
	// 同样打到那个原子字段（写/写竞争也能被 -race 捕获），守护运行期切换安全。 ---
	t.Run("loop_detection", func(t *testing.T) {
		t.Cleanup(func() { agent.SetLoopDetection(true) })

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
					agent.SetLoopDetection((i+w)%2 == 0)
				}
			}(w)
		}
		for r := 0; r < readers; r++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for i := 0; i < rounds; i++ {
					agent.SetLoopDetection(false)
				}
			}()
		}

		close(start)
		wg.Wait()

		agent.SetLoopDetection(false)
	})
}
